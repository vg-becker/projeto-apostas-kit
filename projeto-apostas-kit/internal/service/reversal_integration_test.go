//go:build integration

package service_test

import (
	"context"
	"fmt"
	"testing"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
	"apostas-go/internal/service"
)

func referencing(
	command service.ProcessOperationCommand,
	reference string,
) service.ProcessOperationCommand {
	command.ReferenceExternalTransactionID = reference

	return command
}

func (h *harness) resolvedReference(t *testing.T, id identity.TransactionID) string {
	t.Helper()

	var resolved *string
	row := h.pool.QueryRow(
		context.Background(),
		`SELECT resolved_reference_id::text FROM wager_transactions WHERE id = $1`,
		id.String(),
	)
	if err := row.Scan(&resolved); err != nil || resolved == nil {
		t.Fatalf("resolved reference of %s = %v (%v)", id, resolved, err)
	}

	return *resolved
}

func (h *harness) assertConsistent(t *testing.T, id identity.WalletID) {
	t.Helper()

	result, err := h.wallets.ReconcileWallet(context.Background(), id)
	if err != nil || !result.Consistent {
		t.Fatalf("reconciliation = %+v (%v), want consistent", result, err)
	}
}

func TestRefundOfABetCreditsItBack(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	bet := h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))

	refund := h.process(t, referencing(operation(t, target, wager.Refund, "refund-1", 2500), "bet-1"))

	if refund.Status != wager.Processed || refund.Balance.Amount() != "1000.00" || refund.IdempotentReplay {
		t.Fatalf("refund = %+v", refund)
	}

	state := h.walletState(t, target.ID())
	if state.Balance().Amount() != "1000.00" || state.Version() != 3 {
		t.Fatalf("wallet = %+v", state.Snapshot())
	}

	if h.resolvedReference(t, refund.TransactionID) != bet.TransactionID.String() {
		t.Fatal("the refund must record the resolved reference")
	}

	if h.ledgerEntries(t, target.ID(), "CREDIT") != 2 || h.ledgerEntries(t, target.ID(), "DEBIT") != 1 {
		t.Fatal("expected the opening credit, the bet debit and the refund credit")
	}

	h.assertConsistent(t, target.ID())
}

func TestRollbackOfABetCreditsItBack(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))

	rollback := h.process(t, referencing(operation(t, target, wager.Rollback, "rb-1", 2500), "bet-1"))

	if rollback.Status != wager.Processed || rollback.Balance.Amount() != "1000.00" {
		t.Fatalf("rollback = %+v", rollback)
	}

	h.assertConsistent(t, target.ID())
}

func TestRollbackOfAWinDebitsItBack(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	h.process(t, operation(t, target, wager.Win, "win-1", 5000))

	rollback := h.process(t, referencing(operation(t, target, wager.Rollback, "rb-1", 5000), "win-1"))

	if rollback.Status != wager.Processed || rollback.Balance.Amount() != "1000.00" {
		t.Fatalf("rollback = %+v", rollback)
	}

	if h.ledgerEntries(t, target.ID(), "DEBIT") != 1 {
		t.Fatal("rolling back a win must debit")
	}

	h.assertConsistent(t, target.ID())
}

func TestRollbackOfARefundDebitsAgain(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	h.process(t, referencing(operation(t, target, wager.Refund, "refund-1", 2500), "bet-1"))

	rollback := h.process(t, referencing(operation(t, target, wager.Rollback, "rb-1", 2500), "refund-1"))

	if rollback.Status != wager.Processed || rollback.Balance.Amount() != "975.00" {
		t.Fatalf("rollback = %+v", rollback)
	}

	h.assertConsistent(t, target.ID())
}

func TestWinWithReferenceCreditsAndRecordsTheBet(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	bet := h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))

	win := h.process(t, referencing(operation(t, target, wager.Win, "win-1", 6000), "bet-1"))

	if win.Status != wager.Processed || win.Balance.Amount() != "1035.00" {
		t.Fatalf("win = %+v", win)
	}

	if h.resolvedReference(t, win.TransactionID) != bet.TransactionID.String() {
		t.Fatal("the win must record the referenced bet")
	}

	h.assertConsistent(t, target.ID())
}

