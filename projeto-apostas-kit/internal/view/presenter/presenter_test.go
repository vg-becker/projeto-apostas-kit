package presenter

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
	"apostas-go/internal/service"
)

const (
	walletID      identity.WalletID      = "0192f291-27dd-7d3f-8071-5f8685deef37"
	playerID      identity.PlayerID      = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	transactionID identity.TransactionID = "0192f298-345e-7e38-af88-e43f851a819d"
	entryID       identity.EntryID       = "0192f299-0000-7e38-af88-e43f851a819d"
)

var instant = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func brl(t *testing.T, minorUnits int64) money.Money {
	t.Helper()

	amount, err := money.New(minorUnits, money.BRL)
	if err != nil {
		t.Fatalf("money.New error = %v", err)
	}

	return amount
}

func encode(t *testing.T, value any) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal error = %v", err)
	}

	return string(encoded)
}

func TestWalletMatchesTheContractExample(t *testing.T) {
	opened, err := wallet.Open(wallet.OpenParams{
		ID:                   walletID,
		PlayerID:             playerID,
		InitialBalance:       brl(t, 100000),
		OpeningTransactionID: transactionID,
		OpeningEntryID:       entryID,
		Now:                  instant,
	})
	if err != nil {
		t.Fatalf("wallet.Open error = %v", err)
	}

	got := encode(t, Wallet(opened.Wallet))
	want := `{"id":"0192f291-27dd-7d3f-8071-5f8685deef37",` +
		`"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",` +
		`"balance":{"amount":"1000.00","currency":"BRL"},"version":1}`

	if got != want {
		t.Fatalf("json = %s\nwant   %s", got, want)
	}
}

