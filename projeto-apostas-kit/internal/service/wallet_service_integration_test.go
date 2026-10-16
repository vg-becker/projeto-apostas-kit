//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"apostas-go/internal/model/event"
	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
	"apostas-go/internal/service"
)

func (h *harness) open(t *testing.T, playerID string, minorUnits int64) (*wallet.Wallet, error) {
	t.Helper()

	return h.wallets.OpenWallet(context.Background(), service.OpenWalletCommand{
		PlayerID:       identityPlayer(playerID),
		InitialBalance: brl(t, minorUnits),
	})
}

func TestOpenWalletWithPositiveBalanceWritesEverythingTogether(t *testing.T) {
	h := newHarness(t)
	player := h.newPlayerID(t)

	opened, err := h.wallets.OpenWallet(context.Background(), service.OpenWalletCommand{
		PlayerID:       player,
		InitialBalance: brl(t, 100000),
		CorrelationID:  "correlation-1",
	})
	if err != nil {
		t.Fatalf("OpenWallet error = %v", err)
	}

	if opened.Version() != 1 || opened.Balance().Amount() != "1000.00" {
		t.Fatalf("wallet = %+v", opened.Snapshot())
	}

	h.assertOpeningRecords(t, opened)
	h.assertOpeningEvents(t, opened)
}

