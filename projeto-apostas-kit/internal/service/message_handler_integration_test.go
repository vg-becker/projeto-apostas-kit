//go:build integration

package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"apostas-go/internal/model/event"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
	"apostas-go/internal/service"
)

// transientOutbox falha como se o Postgres tivesse caído no meio do tratamento.
type transientOutbox struct {
	port.OutboxRepository
}

func (transientOutbox) Add(context.Context, event.Event) error {
	return fmt.Errorf("%w: connection lost", port.ErrTransient)
}

func inbound(
	t *testing.T,
	target *wallet.Wallet,
	messageID string,
	external string,
	kind wager.Kind,
	minorUnits int64,
) service.InboundMessage {
	t.Helper()

	command := operation(t, target, kind, external, minorUnits)
	command.CorrelationID = messageID
	command.CausationID = messageID

	return service.InboundMessage{MessageID: messageID, Command: command}
}

func (h *harness) handle(t *testing.T, message service.InboundMessage) service.InboundResult {
	t.Helper()

	result, err := h.handler.Handle(context.Background(), message)
	if err != nil {
		t.Fatalf("Handle(%s) error = %v", message.MessageID, err)
	}

	return result
}

func (h *harness) inboxRows(t *testing.T) int {
	t.Helper()

	return h.count(t, `SELECT count(*) FROM inbox_messages`)
}

func (h *harness) inboxCompleted(t *testing.T, messageID string) bool {
	t.Helper()

	return h.count(
		t,
		`SELECT count(*) FROM inbox_messages WHERE message_id = $1 AND completed_at IS NOT NULL`,
		messageID,
	) == 1
}

func TestANewMessageIsAppliedAndItsInboxCompletedInTheSameCommit(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)

	result := h.handle(t, inbound(t, target, "msg-1", "tx-1", wager.Bet, 2500))

	if result.Outcome != service.InboundApplied || result.Operation.Status != wager.Processed ||
		result.Operation.Balance.Amount() != "975.00" {
		t.Fatalf("result = %+v", result)
	}

	if !h.inboxCompleted(t, "msg-1") || h.ledgerEntries(t, target.ID(), "DEBIT") != 1 ||
		h.walletState(t, target.ID()).Balance().Amount() != "975.00" {
		t.Fatal("the operation and the inbox must be committed together")
	}

	var causation string
	row := h.pool.QueryRow(
		context.Background(),
		`SELECT payload->>'causationId' FROM outbox_events WHERE event_type = 'WagerTransactionProcessed'
		  AND payload->'data'->>'kind' = 'BET'`,
	)
	if err := row.Scan(&causation); err != nil || causation != "msg-1" {
		t.Fatalf("causation = %q (%v), the message id must reach the events", causation, err)
	}
}

func TestARedeliveryIsADuplicateAndChangesNothing(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	message := inbound(t, target, "msg-1", "tx-1", wager.Bet, 2500)
	h.handle(t, message)
	h.process(t, operation(t, target, wager.Win, "win-1", 10000))

	again := h.handle(t, message)

	if again.Outcome != service.InboundDuplicate {
		t.Fatalf("outcome = %s, want %s", again.Outcome, service.InboundDuplicate)
	}

	if h.ledgerEntries(t, target.ID(), "DEBIT") != 1 || h.inboxRows(t) != 1 ||
		h.walletState(t, target.ID()).Balance().Amount() != "1075.00" {
		t.Fatal("a redelivery after the commit and before the delete must have no effect")
	}
}

func TestReusingAMessageIDWithDifferentContentIsAPermanentConflict(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	h.handle(t, inbound(t, target, "msg-1", "tx-1", wager.Bet, 2500))

	_, err := h.handler.Handle(
		context.Background(),
		inbound(t, target, "msg-1", "tx-1", wager.Bet, 9900),
	)

	if !errors.Is(err, service.ErrInboundHashConflict) || service.Classify(err) != service.ClassPermanent {
		t.Fatalf("error = %v, want a permanent %v", err, service.ErrInboundHashConflict)
	}

	if h.ledgerEntries(t, target.ID(), "DEBIT") != 1 || h.inboxRows(t) != 1 {
		t.Fatal("the conflicting message must have no effect")
	}
}

