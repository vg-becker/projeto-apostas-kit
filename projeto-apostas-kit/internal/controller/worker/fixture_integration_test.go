//go:build integration

package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"apostas-go/internal/controller/worker"
	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/platform/clock"
	"apostas-go/internal/platform/idgen"
	"apostas-go/internal/platform/sqsclient"
	"apostas-go/internal/repository/postgres"
	"apostas-go/internal/service"
	"apostas-go/internal/testsupport/postgresdb"
)

// lossyConfirmation entrega ao repositório real, mas perde a confirmação das primeiras
// chamadas de MarkPublished, como se o processo caísse depois de publicar.
type lossyConfirmation struct {
	port.OutboxRepository
	mutex sync.Mutex
	lose  int
}

func (outbox *lossyConfirmation) MarkPublished(
	ctx context.Context,
	id identity.EventID,
	now time.Time,
) error {
	outbox.mutex.Lock()
	defer outbox.mutex.Unlock()

	if outbox.lose > 0 {
		outbox.lose--

		return fmt.Errorf("%w: confirmation lost", port.ErrTransient)
	}

	return outbox.OutboxRepository.MarkPublished(ctx, id, now)
}

type fixture struct {
	pool     *pgxpool.Pool
	client   *sqsclient.Client
	queues   sqsclient.Queues
	outbox   *postgres.OutboxRepository
	wallets  *service.WalletService
	ops      service.OperationServiceDependencies
	messages *service.OperationService
	ids      idgen.Generator
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	pool := postgresdb.Start(t)
	connection := postgres.NewConnection(pool)
	outbox := postgres.NewOutboxRepository(connection)

	dependencies := service.OperationServiceDependencies{
		Transactions: postgres.NewTransactionManager(pool),
		Wallets:      postgres.NewWalletRepository(connection),
		Operations:   postgres.NewTransactionRepository(connection),
		Ledger:       postgres.NewLedgerRepository(connection),
		Outbox:       outbox,
		Clock:        clock.System{},
		IDs:          idgen.Generator{},
		ReferencePolicy: service.ReferencePolicy{
			MaxAttempts: 10, TTL: time.Minute, InitialBackoff: time.Second, MaxBackoff: 2 * time.Second,
			ClaimLease: 5 * time.Second,
		},
	}

	client, err := sqsclient.New(context.Background(), broker.Config())
	if err != nil {
		t.Fatalf("sqsclient.New error = %v", err)
	}

	return &fixture{
		pool:     pool,
		client:   client,
		queues:   broker.Isolated(t, client),
		outbox:   outbox,
		ops:      dependencies,
		messages: service.NewOperationService(dependencies),
		wallets: service.NewWalletService(service.WalletServiceDependencies{
			Transactions: dependencies.Transactions,
			Wallets:      dependencies.Wallets,
			Operations:   dependencies.Operations,
			Ledger:       dependencies.Ledger,
			Outbox:       outbox,
			Clock:        clock.System{},
			IDs:          idgen.Generator{},
		}),
	}
}

func (f *fixture) count(t *testing.T, query string, arguments ...any) int {
	t.Helper()

	var total int
	if err := f.pool.QueryRow(context.Background(), query, arguments...).Scan(&total); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}

	return total
}

