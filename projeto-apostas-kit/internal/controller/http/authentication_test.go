package http

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubAuthenticator devolve sempre o mesmo resultado, sem tocar nenhum serviço: os
// handlers autenticam antes de usá-los.
type stubAuthenticator struct {
	principal Principal
	err       error
}

func (stub stubAuthenticator) Authenticate(*http.Request) (Principal, error) {
	return stub.principal, stub.err
}

func callWith(stub stubAuthenticator, method string, path string) *httptest.ResponseRecorder {
	router := NewRouter(Dependencies{Authenticator: stub})
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))

	return recorder
}

func TestAnUnreachableIdentityProviderIsA503WithRetryAfter(t *testing.T) {
	stub := stubAuthenticator{err: ErrIdentityProviderUnavailable}

	for _, path := range []string{
		"/wagering/transactions/0192f298-345e-7e38-af88-e43f851a819d",
		"/wallets/0192f291-27dd-7d3f-8071-5f8685deef37",
	} {
		recorder := callWith(stub, http.MethodGet, path)

		if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") == "" ||
			!strings.Contains(recorder.Body.String(), "SERVICE_UNAVAILABLE") {
			t.Fatalf("%s = %d %s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestAnUnauthenticatedCallIs401AndDoesNotExplainWhy(t *testing.T) {
	stub := stubAuthenticator{err: errors.New("token expired at 2026-01-01 signature mismatch")}

	recorder := callWith(stub, http.MethodGet, "/wallets/0192f291-27dd-7d3f-8071-5f8685deef37")

	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("response = %d", recorder.Code)
	}

	if strings.Contains(recorder.Body.String(), "expired") || strings.Contains(recorder.Body.String(), "signature") {
		t.Fatalf("the 401 body explains why: %s", recorder.Body.String())
	}
}

func TestAnAuthenticatedIdentityWithoutPermissionIs403(t *testing.T) {
	empty := stubAuthenticator{principal: Principal{}}
	provider := stubAuthenticator{principal: Principal{ProviderID: "provider-a"}}

	for _, path := range []string{
		"/wallets/0192f291-27dd-7d3f-8071-5f8685deef37",
		"/wallets/0192f291-27dd-7d3f-8071-5f8685deef37/ledger",
	} {
		if got := callWith(empty, http.MethodGet, path).Code; got != http.StatusForbidden {
			t.Fatalf("%s with an empty principal = %d, want 403", path, got)
		}

		if got := callWith(provider, http.MethodGet, path).Code; got != http.StatusForbidden {
			t.Fatalf("%s with a provider = %d, want 403", path, got)
		}
	}

	posted := callWith(empty, http.MethodPost, "/wagering/transactions")
	if posted.Code != http.StatusForbidden {
		t.Fatalf("an empty principal submitting an operation = %d, want 403", posted.Code)
	}
}
