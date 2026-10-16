//go:build integration

package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"apostas-go/internal/model/wager"
	"apostas-go/internal/service"
)

// recordingObserver guarda o que o Service observou, para os testes conferirem.
type recordingObserver struct {
	mutex           sync.Mutex
	operations      []service.OperationObservation
	reconciliations []service.ReconciliationObservation
}

func (observer *recordingObserver) ObserveOperation(observation service.OperationObservation) {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()

	observer.operations = append(observer.operations, observation)
}

func (observer *recordingObserver) ObserveReconciliation(
	observation service.ReconciliationObservation,
) {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()

	observer.reconciliations = append(observer.reconciliations, observation)
}

// take devolve o que foi observado desde a última chamada.
func (observer *recordingObserver) take() []service.OperationObservation {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()

	taken := observer.operations
	observer.operations = nil

	return taken
}

func only(t *testing.T, observations []service.OperationObservation) service.OperationObservation {
	t.Helper()

	if len(observations) != 1 {
		t.Fatalf("observations = %+v, want exactly one", observations)
	}

	return observations[0]
}

func TestARequestIsObservedOnceWithItsResultAndIdentifiers(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 10000)
	h.observed.take()

	command := operation(t, target, wager.Bet, "bet-1", 2500)
	command.CorrelationID = "corr-1"

	result := h.process(t, command)
	applied := only(t, h.observed.take())

	if applied.Source != service.SourceRequest || applied.Kind != "BET" ||
		applied.Result != service.ResultApplied || applied.Status != "PROCESSED" ||
		applied.TransactionID != result.TransactionID.String() ||
		applied.WalletID != target.ID().String() || applied.ProviderID != command.ProviderID ||
		applied.CorrelationID != "corr-1" || applied.Duration <= 0 {
		t.Fatalf("observation = %+v", applied)
	}

	h.process(t, command)

	if replay := only(t, h.observed.take()); replay.Result != service.ResultReplay {
		t.Fatalf("replay observation = %+v", replay)
	}
}

func TestARejectionAndAConflictAreObservedWithTheirOwnResults(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 10000)
	h.observed.take()

	h.process(t, operation(t, target, wager.Bet, "too-big", 999999))

	rejected := only(t, h.observed.take())
	if rejected.Result != service.ResultApplied || rejected.Status != "REJECTED" ||
		rejected.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Fatalf("rejection observation = %+v", rejected)
	}

	h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	h.observed.take()

	other := operation(t, target, wager.Bet, "bet-1", 3000)

	if _, err := h.operations.Process(context.Background(), other); !errors.Is(err, service.ErrIdempotencyConflict) {
		t.Fatalf("err = %v, want the idempotency conflict", err)
	}

	if conflict := only(t, h.observed.take()); conflict.Result != service.ResultIdempotencyConflict ||
		conflict.TransactionID != "" {
		t.Fatalf("conflict observation = %+v", conflict)
	}
}

func TestAMessageIsObservedOnceAfterTheCommitWithItsMessageID(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 10000)
	h.observed.take()

	message := inbound(t, target, "msg-1", "bet-1", wager.Bet, 2500)

	h.handle(t, message)

	applied := only(t, h.observed.take())
	if applied.Source != service.SourceMessage || applied.Result != service.ResultApplied ||
		applied.MessageID != "msg-1" || applied.Status != "PROCESSED" {
		t.Fatalf("observation = %+v: the nested operation must not be counted a second time", applied)
	}

	h.handle(t, message)

	if duplicate := only(t, h.observed.take()); duplicate.Result != service.ResultDuplicate ||
		duplicate.MessageID != "msg-1" || duplicate.TransactionID != "" {
		t.Fatalf("duplicate observation = %+v", duplicate)
	}

	reused := inbound(t, target, "msg-1", "bet-2", wager.Bet, 100)

	if _, err := h.handler.Handle(context.Background(), reused); !errors.Is(err, service.ErrInboundHashConflict) {
		t.Fatalf("err = %v, want the hash conflict", err)
	}

	if conflict := only(t, h.observed.take()); conflict.Result != service.ResultIdempotencyConflict {
		t.Fatalf("hash conflict observation = %+v", conflict)
	}
}

func TestATransientFailureIsObservedAsTransientAndNotAsApplied(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 10000)
	h.observed.take()

	failing := h.messages(transientOutbox{OutboxRepository: h.outbox})

	_, err := failing.Handle(context.Background(), inbound(t, target, "msg-1", "bet-1", wager.Bet, 2500))
	if !service.IsTransient(err) {
		t.Fatalf("err = %v, want a transient failure", err)
	}

	if failure := only(t, h.observed.take()); failure.Result != service.ResultTransient {
		t.Fatalf("observation = %+v", failure)
	}
}

func TestTheResolverObservesAWaitingOperationWhenItFinishes(t *testing.T) {
	h := newHarnessWithPolicy(t, testPolicy)
	target := h.openFunded(t, 100000)
	h.observed.take()

	waiting := h.waitingRefund(t, target, "refund-1", 2500, "bet-1")

	if pending := only(t, h.observed.take()); pending.Status != "PENDING_REFERENCE" ||
		pending.Source != service.SourceRequest {
		t.Fatalf("waiting observation = %+v", pending)
	}

	h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	h.observed.take()
	h.clock.Advance(3 * time.Second)
	h.resolve(t)

	resolved := only(t, h.observed.take())
	if resolved.Source != service.SourcePendingReference || resolved.Kind != "REFUND" ||
		resolved.Status != "PROCESSED" || resolved.TransactionID != waiting.TransactionID.String() ||
		resolved.CorrelationID != "correlation-refund-1" {
		t.Fatalf("resolver observation = %+v", resolved)
	}
}

func TestReconciliationIsObservedAsConsistentAndAsDivergent(t *testing.T) {
	h := newHarness(t)
	opened, err := h.open(t, h.newPlayerID(t).String(), 100000)
	if err != nil {
		t.Fatalf("open error = %v", err)
	}

	if _, err := h.wallets.ReconcileWallet(context.Background(), opened.ID()); err != nil {
		t.Fatalf("ReconcileWallet error = %v", err)
	}

	_, err = h.pool.Exec(
		context.Background(),
		`UPDATE wallets SET balance_minor_units = balance_minor_units + 500 WHERE id = $1`,
		opened.ID().String(),
	)
	if err != nil {
		t.Fatalf("tamper: %v", err)
	}

	if _, err := h.wallets.ReconcileWallet(context.Background(), opened.ID()); err != nil {
		t.Fatalf("ReconcileWallet error = %v", err)
	}

	got := h.observed.reconciliations
	if len(got) != 2 || !got[0].Consistent || got[1].Consistent ||
		got[1].WalletID != opened.ID().String() {
		t.Fatalf("reconciliations = %+v", got)
	}
}