// scenario isola um caso: carteira própria e IDs externos com sufixo, pois (provedor, ID
// externo) é único no banco inteiro.
type scenario struct {
	t      *testing.T
	h      *harness
	wallet *wallet.Wallet
	suffix string
}

func (s *scenario) ext(name string) string {
	return name + "-" + s.suffix
}

func (s *scenario) op(kind wager.Kind, name string, minorUnits int64) service.ProcessOperationCommand {
	return operation(s.t, s.wallet, kind, s.ext(name), minorUnits)
}

func (s *scenario) reference(
	command service.ProcessOperationCommand,
	name string,
) service.ProcessOperationCommand {
	return referencing(command, s.ext(name))
}

func (s *scenario) must(command service.ProcessOperationCommand) service.OperationResult {
	s.t.Helper()

	return s.h.process(s.t, command)
}

func TestReversalRejectionsCarryTheRightFailureCode(t *testing.T) {
	h := newHarness(t)

	tests := []struct {
		name    string
		setup   func(s *scenario)
		command func(s *scenario) service.ProcessOperationCommand
		want    wager.FailureCode
	}{
		{
			name:  "refund of a win",
			setup: func(s *scenario) { s.must(s.op(wager.Win, "win", 5000)) },
			command: func(s *scenario) service.ProcessOperationCommand {
				return s.reference(s.op(wager.Refund, "refund", 5000), "win")
			},
			want: wager.ReferenceKindInvalid,
		},
		{
			name:  "refund of a loss",
			setup: func(s *scenario) { s.must(s.op(wager.Loss, "loss", 0)) },
			command: func(s *scenario) service.ProcessOperationCommand {
				return s.reference(s.op(wager.Refund, "refund", 2500), "loss")
			},
			want: wager.ReferenceKindInvalid,
		},
		{
			name: "rollback of a rollback",
			setup: func(s *scenario) {
				s.must(s.op(wager.Bet, "bet", 2500))
				s.must(s.reference(s.op(wager.Rollback, "rb1", 2500), "bet"))
			},
			command: func(s *scenario) service.ProcessOperationCommand {
				return s.reference(s.op(wager.Rollback, "rb2", 2500), "rb1")
			},
			want: wager.ReferenceKindInvalid,
		},
		{
			name:  "win referencing a win",
			setup: func(s *scenario) { s.must(s.op(wager.Win, "win1", 5000)) },
			command: func(s *scenario) service.ProcessOperationCommand {
				return s.reference(s.op(wager.Win, "win2", 100), "win1")
			},
			want: wager.ReferenceKindInvalid,
		},
		{
			name:  "partial amount",
			setup: func(s *scenario) { s.must(s.op(wager.Bet, "bet", 2500)) },
			command: func(s *scenario) service.ProcessOperationCommand {
				return s.reference(s.op(wager.Refund, "refund", 2499), "bet")
			},
			want: wager.AmountMismatch,
		},
		{
			name:  "different round",
			setup: func(s *scenario) { s.must(s.op(wager.Bet, "bet", 2500)) },
			command: func(s *scenario) service.ProcessOperationCommand {
				command := s.reference(s.op(wager.Refund, "refund", 2500), "bet")
				command.RoundID = "round-2"

				return command
			},
			want: wager.ReferenceMismatch,
		},
		{
			name: "reference in another wallet",
			setup: func(s *scenario) {
				other := s.h.openFunded(s.t, 100000)
				s.must(operation(s.t, other, wager.Bet, s.ext("bet"), 2500))
			},
			command: func(s *scenario) service.ProcessOperationCommand {
				return s.reference(s.op(wager.Refund, "refund", 2500), "bet")
			},
			want: wager.ReferenceMismatch,
		},
		{
			name:  "reference that was rejected",
			setup: func(s *scenario) { s.must(s.op(wager.Bet, "bet", 2000000)) },
			command: func(s *scenario) service.ProcessOperationCommand {
				return s.reference(s.op(wager.Refund, "refund", 2000000), "bet")
			},
			want: wager.ReferenceNotProcessed,
		},
		{
			name: "second refund",
			setup: func(s *scenario) {
				s.must(s.op(wager.Bet, "bet", 2500))
				s.must(s.reference(s.op(wager.Refund, "r1", 2500), "bet"))
			},
			command: func(s *scenario) service.ProcessOperationCommand {
				return s.reference(s.op(wager.Refund, "r2", 2500), "bet")
			},
			want: wager.AlreadyReversed,
		},
		{
			name: "rollback after refund",
			setup: func(s *scenario) {
				s.must(s.op(wager.Bet, "bet", 2500))
				s.must(s.reference(s.op(wager.Refund, "r1", 2500), "bet"))
			},
			command: func(s *scenario) service.ProcessOperationCommand {
				return s.reference(s.op(wager.Rollback, "rb", 2500), "bet")
			},
			want: wager.AlreadyReversed,
		},
		{
			name: "refund after rollback",
			setup: func(s *scenario) {
				s.must(s.op(wager.Bet, "bet", 2500))
				s.must(s.reference(s.op(wager.Rollback, "rb", 2500), "bet"))
			},
			command: func(s *scenario) service.ProcessOperationCommand {
				return s.reference(s.op(wager.Refund, "r1", 2500), "bet")
			},
			want: wager.AlreadyReversed,
		},
		{
			name: "rollback of the bet after rolling back its refund",
			setup: func(s *scenario) {
				s.must(s.op(wager.Bet, "bet", 2500))
				s.must(s.reference(s.op(wager.Refund, "r1", 2500), "bet"))
				s.must(s.reference(s.op(wager.Rollback, "rb1", 2500), "r1"))
			},
			command: func(s *scenario) service.ProcessOperationCommand {
				return s.reference(s.op(wager.Rollback, "rb2", 2500), "bet")
			},
			want: wager.AlreadyReversed,
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := &scenario{
				t:      t,
				h:      h,
				wallet: h.openFunded(t, 100000),
				suffix: fmt.Sprint(index),
			}
			test.setup(s)

			result, err := h.operations.Process(context.Background(), test.command(s))

			if err != nil || result.Status != wager.Rejected || result.FailureCode != test.want {
				t.Fatalf("result = %+v (%v), want REJECTED with %s", result, err, test.want)
			}

			h.assertConsistent(t, s.wallet.ID())
		})
	}
}