func TestOperationAndItsHTTPStatus(t *testing.T) {
	tests := []struct {
		name       string
		result     service.OperationResult
		wantStatus int
		wantJSON   string
	}{
		{
			name: "processed",
			result: service.OperationResult{
				TransactionID: transactionID,
				Status:        wager.Processed,
				Balance:       brl(t, 97500),
			},
			wantStatus: http.StatusOK,
			wantJSON: `{"transactionId":"0192f298-345e-7e38-af88-e43f851a819d","status":"PROCESSED",` +
				`"balance":{"amount":"975.00","currency":"BRL"},"idempotentReplay":false}`,
		},
		{
			name: "processed replay",
			result: service.OperationResult{
				TransactionID:    transactionID,
				Status:           wager.Processed,
				Balance:          brl(t, 97500),
				IdempotentReplay: true,
			},
			wantStatus: http.StatusOK,
			wantJSON: `{"transactionId":"0192f298-345e-7e38-af88-e43f851a819d","status":"PROCESSED",` +
				`"balance":{"amount":"975.00","currency":"BRL"},"idempotentReplay":true}`,
		},
		{
			name: "pending reference",
			result: service.OperationResult{
				TransactionID: transactionID,
				Status:        wager.PendingReference,
			},
			wantStatus: http.StatusAccepted,
			wantJSON: `{"transactionId":"0192f298-345e-7e38-af88-e43f851a819d",` +
				`"status":"PENDING_REFERENCE","idempotentReplay":false}`,
		},
		{
			name: "pending",
			result: service.OperationResult{
				TransactionID: transactionID,
				Status:        wager.Pending,
			},
			wantStatus: http.StatusAccepted,
			wantJSON: `{"transactionId":"0192f298-345e-7e38-af88-e43f851a819d",` +
				`"status":"PENDING","idempotentReplay":false}`,
		},
		{
			name: "rejected",
			result: service.OperationResult{
				TransactionID: transactionID,
				Status:        wager.Rejected,
				FailureCode:   wager.InsufficientFunds,
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantJSON: `{"transactionId":"0192f298-345e-7e38-af88-e43f851a819d","status":"REJECTED",` +
				`"failureCode":"INSUFFICIENT_FUNDS","correctable":false,"idempotentReplay":false}`,
		},
		{
			name: "failed",
			result: service.OperationResult{
				TransactionID: transactionID,
				Status:        wager.Failed,
				FailureCode:   wager.InfrastructureFailure,
			},
			wantStatus: http.StatusInternalServerError,
			wantJSON: `{"transactionId":"0192f298-345e-7e38-af88-e43f851a819d","status":"FAILED",` +
				`"failureCode":"INFRASTRUCTURE_FAILURE","correctable":false,"idempotentReplay":false}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := OperationStatus(test.result); got != test.wantStatus {
				t.Fatalf("status = %d, want %d", got, test.wantStatus)
			}

			if got := encode(t, Operation(test.result)); got != test.wantJSON {
				t.Fatalf("json = %s\nwant   %s", got, test.wantJSON)
			}
		})
	}
}

func TestACorrectableCodeIsFlaggedAsCorrectable(t *testing.T) {
	body := Operation(service.OperationResult{
		TransactionID: transactionID,
		Status:        wager.Rejected,
		FailureCode:   wager.CurrencyMismatch,
	})

	if body.Correctable == nil || !*body.Correctable {
		t.Fatalf("correctable = %v, want true for CURRENCY_MISMATCH", body.Correctable)
	}
}

func newBet(t *testing.T) *wager.Transaction {
	t.Helper()

	transaction, err := wager.NewExternal(wager.ExternalParams{
		ID:                    transactionID,
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		PayloadHash:           strings.Repeat("ab", 32),
		WalletID:              walletID,
		PlayerID:              playerID,
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  wager.Bet,
		Money:                 brl(t, 2500),
		Now:                   instant,
	})
	if err != nil {
		t.Fatalf("wager.NewExternal error = %v", err)
	}

	return transaction
}

func TestTransactionDetailForAPendingAndAProcessedBet(t *testing.T) {
	bet := newBet(t)

	pending := Transaction(bet)
	if pending.Status != "PENDING" || pending.Balance != nil || pending.FailureCode != "" ||
		pending.ProviderID != "provider-a" || pending.CreatedAt != "2026-09-08T12:00:00Z" {
		t.Fatalf("pending = %+v", pending)
	}

	if err := bet.MarkProcessed(wager.ProcessedResult{Balance: brl(t, 97500)}, instant); err != nil {
		t.Fatalf("MarkProcessed error = %v", err)
	}

	processed := Transaction(bet)
	if processed.Status != "PROCESSED" || processed.Balance == nil || processed.Balance.Amount != "975.00" ||
		processed.Money.Amount != "25.00" {
		t.Fatalf("processed = %+v", processed)
	}
}

func TestTransactionDetailOfARejectedBetCarriesTheCode(t *testing.T) {
	bet := newBet(t)
	if err := bet.Reject(wager.InsufficientFunds, instant); err != nil {
		t.Fatalf("Reject error = %v", err)
	}

	body := Transaction(bet)

	if body.FailureCode != "INSUFFICIENT_FUNDS" || body.Correctable == nil || *body.Correctable ||
		body.Balance != nil {
		t.Fatalf("body = %+v", body)
	}
}

func TestOpeningTransactionHasNoExternalMetadata(t *testing.T) {
	opening, err := wager.NewOpening(wager.OpeningParams{
		ID:       transactionID,
		WalletID: walletID,
		PlayerID: playerID,
		Money:    brl(t, 100000),
		Now:      instant,
	})
	if err != nil {
		t.Fatalf("wager.NewOpening error = %v", err)
	}

	encoded := encode(t, Transaction(opening))

	for _, field := range []string{"providerId", "externalTransactionId", "roundId", "gameId"} {
		if strings.Contains(encoded, field) {
			t.Fatalf("opening must omit %s: %s", field, encoded)
		}
	}
}

func TestLedgerPageIsNeverNullAndKeepsTheCursor(t *testing.T) {
	empty := encode(t, LedgerPage(port.LedgerPage{}))
	if empty != `{"entries":[]}` {
		t.Fatalf("empty page = %s", empty)
	}

	entry, err := ledger.NewEntry(ledger.EntryParams{
		ID:            entryID,
		WalletID:      walletID,
		TransactionID: transactionID,
		Direction:     ledger.Debit,
		Money:         brl(t, 2500),
		BalanceBefore: brl(t, 100000),
		BalanceAfter:  brl(t, 97500),
		CreatedAt:     instant,
	})
	if err != nil {
		t.Fatalf("ledger.NewEntry error = %v", err)
	}

	page := LedgerPage(port.LedgerPage{Entries: []ledger.Entry{entry}, NextCursor: "abc"})

	if len(page.Entries) != 1 || page.NextCursor != "abc" || page.Entries[0].Direction != "DEBIT" ||
		page.Entries[0].BalanceAfter.Amount != "975.00" {
		t.Fatalf("page = %+v", page)
	}
}

func TestReconciliationMatchesTheContractExample(t *testing.T) {
	body := Reconciliation(service.ReconciliationResult{
		WalletID:          walletID,
		StoredBalance:     brl(t, 97500),
		CalculatedBalance: brl(t, 97500),
		Difference:        brl(t, 0),
		Consistent:        true,
		CheckedEntries:    2,
	})

	want := `{"walletId":"0192f291-27dd-7d3f-8071-5f8685deef37",` +
		`"storedBalance":{"amount":"975.00","currency":"BRL"},` +
		`"calculatedBalance":{"amount":"975.00","currency":"BRL"},` +
		`"difference":{"amount":"0.00","currency":"BRL"},"consistent":true,"checkedEntries":2}`

	if got := encode(t, body); got != want {
		t.Fatalf("json = %s\nwant   %s", got, want)
	}
}

func TestHealthReportsEachDependencyAndTheOverallStatus(t *testing.T) {
	tests := []struct {
		name       string
		results    []HealthResult
		wantStatus int
		wantJSON   string
	}{
		{
			name:       "all up",
			results:    []HealthResult{{Name: "postgres", Up: true}, {Name: "sqs", Up: true}},
			wantStatus: http.StatusOK,
			wantJSON:   `{"status":"UP","checks":{"postgres":"UP","sqs":"UP"}}`,
		},
		{
			name:       "one down",
			results:    []HealthResult{{Name: "postgres", Up: true}, {Name: "sqs", Up: false}},
			wantStatus: http.StatusServiceUnavailable,
			wantJSON:   `{"status":"DOWN","checks":{"postgres":"UP","sqs":"DOWN"}}`,
		},
		{
			name:       "nothing configured",
			wantStatus: http.StatusOK,
			wantJSON:   `{"status":"UP"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, body := Health(test.results)

			if status != test.wantStatus || encode(t, body) != test.wantJSON {
				t.Fatalf("got %d %s, want %d %s", status, encode(t, body), test.wantStatus, test.wantJSON)
			}
		})
	}
}
