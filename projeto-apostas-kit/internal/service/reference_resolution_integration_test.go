//go:build integration

package service_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
	"apostas-go/internal/repository/postgres"
	"apostas-go/internal/service"
)

var testPolicy = service.ReferencePolicy{
	MaxAttempts:    3,
	TTL:            10 * time.Minute,
	InitialBackoff: 2 * time.Second,
	MaxBackoff:     8 * time.Second,
	ClaimLease:     time.Minute,
}

type waitState struct {
	status   string
	attempts int
	next     *time.Time
}

func (h *harness) waitState(t *testing.T, id identity.TransactionID) waitState {
	t.Helper()

	var state waitState
	row := h.pool.QueryRow(
		context.Background(),
		`SELECT status, reference_attempts, next_attempt_at FROM wager_transactions WHERE id = $1`,
		id.String(),
	)
	if err := row.Scan(&state.status, &state.attempts, &state.next); err != nil {
		t.Fatalf("wait state of %s: %v", id, err)
	}

	return state
}

func (h *harness) resolve(t *testing.T) service.ResolutionSummary {
	t.Helper()

	summary, err := h.resolver().ResolveDue(context.Background(), 20)
	if err != nil {
		t.Fatalf("ResolveDue error = %v", err)
	}

	return summary
}

func (h *harness) assertNext(t *testing.T, id identity.TransactionID, attempts int, delay time.Duration) {
	t.Helper()

	state := h.waitState(t, id)
	want := h.clock.Now().Add(delay)

	if state.status != "PENDING_REFERENCE" || state.attempts != attempts ||
		state.next == nil || !state.next.Equal(want) {
		t.Fatalf("state = %+v, want %d attempts and next at %v", state, attempts, want)
	}
}

func (h *harness) waitingRefund(
	t *testing.T,
	target *wallet.Wallet,
	name string,
	minorUnits int64,
	reference string,
) service.OperationResult {
	t.Helper()

	result := h.process(t, referencing(operation(t, target, wager.Refund, name, minorUnits), reference))
	if result.Status != wager.PendingReference || result.IdempotentReplay || result.Balance.IsInitialized() {
		t.Fatalf("refund %s = %+v, want a fresh PENDING_REFERENCE", name, result)
	}

	return result
}

func TestReversalBeforeItsReferenceWaitsAndThenResolves(t *testing.T) {
	h := newHarnessWithPolicy(t, testPolicy)
	target := h.openFunded(t, 100000)
	refundCommand := referencing(operation(t, target, wager.Refund, "refund-1", 2500), "bet-1")

	waiting := h.waitingRefund(t, target, "refund-1", 2500, "bet-1")

	h.assertNext(t, waiting.TransactionID, 1, 2*time.Second)

	if h.eventCount(t, "WagerTransactionPendingReference") != 1 || h.ledgerEntries(t, target.ID(), "DEBIT") != 0 {
		t.Fatal("waiting must emit one pending event and move no money")
	}

	if summary := h.resolve(t); summary.Claimed != 0 {
		t.Fatalf("summary = %+v, nothing is due yet", summary)
	}

	bet := h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	h.clock.Advance(3 * time.Second)

	summary := h.resolve(t)

	if summary.Claimed != 1 || summary.Finished != 1 || summary.Waiting != 0 {
		t.Fatalf("summary = %+v", summary)
	}

	if h.walletState(t, target.ID()).Balance().Amount() != "1000.00" ||
		h.resolvedReference(t, waiting.TransactionID) != bet.TransactionID.String() {
		t.Fatal("the refund must credit the wallet and record the resolved bet")
	}

	replay := h.process(t, refundCommand)
	if replay.Status != wager.Processed || !replay.IdempotentReplay || replay.Balance.Amount() != "1000.00" {
		t.Fatalf("replay = %+v", replay)
	}

	h.assertConsistent(t, target.ID())
	h.assertOriginalTraceSurvived(t, waiting.TransactionID)
}

