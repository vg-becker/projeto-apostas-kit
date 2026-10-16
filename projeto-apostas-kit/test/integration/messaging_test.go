//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	httpcontroller "apostas-go/internal/controller/http"
	"apostas-go/internal/controller/sqs"
	"apostas-go/internal/controller/worker"
	"apostas-go/internal/model/port"
	"apostas-go/internal/platform/auth"
	"apostas-go/internal/platform/clock"
	"apostas-go/internal/platform/idgen"
	"apostas-go/internal/platform/sqsclient"
	"apostas-go/internal/repository/postgres"
	"apostas-go/internal/service"
	"apostas-go/internal/testsupport/postgresdb"
)

type postgresPool = *pgxpool.Pool

type messagingOptions struct {
	// healthQueues substitui as filas verificadas pelo readiness.
	healthQueues []string
	// healthEndpoint, se informado, aponta o readiness para outro endpoint do SQS.
	healthEndpoint string
}

// messagingStack é o serviço completo: borda HTTP com autenticação real, consumidor SQS e
// publisher da outbox rodando, sobre Postgres, LocalStack e Keycloak reais.
type messagingStack struct {
	*stack
	client   *sqsclient.Client
	queues   sqsclient.Queues
	consumer *sqs.Consumer
	worker   *worker.Worker
}

func newMessagingStack(t *testing.T, options messagingOptions) *messagingStack {
	t.Helper()

	pool := postgresdb.Start(t)
	connection := postgres.NewConnection(pool)
	outbox := postgres.NewOutboxRepository(connection)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	client, err := sqsclient.New(context.Background(), broker.Config())
	if err != nil {
		t.Fatalf("sqsclient.New error = %v", err)
	}

	queues := broker.Isolated(t, client)
	dependencies := serviceDependencies(pool, outbox)
	handler := service.NewMessageHandler(service.MessageHandlerDependencies{
		Transactions: dependencies.Transactions,
		Inbox:        postgres.NewInboxRepository(connection),
		Operations:   service.NewOperationService(dependencies),
		Clock:        clock.System{},
		ConsumerName: "wager-transactions",
	})

	server := httptest.NewServer(newRouter(t, outbox, dependencies, readinessChecks(t, pool, client, queues, options)))
	t.Cleanup(server.Close)

	stack := &stack{server: server, pool: pool}

	return &messagingStack{
		stack:    stack,
		client:   client,
		queues:   queues,
		consumer: startConsumer(t, client, handler, queues, logger),
		worker:   startPublisher(t, client, outbox, queues),
	}
}

func serviceDependencies(pool postgresPool, outbox *postgres.OutboxRepository) service.OperationServiceDependencies {
	connection := postgres.NewConnection(pool)

	return service.OperationServiceDependencies{
		Transactions: postgres.NewTransactionManager(pool),
		Wallets:      postgres.NewWalletRepository(connection),
		Operations:   postgres.NewTransactionRepository(connection),
		Ledger:       postgres.NewLedgerRepository(connection),
		Outbox:       outbox,
		Clock:        clock.System{},
		IDs:          idgen.Generator{},
	}
}

func readinessChecks(
	t *testing.T,
	pool postgresPool,
	client *sqsclient.Client,
	queues sqsclient.Queues,
	options messagingOptions,
) []port.HealthCheck {
	t.Helper()

	healthClient := client
	if options.healthEndpoint != "" {
		config := broker.Config()
		config.Endpoint = options.healthEndpoint
		config.RequestTimeout = time.Second

		var err error
		if healthClient, err = sqsclient.New(context.Background(), config); err != nil {
			t.Fatalf("health client: %v", err)
		}
	}

	healthQueues := options.healthQueues
	if len(healthQueues) == 0 {
		healthQueues = []string{queues.Requests, queues.Events}
	}

	return []port.HealthCheck{postgres.NewHealthCheck(pool), sqsclient.NewHealthCheck(healthClient, healthQueues...)}
}