func (h *harness) assertOpeningRecords(t *testing.T, opened *wallet.Wallet) {
	t.Helper()

	id := opened.ID().String()

	counts := map[string]int{
		"wallets":          h.count(t, `SELECT count(*) FROM wallets WHERE id = $1`, id),
		"opening txns":     h.count(t, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind = 'OPENING' AND status = 'PROCESSED'`, id),
		"credit entries":   h.count(t, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'CREDIT' AND balance_before_minor_units = 0 AND balance_after_minor_units = 100000`, id),
		"outbox events":    h.count(t, `SELECT count(*) FROM outbox_events`),
		"external columns": h.count(t, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND provider_id IS NOT NULL`, id),
	}

	want := map[string]int{
		"wallets": 1, "opening txns": 1, "credit entries": 1, "outbox events": 2, "external columns": 0,
	}
	for name, expected := range want {
		if counts[name] != expected {
			t.Fatalf("%s = %d, want %d (all: %v)", name, counts[name], expected, counts)
		}
	}
}

func (h *harness) assertOpeningEvents(t *testing.T, opened *wallet.Wallet) {
	t.Helper()

	records, err := h.outbox.Claim(context.Background(), port.ClaimParams{
		Limit: 10,
		Now:   baseTime,
		Lease: time.Minute,
	})
	if err != nil || len(records) != 2 {
		t.Fatalf("claimed %d (%v), want 2", len(records), err)
	}

	types := map[event.Type]map[string]any{}
	for _, record := range records {
		var decoded map[string]any
		if err := json.Unmarshal(record.Payload, &decoded); err != nil {
			t.Fatalf("payload: %v", err)
		}

		types[record.EventType] = decoded
	}

	processed := types[event.TypeWagerTransactionProcessed]["data"].(map[string]any)
	changed := types[event.TypeWalletBalanceChanged]["data"].(map[string]any)

	if processed["kind"] != string(wager.Opening) || processed["walletId"] != opened.ID().String() {
		t.Fatalf("processed = %v", processed)
	}

	if changed["direction"] != string(ledger.Credit) || changed["walletVersion"] != float64(1) {
		t.Fatalf("changed = %v", changed)
	}

	if changed["balanceAfter"].(map[string]any)["amount"] != "1000.00" {
		t.Fatalf("balanceAfter = %v", changed["balanceAfter"])
	}
}

func TestOpenWalletWithZeroBalanceCreatesOnlyTheWallet(t *testing.T) {
	h := newHarness(t)

	opened, err := h.wallets.OpenWallet(context.Background(), service.OpenWalletCommand{
		PlayerID:       h.newPlayerID(t),
		InitialBalance: brl(t, 0),
	})
	if err != nil {
		t.Fatalf("OpenWallet error = %v", err)
	}

	if opened.Version() != 1 || !opened.Balance().IsZero() {
		t.Fatalf("wallet = %+v", opened.Snapshot())
	}

	for _, table := range []string{"wager_transactions", "wallet_ledger_entries", "outbox_events"} {
		if total := h.count(t, `SELECT count(*) FROM `+table); total != 0 {
			t.Fatalf("%s has %d rows, want 0 for a zero opening", table, total)
		}
	}
}

func TestSecondWalletForSamePlayerAndCurrencyConflictsWithoutSideEffects(t *testing.T) {
	h := newHarness(t)
	player := h.newPlayerID(t)
	command := service.OpenWalletCommand{PlayerID: player, InitialBalance: brl(t, 5000)}

	if _, err := h.wallets.OpenWallet(context.Background(), command); err != nil {
		t.Fatalf("first open error = %v", err)
	}

	_, err := h.wallets.OpenWallet(context.Background(), command)

	if !errors.Is(err, service.ErrWalletExists) {
		t.Fatalf("error = %v, want %v", err, service.ErrWalletExists)
	}

	if h.count(t, `SELECT count(*) FROM wallet_ledger_entries`) != 1 ||
		h.count(t, `SELECT count(*) FROM outbox_events`) != 2 ||
		h.count(t, `SELECT count(*) FROM wager_transactions`) != 1 {
		t.Fatal("the rejected opening left side effects behind")
	}
}

func TestConcurrentOpeningsForOnePlayerCreateExactlyOneWallet(t *testing.T) {
	h := newHarness(t)
	player := h.newPlayerID(t)
	const attempts = 10

	errs := make([]error, attempts)
	start := make(chan struct{})

	var group sync.WaitGroup
	for index := range attempts {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start

			_, errs[index] = h.wallets.OpenWallet(context.Background(), service.OpenWalletCommand{
				PlayerID:       player,
				InitialBalance: brl(t, 10000),
			})
		}()
	}

	close(start)
	group.Wait()

	succeeded, conflicted := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, service.ErrWalletExists):
			conflicted++
		default:
			t.Fatalf("unexpected error = %v", err)
		}
	}

	if succeeded != 1 || conflicted != attempts-1 {
		t.Fatalf("succeeded = %d, conflicted = %d", succeeded, conflicted)
	}

	if h.count(t, `SELECT count(*) FROM wallets`) != 1 ||
		h.count(t, `SELECT count(*) FROM wallet_ledger_entries`) != 1 ||
		h.count(t, `SELECT count(*) FROM outbox_events`) != 2 {
		t.Fatal("concurrent openings duplicated the initial credit")
	}
}

func TestOutboxFailureRollsBackTheWholeOpening(t *testing.T) {
	h := newHarness(t)
	broken := h.build(failingOutbox{OutboxRepository: h.outbox})

	_, err := broken.OpenWallet(context.Background(), service.OpenWalletCommand{
		PlayerID:       h.newPlayerID(t),
		InitialBalance: brl(t, 10000),
	})

	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want %v", err, errBoom)
	}

	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries", "outbox_events"} {
		if total := h.count(t, `SELECT count(*) FROM `+table); total != 0 {
			t.Fatalf("%s has %d rows after rollback, want 0", table, total)
		}
	}
}

func TestOpenWalletRejectsInvalidInputBeforeTouchingTheDatabase(t *testing.T) {
	h := newHarness(t)

	tests := []struct {
		name    string
		command service.OpenWalletCommand
		wantErr error
	}{
		{
			name:    "negative balance",
			command: service.OpenWalletCommand{PlayerID: h.newPlayerID(t), InitialBalance: brl(t, -1)},
			wantErr: wallet.ErrInvalidState,
		},
		{
			name:    "uninitialized balance",
			command: service.OpenWalletCommand{PlayerID: h.newPlayerID(t)},
			wantErr: wallet.ErrInvalidState,
		},
		{
			name:    "missing player",
			command: service.OpenWalletCommand{InitialBalance: brl(t, 100)},
			wantErr: wallet.ErrInvalidState,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := h.wallets.OpenWallet(context.Background(), test.command)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}

	if h.count(t, `SELECT count(*) FROM wallets`) != 0 {
		t.Fatal("invalid openings must not persist anything")
	}
}

func TestSamePlayerCanHaveWalletsInDifferentCurrencies(t *testing.T) {
	h := newHarness(t)
	player := h.newPlayerID(t)
	usd, err := money.New(1000, money.USD)
	if err != nil {
		t.Fatalf("money.New error = %v", err)
	}

	_, brlErr := h.wallets.OpenWallet(context.Background(), service.OpenWalletCommand{
		PlayerID:       player,
		InitialBalance: brl(t, 1000),
	})
	_, usdErr := h.wallets.OpenWallet(context.Background(), service.OpenWalletCommand{
		PlayerID:       player,
		InitialBalance: usd,
	})

	if brlErr != nil || usdErr != nil || h.count(t, `SELECT count(*) FROM wallets`) != 2 {
		t.Fatalf("errors = %v, %v", brlErr, usdErr)
	}
}

func TestGetWallet(t *testing.T) {
	h := newHarness(t)
	opened, err := h.wallets.OpenWallet(context.Background(), service.OpenWalletCommand{
		PlayerID:       h.newPlayerID(t),
		InitialBalance: brl(t, 2500),
	})
	if err != nil {
		t.Fatalf("OpenWallet error = %v", err)
	}

	found, err := h.wallets.GetWallet(context.Background(), opened.ID())
	if err != nil || found.Balance().Amount() != "25.00" {
		t.Fatalf("found = %+v (%v)", found, err)
	}

	missing, _ := h.ids.NewWalletID()
	_, err = h.wallets.GetWallet(context.Background(), missing)

	if !errors.Is(err, service.ErrWalletNotFound) {
		t.Fatalf("error = %v, want %v", err, service.ErrWalletNotFound)
	}
}

func TestTransientDatabaseFailuresAreRecognizable(t *testing.T) {
	if !service.IsTransient(errors.Join(errBoom, port.ErrTransient)) {
		t.Fatal("port.ErrTransient must be recognized")
	}

	if service.IsTransient(errBoom) {
		t.Fatal("an unrelated error must not be transient")
	}
}
