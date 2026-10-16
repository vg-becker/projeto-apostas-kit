//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"apostas-go/internal/model/event"
	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
	"apostas-go/internal/platform/idgen"
	"apostas-go/internal/repository/postgres"
)

var baseTime = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

// fixture reúne um banco real com migrations aplicadas e todos os repositórios.
type fixture struct {
	pool         *pgxpool.Pool
	manager      *postgres.TransactionManager
	connection   *postgres.Connection
	wallets      *postgres.WalletRepository
	transactions *postgres.TransactionRepository
	ledger       *postgres.LedgerRepository
	inbox        *postgres.InboxRepository
	outbox       *postgres.OutboxRepository
	ids          idgen.Generator
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	pool := startDatabase(t)
	connection := postgres.NewConnection(pool)

	return &fixture{
		pool:         pool,
		manager:      postgres.NewTransactionManager(pool),
		connection:   connection,
		wallets:      postgres.NewWalletRepository(connection),
		transactions: postgres.NewTransactionRepository(connection),
		ledger:       postgres.NewLedgerRepository(connection),
		inbox:        postgres.NewInboxRepository(connection),
		outbox:       postgres.NewOutboxRepository(connection),
	}
}

func brl(t *testing.T, minorUnits int64) money.Money {
	t.Helper()

	amount, err := money.New(minorUnits, money.BRL)
	if err != nil {
		t.Fatalf("money.New error = %v", err)
	}

	return amount
}

// openWallet cria e persiste carteira, transação OPENING e lançamento de abertura.
func (f *fixture) openWallet(t *testing.T, initial int64) *wallet.Wallet {
	t.Helper()

	return f.openWalletFor(t, f.newPlayerID(t), initial)
}

func (f *fixture) newPlayerID(t *testing.T) identity.PlayerID {
	t.Helper()

	walletID, err := f.ids.NewWalletID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}

	return identity.PlayerID(walletID)
}

func (f *fixture) openWalletFor(
	t *testing.T,
	playerID identity.PlayerID,
	initial int64,
) *wallet.Wallet {
	t.Helper()

	walletID, _ := f.ids.NewWalletID()
	openingID, _ := f.ids.NewTransactionID()
	entryID, _ := f.ids.NewEntryID()

	opened, err := wallet.Open(wallet.OpenParams{
		ID:                   walletID,
		PlayerID:             playerID,
		InitialBalance:       brl(t, initial),
		OpeningTransactionID: openingID,
		OpeningEntryID:       entryID,
		Now:                  baseTime,
	})
	if err != nil {
		t.Fatalf("wallet.Open error = %v", err)
	}

	err = f.manager.WithinTransaction(context.Background(), func(ctx context.Context) error {
		return f.persistOpening(ctx, opened, openingID)
	})
	if err != nil {
		t.Fatalf("persist opening: %v", err)
	}

	return opened.Wallet
}

func (f *fixture) persistOpening(
	ctx context.Context,
	opened wallet.Opened,
	openingID identity.TransactionID,
) error {
	if err := f.wallets.Create(ctx, opened.Wallet); err != nil {
		return err
	}

	if opened.OpeningEntry == nil {
		return nil
	}

	snapshot := opened.Wallet.Snapshot()
	opening, err := wager.NewOpening(wager.OpeningParams{
		ID:       openingID,
		WalletID: snapshot.ID,
		PlayerID: snapshot.PlayerID,
		Money:    snapshot.Balance,
		Now:      baseTime,
	})
	if err != nil {
		return err
	}

	if err := f.transactions.Insert(ctx, opening); err != nil {
		return err
	}

	return f.ledger.Append(ctx, *opened.OpeningEntry)
}

// externalParams monta uma operação externa válida para a carteira.
func (f *fixture) externalParams(
	t *testing.T,
	target *wallet.Wallet,
	kind wager.Kind,
	externalID string,
	minorUnits int64,
	at time.Time,
) wager.ExternalParams {
	t.Helper()

	params, err := f.buildParams(target, kind, externalID, minorUnits, at)
	if err != nil {
		t.Fatalf("build params: %v", err)
	}

	return params
}

func (f *fixture) buildParams(
	target *wallet.Wallet,
	kind wager.Kind,
	externalID string,
	minorUnits int64,
	at time.Time,
) (wager.ExternalParams, error) {
	id, err := f.ids.NewTransactionID()
	if err != nil {
		return wager.ExternalParams{}, err
	}

	amount, err := money.New(minorUnits, money.BRL)
	if err != nil {
		return wager.ExternalParams{}, err
	}

	return wager.ExternalParams{
		ID:                    id,
		ProviderID:            "provider-a",
		ExternalTransactionID: externalID,
		IdempotencyKey:        "provider-a:" + externalID,
		PayloadHash:           fmt.Sprintf("%x", sha256.Sum256([]byte(externalID))),
		WalletID:              target.ID(),
		PlayerID:              target.PlayerID(),
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  kind,
		Money:                 amount,
		Now:                   at,
	}, nil
}

