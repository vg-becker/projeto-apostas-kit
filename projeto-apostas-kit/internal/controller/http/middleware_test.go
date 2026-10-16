package http

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newLogger() (*slog.Logger, *bytes.Buffer) {
	var buffer bytes.Buffer

	return slog.New(slog.NewJSONHandler(&buffer, nil)), &buffer
}

func TestCorrelationIDValidation(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "uuid", input: "0192f298-345e-7e38-af88-e43f851a819d", want: true},
		{name: "simple", input: "corr-77", want: true},
		{name: "longest", input: strings.Repeat("a", 128), want: true},
		{name: "empty", input: "", want: false},
		{name: "too long", input: strings.Repeat("a", 129), want: false},
		{name: "space", input: "a b", want: false},
		{name: "tab", input: "a\tb", want: false},
		{name: "control character", input: "a\x01b", want: false},
		{name: "non ascii", input: "correlação", want: false},
		{name: "delete", input: "a\x7fb", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validCorrelationID(test.input); got != test.want {
				t.Fatalf("valid(%q) = %v, want %v", test.input, got, test.want)
			}
		})
	}
}

func TestGeneratedCorrelationIDsAreValidAndUnique(t *testing.T) {
	seen := map[string]bool{}

	for range 200 {
		id := newCorrelationID()

		if len(id) != 32 || !validCorrelationID(id) || seen[id] {
			t.Fatalf("id %q is malformed or repeated", id)
		}

		seen[id] = true
	}
}

func TestCorrelationKeepsAValidIDAndReplacesAnInvalidOne(t *testing.T) {
	var seen string
	handler := correlation(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		seen = request.Header.Get(correlationHeader)
	}))

	valid := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(correlationHeader, "corr-77")
	handler.ServeHTTP(valid, request)

	if seen != "corr-77" || valid.Header().Get(correlationHeader) != "corr-77" {
		t.Fatalf("seen = %q, response = %q", seen, valid.Header().Get(correlationHeader))
	}

	for _, bad := range []string{"", "has space", strings.Repeat("x", 200)} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set(correlationHeader, bad)
		handler.ServeHTTP(recorder, request)

		replaced := recorder.Header().Get(correlationHeader)
		if replaced == bad || len(replaced) != 32 || seen != replaced {
			t.Fatalf("input %q: response = %q, handler saw %q", bad, replaced, seen)
		}
	}
}

func TestRecovererAnswersWithAGenericProblemAndHidesThePanicValue(t *testing.T) {
	logger, logs := newLogger()
	handler := recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom-secret-token")
	}), logger)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusInternalServerError ||
		!strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Header().Get("Content-Type"))
	}

	if strings.Contains(recorder.Body.String(), "boom-secret-token") ||
		strings.Contains(logs.String(), "boom-secret-token") {
		t.Fatal("the panic value leaked into the response or the log")
	}

	if !strings.Contains(logs.String(), `"type":"string"`) || !strings.Contains(logs.String(), "stack") {
		t.Fatalf("log = %s, want the panic type and stack", logs.String())
	}
}

func TestRecovererLetsTheServerAbortAResponse(t *testing.T) {
	logger, _ := newLogger()
	handler := recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}), logger)

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("http.ErrAbortHandler must keep propagating")
		}
	}()

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestAccessLogRecordsTheRouteNotTheIDsAndNeverTheBodyOrCredentials(t *testing.T) {
	logger, logs := newLogger()
	mux := http.NewServeMux()
	mux.HandleFunc("/wallets/{walletId}", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTeapot)
	})

	handler := correlation(accessLog(mux, logger))
	request := httptest.NewRequest(
		http.MethodPost,
		"/wallets/0192f291-27dd-7d3f-8071-5f8685deef37",
		strings.NewReader(`{"amount":"123.45"}`),
	)
	request.Header.Set("Authorization", "Bearer super-secret")
	request.Header.Set("Idempotency-Key", "provider-a:tx-1")
	request.Header.Set(correlationHeader, "corr-9")

	handler.ServeHTTP(httptest.NewRecorder(), request)

	line := logs.String()
	for _, want := range []string{`"route":"/wallets/{walletId}"`, `"status":418`, `"correlationId":"corr-9"`, `"method":"POST"`} {
		if !strings.Contains(line, want) {
			t.Fatalf("log %s is missing %s", line, want)
		}
	}

	for _, forbidden := range []string{"0192f291", "123.45", "super-secret", "provider-a:tx-1"} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("log %s must not contain %s", line, forbidden)
		}
	}
}

func TestAccessLogOfAnUnmatchedRequestUsesAFixedRoute(t *testing.T) {
	logger, logs := newLogger()
	handler := accessLog(http.NewServeMux(), logger)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/secret/path/zz-private-id", nil))

	if !strings.Contains(logs.String(), `"route":"unmatched"`) || strings.Contains(logs.String(), "zz-private-id") {
		t.Fatalf("log = %s", logs.String())
	}
}

func TestStatusWriterKeepsTheFirstStatusAndDefaultsTo200(t *testing.T) {
	implicit := &statusWriter{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	_, _ = implicit.Write([]byte("x"))

	explicit := &statusWriter{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	explicit.WriteHeader(http.StatusCreated)
	explicit.WriteHeader(http.StatusInternalServerError)

	if implicit.status != http.StatusOK || explicit.status != http.StatusCreated {
		t.Fatalf("implicit = %d, explicit = %d", implicit.status, explicit.status)
	}
}
