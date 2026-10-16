package sqs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"apostas-go/internal/platform/sqsclient"
	"apostas-go/internal/service"
)

var (
	// ErrAlreadyStarted indica um segundo Start no mesmo consumidor.
	ErrAlreadyStarted = errors.New("sqs: consumer already started")
	// ErrShutdownTimeout indica que o prazo do desligamento venceu: o trabalho em andamento
	// foi abandonado e as mensagens, devolvidas à fila.
	ErrShutdownTimeout = errors.New("sqs: shutdown deadline exceeded, in-flight work abandoned")
)

const (
	capacityPoll   = 50 * time.Millisecond
	abandonGrace   = 10 * time.Second
	cleanupTimeout = 5 * time.Second
)

// Queue é o que o consumidor usa do SQS; *sqsclient.Client a satisfaz.
type Queue interface {
	Receive(ctx context.Context, params sqsclient.ReceiveParams) ([]sqsclient.Message, error)
	Delete(ctx context.Context, queueURL string, receiptHandle string) error
	ChangeVisibility(ctx context.Context, queueURL string, receiptHandle string, seconds int) error
	Send(ctx context.Context, params sqsclient.SendParams) (string, error)
}

// Handler trata uma mensagem de entrada; *service.MessageHandler o satisfaz.
type Handler interface {
	Handle(ctx context.Context, message service.InboundMessage) (service.InboundResult, error)
}

// Consumer lê a fila de operações e as entrega ao Handler. A mensagem só é apagada depois
// que Handle devolve nil, isto é, depois do commit.
type Consumer struct {
	queue    Queue
	handler  Handler
	config   Config
	logger   *slog.Logger
	observer Observer

	mutex    sync.Mutex
	started  bool
	inflight int
	stopRun  context.CancelFunc
	abandon  context.CancelFunc
	done     chan struct{}
	groups   sync.WaitGroup
}

// NewConsumer valida a configuração e cria o consumidor. Logger e observer são opcionais.
func NewConsumer(
	queue Queue,
	handler Handler,
	config Config,
	logger *slog.Logger,
	observer Observer,
) (*Consumer, error) {
	config = config.withDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	if observer == nil {
		observer = noopObserver{}
	}

	return &Consumer{
		queue:    queue,
		handler:  handler,
		config:   config,
		logger:   logger,
		observer: observer,
	}, nil
}

// Start inicia o laço de recepção em segundo plano e retorna.
func (consumer *Consumer) Start() error {
	consumer.mutex.Lock()
	defer consumer.mutex.Unlock()

	if consumer.started {
		return ErrAlreadyStarted
	}

	runContext, stopRun := context.WithCancel(context.Background())
	workContext, abandon := context.WithCancel(context.Background())

	consumer.started = true
	consumer.stopRun = stopRun
	consumer.abandon = abandon
	consumer.done = make(chan struct{})

	go consumer.run(runContext, workContext)

	return nil
}

// Stop para de receber, devolve à fila o que não foi iniciado e espera o trabalho em
// andamento até o prazo de ctx. Vencido o prazo, o trabalho é abandonado (a transação
// SQL faz rollback), as mensagens são devolvidas à fila e o erro é ErrShutdownTimeout.
func (consumer *Consumer) Stop(ctx context.Context) error {
	consumer.mutex.Lock()
	started, done := consumer.started, consumer.done
	stopRun, abandon := consumer.stopRun, consumer.abandon
	consumer.mutex.Unlock()

	if !started {
		return nil
	}

	stopRun()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}

	abandon()

	select {
	case <-done:
	case <-time.After(abandonGrace):
	}

	return fmt.Errorf("%w: %w", ErrShutdownTimeout, ctx.Err())
}

func (consumer *Consumer) run(runContext context.Context, workContext context.Context) {
	defer close(consumer.done)
	defer consumer.groups.Wait()

	failures := 0

	for runContext.Err() == nil {
		free, ok := consumer.waitForCapacity(runContext)
		if !ok {
			return
		}

		messages, err := consumer.receive(runContext, free)
		if err != nil {
			failures++
			consumer.afterReceiveFailure(runContext, err, failures)

			continue
		}

		failures = 0

		consumer.dispatch(runContext, workContext, messages)
	}
}

// waitForCapacity espera haver folga entre as mensagens em voo e devolve quantas cabem.
func (consumer *Consumer) waitForCapacity(runContext context.Context) (int, bool) {
	for {
		consumer.mutex.Lock()
		free := consumer.config.MaxInFlight - consumer.inflight
		consumer.mutex.Unlock()

		if free > 0 {
			return min(free, consumer.config.BatchSize), true
		}

		select {
		case <-runContext.Done():
			return 0, false
		case <-time.After(capacityPoll):
		}
	}
}

func (consumer *Consumer) receive(
	runContext context.Context,
	limit int,
) ([]sqsclient.Message, error) {
	return consumer.queue.Receive(runContext, sqsclient.ReceiveParams{
		QueueURL:                 consumer.config.Queues.Requests,
		MaxMessages:              limit,
		WaitSeconds:              consumer.config.WaitSeconds,
		VisibilityTimeoutSeconds: consumer.config.VisibilityTimeoutSeconds,
	})
}

// afterReceiveFailure registra o erro e espera com backoff, saindo cedo se o consumidor
// estiver parando.
func (consumer *Consumer) afterReceiveFailure(
	runContext context.Context,
	err error,
	failures int,
) {
	if runContext.Err() != nil {
		return
	}

	delay := doubling(consumer.config.ReceiveBackoff, consumer.config.ReceiveBackoffMax, failures-1)
	consumer.logger.Error("receive failed", "error", err, "retryInMs", delay.Milliseconds())

	select {
	case <-runContext.Done():
	case <-time.After(delay):
	}
}

func (consumer *Consumer) adjustInflight(delta int) {
	consumer.mutex.Lock()
	defer consumer.mutex.Unlock()

	consumer.inflight += delta
}