func (f *fixture) newExternal(
	t *testing.T,
	params wager.ExternalParams,
) *wager.Transaction {
	t.Helper()

	transaction, err := wager.NewExternal(params)
	if err != nil {
		t.Fatalf("wager.NewExternal error = %v", err)
	}

	return transaction
}

func (f *fixture) insertTransaction(t *testing.T, transaction *wager.Transaction) {
	t.Helper()

	err := f.transactions.Insert(context.Background(), transaction)
	if err != nil {
		t.Fatalf("insert transaction: %v", err)
	}
}

// placeBet executa uma aposta como o Service fará: lock da carteira, regra do domínio,
// lançamento e estado final na mesma transação. Devolve o estado final da aposta.
func (f *fixture) placeBet(
	ctx context.Context,
	walletID identity.WalletID,
	externalID string,
	minorUnits int64,
	at time.Time,
) (wager.Status, error) {
	var status wager.Status

	err := f.manager.WithinTransaction(ctx, func(ctx context.Context) error {
		var err error
		status, err = f.betInsideTransaction(ctx, walletID, externalID, minorUnits, at)

		return err
	})

	return status, err
}

func (f *fixture) betInsideTransaction(
	ctx context.Context,
	walletID identity.WalletID,
	externalID string,
	minorUnits int64,
	at time.Time,
) (wager.Status, error) {
	target, err := f.wallets.GetForUpdate(ctx, walletID)
	if err != nil {
		return "", err
	}

	params, err := f.buildParams(target, wager.Bet, externalID, minorUnits, at)
	if err != nil {
		return "", err
	}

	transaction, err := wager.NewExternal(params)
	if err != nil {
		return "", err
	}

	if err := f.transactions.Insert(ctx, transaction); err != nil {
		return "", err
	}

	return f.applyBet(ctx, target, transaction, at)
}

func (f *fixture) applyBet(
	ctx context.Context,
	target *wallet.Wallet,
	transaction *wager.Transaction,
	at time.Time,
) (wager.Status, error) {
	expectedVersion := target.Version()
	entryID, err := f.ids.NewEntryID()
	if err != nil {
		return "", err
	}

	entry, err := target.Debit(wallet.MovementParams{
		TransactionID: transaction.ID(),
		EntryID:       entryID,
		Amount:        transaction.Money(),
		Now:           at,
	})
	if errors.Is(err, wallet.ErrInsufficientFunds) {
		return f.rejectBet(ctx, transaction, at)
	}

	if err != nil {
		return "", err
	}

	if err := f.ledger.Append(ctx, entry); err != nil {
		return "", err
	}

	if err := f.wallets.Update(ctx, target, expectedVersion); err != nil {
		return "", err
	}

	return f.finishBet(ctx, transaction, target.Balance(), at)
}

func (f *fixture) rejectBet(
	ctx context.Context,
	transaction *wager.Transaction,
	at time.Time,
) (wager.Status, error) {
	if err := transaction.Reject(wager.InsufficientFunds, at); err != nil {
		return "", err
	}

	return transaction.Status(), f.transactions.Update(ctx, transaction)
}

func (f *fixture) finishBet(
	ctx context.Context,
	transaction *wager.Transaction,
	balance money.Money,
	at time.Time,
) (wager.Status, error) {
	err := transaction.MarkProcessed(wager.ProcessedResult{Balance: balance}, at)
	if err != nil {
		return "", err
	}

	return transaction.Status(), f.transactions.Update(ctx, transaction)
}

// newEvent cria um evento WalletBalanceChanged válido, com ID e agregado próprios.
func (f *fixture) newEvent(t *testing.T, at time.Time) event.Event {
	t.Helper()

	eventID, _ := f.ids.NewEventID()
	entryID, _ := f.ids.NewEntryID()
	walletID, _ := f.ids.NewWalletID()
	transactionID, _ := f.ids.NewTransactionID()

	entry, err := ledger.NewEntry(ledger.EntryParams{
		ID:            entryID,
		WalletID:      walletID,
		TransactionID: transactionID,
		Direction:     ledger.Debit,
		Money:         brl(t, 2500),
		BalanceBefore: brl(t, 100000),
		BalanceAfter:  brl(t, 97500),
		CreatedAt:     at,
	})
	if err != nil {
		t.Fatalf("ledger.NewEntry error = %v", err)
	}

	envelope, err := event.NewWalletBalanceChanged(
		event.Header{EventID: eventID, CorrelationID: "correlation-1", Now: at},
		entry,
		2,
	)
	if err != nil {
		t.Fatalf("event.NewWalletBalanceChanged error = %v", err)
	}

	return envelope
}
