//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/service"
)

func (h *harness) openingTransactionID(t *testing.T, id identity.WalletID) identity.TransactionID {
	t.Helper()

	var opening string
	row := h.pool.QueryRow(
		context.Background(),
		`SELECT id::text FROM wager_transactions WHERE wallet_id = $1 AND kind = 'OPENING'`,
		id.String(),
	)
	if err := row.Scan(&opening); err != nil {
		t.Fatalf("opening transaction: %v", err)
	}

	return identity.TransactionID(opening)
}

func TestProviderReadsItsOwnTransactions(t *testing.T) {
	h := newHarnessWithPolicy(t, testPolicy)
	target := h.openFunded(t, 100000)
	bet := h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	rejected := h.process(t, operation(t, target, wager.Bet, "bet-2", 99999999))
	waiting := h.waitingRefund(t, target, "refund-1", 2500, "missing")
	scope := service.ProviderScope("provider-a")
	ctx := context.Background()

	byID, err := h.queries.GetTransaction(ctx, scope, bet.TransactionID)
	if err != nil || byID.Status() != wager.Processed || byID.ResultBalance().Amount() != "975.00" {
		t.Fatalf("by id = %+v (%v)", byID, err)
	}

	byExternal, err := h.queries.GetProviderTransaction(ctx, scope, "provider-a", "bet-2")
	if err != nil || byExternal.ID() != rejected.TransactionID ||
		byExternal.FailureCode() != wager.InsufficientFunds {
		t.Fatalf("by external id = %+v (%v), want the rejection code visible", byExternal, err)
	}

	pending, err := h.queries.GetTransaction(ctx, scope, waiting.TransactionID)
	if err != nil || pending.Status() != wager.PendingReference {
		t.Fatalf("pending = %+v (%v), the provider must be able to follow pendings", pending, err)
	}
}

func TestAProviderCannotSeeAnotherProvidersTransactions(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	bet := h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	other := service.ProviderScope("provider-b")
	ctx := context.Background()

	_, byID := h.queries.GetTransaction(ctx, other, bet.TransactionID)
	_, ownPathOtherData := h.queries.GetProviderTransaction(ctx, other, "provider-b", "bet-1")
	_, foreignPath := h.queries.GetProviderTransaction(ctx, other, "provider-a", "bet-1")

	if !errors.Is(byID, service.ErrTransactionNotFound) {
		t.Fatalf("by id error = %v, want %v (existence must not leak)", byID, service.ErrTransactionNotFound)
	}

	if !errors.Is(ownPathOtherData, service.ErrTransactionNotFound) {
		t.Fatalf("own path error = %v, want %v", ownPathOtherData, service.ErrTransactionNotFound)
	}

	if !errors.Is(foreignPath, service.ErrAccessDenied) {
		t.Fatalf("foreign path error = %v, want %v", foreignPath, service.ErrAccessDenied)
	}
}

func TestOpeningTransactionsAreInternalOnly(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	opening := h.openingTransactionID(t, target.ID())
	ctx := context.Background()

	_, asProvider := h.queries.GetTransaction(ctx, service.ProviderScope("provider-a"), opening)
	found, asInternal := h.queries.GetTransaction(ctx, service.InternalScope(), opening)

	if !errors.Is(asProvider, service.ErrTransactionNotFound) {
		t.Fatalf("provider error = %v, want %v", asProvider, service.ErrTransactionNotFound)
	}

	if asInternal != nil || found.Kind() != wager.Opening {
		t.Fatalf("internal = %+v (%v)", found, asInternal)
	}
}

func TestInternalScopeReadsAnyProvider(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	bet := h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	ctx := context.Background()

	byID, idErr := h.queries.GetTransaction(ctx, service.InternalScope(), bet.TransactionID)
	byPath, pathErr := h.queries.GetProviderTransaction(ctx, service.InternalScope(), "provider-a", "bet-1")

	if idErr != nil || pathErr != nil || byID.ID() != byPath.ID() {
		t.Fatalf("errors = %v, %v", idErr, pathErr)
	}
}

func TestAnEmptyScopeIsDeniedEverywhere(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	bet := h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	ctx := context.Background()

	_, byID := h.queries.GetTransaction(ctx, service.AccessScope{}, bet.TransactionID)
	_, byPath := h.queries.GetProviderTransaction(ctx, service.AccessScope{}, "provider-a", "bet-1")

	if !errors.Is(byID, service.ErrAccessDenied) || !errors.Is(byPath, service.ErrAccessDenied) {
		t.Fatalf("errors = %v, %v, want %v", byID, byPath, service.ErrAccessDenied)
	}
}

func TestUnknownTransactionsAreNotFound(t *testing.T) {
	h := newHarness(t)
	missing, _ := h.ids.NewTransactionID()
	ctx := context.Background()

	_, byID := h.queries.GetTransaction(ctx, service.InternalScope(), missing)
	_, byPath := h.queries.GetProviderTransaction(ctx, service.ProviderScope("provider-a"), "provider-a", "nope")

	if !errors.Is(byID, service.ErrTransactionNotFound) || !errors.Is(byPath, service.ErrTransactionNotFound) {
		t.Fatalf("errors = %v, %v", byID, byPath)
	}
}

func TestAProviderReplayingAnotherProvidersKeyGetsItsOwnOperation(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	original := operation(t, target, wager.Bet, "bet-1", 2500)
	first := h.process(t, original)

	copied := original
	copied.ProviderID = "provider-b"

	second := h.process(t, copied)

	if second.IdempotentReplay || second.TransactionID == first.TransactionID {
		t.Fatalf("second = %+v, a replay must never cross providers", second)
	}

	stillTheOriginal, err := h.queries.GetTransaction(
		context.Background(),
		service.ProviderScope("provider-a"),
		first.TransactionID,
	)
	if err != nil || stillTheOriginal.Status() != wager.Processed {
		t.Fatalf("provider-a data changed: %+v (%v)", stillTheOriginal, err)
	}
}

func TestLedgerListingThroughTheService(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	h.process(t, operation(t, target, wager.Win, "win-1", 1000))
	ctx := context.Background()

	first, err := h.queries.ListLedger(ctx, port.LedgerQuery{WalletID: target.ID(), Limit: 2})
	if err != nil || len(first.Entries) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %d (%v)", len(first.Entries), err)
	}

	second, err := h.queries.ListLedger(
		ctx,
		port.LedgerQuery{WalletID: target.ID(), Cursor: first.NextCursor, Limit: 2},
	)
	if err != nil || len(second.Entries) != 1 || second.NextCursor != "" {
		t.Fatalf("second page = %d (%v)", len(second.Entries), err)
	}

	missing, _ := h.ids.NewWalletID()
	if _, err := h.queries.ListLedger(ctx, port.LedgerQuery{WalletID: missing}); !errors.Is(err, service.ErrWalletNotFound) {
		t.Fatalf("unknown wallet error = %v, want %v", err, service.ErrWalletNotFound)
	}

	_, err = h.queries.ListLedger(ctx, port.LedgerQuery{WalletID: target.ID(), Cursor: "garbage"})
	if !errors.Is(err, port.ErrInvalidCursor) {
		t.Fatalf("cursor error = %v, want %v", err, port.ErrInvalidCursor)
	}
}
