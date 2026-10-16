//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wallet"
)

func assertSameWallet(t *testing.T, got *wallet.Wallet, want *wallet.Wallet) {
	t.Helper()

	gotSnapshot := got.Snapshot()
	wantSnapshot := want.Snapshot()

	if gotSnapshot.ID != wantSnapshot.ID ||
		gotSnapshot.PlayerID != wantSnapshot.PlayerID ||
		!gotSnapshot.Balance.Equal(wantSnapshot.Balance) ||
		gotSnapshot.Version != wantSnapshot.Version ||
		!gotSnapshot.CreatedAt.Equal(wantSnapshot.CreatedAt) ||
		!gotSnapshot.UpdatedAt.Equal(wantSnapshot.UpdatedAt) {
		t.Fatalf("wallet = %+v, want %+v", gotSnapshot, wantSnapshot)
	}
}

func TestWalletRoundTrip(t *testing.T) {
	f := newFixture(t)
	created := f.openWallet(t, 100000)

	got, err := f.wallets.Get(context.Background(), created.ID())
	if err != nil {
		t.Fatalf("Get error = %v", err)
	}

	assertSameWallet(t, got, created)

	byPlayer, err := f.wallets.FindByPlayerAndCurrency(
		context.Background(),
		created.PlayerID(),
		money.BRL,
	)
	if err != nil {
		t.Fatalf("FindByPlayerAndCurrency error = %v", err)
	}

	assertSameWallet(t, byPlayer, created)
}

func TestWalletNotFound(t *testing.T) {
	f := newFixture(t)
	missing, _ := f.ids.NewWalletID()
	player := f.newPlayerID(t)

	_, getErr := f.wallets.Get(context.Background(), missing)
	_, lockErr := f.wallets.GetForUpdate(context.Background(), missing)
	_, findErr := f.wallets.FindByPlayerAndCurrency(context.Background(), player, money.BRL)

	for _, err := range []error{getErr, lockErr, findErr} {
		if !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("error = %v, want %v", err, port.ErrNotFound)
		}
	}
}

func TestWalletUniquePerPlayerAndCurrency(t *testing.T) {
	f := newFixture(t)
	first := f.openWallet(t, 0)

	secondID, _ := f.ids.NewWalletID()
	duplicate, err := wallet.Open(wallet.OpenParams{
		ID:             secondID,
		PlayerID:       first.PlayerID(),
		InitialBalance: brl(t, 0),
		Now:            baseTime,
	})
	if err != nil {
		t.Fatalf("wallet.Open error = %v", err)
	}

	err = f.wallets.Create(context.Background(), duplicate.Wallet)

	if !errors.Is(err, port.ErrAlreadyExists) {
		t.Fatalf("error = %v, want %v", err, port.ErrAlreadyExists)
	}
}

func TestWalletUpdateChecksVersion(t *testing.T) {
	f := newFixture(t)
	created := f.openWallet(t, 100000)
	ctx := context.Background()

	first, _ := f.wallets.Get(ctx, created.ID())
	stale, _ := f.wallets.Get(ctx, created.ID())

	f.creditWallet(t, first, 500)
	if err := f.wallets.Update(ctx, first, 1); err != nil {
		t.Fatalf("first update error = %v", err)
	}

	f.creditWallet(t, stale, 700)
	err := f.wallets.Update(ctx, stale, 1)

	if !errors.Is(err, port.ErrVersionConflict) {
		t.Fatalf("stale update error = %v, want %v", err, port.ErrVersionConflict)
	}

	current, _ := f.wallets.Get(ctx, created.ID())
	if current.Balance().MinorUnits() != 100500 || current.Version() != 2 {
		t.Fatalf("lost update: %+v", current.Snapshot())
	}
}

func TestWalletUpdateOfUnknownWalletConflicts(t *testing.T) {
	f := newFixture(t)
	created := f.openWallet(t, 100)
	ghostID, _ := f.ids.NewWalletID()
	ghost, err := wallet.Rehydrate(wallet.Snapshot{
		ID:        ghostID,
		PlayerID:  created.PlayerID(),
		Balance:   brl(t, 1),
		Version:   2,
		CreatedAt: baseTime,
		UpdatedAt: baseTime,
	})
	if err != nil {
		t.Fatalf("rehydrate error = %v", err)
	}

	err = f.wallets.Update(context.Background(), ghost, 1)

	if !errors.Is(err, port.ErrVersionConflict) {
		t.Fatalf("error = %v, want %v", err, port.ErrVersionConflict)
	}
}

func (f *fixture) creditWallet(t *testing.T, target *wallet.Wallet, minorUnits int64) {
	t.Helper()

	transactionID, _ := f.ids.NewTransactionID()
	entryID, _ := f.ids.NewEntryID()

	_, err := target.Credit(wallet.MovementParams{
		TransactionID: identity.TransactionID(transactionID),
		EntryID:       entryID,
		Amount:        brl(t, minorUnits),
		Now:           baseTime,
	})
	if err != nil {
		t.Fatalf("credit error = %v", err)
	}
}
