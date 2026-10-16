//go:build integration

package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"apostas-go/internal/model/event"
	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/port"
	"apostas-go/internal/platform/idgen"
	"apostas-go/internal/repository/postgres"
	"apostas-go/internal/service"
	"apostas-go/internal/testsupport/postgresdb"
)

var (
	baseTime = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	errBoom  = errors.New("boom")
)

// harness monta o Service com os repositórios reais sobre um Postgres em container.
type harness struct {
	pool    *pgxpool.Pool
	wallets *service.WalletService
	ids     idgen.Generator
	outbox  *postgres.OutboxRepository
	build   func(outbox port.OutboxRepository) *service.WalletService

	operations      *service.OperationService
	buildOperations func(outbox port.OutboxRepository) *service.OperationService
	clock           *steppingClock
	policy          service.ReferencePolicy
	resolver        func() *service.ReferenceResolver
	resumer         func() *service.PendingResumer
	queries         *service.QueryService
	messages        func(outbox port.OutboxRepository) *service.MessageHandler
	handler         *service.MessageHandler
	observed        *recordingObserver
	transactions    *postgres.TransactionRepository
	newPublisher    func(
		destination port.EventPublisher,
		outbox port.OutboxRepository,
		policy service.PublishPolicy,
	) *service.OutboxPublisher
}

// steppingClock é um relógio de teste que só anda quando o teste manda.
type steppingClock struct {
	mutex   sync.Mutex
	instant time.Time
}

func (clock *steppingClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()

	return clock.instant
}

func (clock *steppingClock) Advance(duration time.Duration) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()

	clock.instant = clock.instant.Add(duration)
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	return newHarnessWithPolicy(t, service.ReferencePolicy{})
}

func newHarnessWithPolicy(t *testing.T, policy service.ReferencePolicy) *harness {
	t.Helper()

	stepping := &steppingClock{instant: baseTime}
	observed := &recordingObserver{}
	pool := postgresdb.Start(t)
	connection := postgres.NewConnection(pool)
	manager := postgres.NewTransactionManager(pool)
	outbox := postgres.NewOutboxRepository(connection)

	build := func(selected port.OutboxRepository) *service.WalletService {
		return service.NewWalletService(service.WalletServiceDependencies{
			Transactions: manager,
			Wallets:      postgres.NewWalletRepository(connection),
			Operations:   postgres.NewTransactionRepository(connection),
			Ledger:       postgres.NewLedgerRepository(connection),
			Outbox:       selected,
			Clock:        stepping,
			IDs:          idgen.Generator{},
			Observer:     observed,
		})
	}

	dependencies := func(selected port.OutboxRepository) service.OperationServiceDependencies {
		return service.OperationServiceDependencies{
			Transactions:    manager,
			Wallets:         postgres.NewWalletRepository(connection),
			Operations:      postgres.NewTransactionRepository(connection),
			Ledger:          postgres.NewLedgerRepository(connection),
			Outbox:          selected,
			Clock:           stepping,
			IDs:             idgen.Generator{},
			ReferencePolicy: policy,
			Observer:        observed,
		}
	}

	buildOperations := func(selected port.OutboxRepository) *service.OperationService {
		return service.NewOperationService(dependencies(selected))
	}

	messages := func(selected port.OutboxRepository) *service.MessageHandler {
		return service.NewMessageHandler(service.MessageHandlerDependencies{
			Transactions: manager,
			Inbox:        postgres.NewInboxRepository(connection),
			Operations:   buildOperations(selected),
			Clock:        stepping,
			ConsumerName: "wager-transactions",
		})
	}

	return &harness{
		messages:        messages,
		handler:         messages(outbox),
		observed:        observed,
		pool:            pool,
		wallets:         build(outbox),
		ids:             idgen.Generator{},
		outbox:          outbox,
		build:           build,
		operations:      buildOperations(outbox),
		buildOperations: buildOperations,
		clock:           stepping,
		policy:          policy,
		resolver: func() *service.ReferenceResolver {
			return service.NewReferenceResolver(dependencies(outbox))
		},
		resumer: func() *service.PendingResumer {
			return service.NewPendingResumer(dependencies(outbox))
		},
		queries: service.NewQueryService(service.QueryServiceDependencies{
			Wallets:    postgres.NewWalletRepository(connection),
			Operations: postgres.NewTransactionRepository(connection),
			Ledger:     postgres.NewLedgerRepository(connection),
		}),
		transactions: postgres.NewTransactionRepository(connection),
		newPublisher: func(
			destination port.EventPublisher,
			selected port.OutboxRepository,
			publishPolicy service.PublishPolicy,
		) *service.OutboxPublisher {
			return service.NewOutboxPublisher(service.OutboxPublisherDependencies{
				Outbox:    selected,
				Publisher: destination,
				Clock:     stepping,
				Policy:    publishPolicy,
			})
		},
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

func (h *harness) newPlayerID(t *testing.T) identity.PlayerID {
	t.Helper()

	id, err := h.ids.NewWalletID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}

	return identity.PlayerID(id)
}

func (h *harness) count(t *testing.T, query string, arguments ...any) int {
	t.Helper()

	var total int
	row := h.pool.QueryRow(context.Background(), query, arguments...)
	if err := row.Scan(&total); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}

	return total
}

// failingOutbox delega ao repositório real e falha em Add, para provar a atomicidade.
type failingOutbox struct {
	port.OutboxRepository
}

func (failingOutbox) Add(context.Context, event.Event) error {
	return errBoom
}

func identityPlayer(text string) identity.PlayerID {
	return identity.PlayerID(text)
}