// assertOriginalTraceSurvived confere que o evento emitido pelo resolver mantém o
// correlationId e o causationId da requisição original.
func (h *harness) assertOriginalTraceSurvived(t *testing.T, id identity.TransactionID) {
	t.Helper()

	var correlation, causation string
	row := h.pool.QueryRow(
		context.Background(),
		`SELECT payload->>'correlationId', payload->>'causationId'
		   FROM outbox_events
		  WHERE event_type = 'WagerTransactionProcessed'
		    AND payload->'data'->>'transactionId' = $1`,
		id.String(),
	)
	if err := row.Scan(&correlation, &causation); err != nil ||
		correlation != "correlation-refund-1" || causation != "message-refund-1" {
		t.Fatalf("trace = %q / %q (%v), want the original request ids", correlation, causation, err)
	}
}

func TestBackoffGrowsAndExpiryByAttemptsRejectsWithNotFound(t *testing.T) {
	h := newHarnessWithPolicy(t, testPolicy)
	target := h.openFunded(t, 100000)
	command := referencing(operation(t, target, wager.Refund, "refund-1", 2500), "bet-1")
	waiting := h.waitingRefund(t, target, "refund-1", 2500, "bet-1")
	h.assertNext(t, waiting.TransactionID, 1, 2*time.Second)

	for _, step := range []struct {
		advance  time.Duration
		attempts int
		delay    time.Duration
	}{
		{advance: 2 * time.Second, attempts: 2, delay: 4 * time.Second},
		{advance: 4 * time.Second, attempts: 3, delay: 8 * time.Second},
	} {
		h.clock.Advance(step.advance)

		if summary := h.resolve(t); summary.Waiting != 1 {
			t.Fatalf("summary = %+v, want the operation still waiting", summary)
		}

		h.assertNext(t, waiting.TransactionID, step.attempts, step.delay)
	}

	h.clock.Advance(8 * time.Second)

	if summary := h.resolve(t); summary.Finished != 1 {
		t.Fatalf("summary = %+v, want the expiry", summary)
	}

	replay := h.process(t, command)
	if replay.Status != wager.Rejected || replay.FailureCode != wager.ReferenceNotFound || !replay.IdempotentReplay {
		t.Fatalf("replay = %+v", replay)
	}

	if h.eventCount(t, "WagerTransactionRejected") != 1 || h.resolve(t).Claimed != 0 {
		t.Fatal("expiry must emit one rejection and leave nothing to claim")
	}
}

func TestExpiryByTTL(t *testing.T) {
	policy := testPolicy
	policy.MaxAttempts = 100
	policy.TTL = time.Minute
	h := newHarnessWithPolicy(t, policy)
	target := h.openFunded(t, 100000)
	waiting := h.waitingRefund(t, target, "refund-1", 2500, "bet-1")

	h.clock.Advance(61 * time.Second)
	summary := h.resolve(t)

	state := h.waitState(t, waiting.TransactionID)
	if summary.Finished != 1 || state.status != "REJECTED" {
		t.Fatalf("summary = %+v, state = %+v", summary, state)
	}
}

func (h *harness) insertPendingBet(t *testing.T, target *wallet.Wallet, name string) {
	t.Helper()

	bet, err := wager.NewExternal(wager.ExternalParams{
		ID:                    mustTransactionID(t, h),
		ProviderID:            "provider-a",
		ExternalTransactionID: name,
		IdempotencyKey:        "provider-a:" + name,
		PayloadHash:           fmt.Sprintf("%x", sha256.Sum256([]byte(name))),
		WalletID:              target.ID(),
		PlayerID:              target.PlayerID(),
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  wager.Bet,
		Money:                 brl(t, 2500),
		Now:                   h.clock.Now(),
	})
	if err != nil {
		t.Fatalf("NewExternal error = %v", err)
	}

	repository := postgres.NewTransactionRepository(postgres.NewConnection(h.pool))
	if err := repository.Insert(context.Background(), bet); err != nil {
		t.Fatalf("insert pending bet: %v", err)
	}
}

func mustTransactionID(t *testing.T, h *harness) identity.TransactionID {
	t.Helper()

	id, err := h.ids.NewTransactionID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}

	return id
}

func TestReferenceThatExistsButNeverFinishesExpiresAsNotProcessed(t *testing.T) {
	policy := testPolicy
	policy.MaxAttempts = 2
	h := newHarnessWithPolicy(t, policy)
	target := h.openFunded(t, 100000)
	h.insertPendingBet(t, target, "bet-1")

	waiting := h.waitingRefund(t, target, "refund-1", 2500, "bet-1")
	h.clock.Advance(2 * time.Second)

	if summary := h.resolve(t); summary.Waiting != 1 {
		t.Fatalf("summary = %+v, want it to keep waiting", summary)
	}

	h.clock.Advance(4 * time.Second)
	h.resolve(t)

	replay := h.process(t, referencing(operation(t, target, wager.Refund, "refund-1", 2500), "bet-1"))
	if replay.Status != wager.Rejected || replay.FailureCode != wager.ReferenceNotProcessed {
		t.Fatalf("replay = %+v (%s), want REFERENCE_NOT_PROCESSED", replay, waiting.TransactionID)
	}
}

