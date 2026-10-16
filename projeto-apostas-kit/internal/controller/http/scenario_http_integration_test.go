//go:build integration

package http_test

import (
	"net/http"
	"testing"
)

func (f *fixture) balanceOf(t *testing.T, walletID string) string {
	t.Helper()

	current := f.call(t, http.MethodGet, "/wallets/"+walletID, "internal", nil, "").object(t)

	return current["balance"].(map[string]any)["amount"].(string)
}

func (f *fixture) assertOutcome(t *testing.T, response reply, wantStatus int, wantBalance string) {
	t.Helper()

	body := response.object(t)
	if response.status != wantStatus || body["status"] != "PROCESSED" ||
		body["balance"].(map[string]any)["amount"] != wantBalance {
		t.Fatalf("response = %d %s, want a processed operation with balance %s", response.status, response.body, wantBalance)
	}
}

// TestProviderJourneyThroughTheWholeContract percorre o contrato como um provedor e o
// serviço interno o usariam, conferindo o saldo em cada passo e, no fim, o ledger.
func TestProviderJourneyThroughTheWholeContract(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "1000.00")

	bet := f.bet(t, wallet, "bet-1", "25.00")
	f.assertOutcome(t, bet, http.StatusOK, "975.00")

	win := f.submit(t, "provider-a:win-1", referenceBody(wallet, "win-1", "WIN", "100.00", "bet-1"))
	f.assertOutcome(t, win, http.StatusOK, "1075.00")

	refund := f.submit(t, "provider-a:refund-1", referenceBody(wallet, "refund-1", "REFUND", "25.00", "bet-1"))
	f.assertOutcome(t, refund, http.StatusOK, "1100.00")

	secondRefund := f.submit(t, "provider-a:refund-2", referenceBody(wallet, "refund-2", "REFUND", "25.00", "bet-1"))
	if secondRefund.status != http.StatusUnprocessableEntity ||
		secondRefund.object(t)["failureCode"] != "ALREADY_REVERSED" {
		t.Fatalf("second refund = %d %s", secondRefund.status, secondRefund.body)
	}

	if f.balanceOf(t, wallet.id) != "1100.00" {
		t.Fatalf("balance = %s, a rejected refund must not move money", f.balanceOf(t, wallet.id))
	}

	f.assertJourneyReadsAndLedger(t, wallet, bet.object(t)["transactionId"].(string))
}

func (f *fixture) assertJourneyReadsAndLedger(t *testing.T, wallet walletRef, betID string) {
	t.Helper()

	own := f.call(t, http.MethodGet, "/wagering/transactions/"+betID, "provider-a", nil, "")
	foreign := f.call(t, http.MethodGet, "/wagering/transactions/"+betID, "provider-b", nil, "")

	if own.status != http.StatusOK || foreign.status != http.StatusNotFound {
		t.Fatalf("own = %d, foreign = %d", own.status, foreign.status)
	}

	ledger := f.call(t, http.MethodGet, "/wallets/"+wallet.id+"/ledger?limit=50", "internal", nil, "")
	entries := ledger.object(t)["entries"].([]any)

	// abertura (crédito), aposta (débito), vitória (crédito) e reembolso (crédito)
	if len(entries) != 4 {
		t.Fatalf("ledger has %d entries, want 4: %s", len(entries), ledger.body)
	}

	reconciliation := f.call(t, http.MethodPost, "/wallets/"+wallet.id+"/reconciliation", "internal", nil, "").object(t)

	if reconciliation["consistent"] != true || reconciliation["checkedEntries"] != float64(4) ||
		reconciliation["storedBalance"].(map[string]any)["amount"] != "1100.00" {
		t.Fatalf("reconciliation = %v", reconciliation)
	}
}
