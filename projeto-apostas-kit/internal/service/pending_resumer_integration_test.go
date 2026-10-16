//go:build integration

package service_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/service"
)

// accept simula o aceite assíncrono: confirma a transação em PENDING e "cai" antes de
// executá-la. O hash é o mesmo que o Process calcularia, então o replay a reconhece.
func (h *harness) accept(t *testing.T, command service.ProcessOperationCommand) identity.TransactionID {
	t.Helper()

	hash, err := service.CanonicalHash(command)
	if err != nil {
		t.Fatalf("CanonicalHash error = %v", err)
	}

	id := mustTransactionID(t, h)
	accepted, err := wager.NewExternal(wager.ExternalParams{
		ID:                             id,
		ProviderID:                     command.ProviderID,
		ExternalTransactionID:          command.ExternalTransactionID,
		IdempotencyKey:                 command.IdempotencyKey,
		PayloadHash:                    hash,
		WalletID:                       command.WalletID,
		PlayerID:                       command.PlayerID,
		RoundID:                        command.RoundID,
		GameID:                         command.GameID,
		Kind:                           command.Kind,
		Money:                          command.Money,
		ReferenceExternalTransactionID: command.ReferenceExternalTransactionID,
		CorrelationID:                  command.CorrelationID,
		CausationID:                    command.CausationID,
		Now:                            h.clock.Now(),
	})
	if err != nil {
		t.Fatalf("NewExternal error = %v", err)
	}

	if err := h.transactions.Insert(context.Background(), accepted); err != nil {
		t.Fatalf("insert accepted transaction: %v", err)
	}

	return id
}

func (h *harness) resume(t *testing.T) service.ResolutionSummary {
	t.Helper()

	summary, err := h.resumer().ResumeAbandoned(context.Background(), 20)
	if err != nil {
		t.Fatalf("ResumeAbandoned error = %v", err)
	}

	return summary
}

func (h *harness) status(t *testing.T, id identity.TransactionID) (string, string) {
	t.Helper()

	var status string
	var code *string
	row := h.pool.QueryRow(
		context.Background(),
		`SELECT status, failure_code FROM wager_transactions WHERE id = $1`,
		id.String(),
	)
	if err := row.Scan(&status, &code); err != nil {
		t.Fatalf("status of %s: %v", id, err)
	}

	if code == nil {
		return status, ""
	}

	return status, *code
}

func TestAnAcceptedButUnprocessedBetIsResumedByAnotherInstance(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	command := operation(t, target, wager.Bet, "bet-1", 2500)
	id := h.accept(t, command)

	summary := h.resume(t)

	if summary.Claimed != 1 || summary.Finished != 1 {
		t.Fatalf("summary = %+v", summary)
	}

	status, _ := h.status(t, id)
	if status != "PROCESSED" || h.walletState(t, target.ID()).Balance().Amount() != "975.00" ||
		h.ledgerEntries(t, target.ID(), "DEBIT") != 1 {
		t.Fatalf("status = %s: the accepted bet was not applied exactly once", status)
	}

	if h.eventCount(t, "WagerTransactionProcessed") != 2 || h.eventCount(t, "WalletBalanceChanged") != 2 {
		t.Fatal("resuming must emit Processed and BalanceChanged")
	}

	replay := h.process(t, command)
	if replay.TransactionID != id || replay.Status != wager.Processed || !replay.IdempotentReplay {
		t.Fatalf("replay = %+v", replay)
	}

	if again := h.resume(t); again.Claimed != 0 {
		t.Fatalf("second cycle = %+v, nothing is left to resume", again)
	}
}

func TestResumedBetWithoutFundsIsRejected(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 10000)
	id := h.accept(t, operation(t, target, wager.Bet, "bet-1", 99999999))

	h.resume(t)

	status, code := h.status(t, id)
	if status != "REJECTED" || code != string(wager.InsufficientFunds) ||
		h.walletState(t, target.ID()).Balance().Amount() != "100.00" {
		t.Fatalf("status = %s, code = %s", status, code)
	}
}

