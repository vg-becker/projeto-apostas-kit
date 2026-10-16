//go:build integration

package service_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"apostas-go/internal/model/money"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
	"apostas-go/internal/service"
)

func usdAmount(t *testing.T, minorUnits int64) money.Money {
	t.Helper()

	amount, err := money.New(minorUnits, money.USD)
	if err != nil {
		t.Fatalf("money.New error = %v", err)
	}

	return amount
}

type processed struct {
	result service.OperationResult
	err    error
}

// submitConcurrently dispara todas as operações ao mesmo tempo.
func (h *harness) submitConcurrently(commands []service.ProcessOperationCommand) []processed {
	outcomes := make([]processed, len(commands))
	start := make(chan struct{})

	var group sync.WaitGroup
	for index, command := range commands {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start

			outcomes[index].result, outcomes[index].err = h.operations.Process(
				context.Background(),
				command,
			)
		}()
	}

	close(start)
	group.Wait()

	return outcomes
}

func TestFiftyIdenticalSubmissionsDebitOnce(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	command := operation(t, target, wager.Bet, "bet-1", 2500)
	commands := make([]service.ProcessOperationCommand, 50)
	for index := range commands {
		commands[index] = command
	}

	originals, replays := countOriginalsAndReplays(t, h.submitConcurrently(commands))

	if originals != 1 || replays != 49 {
		t.Fatalf("originals = %d, replays = %d, want 1 and 49", originals, replays)
	}

	if h.ledgerEntries(t, target.ID(), "DEBIT") != 1 ||
		h.walletState(t, target.ID()).Balance().Amount() != "975.00" {
		t.Fatal("the bet must be debited exactly once")
	}
}

// countOriginalsAndReplays exige que todas as respostas descrevam a mesma transação.
func countOriginalsAndReplays(t *testing.T, outcomes []processed) (int, int) {
	t.Helper()

	originals, replays := 0, 0
	for _, outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("submission error = %v", outcome.err)
		}

		same := outcome.result.TransactionID == outcomes[0].result.TransactionID
		if !same || outcome.result.Status != wager.Processed ||
			outcome.result.Balance.Amount() != "975.00" {
			t.Fatalf("diverging result = %+v", outcome.result)
		}

		if outcome.result.IdempotentReplay {
			replays++
		} else {
			originals++
		}
	}

	return originals, replays
}

func TestTwoBetsOf80OverABalanceOf100(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 10000)
	first := operation(t, target, wager.Bet, "bet-1", 8000)
	second := operation(t, target, wager.Bet, "bet-2", 8000)

	outcomes := h.submitConcurrently([]service.ProcessOperationCommand{first, second})

	counts := map[wager.Status]int{}
	for _, outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("error = %v", outcome.err)
		}

		counts[outcome.result.Status]++
	}

	if counts[wager.Processed] != 1 || counts[wager.Rejected] != 1 {
		t.Fatalf("counts = %v, want one processed and one rejected", counts)
	}

	state := h.walletState(t, target.ID())
	if state.Balance().Amount() != "20.00" || h.ledgerEntries(t, target.ID(), "DEBIT") != 1 {
		t.Fatalf("wallet = %+v", state.Snapshot())
	}

	h.assertResendsKeepTheSameOutcomes(t, []service.ProcessOperationCommand{first, second}, outcomes)
}

func (h *harness) assertResendsKeepTheSameOutcomes(
	t *testing.T,
	commands []service.ProcessOperationCommand,
	originals []processed,
) {
	t.Helper()

	for index, command := range commands {
		resent, err := h.operations.Process(context.Background(), command)

		if err != nil || !resent.IdempotentReplay ||
			resent.Status != originals[index].result.Status ||
			resent.TransactionID != originals[index].result.TransactionID {
			t.Fatalf("resend %d = %+v (%v), original = %+v", index, resent, err, originals[index].result)
		}
	}

	if h.walletState(t, commands[0].WalletID).Balance().Amount() != "20.00" {
		t.Fatal("resends must not change the final balance")
	}
}

