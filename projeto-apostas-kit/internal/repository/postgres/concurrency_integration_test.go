//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/wager"
)

type betOutcome struct {
	status wager.Status
	err    error
}

// raceBets dispara as apostas ao mesmo tempo e devolve o desfecho de cada uma.
func raceBets(
	t *testing.T,
	f *fixture,
	walletID identity.WalletID,
	count int,
	minorUnits int64,
) []betOutcome {
	t.Helper()

	outcomes := make([]betOutcome, count)
	start := make(chan struct{})

	var group sync.WaitGroup
	for index := range count {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start

			status, err := f.placeBet(
				context.Background(),
				walletID,
				fmt.Sprintf("race-%d", index),
				minorUnits,
				baseTime,
			)
			outcomes[index] = betOutcome{status: status, err: err}
		}()
	}

	close(start)
	group.Wait()

	return outcomes
}

func countStatuses(t *testing.T, outcomes []betOutcome) map[wager.Status]int {
	t.Helper()

	counts := map[wager.Status]int{}
	for _, outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("bet error = %v", outcome.err)
		}

		counts[outcome.status]++
	}

	return counts
}

func (f *fixture) assertFinalState(
	t *testing.T,
	walletID identity.WalletID,
	wantBalance int64,
	wantDebits int,
) {
	t.Helper()

	ctx := context.Background()

	reconciled, err := f.ledger.CalculateBalance(ctx, walletID, money.BRL)
	if err != nil {
		t.Fatalf("CalculateBalance error = %v", err)
	}

	if reconciled.StoredBalance.MinorUnits() != wantBalance ||
		!reconciled.Balance.Equal(reconciled.StoredBalance) {
		t.Fatalf("stored = %s, ledger = %s, want %d", reconciled.StoredBalance, reconciled.Balance, wantBalance)
	}

	var debits int
	row := f.pool.QueryRow(
		ctx,
		`SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`,
		walletID.String(),
	)
	if err := row.Scan(&debits); err != nil || debits != wantDebits {
		t.Fatalf("debit entries = %d (%v), want %d", debits, err, wantDebits)
	}
}

func TestConcurrentBetsNeverLoseUpdates(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 10000)

	counts := countStatuses(t, raceBets(t, f, target.ID(), 20, 500))

	if counts[wager.Processed] != 20 || counts[wager.Rejected] != 0 {
		t.Fatalf("counts = %v, want 20 processed", counts)
	}

	f.assertFinalState(t, target.ID(), 0, 20)
}

func TestBetsBeyondBalanceAreRejectedAndBalanceNeverGoesNegative(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 10000)

	counts := countStatuses(t, raceBets(t, f, target.ID(), 30, 500))

	if counts[wager.Processed] != 20 || counts[wager.Rejected] != 10 {
		t.Fatalf("counts = %v, want 20 processed and 10 rejected", counts)
	}

	f.assertFinalState(t, target.ID(), 0, 20)
}

func TestTwoBetsOf80OverBalanceOf100(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 10000)

	counts := countStatuses(t, raceBets(t, f, target.ID(), 2, 8000))

	if counts[wager.Processed] != 1 || counts[wager.Rejected] != 1 {
		t.Fatalf("counts = %v, want one processed and one rejected", counts)
	}

	f.assertFinalState(t, target.ID(), 2000, 1)
}
