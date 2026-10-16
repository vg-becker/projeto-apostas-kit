//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
)

func (f *fixture) betsAt(t *testing.T, walletID identity.WalletID, count int) {
	t.Helper()

	for index := range count {
		at := baseTime.Add(time.Duration(index+1) * time.Second)
		name := string(rune('a' + index))

		status, err := f.placeBet(context.Background(), walletID, "bet-"+name, 100, at)
		if err != nil || status != wager.Processed {
			t.Fatalf("bet %s: status = %s, error = %v", name, status, err)
		}
	}
}

func listAll(t *testing.T, f *fixture, walletID identity.WalletID, limit int) []ledger.Entry {
	t.Helper()

	var all []ledger.Entry
	cursor := ""
	for range 100 {
		page, err := f.ledger.ListByWallet(
			context.Background(),
			port.LedgerQuery{WalletID: walletID, Cursor: cursor, Limit: limit},
		)
		if err != nil {
			t.Fatalf("ListByWallet error = %v", err)
		}

		all = append(all, page.Entries...)
		if page.NextCursor == "" {
			return all
		}

		cursor = page.NextCursor
	}

	t.Fatal("pagination never ended")

	return nil
}

func TestAppendRejectsDuplicateWalletTransactionPair(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	first := listAll(t, f, target.ID(), 10)[0]
	entryID, _ := f.ids.NewEntryID()

	duplicate, err := ledger.NewEntry(ledger.EntryParams{
		ID:            entryID,
		WalletID:      first.WalletID(),
		TransactionID: first.TransactionID(),
		Direction:     ledger.Credit,
		Money:         brl(t, 100),
		BalanceBefore: brl(t, 0),
		BalanceAfter:  brl(t, 100),
		CreatedAt:     baseTime,
	})
	if err != nil {
		t.Fatalf("NewEntry error = %v", err)
	}

	err = f.ledger.Append(context.Background(), duplicate)

	if !errors.Is(err, port.ErrAlreadyExists) {
		t.Fatalf("error = %v, want %v", err, port.ErrAlreadyExists)
	}

	assertConstraint(t, err, "wallet_ledger_entries_wallet_transaction_unique")
}

func TestLedgerRoundTripKeepsEveryField(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	f.betsAt(t, target.ID(), 1)

	entries := listAll(t, f, target.ID(), 10)

	if len(entries) != 2 {
		t.Fatalf("entries = %d, want opening and bet", len(entries))
	}

	debit := entries[1]
	if debit.Direction() != ledger.Debit || debit.Money().Amount() != "1.00" ||
		debit.BalanceBefore().Amount() != "1000.00" || debit.BalanceAfter().Amount() != "999.00" {
		t.Fatalf("debit = %+v", debit)
	}
}

func TestPaginationIsStableWhileEntriesAreAppended(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	f.betsAt(t, target.ID(), 6)
	ctx := context.Background()

	firstPage, err := f.ledger.ListByWallet(ctx, port.LedgerQuery{WalletID: target.ID(), Limit: 3})
	if err != nil || len(firstPage.Entries) != 3 || firstPage.NextCursor == "" {
		t.Fatalf("first page = %d (%v), want 3 with cursor", len(firstPage.Entries), err)
	}

	later := baseTime.Add(time.Hour)
	if _, err := f.placeBet(ctx, target.ID(), "bet-late", 100, later); err != nil {
		t.Fatalf("late bet error = %v", err)
	}

	remaining := listFrom(t, f, target.ID(), firstPage.NextCursor, 3)
	seen := map[string]bool{}
	for _, entry := range append(firstPage.Entries, remaining...) {
		if seen[entry.ID().String()] {
			t.Fatalf("entry %s returned twice", entry.ID())
		}

		seen[entry.ID().String()] = true
	}

	if len(seen) != 8 {
		t.Fatalf("saw %d entries, want opening + 6 bets + late bet = 8", len(seen))
	}
}

