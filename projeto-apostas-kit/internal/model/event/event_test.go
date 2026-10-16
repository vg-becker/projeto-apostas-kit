package event

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/wager"
)

const (
	eventID       identity.EventID       = "0192f2a0-0000-7e38-af88-e43f851a819d"
	transactionID identity.TransactionID = "0192f298-345e-7e38-af88-e43f851a819d"
	walletID      identity.WalletID      = "0192f291-27dd-7d3f-8071-5f8685deef37"
	playerID      identity.PlayerID      = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	entryID       identity.EntryID       = "0192f299-0000-7e38-af88-e43f851a819d"
)

var occurred = time.Date(2026, 9, 8, 9, 0, 0, 0, time.FixedZone("BRT", -3*3600))

func brl(t *testing.T, minorUnits int64) money.Money {
	t.Helper()

	amount, err := money.New(minorUnits, money.BRL)
	if err != nil {
		t.Fatalf("money.New error = %v", err)
	}

	return amount
}

func header() Header {
	return Header{
		EventID:       eventID,
		CorrelationID: "correlation-1",
		Now:           occurred,
	}
}

func newBet(t *testing.T, kind wager.Kind, reference string) *wager.Transaction {
	t.Helper()

	transaction, err := wager.NewExternal(wager.ExternalParams{
		ID:                             transactionID,
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "transaction-123",
		IdempotencyKey:                 "provider-a:transaction-123",
		PayloadHash:                    strings.Repeat("ab", 32),
		WalletID:                       walletID,
		PlayerID:                       playerID,
		RoundID:                        "round-987",
		GameID:                         "fortune-chimp",
		Kind:                           kind,
		Money:                          brl(t, 2500),
		ReferenceExternalTransactionID: reference,
		Now:                            occurred,
	})
	if err != nil {
		t.Fatalf("NewExternal error = %v", err)
	}

	return transaction
}

func decode(t *testing.T, event Event) map[string]any {
	t.Helper()

	raw, err := event.Marshal()
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal error = %v", err)
	}

	return decoded
}

func TestProcessedEvent(t *testing.T) {
	transaction := newBet(t, wager.Bet, "")
	if err := transaction.MarkProcessed(wager.ProcessedResult{Balance: brl(t, 97500)}, occurred); err != nil {
		t.Fatalf("MarkProcessed error = %v", err)
	}

	envelope, err := NewWagerTransactionProcessed(header(), transaction)
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	decoded := decode(t, envelope)
	data := decoded["data"].(map[string]any)
	balance := data["balance"].(map[string]any)

	if decoded["eventType"] != "WagerTransactionProcessed" || decoded["version"] != float64(1) {
		t.Fatalf("envelope = %v", decoded)
	}

	if decoded["aggregateId"] != string(transactionID) || balance["amount"] != "975.00" {
		t.Fatalf("envelope = %v", decoded)
	}

	if decoded["occurredAt"] != "2026-09-08T12:00:00Z" {
		t.Fatalf("occurredAt = %v, want UTC", decoded["occurredAt"])
	}

	if _, present := decoded["causationId"]; present {
		t.Fatal("causationId must be omitted when empty")
	}
}

func TestProcessedEventForLoss(t *testing.T) {
	params := wager.ExternalParams{
		ID:                    transactionID,
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-9",
		IdempotencyKey:        "provider-a:transaction-9",
		PayloadHash:           strings.Repeat("cd", 32),
		WalletID:              walletID,
		PlayerID:              playerID,
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  wager.Loss,
		Money:                 brl(t, 0),
		Now:                   occurred,
	}

	loss, err := wager.NewExternal(params)
	if err != nil {
		t.Fatalf("NewExternal error = %v", err)
	}

	if err := loss.MarkProcessed(wager.ProcessedResult{Balance: brl(t, 5000)}, occurred); err != nil {
		t.Fatalf("MarkProcessed error = %v", err)
	}

	if _, err := NewWagerTransactionProcessed(header(), loss); err != nil {
		t.Fatalf("LOSS must produce a processed event, error = %v", err)
	}
}

func TestProcessedEventForOpeningOmitsExternalMetadata(t *testing.T) {
	opening, err := wager.NewOpening(wager.OpeningParams{
		ID:       transactionID,
		WalletID: walletID,
		PlayerID: playerID,
		Money:    brl(t, 100000),
		Now:      occurred,
	})
	if err != nil {
		t.Fatalf("NewOpening error = %v", err)
	}

	envelope, err := NewWagerTransactionProcessed(header(), opening)
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	data := decode(t, envelope)["data"].(map[string]any)
	for _, field := range []string{"providerId", "externalTransactionId", "resolvedReferenceId"} {
		if _, present := data[field]; present {
			t.Fatalf("opening event must omit %s", field)
		}
	}

	if data["kind"] != "OPENING" {
		t.Fatalf("kind = %v", data["kind"])
	}
}

