//go:build integration

package http_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestABetFollowsTheContractExampleAndItsReplayIsIdentical(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "1000.00")

	first := f.bet(t, wallet, "transaction-123", "25.00")
	body := first.object(t)

	if first.status != http.StatusOK || body["status"] != "PROCESSED" || body["idempotentReplay"] != false ||
		len(body) != 4 {
		t.Fatalf("first = %d %s", first.status, first.body)
	}

	balance := body["balance"].(map[string]any)
	if balance["amount"] != "975.00" || balance["currency"] != "BRL" {
		t.Fatalf("balance = %v", balance)
	}

	replay := f.bet(t, wallet, "transaction-123", "25.00").object(t)

	if replay["transactionId"] != body["transactionId"] || replay["idempotentReplay"] != true ||
		replay["balance"].(map[string]any)["amount"] != "975.00" {
		t.Fatalf("replay = %v", replay)
	}

	if f.debits(t, wallet.id) != 1 {
		t.Fatal("the replay must not debit again")
	}
}

func TestTheIdempotencyKeyIsRequiredAndUsedAsReceived(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "1000.00")
	body := operationBody(wallet, "tx-1", "BET", "10.00")

	missing := f.call(t, http.MethodPost, "/wagering/transactions", "provider-a", nil, body)
	assertProblem(t, missing, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY")

	first := f.submit(t, "custom-key-1", body)
	sameKey := f.submit(t, "custom-key-1", body)
	otherKey := f.submit(t, "custom-key-2", body)

	if first.status != 200 || sameKey.object(t)["idempotentReplay"] != true {
		t.Fatalf("first = %d, same key = %s", first.status, sameKey.body)
	}

	assertProblem(t, otherKey, http.StatusConflict, "IDEMPOTENCY_CONFLICT")

	if f.debits(t, wallet.id) != 1 {
		t.Fatal("the operation must be applied exactly once")
	}
}

func TestReusingAKeyForDifferentContentConflicts(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "1000.00")
	f.bet(t, wallet, "tx-1", "10.00")

	changed := f.submit(t, "provider-a:tx-1", operationBody(wallet, "tx-1", "BET", "99.00"))

	assertProblem(t, changed, http.StatusConflict, "IDEMPOTENCY_CONFLICT")

	if f.debits(t, wallet.id) != 1 {
		t.Fatal("a conflicting resend must not be applied")
	}
}

func TestABusinessRejectionIsA422WithTheFailureCode(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "100.00")

	rejected := f.bet(t, wallet, "tx-1", "500.00")
	body := rejected.object(t)

	if rejected.status != http.StatusUnprocessableEntity || body["status"] != "REJECTED" ||
		body["failureCode"] != "INSUFFICIENT_FUNDS" || body["correctable"] != false {
		t.Fatalf("rejected = %d %s", rejected.status, rejected.body)
	}

	replay := f.bet(t, wallet, "tx-1", "500.00")

	if replay.status != http.StatusUnprocessableEntity || replay.object(t)["idempotentReplay"] != true {
		t.Fatalf("replay = %d %s", replay.status, replay.body)
	}
}

func TestAReversalBeforeItsReferenceIsAcceptedAndResolvedLater(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "1000.00")
	refund := referenceBody(wallet, "refund-1", "REFUND", "25.00", "bet-1")

	waiting := f.submit(t, "provider-a:refund-1", refund)
	waitingBody := waiting.object(t)

	if waiting.status != http.StatusAccepted || waitingBody["status"] != "PENDING_REFERENCE" ||
		waitingBody["balance"] != nil ||
		waiting.header.Get("Location") != "/wagering/transactions/"+waitingBody["transactionId"].(string) {
		t.Fatalf("waiting = %d %s, location = %q", waiting.status, waiting.body, waiting.header.Get("Location"))
	}

	f.bet(t, wallet, "bet-1", "25.00")
	f.clock.Advance(3 * time.Second)

	summary, err := f.resolver.ResolveDue(t.Context(), 10)
	if err != nil || summary.Finished != 1 {
		t.Fatalf("summary = %+v (%v)", summary, err)
	}

	replay := f.submit(t, "provider-a:refund-1", refund)

	if replay.status != http.StatusOK || replay.object(t)["status"] != "PROCESSED" ||
		replay.object(t)["idempotentReplay"] != true {
		t.Fatalf("replay = %d %s", replay.status, replay.body)
	}
}

func TestOnlyAnAuthenticatedProviderMaySubmitAndOnlyForItself(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "1000.00")
	body := operationBody(wallet, "tx-1", "BET", "10.00")
	headers := map[string]string{"Idempotency-Key": "k-1"}

	anonymous := f.call(t, http.MethodPost, "/wagering/transactions", "", headers, body)
	forged := f.call(t, http.MethodPost, "/wagering/transactions", "forged", headers, body)
	internal := f.call(t, http.MethodPost, "/wagering/transactions", "internal", headers, body)
	otherProvider := f.call(t, http.MethodPost, "/wagering/transactions", "provider-b", headers, body)
	missingProvider := f.submit(t, "k-2", strings.Replace(body, `"providerId":"provider-a",`, "", 1))

	assertProblem(t, anonymous, http.StatusUnauthorized, "UNAUTHENTICATED")
	assertProblem(t, forged, http.StatusUnauthorized, "UNAUTHENTICATED")
	assertProblem(t, internal, http.StatusForbidden, "FORBIDDEN")
	assertProblem(t, otherProvider, http.StatusForbidden, "PROVIDER_MISMATCH")
	assertProblem(t, missingProvider, http.StatusBadRequest, "INVALID_REQUEST")

	if f.debits(t, wallet.id) != 0 || f.count(t, `SELECT count(*) FROM wager_transactions WHERE kind = 'BET'`) != 0 {
		t.Fatal("a refused request had financial effects")
	}
}

