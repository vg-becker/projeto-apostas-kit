//go:build integration

package sqs_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"apostas-go/internal/controller/sqs"
	"apostas-go/internal/platform/clock"
	"apostas-go/internal/platform/idgen"
	"apostas-go/internal/platform/sqsclient"
	"apostas-go/internal/repository/postgres"
	"apostas-go/internal/service"
	"apostas-go/internal/testsupport/postgresdb"
)

type handlerFunc func(ctx context.Context, message service.InboundMessage) (service.InboundResult, error)

func (function handlerFunc) Handle(
	ctx context.Context,
	message service.InboundMessage,
) (service.InboundResult, error) {
	return function(ctx, message)
}

// recordingObserver guarda os desfechos para os testes conferirem.
type recordingObserver struct {
	mutex  sync.Mutex
	events []sqs.Event
}

func (observer *recordingObserver) Observe(event sqs.Event) {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()

	observer.events = append(observer.events, event)
}

func (observer *recordingObserver) count(outcome sqs.Outcome) int {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()

	total := 0
	for _, event := range observer.events {
		if event.Outcome == outcome {
			total++
		}
	}

	return total
}

func (observer *recordingObserver) receiveCounts(outcome sqs.Outcome) []int {
	observer.mutex.Lock()
	defer observer.mutex.Unlock()

	var counts []int
	for _, event := range observer.events {
		if event.Outcome == outcome {
			counts = append(counts, event.ReceiveCount)
		}
	}

	return counts
}

type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedBuffer) Write(payload []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()

	return buffer.buffer.Write(payload)
}

func (buffer *lockedBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()

	return buffer.buffer.String()
}

type fixtureOptions struct {
	wrapHandler func(inner sqs.Handler) sqs.Handler
	wrapQueue   func(inner sqs.Queue) sqs.Queue
	configure   func(config *sqs.Config)
	deferStart  bool
}

type fixture struct {
	pool     *pgxpool.Pool
	client   *sqsclient.Client
	queues   sqsclient.Queues
	observer *recordingObserver
	consumer *sqs.Consumer
	logs     *lockedBuffer
	wallets  *service.WalletService
	resolver *service.ReferenceResolver
	ids      idgen.Generator
}

func newFixture(t *testing.T, options fixtureOptions) *fixture {
	t.Helper()

	pool := postgresdb.Start(t)
	connection := postgres.NewConnection(pool)
	manager := postgres.NewTransactionManager(pool)
	outbox := postgres.NewOutboxRepository(connection)

	dependencies := service.OperationServiceDependencies{
		Transactions: manager,
		Wallets:      postgres.NewWalletRepository(connection),
		Operations:   postgres.NewTransactionRepository(connection),
		Ledger:       postgres.NewLedgerRepository(connection),
		Outbox:       outbox,
		Clock:        clock.System{},
		IDs:          idgen.Generator{},
		ReferencePolicy: service.ReferencePolicy{
			MaxAttempts: 10, TTL: time.Minute, InitialBackoff: time.Second, MaxBackoff: 2 * time.Second,
		},
	}

	client, err := sqsclient.New(context.Background(), broker.Config())
	if err != nil {
		t.Fatalf("sqsclient.New error = %v", err)
	}

	f := &fixture{
		pool:     pool,
		client:   client,
		queues:   broker.Isolated(t, client),
		observer: &recordingObserver{},
		logs:     &lockedBuffer{},
		resolver: service.NewReferenceResolver(dependencies),
		wallets: service.NewWalletService(service.WalletServiceDependencies{
			Transactions: manager,
			Wallets:      dependencies.Wallets,
			Operations:   dependencies.Operations,
			Ledger:       dependencies.Ledger,
			Outbox:       outbox,
			Clock:        clock.System{},
			IDs:          idgen.Generator{},
		}),
	}

	var handler sqs.Handler = service.NewMessageHandler(service.MessageHandlerDependencies{
		Transactions: manager,
		Inbox:        postgres.NewInboxRepository(connection),
		Operations:   service.NewOperationService(dependencies),
		Clock:        clock.System{},
		ConsumerName: "wager-transactions",
	})
	if options.wrapHandler != nil {
		handler = options.wrapHandler(handler)
	}

	f.consumer = f.newConsumer(t, handler, options)

	if !options.deferStart {
		f.start(t)
	}

	return f
}

