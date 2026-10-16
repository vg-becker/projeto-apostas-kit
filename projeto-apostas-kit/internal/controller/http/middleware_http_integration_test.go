//go:build integration

package http_test

import (
	"net/http"
	"strings"
	"testing"
)

func (f *fixture) eventsWithCorrelation(t *testing.T, correlationID string) int {
	t.Helper()

	return f.count(t, `SELECT count(*) FROM outbox_events WHERE payload->>'correlationId' = $1`, correlationID)
}

func TestTheCorrelationIDTravelsFromTheRequestToTheOutboxEvents(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "1000.00")

	response := f.call(t, http.MethodPost, "/wagering/transactions", "provider-a",
		map[string]string{"Idempotency-Key": "k-1", "X-Correlation-Id": "corr-77"},
		operationBody(wallet, "tx-1", "BET", "25.00"))

	if response.status != http.StatusOK || response.header.Get("X-Correlation-Id") != "corr-77" {
		t.Fatalf("response = %d, correlation = %q", response.status, response.header.Get("X-Correlation-Id"))
	}

	if f.eventsWithCorrelation(t, "corr-77") != 2 {
		t.Fatal("the Processed and BalanceChanged events must carry the request correlation id")
	}
}

func TestAMissingOrInvalidCorrelationIDIsReplacedAndStillTraced(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "1000.00")

	for index, sent := range []string{"", "has space"} {
		headers := map[string]string{"Idempotency-Key": "k-" + string(rune('a'+index))}
		if sent != "" {
			headers["X-Correlation-Id"] = sent
		}

		response := f.call(t, http.MethodPost, "/wagering/transactions", "provider-a", headers,
			operationBody(wallet, "tx-"+string(rune('a'+index)), "BET", "1.00"))

		generated := response.header.Get("X-Correlation-Id")
		if response.status != http.StatusOK || len(generated) != 32 || generated == sent {
			t.Fatalf("sent %q: response = %d, correlation = %q", sent, response.status, generated)
		}

		if f.eventsWithCorrelation(t, generated) != 2 {
			t.Fatalf("sent %q: the generated id %q must reach the events", sent, generated)
		}
	}
}

func TestEveryResponseCarriesACorrelationID(t *testing.T) {
	f := newFixture(t)

	for _, path := range []string{"/health/live", "/nope", "/wallets"} {
		response := f.call(t, http.MethodGet, path, "", nil, "")

		if response.header.Get("X-Correlation-Id") == "" {
			t.Fatalf("%s answered without a correlation id", path)
		}
	}
}

func TestAPanicBecomesAGenericProblemAndTheServerKeepsServing(t *testing.T) {
	f := newFixture(t)

	crashed := f.call(t, http.MethodGet, "/wallets/"+f.newID(t), "panic", nil, "")

	assertProblem(t, crashed, http.StatusInternalServerError, "INTERNAL_ERROR")

	if strings.Contains(string(crashed.body), "boom-secret-token") ||
		strings.Contains(f.logs.String(), "boom-secret-token") {
		t.Fatal("the panic value leaked")
	}

	if !strings.Contains(f.logs.String(), "panic recovered") {
		t.Fatal("the panic must be logged")
	}

	if after := f.call(t, http.MethodGet, "/health/live", "", nil, ""); after.status != http.StatusOK {
		t.Fatalf("after the panic: %d", after.status)
	}
}

func TestTheAccessLogHasRouteStatusAndCorrelationButNoBodyOrCredentials(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "1000.00")

	f.call(t, http.MethodPost, "/wagering/transactions", "provider-a",
		map[string]string{"Idempotency-Key": "provider-a:secret-key-1", "X-Correlation-Id": "corr-log-1"},
		operationBody(wallet, "tx-1", "BET", "123.45"))

	logs := f.logs.String()

	for _, want := range []string{
		`"route":"/wagering/transactions"`,
		`"status":200`,
		`"method":"POST"`,
		`"correlationId":"corr-log-1"`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs are missing %s:\n%s", want, logs)
		}
	}

	for _, forbidden := range []string{"123.45", "Bearer", "secret-key-1", wallet.id, wallet.playerID} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("logs must not contain %q:\n%s", forbidden, logs)
		}
	}
}