func TestResumedReversalResolvesItsReference(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	id := h.accept(t, referencing(operation(t, target, wager.Refund, "refund-1", 2500), "bet-1"))

	h.resume(t)

	status, _ := h.status(t, id)
	if status != "PROCESSED" || h.walletState(t, target.ID()).Balance().Amount() != "1000.00" {
		t.Fatalf("status = %s", status)
	}
}

func TestResumedReversalWithoutItsReferenceWaitsAndIsResolvedLater(t *testing.T) {
	h := newHarnessWithPolicy(t, testPolicy)
	target := h.openFunded(t, 100000)
	id := h.accept(t, referencing(operation(t, target, wager.Refund, "refund-1", 2500), "bet-1"))

	summary := h.resume(t)

	if summary.Waiting != 1 || h.eventCount(t, "WagerTransactionPendingReference") != 1 {
		t.Fatalf("summary = %+v", summary)
	}

	h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	h.clock.Advance(3 * time.Second)

	if resolved := h.resolve(t); resolved.Finished != 1 {
		t.Fatalf("resolver summary = %+v", resolved)
	}

	status, _ := h.status(t, id)
	if status != "PROCESSED" {
		t.Fatalf("status = %s", status)
	}
}

func TestAnInstanceThatDiesWhileHoldingTheClaimDoesNotBlockRecovery(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	id := h.accept(t, operation(t, target, wager.Bet, "bet-1", 2500))

	dead, err := h.transactions.ClaimPending(
		context.Background(),
		port.ClaimParams{Limit: 10, Now: h.clock.Now(), Lease: time.Minute},
	)
	if err != nil || len(dead) != 1 {
		t.Fatalf("claim by the instance that dies = %d (%v)", len(dead), err)
	}

	if during := h.resume(t); during.Claimed != 0 {
		t.Fatalf("summary = %+v, the lease must protect the claim", during)
	}

	h.clock.Advance(61 * time.Second)

	if after := h.resume(t); after.Claimed != 1 || after.Finished != 1 {
		t.Fatalf("summary = %+v, the claim must be recovered after the lease", after)
	}

	if status, _ := h.status(t, id); status != "PROCESSED" {
		t.Fatalf("status = %s", status)
	}
}

func TestTwoResumersNeverApplyTheSameAcceptedOperationTwice(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	const operations = 6

	for index := range operations {
		h.accept(t, operation(t, target, wager.Bet, fmt.Sprintf("bet-%d", index), 100))
	}

	summaries := make([]service.ResolutionSummary, 2)
	errs := make([]error, 2)

	var group sync.WaitGroup
	for index := range summaries {
		group.Add(1)
		go func() {
			defer group.Done()
			summaries[index], errs[index] = h.resumer().ResumeAbandoned(context.Background(), 20)
		}()
	}

	group.Wait()

	claimed := 0
	for index, summary := range summaries {
		if errs[index] != nil {
			t.Fatalf("resumer %d error = %v", index, errs[index])
		}

		claimed += summary.Claimed
	}

	if claimed != operations || h.ledgerEntries(t, target.ID(), "DEBIT") != operations ||
		h.walletState(t, target.ID()).Balance().Amount() != "994.00" {
		t.Fatalf("claimed = %d: each accepted bet must be applied exactly once", claimed)
	}

	h.assertConsistent(t, target.ID())
}

func TestAnAcceptedOperationThatCanNeverApplyFailsForAudit(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	foreign := operation(t, target, wager.Bet, "bet-1", 2500)
	foreign.PlayerID = h.newPlayerID(t)
	id := h.accept(t, foreign)
	eventsBefore := h.count(t, `SELECT count(*) FROM outbox_events`)

	summary := h.resume(t)

	status, code := h.status(t, id)
	if summary.Finished != 1 || status != "FAILED" || code != string(wager.InfrastructureFailure) {
		t.Fatalf("summary = %+v, status = %s, code = %s", summary, status, code)
	}

	if h.walletState(t, target.ID()).Balance().Amount() != "1000.00" ||
		h.count(t, `SELECT count(*) FROM outbox_events`) != eventsBefore {
		t.Fatal("a failed operation must not move money or emit events")
	}

}
