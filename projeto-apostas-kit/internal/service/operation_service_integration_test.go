//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
	"apostas-go/internal/service"
)

func (h *harness) openFunded(t *testing.T, minorUnits int64) *wallet.Wallet {
	t.Helper()

	opened, err := h.wallets.OpenWallet(context.Background(), service.OpenWalletCommand{
		PlayerID:       h.newPlayerID(t),
		InitialBalance: brl(t, minorUnits),
	})
	if err != nil {
		t.Fatalf("OpenWallet error = %v", err)
	}

	return opened
}

func operation(
	t *testing.T,
	target *wallet.Wallet,
	kind wager.Kind,
	externalID string,
	minorUnits int64,
) service.ProcessOperationCommand {
	t.Helper()

	return service.ProcessOperationCommand{
		ProviderID:            "provider-a",
		ExternalTransactionID: externalID,
		IdempotencyKey:        "provider-a:" + externalID,
		PlayerID:              target.PlayerID(),
		WalletID:              target.ID(),
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  kind,
		Money:                 brl(t, minorUnits),
		CorrelationID:         "correlation-" + externalID,
		CausationID:           "message-" + externalID,
	}
}

func (h *harness) process(
	t *testing.T,
	command service.ProcessOperationCommand,
) service.OperationResult {
	t.Helper()

	result, err := h.operations.Process(context.Background(), command)
	if err != nil {
		t.Fatalf("Process(%s) error = %v", command.ExternalTransactionID, err)
	}

	return result
}

func (h *harness) walletState(t *testing.T, id identity.WalletID) *wallet.Wallet {
	t.Helper()

	found, err := h.wallets.GetWallet(context.Background(), id)
	if err != nil {
		t.Fatalf("GetWallet error = %v", err)
	}

	return found
}

func (h *harness) eventCount(t *testing.T, eventType string) int {
	t.Helper()

	return h.count(t, `SELECT count(*) FROM outbox_events WHERE event_type = $1`, eventType)
}

func (h *harness) ledgerEntries(t *testing.T, id identity.WalletID, direction string) int {
	t.Helper()

	return h.count(
		t,
		`SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = $2`,
		id.String(),
		direction,
	)
}

func TestBetDebitsAndWritesEverythingTogether(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)

	result := h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))

	if result.Status != wager.Processed || result.Balance.Amount() != "975.00" ||
		result.IdempotentReplay || result.FailureCode != "" {
		t.Fatalf("result = %+v", result)
	}

	state := h.walletState(t, target.ID())
	if state.Balance().Amount() != "975.00" || state.Version() != 2 {
		t.Fatalf("wallet = %+v", state.Snapshot())
	}

	if h.ledgerEntries(t, target.ID(), "DEBIT") != 1 {
		t.Fatal("exactly one debit entry expected")
	}

	// 2 eventos da abertura + Processed + BalanceChanged da aposta.
	if h.eventCount(t, "WagerTransactionProcessed") != 2 || h.eventCount(t, "WalletBalanceChanged") != 2 {
		t.Fatal("bet must enqueue Processed and BalanceChanged")
	}

	var walletVersion, causation string
	row := h.pool.QueryRow(
		context.Background(),
		`SELECT payload->'data'->>'walletVersion', payload->>'causationId'
		   FROM outbox_events
		  WHERE event_type = 'WalletBalanceChanged'
		    AND payload->'data'->>'direction' = 'DEBIT'`,
	)
	if err := row.Scan(&walletVersion, &causation); err != nil || walletVersion != "2" ||
		causation != result.TransactionID.String() {
		t.Fatalf("balance event version = %s, causation = %s (%v)", walletVersion, causation, err)
	}
}

func TestWinCreditsTheWallet(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)

	result := h.process(t, operation(t, target, wager.Win, "win-1", 5000))

	if result.Status != wager.Processed || result.Balance.Amount() != "1050.00" {
		t.Fatalf("result = %+v", result)
	}

	if h.ledgerEntries(t, target.ID(), "CREDIT") != 2 || h.walletState(t, target.ID()).Version() != 2 {
		t.Fatal("win must append a credit and bump the version")
	}
}

func TestLossMovesNothing(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	entriesBefore := h.count(t, `SELECT count(*) FROM wallet_ledger_entries`)
	balanceEventsBefore := h.eventCount(t, "WalletBalanceChanged")

	result := h.process(t, operation(t, target, wager.Loss, "loss-1", 0))

	if result.Status != wager.Processed || result.Balance.Amount() != "1000.00" {
		t.Fatalf("result = %+v", result)
	}

	state := h.walletState(t, target.ID())
	if state.Version() != 1 || state.Balance().Amount() != "1000.00" {
		t.Fatalf("loss changed the wallet: %+v", state.Snapshot())
	}

	if h.count(t, `SELECT count(*) FROM wallet_ledger_entries`) != entriesBefore ||
		h.eventCount(t, "WalletBalanceChanged") != balanceEventsBefore ||
		h.eventCount(t, "WagerTransactionProcessed") != 2 {
		t.Fatal("loss must emit only WagerTransactionProcessed and no ledger entry")
	}
}

