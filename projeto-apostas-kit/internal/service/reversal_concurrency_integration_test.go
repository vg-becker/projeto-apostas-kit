//go:build integration

package service_test

import (
	"fmt"
	"testing"

	"apostas-go/internal/model/wager"
	"apostas-go/internal/service"
)

func TestConcurrentReversalsOfOneBetCreditItOnce(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))

	const attempts = 20

	commands := make([]service.ProcessOperationCommand, attempts)
	for index := range commands {
		kind := wager.Refund
		if index%2 == 1 {
			kind = wager.Rollback
		}

		commands[index] = referencing(
			operation(t, target, kind, fmt.Sprintf("reversal-%d", index), 2500),
			"bet-1",
		)
	}

	processedCount, alreadyReversed := countReversalOutcomes(t, h.submitConcurrently(commands))

	if processedCount != 1 || alreadyReversed != attempts-1 {
		t.Fatalf("processed = %d, already reversed = %d", processedCount, alreadyReversed)
	}

	if h.walletState(t, target.ID()).Balance().Amount() != "1000.00" ||
		h.ledgerEntries(t, target.ID(), "CREDIT") != 2 {
		t.Fatal("the debit must be returned exactly once")
	}

	h.assertConsistent(t, target.ID())
}

func countReversalOutcomes(t *testing.T, outcomes []processed) (int, int) {
	t.Helper()

	processedCount, alreadyReversed := 0, 0
	for _, outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("error = %v", outcome.err)
		}

		switch {
		case outcome.result.Status == wager.Processed:
			processedCount++
		case outcome.result.FailureCode == wager.AlreadyReversed:
			alreadyReversed++
		default:
			t.Fatalf("unexpected result = %+v", outcome.result)
		}
	}

	return processedCount, alreadyReversed
}