func newRouter(
	t *testing.T,
	outbox *postgres.OutboxRepository,
	dependencies service.OperationServiceDependencies,
	readiness []port.HealthCheck,
) http.Handler {
	t.Helper()

	authenticator, err := auth.New(realmConfig(), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("auth.New error = %v", err)
	}

	return httpcontroller.NewRouter(httpcontroller.Dependencies{
		Authenticator: authenticator,
		Wallets: service.NewWalletService(service.WalletServiceDependencies{
			Transactions: dependencies.Transactions,
			Wallets:      dependencies.Wallets,
			Operations:   dependencies.Operations,
			Ledger:       dependencies.Ledger,
			Outbox:       outbox,
			Clock:        dependencies.Clock,
			IDs:          dependencies.IDs,
		}),
		Operations: service.NewOperationService(dependencies),
		Queries: service.NewQueryService(service.QueryServiceDependencies{
			Wallets:    dependencies.Wallets,
			Operations: dependencies.Operations,
			Ledger:     dependencies.Ledger,
		}),
		Readiness: readiness,
		Logger:    slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
}

func startConsumer(
	t *testing.T,
	client *sqsclient.Client,
	handler *service.MessageHandler,
	queues sqsclient.Queues,
	logger *slog.Logger,
) *sqs.Consumer {
	t.Helper()

	consumer, err := sqs.NewConsumer(client, handler, sqs.Config{
		Queues:            queues,
		WaitSeconds:       1,
		ProcessingTimeout: 20 * time.Second,
		RetryBase:         time.Second,
		RetryMax:          time.Second,
	}, logger, nil)
	if err != nil {
		t.Fatalf("NewConsumer error = %v", err)
	}

	if err := consumer.Start(); err != nil {
		t.Fatalf("consumer.Start error = %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		_ = consumer.Stop(ctx)
	})

	return consumer
}

func startPublisher(
	t *testing.T,
	client *sqsclient.Client,
	outbox *postgres.OutboxRepository,
	queues sqsclient.Queues,
) *worker.Worker {
	t.Helper()

	publisher := service.NewOutboxPublisher(service.OutboxPublisherDependencies{
		Outbox:    outbox,
		Publisher: sqsclient.NewEventPublisher(client, queues.Events),
		Clock:     clock.System{},
		Policy:    service.PublishPolicy{BatchSize: 50, Lease: 2 * time.Second, InitialBackoff: 300 * time.Millisecond, MaxBackoff: time.Second},
	})

	running, err := worker.New(
		worker.Config{Name: "outbox-publisher", Interval: 100 * time.Millisecond, MaxErrorInterval: time.Second},
		worker.OutboxPublisherTask(publisher),
		nil,
	)
	if err != nil {
		t.Fatalf("worker.New error = %v", err)
	}

	if err := running.Start(); err != nil {
		t.Fatalf("worker.Start error = %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		_ = running.Stop(ctx)
	})

	return running
}

func randomID() string {
	var raw [8]byte

	_, _ = rand.Read(raw[:])

	return hex.EncodeToString(raw[:])
}

// sqsBody monta uma mensagem WagerTransactionRequested do provider-a.
func sqsBody(messageID string, wallet walletRef, external string, amount string) string {
	return fmt.Sprintf(
		`{"messageId":%q,"type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00.000Z",`+
			`"data":{"providerId":"provider-a","externalTransactionId":%q,"idempotencyKey":%q,`+
			`"playerId":%q,"walletId":%q,"roundId":"round-1","gameId":"game-1","kind":"BET",`+
			`"money":{"amount":%q,"currency":"BRL"}}}`,
		messageID, external, "provider-a:"+external, wallet.playerID, wallet.id, amount,
	)
}

func (m *messagingStack) send(t *testing.T, wallet walletRef, payload string) {
	t.Helper()

	_, err := m.client.Send(context.Background(), sqsclient.SendParams{
		QueueURL:        m.queues.Requests,
		Body:            payload,
		GroupID:         wallet.id,
		DeduplicationID: randomID(),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
}

// drain recolhe e apaga as mensagens da fila (apagar libera as seguintes do mesmo grupo)
// até juntar quantas se espera, ou o prazo.
func (m *messagingStack) drain(t *testing.T, queueURL string, expected int, within time.Duration) []sqsclient.Message {
	t.Helper()

	var collected []sqsclient.Message

	deadline := time.Now().Add(within)
	for len(collected) < expected && time.Now().Before(deadline) {
		messages, err := m.client.Receive(context.Background(), sqsclient.ReceiveParams{
			QueueURL: queueURL, MaxMessages: 10, WaitSeconds: 1,
		})
		if err != nil {
			t.Fatalf("receive: %v", err)
		}

		for _, message := range messages {
			if err := m.client.Delete(context.Background(), queueURL, message.ReceiptHandle); err != nil {
				t.Fatalf("delete: %v", err)
			}
		}

		collected = append(collected, messages...)
	}

	return collected
}

func (m *messagingStack) debits(t *testing.T, walletID string) int {
	t.Helper()

	return m.countWhere(t, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, walletID)
}

func (m *messagingStack) countWhere(t *testing.T, query string, arguments ...any) int {
	t.Helper()

	var total int
	if err := m.pool.QueryRow(context.Background(), query, arguments...).Scan(&total); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}

	return total
}

func TestAnOperationTravelsInBySQSAndOutByHTTPAndTheEventStream(t *testing.T) {
	m := newMessagingStack(t, messagingOptions{})
	wallet := m.openWallet(t, "1000.00")
	providerA := bearer(t, "provider-a")
	internal := bearer(t, "internal-service")
	path := "/providers/provider-a/wagering/transactions/tx-1"

	m.send(t, wallet, sqsBody("msg-1", wallet, "tx-1", "25.00"))

	eventually(t, 40*time.Second, func() bool {
		return m.call(t, http.MethodGet, path, providerA, nil, "").status == http.StatusOK
	})

	detail := m.call(t, http.MethodGet, path, providerA, nil, "").object(t)
	balance := detail["balance"].(map[string]any)

	if detail["status"] != "PROCESSED" || balance["amount"] != "975.00" {
		t.Fatalf("transaction = %v", detail)
	}

	m.assertLedgerAndReconciliation(t, wallet, internal)
	m.assertProviderIsolation(t, path, detail["transactionId"].(string))
	m.assertEventStream(t)
}

func (m *messagingStack) assertLedgerAndReconciliation(t *testing.T, wallet walletRef, internal string) {
	t.Helper()

	ledger := m.call(t, http.MethodGet, "/wallets/"+wallet.id+"/ledger", internal, nil, "").object(t)
	reconciliation := m.call(t, http.MethodPost, "/wallets/"+wallet.id+"/reconciliation", internal, nil, "").object(t)

	if len(ledger["entries"].([]any)) != 2 || reconciliation["consistent"] != true ||
		reconciliation["storedBalance"].(map[string]any)["amount"] != "975.00" {
		t.Fatalf("ledger = %v, reconciliation = %v", ledger, reconciliation)
	}
}

func (m *messagingStack) assertProviderIsolation(t *testing.T, path string, transactionID string) {
	t.Helper()

	providerB := bearer(t, "provider-b")

	assertProblem(t, m.call(t, http.MethodGet, path, providerB, nil, ""), http.StatusForbidden, "FORBIDDEN")
	assertProblem(t, m.call(t, http.MethodGet, "/wagering/transactions/"+transactionID, providerB, nil, ""),
		http.StatusNotFound, "TRANSACTION_NOT_FOUND")
}

// assertEventStream confere que os quatro eventos (abertura e aposta) chegaram à fila de
// saída, cada um uma vez, e que os da aposta carregam o messageId como correlação.
func (m *messagingStack) assertEventStream(t *testing.T) {
	t.Helper()

	eventually(t, 30*time.Second, func() bool {
		return m.countWhere(t, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	})

	delivered := m.drain(t, m.queues.Events, 4, 20*time.Second)
	if len(delivered) != 4 {
		t.Fatalf("event stream has %d messages, want 4", len(delivered))
	}

	unique, traced := map[string]bool{}, 0

	for _, message := range delivered {
		unique[message.Attributes["eventId"]] = true

		var envelope struct {
			CorrelationID string `json:"correlationId"`
		}
		if err := json.Unmarshal([]byte(message.Body), &envelope); err != nil {
			t.Fatalf("decode event: %v", err)
		}

		if envelope.CorrelationID == "msg-1" {
			traced++
		}
	}

	if len(unique) != 4 || traced != 2 {
		t.Fatalf("unique events = %d, events traced to msg-1 = %d, want 4 and 2", len(unique), traced)
	}
}

func TestTheSameOperationOverHTTPAndSQSIsAppliedOnce(t *testing.T) {
	m := newMessagingStack(t, messagingOptions{})
	wallet := m.openWallet(t, "1000.00")
	providerA := bearer(t, "provider-a")

	first := m.submit(t, providerA, "provider-a:tx-1", operationBody(wallet, "provider-a", "tx-1", "25.00"))
	if first.status != http.StatusOK || first.object(t)["idempotentReplay"] != false {
		t.Fatalf("first = %d %s", first.status, first.body)
	}

	m.send(t, wallet, sqsBody("msg-1", wallet, "tx-1", "25.00"))
	m.send(t, wallet, sqsBody("msg-2", wallet, "tx-1", "25.00"))

	eventually(t, 40*time.Second, func() bool {
		return m.countWhere(t, `SELECT count(*) FROM inbox_messages WHERE completed_at IS NOT NULL`) == 2
	})

	replay := m.submit(t, providerA, "provider-a:tx-1", operationBody(wallet, "provider-a", "tx-1", "25.00"))

	if replay.status != http.StatusOK || replay.object(t)["idempotentReplay"] != true ||
		replay.object(t)["transactionId"] != first.object(t)["transactionId"] {
		t.Fatalf("replay = %d %s", replay.status, replay.body)
	}

	if m.debits(t, wallet.id) != 1 {
		t.Fatalf("debits = %d: the same operation over HTTP and SQS must be applied once", m.debits(t, wallet.id))
	}
}

func TestARejectionAndAPoisonMessageAreVisibleToTheirOwners(t *testing.T) {
	m := newMessagingStack(t, messagingOptions{})
	wallet := m.openWallet(t, "10.00")
	providerA := bearer(t, "provider-a")

	m.send(t, wallet, sqsBody("msg-1", wallet, "tx-1", "99999.00"))
	m.send(t, wallet, "this is not json")

	eventually(t, 40*time.Second, func() bool {
		return m.call(t, http.MethodGet, "/providers/provider-a/wagering/transactions/tx-1", providerA, nil, "").status == http.StatusOK
	})

	detail := m.call(t, http.MethodGet, "/providers/provider-a/wagering/transactions/tx-1", providerA, nil, "").object(t)
	if detail["status"] != "REJECTED" || detail["failureCode"] != "INSUFFICIENT_FUNDS" {
		t.Fatalf("transaction = %v", detail)
	}

	dead := m.drain(t, m.queues.DeadLetter, 1, 30*time.Second)
	if len(dead) != 1 || dead[0].Body != "this is not json" || dead[0].Attributes["failureReason"] != "invalid_json" {
		t.Fatalf("dead letters = %+v", dead)
	}

	if m.debits(t, wallet.id) != 0 {
		t.Fatal("neither the rejection nor the poison message may move money")
	}
}

func TestReadinessReflectsTheRealBroker(t *testing.T) {
	healthy := newMessagingStack(t, messagingOptions{})

	ready := healthy.call(t, http.MethodGet, "/health/ready", "", nil, "")
	if ready.status != http.StatusOK || string(ready.body) != `{"status":"UP","checks":{"postgres":"UP","sqs":"UP"}}` {
		t.Fatalf("ready = %d %s", ready.status, ready.body)
	}

	missingQueue := newMessagingStack(t, messagingOptions{healthQueues: []string{broker.QueueURL("gone.fifo")}})
	down := missingQueue.call(t, http.MethodGet, "/health/ready", "", nil, "")

	if down.status != http.StatusServiceUnavailable ||
		string(down.body) != `{"status":"DOWN","checks":{"postgres":"UP","sqs":"DOWN"}}` {
		t.Fatalf("missing queue: %d %s", down.status, down.body)
	}

	unreachable := newMessagingStack(t, messagingOptions{healthEndpoint: "http://127.0.0.1:1"})
	lost := unreachable.call(t, http.MethodGet, "/health/ready", "", nil, "")

	if lost.status != http.StatusServiceUnavailable || string(lost.body) != `{"status":"DOWN","checks":{"postgres":"UP","sqs":"DOWN"}}` {
		t.Fatalf("unreachable broker: %d %s", lost.status, lost.body)
	}
}