func (f *fixture) newConsumer(t *testing.T, handler sqs.Handler, options fixtureOptions) *sqs.Consumer {
	t.Helper()

	var queue sqs.Queue = f.client
	if options.wrapQueue != nil {
		queue = options.wrapQueue(queue)
	}

	config := sqs.Config{
		Queues:            f.queues,
		WaitSeconds:       1,
		ProcessingTimeout: 20 * time.Second,
		RetryBase:         time.Second,
		RetryMax:          time.Second,
		ReceiveBackoff:    200 * time.Millisecond,
		ReceiveBackoffMax: time.Second,
	}
	if options.configure != nil {
		options.configure(&config)
	}

	consumer, err := sqs.NewConsumer(queue, handler, config, slog.New(slog.NewJSONHandler(f.logs, nil)), f.observer)
	if err != nil {
		t.Fatalf("NewConsumer error = %v", err)
	}

	return consumer
}

func (f *fixture) start(t *testing.T) {
	t.Helper()

	if err := f.consumer.Start(); err != nil {
		t.Fatalf("Start error = %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		_ = f.consumer.Stop(ctx)
	})
}

type walletRef struct {
	id       string
	playerID string
}

func (f *fixture) openWallet(t *testing.T, minorUnits int64) walletRef {
	t.Helper()

	playerID, err := f.ids.NewWalletID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}

	balance, err := moneyOf(minorUnits)
	if err != nil {
		t.Fatalf("money: %v", err)
	}

	opened, err := f.wallets.OpenWallet(context.Background(), service.OpenWalletCommand{
		PlayerID:       identityPlayer(playerID.String()),
		InitialBalance: balance,
	})
	if err != nil {
		t.Fatalf("OpenWallet error = %v", err)
	}

	return walletRef{id: opened.ID().String(), playerID: playerID.String()}
}

// body monta uma mensagem WagerTransactionRequested como no enunciado.
func body(messageID string, wallet walletRef, external string, kind string, amount string) string {
	return fmt.Sprintf(
		`{"messageId":%q,"type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00.000Z",`+
			`"data":{"providerId":"provider-a","externalTransactionId":%q,"idempotencyKey":%q,`+
			`"playerId":%q,"walletId":%q,"roundId":"round-1","gameId":"game-1","kind":%q,`+
			`"money":{"amount":%q,"currency":"BRL"}}}`,
		messageID, external, "provider-a:"+external, wallet.playerID, wallet.id, kind, amount,
	)
}

func randomID() string {
	var raw [8]byte

	_, _ = rand.Read(raw[:])

	return hex.EncodeToString(raw[:])
}

// publish coloca uma mensagem na fila de operações, com um MessageDeduplicationId próprio
// para que reenvios do mesmo conteúdo cheguem de fato (a idempotência é da inbox).
func (f *fixture) publish(t *testing.T, group string, payload string) {
	t.Helper()

	_, err := f.client.Send(context.Background(), sqsclient.SendParams{
		QueueURL:        f.queues.Requests,
		Body:            payload,
		GroupID:         group,
		DeduplicationID: randomID(),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
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

func (f *fixture) debits(t *testing.T, walletID string) int {
	t.Helper()

	return f.count(
		t,
		`SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`,
		walletID,
	)
}

// depth devolve as mensagens visíveis e as em voo da fila.
func (f *fixture) depth(t *testing.T, queueURL string) (int, int) {
	t.Helper()

	attributes, err := f.client.QueueAttributes(context.Background(), queueURL)
	if err != nil {
		t.Fatalf("queue attributes: %v", err)
	}

	visible, _ := strconv.Atoi(attributes["ApproximateNumberOfMessages"])
	inFlight, _ := strconv.Atoi(attributes["ApproximateNumberOfMessagesNotVisible"])

	return visible, inFlight
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

	t.Fatalf("not met within %v: %s\nlogs:\n%s", within, what, f.logs.String())
}

func (f *fixture) queueIsEmpty(t *testing.T, queueURL string) bool {
	t.Helper()

	visible, inFlight := f.depth(t, queueURL)

	return visible == 0 && inFlight == 0
}

// drainDeadLetters recolhe as mensagens da DLQ até juntar quantas se espera, ou o prazo.
func (f *fixture) drainDeadLetters(t *testing.T, expected int, within time.Duration) []sqsclient.Message {
	t.Helper()

	var collected []sqsclient.Message

	deadline := time.Now().Add(within)
	for len(collected) < expected && time.Now().Before(deadline) {
		messages, err := f.client.Receive(context.Background(), sqsclient.ReceiveParams{
			QueueURL: f.queues.DeadLetter, MaxMessages: 10, WaitSeconds: 1,
		})
		if err != nil {
			t.Fatalf("receive dlq: %v", err)
		}

		// Numa fila FIFO, as mensagens de um grupo em voo travam as demais do mesmo grupo:
		// apagar cada uma ao recolher é o que libera as seguintes, como faria um operador.
		for _, message := range messages {
			if err := f.client.Delete(context.Background(), f.queues.DeadLetter, message.ReceiptHandle); err != nil {
				t.Fatalf("delete dlq message: %v", err)
			}
		}

		collected = append(collected, messages...)
	}

	return collected
}