func TestIndependentWalletsAdvanceInParallelAndNeverGoNegative(t *testing.T) {
	h := newHarness(t)
	const wallets, betsPerWallet = 5, 8

	var commands []service.ProcessOperationCommand
	var owners []*wallet.Wallet
	for range wallets {
		owner := h.openFunded(t, 10000)
		owners = append(owners, owner)

		for bet := range betsPerWallet {
			name := fmt.Sprintf("bet-%s-%d", owner.ID(), bet)
			commands = append(commands, operation(t, owner, wager.Bet, name, 2500))
		}
	}

	counts := map[wager.Status]int{}
	for _, outcome := range h.submitConcurrently(commands) {
		if outcome.err != nil {
			t.Fatalf("error = %v", outcome.err)
		}

		counts[outcome.result.Status]++
	}

	if counts[wager.Processed] != wallets*4 || counts[wager.Rejected] != wallets*4 {
		t.Fatalf("counts = %v, want 4 processed and 4 rejected per wallet", counts)
	}

	for _, owner := range owners {
		h.assertDrained(t, owner)
	}
}

func (h *harness) assertDrained(t *testing.T, owner *wallet.Wallet) {
	t.Helper()

	state := h.walletState(t, owner.ID())
	debits := h.ledgerEntries(t, owner.ID(), "DEBIT")

	if state.Balance().Amount() != "0.00" || debits != 4 {
		t.Fatalf("wallet %s: balance = %s, debits = %d, want 0.00 and 4", owner.ID(), state.Balance(), debits)
	}

	reconciled, err := h.wallets.ReconcileWallet(context.Background(), owner.ID())
	if err != nil || !reconciled.Consistent {
		t.Fatalf("reconciliation = %+v (%v)", reconciled, err)
	}
}

func TestALockedWalletDoesNotBlockAnotherWallet(t *testing.T) {
	h := newHarness(t)
	locked := h.openFunded(t, 100000)
	free := h.openFunded(t, 100000)
	ctx := context.Background()

	blocker, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	defer func() { _ = blocker.Rollback(ctx) }()

	if _, err := blocker.Exec(ctx, `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, locked.ID().String()); err != nil {
		t.Fatalf("lock wallet: %v", err)
	}

	blockedDone := make(chan error, 1)
	go func() {
		_, err := h.operations.Process(ctx, operation(t, locked, wager.Bet, "bet-locked", 100))
		blockedDone <- err
	}()

	freeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	result, err := h.operations.Process(freeContext, operation(t, free, wager.Bet, "bet-free", 100))
	if err != nil || result.Status != wager.Processed {
		t.Fatalf("free wallet result = %+v (%v), it must not wait for another wallet", result, err)
	}

	select {
	case err := <-blockedDone:
		t.Fatalf("the locked wallet finished while still locked (%v)", err)
	case <-time.After(300 * time.Millisecond):
	}

	if err := blocker.Rollback(ctx); err != nil {
		t.Fatalf("release lock: %v", err)
	}

	if err := <-blockedDone; err != nil {
		t.Fatalf("locked wallet error after release = %v", err)
	}
}

// TestRepeatedRoundsOfIdenticalSubmissionsNeverReportAFalseConflict cobre uma corrida
// que já ocorreu: uma requisição que enxergava a operação recém-confirmada por outra
// recebia ErrIdempotencyConflict em vez do replay.
func TestRepeatedRoundsOfIdenticalSubmissionsNeverReportAFalseConflict(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 1000000)

	const rounds, copies = 12, 16

	for round := range rounds {
		command := operation(t, target, wager.Bet, fmt.Sprintf("round-%d", round), 100)
		commands := make([]service.ProcessOperationCommand, copies)
		for index := range commands {
			commands[index] = command
		}

		if originals := countOriginals(t, h.submitConcurrently(commands)); originals != 1 {
			t.Fatalf("round %d: %d original results, want exactly 1", round, originals)
		}
	}

	if h.ledgerEntries(t, target.ID(), "DEBIT") != rounds {
		t.Fatalf("debits = %d, want one per round", h.ledgerEntries(t, target.ID(), "DEBIT"))
	}
}

func countOriginals(t *testing.T, outcomes []processed) int {
	t.Helper()

	originals := 0
	for _, outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("error = %v", outcome.err)
		}

		if !outcome.result.IdempotentReplay {
			originals++
		}
	}

	return originals
}
