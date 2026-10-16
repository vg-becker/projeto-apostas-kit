//go:build integration

package http_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func (f *fixture) openingID(t *testing.T, walletID string) string {
	t.Helper()

	var id string
	row := f.pool.QueryRow(
		context.Background(),
		`SELECT id::text FROM wager_transactions WHERE wallet_id = $1 AND kind = 'OPENING'`,
		walletID,
	)
	if err := row.Scan(&id); err != nil {
		t.Fatalf("opening id: %v", err)
	}

	return id
}

func TestAProviderFollowsItsOwnTransactions(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "100.00")
	processed := f.bet(t, wallet, "tx-ok", "10.00").object(t)["transactionId"].(string)
	rejected := f.bet(t, wallet, "tx-no", "999.00").object(t)["transactionId"].(string)
	waiting := f.submit(t, "k-r", referenceBody(wallet, "refund-1", "REFUND", "10.00", "missing")).
		object(t)["transactionId"].(string)

	byID := f.call(t, http.MethodGet, "/wagering/transactions/"+processed, "provider-a", nil, "").object(t)
	byPath := f.call(t, http.MethodGet, "/providers/provider-a/wagering/transactions/tx-no", "provider-a", nil, "")
	pending := f.call(t, http.MethodGet, "/wagering/transactions/"+waiting, "provider-a", nil, "").object(t)

	if byID["status"] != "PROCESSED" || byID["providerId"] != "provider-a" ||
		byID["balance"].(map[string]any)["amount"] != "90.00" || byID["failureCode"] != nil {
		t.Fatalf("processed = %v", byID)
	}

	if byPath.status != 200 || byPath.object(t)["transactionId"] != rejected ||
		byPath.object(t)["failureCode"] != "INSUFFICIENT_FUNDS" {
		t.Fatalf("rejected = %d %s", byPath.status, byPath.body)
	}

	if pending["status"] != "PENDING_REFERENCE" || pending["referenceExternalTransactionId"] != "missing" {
		t.Fatalf("pending = %v", pending)
	}
}

func TestProvidersCannotReadEachOthersTransactions(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "100.00")
	owned := f.bet(t, wallet, "tx-1", "10.00").object(t)["transactionId"].(string)

	byID := f.call(t, http.MethodGet, "/wagering/transactions/"+owned, "provider-b", nil, "")
	foreignPath := f.call(t, http.MethodGet, "/providers/provider-a/wagering/transactions/tx-1", "provider-b", nil, "")
	ownPath := f.call(t, http.MethodGet, "/providers/provider-b/wagering/transactions/tx-1", "provider-b", nil, "")

	assertProblem(t, byID, http.StatusNotFound, "TRANSACTION_NOT_FOUND")
	assertProblem(t, foreignPath, http.StatusForbidden, "FORBIDDEN")
	assertProblem(t, ownPath, http.StatusNotFound, "TRANSACTION_NOT_FOUND")
}

func TestAReplayNeverCrossesProviders(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "100.00")
	first := f.bet(t, wallet, "tx-1", "10.00").object(t)

	body := strings.Replace(operationBody(wallet, "tx-1", "BET", "10.00"), "provider-a", "provider-b", 1)
	second := f.call(t, http.MethodPost, "/wagering/transactions", "provider-b",
		map[string]string{"Idempotency-Key": "provider-a:tx-1"}, body).object(t)

	if second["idempotentReplay"] != false || second["transactionId"] == first["transactionId"] {
		t.Fatalf("second = %v, a replay must not cross providers", second)
	}

	stillTheOriginal := f.call(t, http.MethodGet, "/wagering/transactions/"+first["transactionId"].(string), "provider-a", nil, "")
	if stillTheOriginal.status != 200 || f.debits(t, wallet.id) != 2 {
		t.Fatalf("original = %d, debits = %d", stillTheOriginal.status, f.debits(t, wallet.id))
	}
}

func TestTheInternalServiceReadsAnythingAndOpeningsStayInternal(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "100.00")
	bet := f.bet(t, wallet, "tx-1", "10.00").object(t)["transactionId"].(string)
	opening := f.openingID(t, wallet.id)

	internalBet := f.call(t, http.MethodGet, "/wagering/transactions/"+bet, "internal", nil, "")
	internalOpening := f.call(t, http.MethodGet, "/wagering/transactions/"+opening, "internal", nil, "").object(t)
	providerOpening := f.call(t, http.MethodGet, "/wagering/transactions/"+opening, "provider-a", nil, "")

	if internalBet.status != 200 || internalOpening["kind"] != "OPENING" || internalOpening["providerId"] != nil {
		t.Fatalf("internal bet = %d, opening = %v", internalBet.status, internalOpening)
	}

	assertProblem(t, providerOpening, http.StatusNotFound, "TRANSACTION_NOT_FOUND")
}

func TestTransactionReadsRequireAuthenticationAndValidIDs(t *testing.T) {
	f := newFixture(t)

	anonymous := f.call(t, http.MethodGet, "/wagering/transactions/"+f.newID(t), "", nil, "")
	malformed := f.call(t, http.MethodGet, "/wagering/transactions/nope", "provider-a", nil, "")
	unknown := f.call(t, http.MethodGet, "/wagering/transactions/"+f.newID(t), "provider-a", nil, "")
	unknownPath := f.call(t, http.MethodGet, "/providers/provider-a/wagering/transactions/ghost", "provider-a", nil, "")

	assertProblem(t, anonymous, http.StatusUnauthorized, "UNAUTHENTICATED")
	assertProblem(t, malformed, http.StatusBadRequest, "INVALID_REQUEST")
	assertProblem(t, unknown, http.StatusNotFound, "TRANSACTION_NOT_FOUND")
	assertProblem(t, unknownPath, http.StatusNotFound, "TRANSACTION_NOT_FOUND")
}