func TestReversalWithoutFundsHasItsOwnFailureCode(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	h.process(t, operation(t, target, wager.Win, "win-1", 50000))
	h.process(t, operation(t, target, wager.Bet, "bet-1", 140000))
	entriesBefore := h.count(t, `SELECT count(*) FROM wallet_ledger_entries`)

	rollback := h.process(t, referencing(operation(t, target, wager.Rollback, "rb-1", 50000), "win-1"))
	plainBet := h.process(t, operation(t, target, wager.Bet, "bet-2", 99999999))

	if rollback.Status != wager.Rejected || rollback.FailureCode != wager.ReversalInsufficientFunds {
		t.Fatalf("rollback = %+v, want REVERSAL_INSUFFICIENT_FUNDS", rollback)
	}

	if plainBet.FailureCode != wager.InsufficientFunds || plainBet.FailureCode == rollback.FailureCode {
		t.Fatalf("bet = %+v, want INSUFFICIENT_FUNDS distinct from the reversal code", plainBet)
	}

	if h.count(t, `SELECT count(*) FROM wallet_ledger_entries`) != entriesBefore ||
		h.walletState(t, target.ID()).Balance().Amount() != "100.00" {
		t.Fatal("rejected operations must not move money")
	}

	h.assertConsistent(t, target.ID())
}