func TestATransientFailureUndoesEverythingIncludingTheInboxAndALaterDeliveryWorks(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	message := inbound(t, target, "msg-1", "tx-1", wager.Bet, 2500)
	broken := h.messages(transientOutbox{OutboxRepository: h.outbox})

	_, err := broken.Handle(context.Background(), message)

	if service.Classify(err) != service.ClassTransient {
		t.Fatalf("error = %v, want a transient failure", err)
	}

	if h.inboxRows(t) != 0 || h.ledgerEntries(t, target.ID(), "DEBIT") != 0 ||
		h.walletState(t, target.ID()).Balance().Amount() != "1000.00" {
		t.Fatal("a failed handling must leave neither the inbox row nor any effect")
	}

	retry := h.handle(t, message)

	if retry.Outcome != service.InboundApplied || retry.Operation.IdempotentReplay {
		t.Fatalf("retry = %+v, want a normal first processing", retry)
	}
}

func TestAnUnexpectedFailureIsClassifiedAsUnknownAndAlsoUndoesEverything(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	broken := h.messages(failingOutbox{OutboxRepository: h.outbox})

	_, err := broken.Handle(context.Background(), inbound(t, target, "msg-1", "tx-1", wager.Bet, 2500))

	if !errors.Is(err, errBoom) || service.Classify(err) != service.ClassUnknown || h.inboxRows(t) != 0 {
		t.Fatalf("error = %v, class = %d, inbox rows = %d", err, service.Classify(err), h.inboxRows(t))
	}
}

func TestABusinessRejectionIsTerminalAndCompletesTheInbox(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 10000)
	message := inbound(t, target, "msg-1", "tx-1", wager.Bet, 99999999)

	result := h.handle(t, message)

	if result.Outcome != service.InboundApplied || result.Operation.Status != wager.Rejected ||
		result.Operation.FailureCode != wager.InsufficientFunds || !h.inboxCompleted(t, "msg-1") {
		t.Fatalf("result = %+v", result)
	}

	if again := h.handle(t, message); again.Outcome != service.InboundDuplicate {
		t.Fatalf("redelivery = %+v, want a duplicate", again)
	}
}

func TestAPendingReferenceCompletesTheInboxAndTheWorkerTakesOver(t *testing.T) {
	h := newHarnessWithPolicy(t, testPolicy)
	target := h.openFunded(t, 100000)
	refund := inbound(t, target, "msg-refund", "refund-1", wager.Refund, 2500)
	refund.Command.ReferenceExternalTransactionID = "bet-1"

	waiting := h.handle(t, refund)

	if waiting.Outcome != service.InboundApplied || waiting.Operation.Status != wager.PendingReference ||
		!h.inboxCompleted(t, "msg-refund") {
		t.Fatalf("waiting = %+v", waiting)
	}

	if again := h.handle(t, refund); again.Outcome != service.InboundDuplicate {
		t.Fatalf("redelivery = %+v, want a duplicate", again)
	}

	h.handle(t, inbound(t, target, "msg-bet", "bet-1", wager.Bet, 2500))
	h.clock.Advance(3 * time.Second)

	if summary := h.resolve(t); summary.Finished != 1 ||
		h.walletState(t, target.ID()).Balance().Amount() != "1000.00" {
		t.Fatalf("summary = %+v: the resolver must finish what the message left pending", summary)
	}
}

func TestTwentyConcurrentDeliveriesOfOneMessageApplyItOnce(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	message := inbound(t, target, "msg-1", "tx-1", wager.Bet, 2500)
	const deliveries = 20

	outcomes := make([]service.InboundOutcome, deliveries)
	errs := make([]error, deliveries)
	start := make(chan struct{})

	var group sync.WaitGroup
	for index := range deliveries {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start

			result, err := h.handler.Handle(context.Background(), message)
			outcomes[index], errs[index] = result.Outcome, err
		}()
	}

	close(start)
	group.Wait()

	applied, duplicates := 0, 0
	for index, outcome := range outcomes {
		if errs[index] != nil {
			t.Fatalf("delivery %d error = %v", index, errs[index])
		}

		if outcome == service.InboundApplied {
			applied++
		} else {
			duplicates++
		}
	}

	if applied != 1 || duplicates != deliveries-1 || h.ledgerEntries(t, target.ID(), "DEBIT") != 1 {
		t.Fatalf("applied = %d, duplicates = %d, debits = %d", applied, duplicates, h.ledgerEntries(t, target.ID(), "DEBIT"))
	}
}