func TestBetWithoutFundsIsRejectedAndPersisted(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 10000)

	result, err := h.operations.Process(
		context.Background(),
		operation(t, target, wager.Bet, "bet-1", 20000),
	)

	if err != nil || result.Status != wager.Rejected ||
		result.FailureCode != wager.InsufficientFunds || result.Balance.IsInitialized() {
		t.Fatalf("result = %+v (%v)", result, err)
	}

	state := h.walletState(t, target.ID())
	if state.Balance().Amount() != "100.00" || state.Version() != 1 ||
		h.ledgerEntries(t, target.ID(), "DEBIT") != 0 {
		t.Fatalf("rejection moved money: %+v", state.Snapshot())
	}

	if h.eventCount(t, "WagerTransactionRejected") != 1 || h.eventCount(t, "WalletBalanceChanged") != 1 {
		t.Fatal("rejection must emit only WagerTransactionRejected")
	}

	stored := h.count(
		t,
		`SELECT count(*) FROM wager_transactions WHERE status = 'REJECTED' AND failure_code = 'INSUFFICIENT_FUNDS'`,
	)
	if stored != 1 {
		t.Fatalf("rejected transactions persisted = %d, want 1", stored)
	}
}

func TestReplayReturnsTheOriginalResultWithoutNewEffects(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	command := operation(t, target, wager.Bet, "bet-1", 2500)
	first := h.process(t, command)

	h.process(t, operation(t, target, wager.Win, "win-1", 10000))
	replay := h.process(t, command)

	if !replay.IdempotentReplay || replay.TransactionID != first.TransactionID ||
		replay.Status != wager.Processed {
		t.Fatalf("replay = %+v, first = %+v", replay, first)
	}

	if replay.Balance.Amount() != "975.00" {
		t.Fatalf("replay balance = %s, want the balance observed at processing (975.00)", replay.Balance)
	}

	if h.ledgerEntries(t, target.ID(), "DEBIT") != 1 || h.walletState(t, target.ID()).Version() != 3 {
		t.Fatal("replay must not apply the operation again")
	}

	if h.eventCount(t, "WagerTransactionProcessed") != 3 {
		t.Fatal("replay must not emit events")
	}
}

func TestReplayOfARejectionKeepsTheRejection(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 10000)
	command := operation(t, target, wager.Bet, "bet-1", 20000)
	first, _ := h.operations.Process(context.Background(), command)

	h.process(t, operation(t, target, wager.Win, "win-1", 100000))
	replay, err := h.operations.Process(context.Background(), command)

	if err != nil || !replay.IdempotentReplay || replay.Status != wager.Rejected ||
		replay.FailureCode != wager.InsufficientFunds || replay.TransactionID != first.TransactionID {
		t.Fatalf("replay = %+v (%v), want the original rejection even though funds exist now", replay, err)
	}
}

func TestSameKeyWithDifferentContentConflicts(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	conflicting := operation(t, target, wager.Bet, "bet-1", 9900)

	_, err := h.operations.Process(context.Background(), conflicting)

	if !errors.Is(err, service.ErrIdempotencyConflict) {
		t.Fatalf("error = %v, want %v", err, service.ErrIdempotencyConflict)
	}

	if h.walletState(t, target.ID()).Balance().Amount() != "975.00" {
		t.Fatal("a conflicting resend must not change the balance")
	}
}

func TestSameExternalIDUnderAnotherKeyConflicts(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	h.process(t, operation(t, target, wager.Bet, "bet-1", 2500))
	other := operation(t, target, wager.Bet, "bet-1", 2500)
	other.IdempotencyKey = "another-key"

	_, err := h.operations.Process(context.Background(), other)

	if !errors.Is(err, service.ErrIdempotencyConflict) {
		t.Fatalf("error = %v, want %v", err, service.ErrIdempotencyConflict)
	}

	if h.ledgerEntries(t, target.ID(), "DEBIT") != 1 {
		t.Fatal("the operation must not be applied twice under another key")
	}
}

func TestProvidersHaveIndependentKeySpaces(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	first := operation(t, target, wager.Bet, "bet-1", 2500)
	second := first
	second.ProviderID = "provider-b"

	resultA := h.process(t, first)
	resultB := h.process(t, second)

	if resultA.TransactionID == resultB.TransactionID || resultB.IdempotentReplay {
		t.Fatalf("providers collided: %+v / %+v", resultA, resultB)
	}

	if h.ledgerEntries(t, target.ID(), "DEBIT") != 2 {
		t.Fatal("each provider operation must be applied once")
	}
}