func TestRejectedEvent(t *testing.T) {
	transaction := newBet(t, wager.Bet, "")
	if err := transaction.Reject(wager.InsufficientFunds, occurred); err != nil {
		t.Fatalf("Reject error = %v", err)
	}

	header := header()
	header.CausationID = "message-1"

	envelope, err := NewWagerTransactionRejected(header, transaction)
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	decoded := decode(t, envelope)
	data := decoded["data"].(map[string]any)

	if data["failureCode"] != "INSUFFICIENT_FUNDS" || data["correctable"] != false {
		t.Fatalf("data = %v", data)
	}

	if decoded["causationId"] != "message-1" {
		t.Fatalf("causationId = %v", decoded["causationId"])
	}
}

func TestPendingReferenceEvent(t *testing.T) {
	transaction := newBet(t, wager.Refund, "transaction-1")
	if err := transaction.AwaitReference(occurred); err != nil {
		t.Fatalf("AwaitReference error = %v", err)
	}

	envelope, err := NewWagerTransactionPendingReference(header(), transaction)
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	data := decode(t, envelope)["data"].(map[string]any)
	if data["referenceExternalTransactionId"] != "transaction-1" {
		t.Fatalf("data = %v", data)
	}
}

func TestEventsRequireMatchingTransactionState(t *testing.T) {
	pending := newBet(t, wager.Bet, "")

	if _, err := NewWagerTransactionProcessed(header(), pending); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("processed error = %v, want %v", err, ErrInvalidEvent)
	}

	if _, err := NewWagerTransactionRejected(header(), pending); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("rejected error = %v, want %v", err, ErrInvalidEvent)
	}

	if _, err := NewWagerTransactionPendingReference(header(), pending); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("pending reference error = %v, want %v", err, ErrInvalidEvent)
	}
}

func newEntry(t *testing.T) ledger.Entry {
	t.Helper()

	entry, err := ledger.NewEntry(ledger.EntryParams{
		ID:            entryID,
		WalletID:      walletID,
		TransactionID: transactionID,
		Direction:     ledger.Debit,
		Money:         brl(t, 2500),
		BalanceBefore: brl(t, 100000),
		BalanceAfter:  brl(t, 97500),
		CreatedAt:     occurred,
	})
	if err != nil {
		t.Fatalf("NewEntry error = %v", err)
	}

	return entry
}

func TestBalanceChangedEvent(t *testing.T) {
	envelope, err := NewWalletBalanceChanged(header(), newEntry(t), 2)
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	decoded := decode(t, envelope)
	data := decoded["data"].(map[string]any)

	if decoded["eventType"] != "WalletBalanceChanged" || decoded["aggregateId"] != string(walletID) {
		t.Fatalf("envelope = %v", decoded)
	}

	want := map[string]any{
		"walletId":      string(walletID),
		"transactionId": string(transactionID),
		"direction":     "DEBIT",
		"walletVersion": float64(2),
	}
	for field, value := range want {
		if data[field] != value {
			t.Fatalf("%s = %v, want %v", field, data[field], value)
		}
	}

	for _, field := range []string{"money", "balanceBefore", "balanceAfter"} {
		if _, ok := data[field].(map[string]any)["currency"]; !ok {
			t.Fatalf("%s must carry amount and currency", field)
		}
	}
}

func TestBalanceChangedRejectsInvalidInput(t *testing.T) {
	if _, err := NewWalletBalanceChanged(header(), ledger.Entry{}, 2); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("empty entry error = %v, want %v", err, ErrInvalidEvent)
	}

	if _, err := NewWalletBalanceChanged(header(), newEntry(t), 0); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("version error = %v, want %v", err, ErrInvalidEvent)
	}
}

func TestHeaderValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(header *Header)
	}{
		{name: "missing event id", mutate: func(h *Header) { h.EventID = "" }},
		{name: "missing correlation", mutate: func(h *Header) { h.CorrelationID = "" }},
		{name: "blank correlation", mutate: func(h *Header) { h.CorrelationID = " " }},
		{name: "blank causation", mutate: func(h *Header) { h.CausationID = " x" }},
		{name: "missing time", mutate: func(h *Header) { h.Now = time.Time{} }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := header()
			test.mutate(&header)

			_, err := NewWalletBalanceChanged(header, newEntry(t), 2)

			if !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidEvent)
			}
		})
	}
}

func TestMarshalIsStableAndKeepsEventID(t *testing.T) {
	envelope, err := NewWalletBalanceChanged(header(), newEntry(t), 2)
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	first, firstErr := envelope.Marshal()
	second, secondErr := envelope.Marshal()
	if firstErr != nil || secondErr != nil {
		t.Fatalf("marshal errors = %v, %v", firstErr, secondErr)
	}

	if !bytes.Equal(first, second) {
		t.Fatal("marshal must be deterministic")
	}

	if envelope.EventID() != eventID || envelope.Version() != 1 || envelope.CorrelationID() == "" {
		t.Fatalf("accessors wrong: %+v", envelope)
	}

	var _ Event = envelope
}