func listFrom(
	t *testing.T,
	f *fixture,
	walletID identity.WalletID,
	cursor string,
	limit int,
) []ledger.Entry {
	t.Helper()

	var all []ledger.Entry
	for range 100 {
		page, err := f.ledger.ListByWallet(
			context.Background(),
			port.LedgerQuery{WalletID: walletID, Cursor: cursor, Limit: limit},
		)
		if err != nil {
			t.Fatalf("ListByWallet error = %v", err)
		}

		all = append(all, page.Entries...)
		if page.NextCursor == "" {
			return all
		}

		cursor = page.NextCursor
	}

	t.Fatal("pagination never ended")

	return nil
}

func TestPagesAreOrderedAndSizedByLimit(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	f.betsAt(t, target.ID(), 6)

	small := listAll(t, f, target.ID(), 2)
	large := listAll(t, f, target.ID(), 1000)

	if len(small) != 7 || len(large) != 7 {
		t.Fatalf("small = %d, large = %d, want 7 each", len(small), len(large))
	}

	for index := range small {
		if small[index].ID() != large[index].ID() {
			t.Fatalf("order differs at %d", index)
		}

		if index > 0 && small[index].CreatedAt().Before(small[index-1].CreatedAt()) {
			t.Fatalf("entries out of order at %d", index)
		}
	}
}

func TestInvalidCursorsAreRejected(t *testing.T) {
	f := newFixture(t)
	first := f.openWallet(t, 100000)
	second := f.openWallet(t, 100000)
	f.betsAt(t, first.ID(), 4)
	ctx := context.Background()

	page, err := f.ledger.ListByWallet(ctx, port.LedgerQuery{WalletID: first.ID(), Limit: 2})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("page error = %v, cursor = %q", err, page.NextCursor)
	}

	cursors := map[string]identity.WalletID{
		"garbage":       first.ID(),
		page.NextCursor: second.ID(),
		"e30":           first.ID(),
	}

	for cursor, walletID := range cursors {
		_, err := f.ledger.ListByWallet(ctx, port.LedgerQuery{WalletID: walletID, Cursor: cursor})

		if !errors.Is(err, port.ErrInvalidCursor) {
			t.Fatalf("cursor %q: error = %v, want %v", cursor, err, port.ErrInvalidCursor)
		}
	}
}

func TestCalculateBalanceMatchesAndDetectsDivergence(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	f.betsAt(t, target.ID(), 3)
	ctx := context.Background()

	result, err := f.ledger.CalculateBalance(ctx, target.ID(), money.BRL)
	if err != nil {
		t.Fatalf("CalculateBalance error = %v", err)
	}

	if result.EntryCount != 4 || !result.Balance.Equal(result.StoredBalance) ||
		result.Balance.Amount() != "997.00" {
		t.Fatalf("result = %+v", result)
	}

	_, err = f.pool.Exec(
		ctx,
		`UPDATE wallets SET balance_minor_units = balance_minor_units + 1 WHERE id = $1`,
		target.ID().String(),
	)
	if err != nil {
		t.Fatalf("corrupt balance: %v", err)
	}

	diverged, err := f.ledger.CalculateBalance(ctx, target.ID(), money.BRL)
	if err != nil || diverged.StoredBalance.MinorUnits()-diverged.Balance.MinorUnits() != 1 {
		t.Fatalf("diverged = %+v (%v), want a difference of 1", diverged, err)
	}
}

func TestCalculateBalanceOfUnknownWalletOrCurrency(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	missing, _ := f.ids.NewWalletID()
	ctx := context.Background()

	_, unknown := f.ledger.CalculateBalance(ctx, missing, money.BRL)
	_, otherCurrency := f.ledger.CalculateBalance(ctx, target.ID(), money.USD)

	if !errors.Is(unknown, port.ErrNotFound) || !errors.Is(otherCurrency, port.ErrNotFound) {
		t.Fatalf("errors = %v, %v", unknown, otherCurrency)
	}
}

func TestCalculateBalanceOfWalletWithoutEntries(t *testing.T) {
	f := newFixture(t)
	empty := f.openWallet(t, 0)

	result, err := f.ledger.CalculateBalance(context.Background(), empty.ID(), money.BRL)

	if err != nil || result.EntryCount != 0 || !result.Balance.IsZero() || !result.StoredBalance.IsZero() {
		t.Fatalf("result = %+v (%v)", result, err)
	}
}