func TestTwoMessageIDsForTheSameOperationAreAReplayNotASecondDebit(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	h.handle(t, inbound(t, target, "msg-1", "tx-1", wager.Bet, 2500))

	second := h.handle(t, inbound(t, target, "msg-2", "tx-1", wager.Bet, 2500))

	if second.Outcome != service.InboundApplied || !second.Operation.IdempotentReplay ||
		h.ledgerEntries(t, target.ID(), "DEBIT") != 1 {
		t.Fatalf("second = %+v, debits = %d", second, h.ledgerEntries(t, target.ID(), "DEBIT"))
	}
}

func TestTheSameOperationOverHTTPAndSQSIsAppliedOnce(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)

	viaHTTP := h.process(t, operation(t, target, wager.Bet, "tx-1", 2500))
	viaSQS := h.handle(t, inbound(t, target, "msg-1", "tx-1", wager.Bet, 2500))

	if !viaSQS.Operation.IdempotentReplay || viaSQS.Operation.TransactionID != viaHTTP.TransactionID {
		t.Fatalf("SQS after HTTP = %+v, want the replay of %s", viaSQS, viaHTTP.TransactionID)
	}

	viaSQSFirst := h.handle(t, inbound(t, target, "msg-2", "tx-2", wager.Bet, 1000))
	viaHTTPAfter := h.process(t, operation(t, target, wager.Bet, "tx-2", 1000))

	if !viaHTTPAfter.IdempotentReplay || viaHTTPAfter.TransactionID != viaSQSFirst.Operation.TransactionID ||
		h.ledgerEntries(t, target.ID(), "DEBIT") != 2 {
		t.Fatalf("HTTP after SQS = %+v, debits = %d", viaHTTPAfter, h.ledgerEntries(t, target.ID(), "DEBIT"))
	}
}

func TestTheSameKeyWithDifferentContentAcrossEntriesIsAPermanentConflict(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	h.process(t, operation(t, target, wager.Bet, "tx-1", 2500))

	_, err := h.handler.Handle(context.Background(), inbound(t, target, "msg-1", "tx-1", wager.Bet, 7700))

	if !errors.Is(err, service.ErrIdempotencyConflict) || service.Classify(err) != service.ClassPermanent {
		t.Fatalf("error = %v, want a permanent idempotency conflict", err)
	}

	if h.inboxRows(t) != 0 || h.ledgerEntries(t, target.ID(), "DEBIT") != 1 {
		t.Fatal("the refused message must leave no trace")
	}
}

func TestProvidersAreIsolatedOverMessagingToo(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	fromA := inbound(t, target, "msg-1", "tx-1", wager.Bet, 2500)
	fromB := inbound(t, target, "msg-2", "tx-1", wager.Bet, 2500)
	fromB.Command.ProviderID = "provider-b"
	fromB.Command.IdempotencyKey = "provider-b:tx-1"

	h.handle(t, fromA)
	second := h.handle(t, fromB)

	if second.Outcome != service.InboundApplied || second.Operation.IdempotentReplay ||
		h.ledgerEntries(t, target.ID(), "DEBIT") != 2 {
		t.Fatalf("second = %+v", second)
	}
}

func TestInputErrorsArePermanentAndLeaveNoTrace(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	missing, _ := h.ids.NewWalletID()
	unknownWallet := inbound(t, target, "msg-1", "tx-1", wager.Bet, 100)
	unknownWallet.Command.WalletID = missing

	tests := []struct {
		name    string
		message service.InboundMessage
		wantErr error
	}{
		{name: "unknown wallet", message: unknownWallet, wantErr: service.ErrWalletNotFound},
		{name: "empty message id", message: inbound(t, target, "", "tx-2", wager.Bet, 100), wantErr: service.ErrInvalidMessage},
		{
			name:    "message id too long",
			message: inbound(t, target, strings.Repeat("m", 129), "tx-3", wager.Bet, 100),
			wantErr: service.ErrInvalidMessage,
		},
		{name: "padded message id", message: inbound(t, target, " msg ", "tx-4", wager.Bet, 100), wantErr: service.ErrInvalidMessage},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := h.handler.Handle(context.Background(), test.message)

			if !errors.Is(err, test.wantErr) || service.Classify(err) != service.ClassPermanent {
				t.Fatalf("error = %v, want a permanent %v", err, test.wantErr)
			}
		})
	}

	if h.inboxRows(t) != 0 || h.ledgerEntries(t, target.ID(), "DEBIT") != 0 {
		t.Fatal("refused messages must leave no trace")
	}
}