func TestInvalidOperationsAreRefusedWithoutAnyEffect(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "1000.00")
	stranger := f.newID(t)
	good := operationBody(wallet, "tx-1", "BET", "10.00")

	replace := func(old string, replacement string) string {
		return strings.Replace(good, old, replacement, 1)
	}

	tests := []struct {
		name    string
		body    string
		headers map[string]string
		status  int
		code    string
	}{
		{name: "opening", body: replace(`"kind":"BET"`, `"kind":"OPENING"`), status: 400, code: "KIND_NOT_ALLOWED"},
		{name: "unknown kind", body: replace(`"kind":"BET"`, `"kind":"SPIN"`), status: 400, code: "INVALID_KIND"},
		{name: "loss with amount", body: replace(`"kind":"BET"`, `"kind":"LOSS"`), status: 400, code: "INVALID_AMOUNT"},
		{name: "zero bet", body: replace(`"10.00"`, `"0.00"`), status: 400, code: "INVALID_AMOUNT"},
		{name: "three decimals", body: replace(`"10.00"`, `"10.001"`), status: 400, code: "INVALID_AMOUNT"},
		{name: "amount as number", body: replace(`"10.00"`, `10.0`), status: 400, code: "INVALID_JSON"},
		{name: "missing money", body: replace(`,"money":{"amount":"10.00","currency":"BRL"}`, ``), status: 400, code: "INVALID_REQUEST"},
		{name: "unknown field", body: replace(`"gameId"`, `"bonus":1,"gameId"`), status: 400, code: "INVALID_JSON"},
		{name: "unknown wallet", body: replace(wallet.id, stranger), status: 404, code: "WALLET_NOT_FOUND"},
		{name: "other player", body: replace(wallet.playerID, stranger), status: 400, code: "WALLET_PLAYER_MISMATCH"},
		{name: "other currency", body: replace(`"currency":"BRL"`, `"currency":"USD"`), status: 400, code: "CURRENCY_MISMATCH"},
		{name: "malformed wallet id", body: replace(wallet.id, "nope"), status: 400, code: "INVALID_REQUEST"},
		{name: "plain text", body: good, headers: map[string]string{"Content-Type": "text/plain"}, status: 415, code: "UNSUPPORTED_MEDIA_TYPE"},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			headers := map[string]string{"Idempotency-Key": fmt.Sprintf("key-%d", index)}
			for name, value := range test.headers {
				headers[name] = value
			}

			response := f.call(t, http.MethodPost, "/wagering/transactions", "provider-a", headers, test.body)

			assertProblem(t, response, test.status, test.code)
		})
	}

	if f.count(t, `SELECT count(*) FROM wager_transactions WHERE kind <> 'OPENING'`) != 0 {
		t.Fatal("invalid operations were persisted")
	}
}

func TestADatabaseOutageIsA503WithRetryAfterAndNoLeak(t *testing.T) {
	f := newFixtureWith(t, unavailableTransactions{})
	wallet := f.createWallet(t, "1000.00")

	response := f.bet(t, wallet, "tx-1", "10.00")

	assertProblem(t, response, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")

	if response.header.Get("Retry-After") == "" {
		t.Fatal("a 503 must carry Retry-After")
	}

	if strings.Contains(string(response.body), "hunter2") || strings.Contains(string(response.body), "refused") {
		t.Fatalf("the 503 body leaks internals: %s", response.body)
	}
}

func TestUnknownRoutesAndWrongMethodsAreProblems(t *testing.T) {
	f := newFixture(t)

	unknown := f.call(t, http.MethodGet, "/nope", "internal", nil, "")
	trailingSlash := f.call(t, http.MethodGet, "/wallets/", "internal", nil, "")
	wrongMethod := f.call(t, http.MethodDelete, "/wallets", "internal", nil, "")
	readOnlyPost := f.call(t, http.MethodPost, "/wagering/transactions/"+f.newID(t), "provider-a", nil, "")

	assertProblem(t, unknown, http.StatusNotFound, "ROUTE_NOT_FOUND")
	assertProblem(t, trailingSlash, http.StatusNotFound, "ROUTE_NOT_FOUND")
	assertProblem(t, wrongMethod, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
	assertProblem(t, readOnlyPost, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")

	if wrongMethod.header.Get("Allow") != "POST" {
		t.Fatalf("Allow = %q, want POST", wrongMethod.header.Get("Allow"))
	}
}