func TestIdempotencySurvivesANewServiceInstance(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	command := operation(t, target, wager.Bet, "bet-1", 2500)
	first := h.process(t, command)

	restarted := h.buildOperations(h.outbox)
	replay, err := restarted.Process(context.Background(), command)

	if err != nil || !replay.IdempotentReplay || replay.TransactionID != first.TransactionID {
		t.Fatalf("replay = %+v (%v)", replay, err)
	}
}

func TestOutboxFailureRollsBackTheOperationAndALaterRetrySucceeds(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	command := operation(t, target, wager.Bet, "bet-1", 2500)
	broken := h.buildOperations(failingOutbox{OutboxRepository: h.outbox})

	_, err := broken.Process(context.Background(), command)

	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want %v", err, errBoom)
	}

	state := h.walletState(t, target.ID())
	if state.Balance().Amount() != "1000.00" || state.Version() != 1 ||
		h.ledgerEntries(t, target.ID(), "DEBIT") != 0 ||
		h.count(t, `SELECT count(*) FROM wager_transactions WHERE kind = 'BET'`) != 0 {
		t.Fatal("a failed operation must leave nothing behind")
	}

	retry := h.process(t, command)

	if retry.Status != wager.Processed || retry.IdempotentReplay {
		t.Fatalf("retry = %+v, want a normal first processing", retry)
	}
}

func TestInvalidCommandsPersistNothing(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	transactionsBefore := h.count(t, `SELECT count(*) FROM wager_transactions`)

	tests := []struct {
		name    string
		mutate  func(command *service.ProcessOperationCommand)
		wantErr error
	}{
		{name: "opening is internal", mutate: func(c *service.ProcessOperationCommand) {
			c.Kind = wager.Opening
		}, wantErr: wager.ErrOpeningNotAllowed},
		{name: "loss with amount", mutate: func(c *service.ProcessOperationCommand) {
			c.Kind = wager.Loss
		}, wantErr: wager.ErrAmountPolicy},
		{name: "bet with zero", mutate: func(c *service.ProcessOperationCommand) {
			c.Money = brl(t, 0)
		}, wantErr: wager.ErrAmountPolicy},
		{name: "missing provider", mutate: func(c *service.ProcessOperationCommand) {
			c.ProviderID = ""
		}, wantErr: wager.ErrInvalidTransaction},
		{name: "missing key", mutate: func(c *service.ProcessOperationCommand) {
			c.IdempotencyKey = ""
		}, wantErr: wager.ErrInvalidTransaction},
		{name: "unknown kind", mutate: func(c *service.ProcessOperationCommand) {
			c.Kind = "SPIN"
		}, wantErr: wager.ErrInvalidTransaction},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := operation(t, target, wager.Bet, "bet-x", 2500)
			test.mutate(&command)

			_, err := h.operations.Process(context.Background(), command)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}

	if h.count(t, `SELECT count(*) FROM wager_transactions`) != transactionsBefore {
		t.Fatal("invalid commands must not persist transactions")
	}
}

func TestWalletMismatchesAreInputErrorsAndPersistNothing(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	transactionsBefore := h.count(t, `SELECT count(*) FROM wager_transactions`)
	missing, _ := h.ids.NewWalletID()

	unknown := operation(t, target, wager.Bet, "bet-1", 100)
	unknown.WalletID = missing

	otherPlayer := operation(t, target, wager.Bet, "bet-2", 100)
	otherPlayer.PlayerID = h.newPlayerID(t)

	usd := operation(t, target, wager.Bet, "bet-3", 100)
	usd.Money = usdAmount(t, 100)

	tests := []struct {
		name    string
		command service.ProcessOperationCommand
		wantErr error
	}{
		{name: "unknown wallet", command: unknown, wantErr: service.ErrWalletNotFound},
		{name: "other player", command: otherPlayer, wantErr: service.ErrWalletPlayerMismatch},
		{name: "other currency", command: usd, wantErr: service.ErrCurrencyMismatch},
	}

	for _, test := range tests {
		_, err := h.operations.Process(context.Background(), test.command)

		if !errors.Is(err, test.wantErr) {
			t.Fatalf("%s: error = %v, want %v", test.name, err, test.wantErr)
		}
	}

	if h.count(t, `SELECT count(*) FROM wager_transactions`) != transactionsBefore {
		t.Fatal("input errors must not be persisted, so the client can fix and resend")
	}
}

func TestStoredHashEqualsTheCanonicalHash(t *testing.T) {
	h := newHarness(t)
	target := h.openFunded(t, 100000)
	command := operation(t, target, wager.Bet, "bet-1", 2500)
	h.process(t, command)

	want, err := service.CanonicalHash(command)
	if err != nil {
		t.Fatalf("CanonicalHash error = %v", err)
	}

	var stored string
	row := h.pool.QueryRow(
		context.Background(),
		`SELECT payload_hash FROM wager_transactions WHERE external_transaction_id = 'bet-1'`,
	)
	if err := row.Scan(&stored); err != nil || stored != want {
		t.Fatalf("stored hash = %q (%v), want %q", stored, err, want)
	}
}
