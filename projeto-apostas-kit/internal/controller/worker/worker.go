package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"time"
)

var (
	// ErrAlreadyStarted indica um segundo Start no mesmo worker.
	ErrAlreadyStarted = errors.New("worker: already started")
	// ErrShutdownTimeout indica que o prazo do desligamento venceu: a iteração em andamento
	// foi cancelada.
	ErrShutdownTimeout = errors.New("worker: shutdown deadline exceeded, iteration abandoned")
	// ErrInvalidConfig indica um worker sem nome, sem intervalo ou sem tarefa.
	ErrInvalidConfig = errors.New("worker: invalid configuration")

	errTaskPanicked = errors.New("worker: task panicked")
)

const (
	// DefaultJitter é a fração aleatória acrescentada à espera, para que várias instâncias
	// não batam no banco em sincronia.
	DefaultJitter = 0.1

	defaultMaxErrorInterval = 30 * time.Second
	abandonGrace            = 10 * time.Second
)

// Task é uma iteração do trabalho periódico. Devolve true em more quando ainda há trabalho
// imediato (o worker repete sem esperar) e um erro quando a iteração falhou.
type Task func(ctx context.Context) (more bool, err error)

// Config descreve um worker.
type Config struct {
	Name string
	// Interval é a espera entre iterações quando não há mais trabalho.
	Interval time.Duration
	// MaxErrorInterval limita a espera depois de erros seguidos, que dobra a cada erro e
	// volta ao normal no primeiro sucesso. Zero usa 30 s.
	MaxErrorInterval time.Duration
	// Jitter é a fração aleatória acrescentada a cada espera (0 desliga).
	Jitter float64
	// Observer é opcional e recebe o resultado de cada iteração.
	Observer Observer
}

// Iteration é o resultado de uma iteração, para métricas.
type Iteration struct {
	Worker   string
	Duration time.Duration
	// Failed indica que a tarefa devolveu erro ou entrou em panic.
	Failed bool
	// Panicked indica que a tarefa entrou em panic (e Failed também vale).
	Panicked bool
}

// Observer recebe cada iteração. A implementação deve ser rápida e segura para uso
// concorrente.
type Observer interface {
	Observe(iteration Iteration)
}

// Worker executa uma Task em laço, com início e término observáveis.
type Worker struct {
	config Config
	task   Task
	logger *slog.Logger

	mutex   sync.Mutex
	started bool
	stopRun context.CancelFunc
	abandon context.CancelFunc
	done    chan struct{}
}

// New valida a configuração e cria o worker. O logger é opcional.
func New(config Config, task Task, logger *slog.Logger) (*Worker, error) {
	if config.Name == "" || config.Interval <= 0 || task == nil {
		return nil, fmt.Errorf("%w: name, interval and task are required", ErrInvalidConfig)
	}

	if config.MaxErrorInterval <= 0 {
		config.MaxErrorInterval = defaultMaxErrorInterval
	}

	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return &Worker{
		config: config,
		task:   task,
		logger: logger.With("worker", config.Name),
		done:   make(chan struct{}),
	}, nil
}

// Done é fechado quando o laço terminou de fato; é o término observável do worker.
func (worker *Worker) Done() <-chan struct{} {
	return worker.done
}

// Start inicia o laço em segundo plano e retorna.
func (worker *Worker) Start() error {
	worker.mutex.Lock()
	defer worker.mutex.Unlock()

	if worker.started {
		return ErrAlreadyStarted
	}

	runContext, stopRun := context.WithCancel(context.Background())
	workContext, abandon := context.WithCancel(context.Background())

	worker.started = true
	worker.stopRun = stopRun
	worker.abandon = abandon

	go worker.run(runContext, workContext)

	return nil
}

// Stop impede novas iterações e espera a em andamento até o prazo de ctx. Vencido o prazo,
// a iteração é cancelada e o erro é ErrShutdownTimeout. Seguro antes do Start e repetido.
func (worker *Worker) Stop(ctx context.Context) error {
	worker.mutex.Lock()
	started, stopRun, abandon := worker.started, worker.stopRun, worker.abandon
	worker.mutex.Unlock()

	if !started {
		return nil
	}

	stopRun()

	select {
	case <-worker.done:
		return nil
	case <-ctx.Done():
	}

	abandon()

	select {
	case <-worker.done:
	case <-time.After(abandonGrace):
	}

	return fmt.Errorf("%w: %w", ErrShutdownTimeout, ctx.Err())
}

func (worker *Worker) run(runContext context.Context, workContext context.Context) {
	defer close(worker.done)

	failures := 0

	for runContext.Err() == nil {
		started := time.Now()
		more, err := worker.iterate(workContext)

		worker.observe(started, err)

		wait := worker.nextWait(runContext, more, err, &failures)
		if wait <= 0 {
			continue
		}

		select {
		case <-runContext.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (worker *Worker) observe(started time.Time, err error) {
	if worker.config.Observer == nil {
		return
	}

	worker.config.Observer.Observe(Iteration{
		Worker:   worker.config.Name,
		Duration: time.Since(started),
		Failed:   err != nil,
		Panicked: errors.Is(err, errTaskPanicked),
	})
}

// iterate roda a tarefa. Um panic vira erro: o bug de uma iteração não derruba o processo.
// O valor do panic nunca é registrado, pois poderia carregar dados.
func (worker *Worker) iterate(ctx context.Context) (more bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			worker.logger.Error(
				"task panicked",
				"type", fmt.Sprintf("%T", recovered),
				"stack", string(debug.Stack()),
			)

			more, err = false, errTaskPanicked
		}
	}()

	return worker.task(ctx)
}

// nextWait decide quanto esperar: nada se há mais trabalho; o intervalo normal depois de um
// sucesso; um intervalo que dobra a cada erro seguido, limitado ao máximo.
func (worker *Worker) nextWait(
	runContext context.Context,
	more bool,
	err error,
	failures *int,
) time.Duration {
	if err == nil {
		*failures = 0

		if more {
			return 0
		}

		return worker.withJitter(worker.config.Interval)
	}

	*failures++

	if runContext.Err() == nil {
		worker.logger.Error("iteration failed", "error", err, "consecutiveFailures", *failures)
	}

	backoff := doubling(worker.config.Interval, worker.config.MaxErrorInterval, *failures)

	return worker.withJitter(backoff)
}

func (worker *Worker) withJitter(delay time.Duration) time.Duration {
	if worker.config.Jitter <= 0 {
		return delay
	}

	return delay + time.Duration(rand.Float64()*worker.config.Jitter*float64(delay))
}

// doubling dobra o atraso inicial a cada tentativa, sem passar do máximo.
func doubling(initial time.Duration, maximum time.Duration, attempts int) time.Duration {
	delay := initial

	for range attempts {
		delay *= 2
		if delay >= maximum {
			return maximum
		}
	}

	return min(delay, maximum)
}