func TestReferenceThatEndsUnsuccessfullyRejectsTheWaitingOperation(t *testing.T) {
	h := newHarnessWithPolicy(t, testPolicy)
	target := h.openFunded(t, 100000)
	waiting := h.waitingRefund(t, target, "refund-1", 2500, "bet-1")

	failedBet := h.process(t, operation(t, target, wager.Bet, "bet-1", 2000000))
	h.clock.Advance(3 * time.Second)

	summary := h.resolve(t)

	state := h.waitState(t, waiting.TransactionID)
	if failedBet.Status != wager.Rejected || summary.Finished != 1 || state.status != "REJECTED" {
		t.Fatalf("bet = %+v, summary = %+v, state = %+v", failedBet, summary, state)
	}
}

func TestWinWithReferenceWaitsAndThenCredits(t *testing.T) {
	h := newHarnessWithPolicy(t, testPolicy)
	target := h.openFunded(t, 100000)
	win := h.process(t, referencing(operation(t, target, wager.Win, "win-1", 6000), "bet-1"))
	if win.Status != wager.PendingReference {
		t.Fatalf("win = %+v, want PENDING_REFERENCE", win)
	}

	bet := h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	h.clock.Advance(3 * time.Second)
	summary := h.resolve(t)

	if summary.Finished != 1 || h.walletState(t, target.ID()).Balance().Amount() != "1035.00" ||
		h.resolvedReference(t, win.TransactionID) != bet.TransactionID.String() {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestAnotherProvidersReferenceIsNeverVisible(t *testing.T) {
	h := newHarnessWithPolicy(t, testPolicy)
	target := h.openFunded(t, 100000)
	h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))

	foreign := referencing(operation(t, target, wager.Refund, "refund-1", 2500), "bet-1")
	foreign.ProviderID = "provider-b"
	foreign.IdempotencyKey = "provider-b:refund-1"

	result := h.process(t, foreign)
	h.clock.Advance(3 * time.Second)
	summary := h.resolve(t)

	if result.Status != wager.PendingReference || summary.Waiting != 1 ||
		h.walletState(t, target.ID()).Balance().Amount() != "975.00" {
		t.Fatalf("result = %+v, summary = %+v: provider-b must not resolve provider-a's bet", result, summary)
	}
}

func TestTwoResolversNeverResolveTheSameOperationTwice(t *testing.T) {
	h := newHarnessWithPolicy(t, testPolicy)
	target := h.openFunded(t, 100000)
	const operations = 6

	for index := range operations {
		h.waitingRefund(t, target, fmt.Sprintf("refund-%d", index), 100, fmt.Sprintf("bet-%d", index))
	}

	for index := range operations {
		h.process(t, operation(t, target, wager.Bet, fmt.Sprintf("bet-%d", index), 100))
	}

	h.clock.Advance(3 * time.Second)

	summaries := make([]service.ResolutionSummary, 2)
	errs := make([]error, 2)

	var group sync.WaitGroup
	for index := range summaries {
		group.Add(1)
		go func() {
			defer group.Done()
			summaries[index], errs[index] = h.resolver().ResolveDue(context.Background(), 20)
		}()
	}

	group.Wait()

	claimed, finished := 0, 0
	for index, summary := range summaries {
		if errs[index] != nil {
			t.Fatalf("resolver %d error = %v", index, errs[index])
		}

		claimed += summary.Claimed
		finished += summary.Finished
	}

	if claimed != operations || finished != operations {
		t.Fatalf("claimed = %d, finished = %d, want %d each", claimed, finished, operations)
	}

	if h.ledgerEntries(t, target.ID(), "CREDIT") != 1+operations ||
		h.walletState(t, target.ID()).Balance().Amount() != "1000.00" {
		t.Fatal("each refund must credit exactly once")
	}

	h.assertConsistent(t, target.ID())
}