func (f *fixture) unpublished(t *testing.T) int {
	t.Helper()

	return f.count(t, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`)
}

func (f *fixture) eventually(t *testing.T, within time.Duration, what string, check func() bool) {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if check() {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("not met within %v: %s", within, what)
}

type walletRef struct {
	id       identity.WalletID
	playerID identity.PlayerID
}

func (f *fixture) openWallet(t *testing.T, minorUnits int64) walletRef {
	t.Helper()

	playerID, err := f.ids.NewWalletID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}

	balance, err := money.New(minorUnits, money.BRL)
	if err != nil {
		t.Fatalf("money: %v", err)
	}

	opened, err := f.wallets.OpenWallet(context.Background(), service.OpenWalletCommand{
		PlayerID:       identity.PlayerID(playerID.String()),
		InitialBalance: balance,
	})
	if err != nil {
		t.Fatalf("OpenWallet error = %v", err)
	}

	return walletRef{id: opened.ID(), playerID: opened.PlayerID()}
}

func (f *fixture) command(
	t *testing.T,
	target walletRef,
	kind wager.Kind,
	external string,
	minorUnits int64,
) service.ProcessOperationCommand {
	t.Helper()

	amount, err := money.New(minorUnits, money.BRL)
	if err != nil {
		t.Fatalf("money: %v", err)
	}

	return service.ProcessOperationCommand{
		ProviderID:            "provider-a",
		ExternalTransactionID: external,
		IdempotencyKey:        "provider-a:" + external,
		PlayerID:              target.playerID,
		WalletID:              target.id,
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  kind,
		Money:                 amount,
	}
}

func (f *fixture) bet(t *testing.T, target walletRef, external string, minorUnits int64) {
	t.Helper()

	if _, err := f.messages.Process(context.Background(), f.command(t, target, wager.Bet, external, minorUnits)); err != nil {
		t.Fatalf("Process(%s) error = %v", external, err)
	}
}

// walletWithBets abre uma carteira e faz n apostas: 2 eventos da abertura mais 2 por aposta.
func (f *fixture) walletWithBets(t *testing.T, bets int) (walletRef, int) {
	t.Helper()

	target := f.openWallet(t, 100000)
	for index := range bets {
		f.bet(t, target, fmt.Sprintf("tx-%s-%d", target.id, index), 100)
	}

	return target, 2 + 2*bets
}

func (f *fixture) outboxPayloads(t *testing.T) map[string]string {
	t.Helper()

	rows, err := f.pool.Query(context.Background(), `SELECT event_id::text, payload::text FROM outbox_events`)
	if err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	defer rows.Close()

	payloads := map[string]string{}
	for rows.Next() {
		var id, payload string
		if err := rows.Scan(&id, &payload); err != nil {
			t.Fatalf("scan: %v", err)
		}

		payloads[id] = payload
	}

	return payloads
}

// drain recolhe e apaga as mensagens da fila (apagar libera as seguintes do mesmo grupo,
// que o FIFO segura enquanto uma está em voo) até juntar quantas se espera, ou o prazo.
func (f *fixture) drain(t *testing.T, queueURL string, expected int, within time.Duration) []sqsclient.Message {
	t.Helper()

	var collected []sqsclient.Message

	deadline := time.Now().Add(within)
	for len(collected) < expected && time.Now().Before(deadline) {
		messages, err := f.client.Receive(context.Background(), sqsclient.ReceiveParams{
			QueueURL: queueURL, MaxMessages: 10, WaitSeconds: 1,
		})
		if err != nil {
			t.Fatalf("receive: %v", err)
		}

		for _, message := range messages {
			if err := f.client.Delete(context.Background(), queueURL, message.ReceiptHandle); err != nil {
				t.Fatalf("delete: %v", err)
			}
		}

		collected = append(collected, messages...)
	}

	return collected
}

// assertDeliveredOnce confere que a fila recebeu exatamente os eventos da outbox, cada um
// uma vez, com o corpo e o agregado originais.
func (f *fixture) assertDeliveredOnce(t *testing.T, delivered []sqsclient.Message) {
	t.Helper()

	stored := f.outboxPayloads(t)
	seen := map[string]int{}

	for _, message := range delivered {
		id := message.Attributes["eventId"]
		seen[id]++

		if stored[id] != message.Body || message.GroupID == "" {
			t.Fatalf("event %s: body differs from the outbox or has no group", id)
		}
	}

	ids := make([]string, 0, len(seen))
	for id, times := range seen {
		ids = append(ids, id)

		if times != 1 {
			t.Fatalf("event %s delivered %d times, the destination must hold exactly one copy", id, times)
		}
	}

	sort.Strings(ids)

	if len(ids) != len(stored) {
		t.Fatalf("delivered %d distinct events, the outbox holds %d", len(ids), len(stored))
	}
}

func (f *fixture) publisherWorker(
	t *testing.T,
	outbox port.OutboxRepository,
	queueURL string,
	policy service.PublishPolicy,
) *worker.Worker {
	t.Helper()

	publisher := service.NewOutboxPublisher(service.OutboxPublisherDependencies{
		Outbox:    outbox,
		Publisher: sqsclient.NewEventPublisher(f.client, queueURL),
		Clock:     clock.System{},
		Policy:    policy,
	})

	running, err := worker.New(
		worker.Config{Name: "outbox-publisher", Interval: 100 * time.Millisecond, MaxErrorInterval: time.Second},
		worker.OutboxPublisherTask(publisher),
		nil,
	)
	if err != nil {
		t.Fatalf("worker.New error = %v", err)
	}

	return running
}

func startWorker(t *testing.T, running *worker.Worker) {
	t.Helper()

	if err := running.Start(); err != nil {
		t.Fatalf("Start error = %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		_ = running.Stop(ctx)
	})
}

var fastPolicy = service.PublishPolicy{
	BatchSize:      50,
	Lease:          2 * time.Second,
	InitialBackoff: 300 * time.Millisecond,
	MaxBackoff:     time.Second,
}

// accept simula o aceite assíncrono: confirma a transação em PENDING e "cai" antes de executá-la.
func (f *fixture) accept(t *testing.T, command service.ProcessOperationCommand) identity.TransactionID {
	t.Helper()

	hash, err := service.CanonicalHash(command)
	if err != nil {
		t.Fatalf("CanonicalHash error = %v", err)
	}

	id, err := f.ids.NewTransactionID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}

	accepted, err := wager.NewExternal(wager.ExternalParams{
		ID:                    id,
		ProviderID:            command.ProviderID,
		ExternalTransactionID: command.ExternalTransactionID,
		IdempotencyKey:        command.IdempotencyKey,
		PayloadHash:           hash,
		WalletID:              command.WalletID,
		PlayerID:              command.PlayerID,
		RoundID:               command.RoundID,
		GameID:                command.GameID,
		Kind:                  command.Kind,
		Money:                 command.Money,
		Now:                   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("NewExternal error = %v", err)
	}

	if err := f.ops.Operations.Insert(context.Background(), accepted); err != nil {
		t.Fatalf("insert accepted transaction: %v", err)
	}

	return id
}

// walletVersion lê o walletVersion do payload de um WalletBalanceChanged.
func walletVersion(t *testing.T, body string) float64 {
	t.Helper()

	var decoded struct {
		Data struct {
			WalletVersion float64 `json:"walletVersion"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode event: %v", err)
	}

	return decoded.Data.WalletVersion
}
