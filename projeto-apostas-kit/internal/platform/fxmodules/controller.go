package fxmodules

import (
	"log/slog"
	"net/http"

	"go.uber.org/fx"

	httpcontroller "apostas-go/internal/controller/http"
	"apostas-go/internal/controller/sqs"
	"apostas-go/internal/controller/worker"
	"apostas-go/internal/model/port"
	"apostas-go/internal/platform/auth"
	"apostas-go/internal/platform/config"
	"apostas-go/internal/platform/metrics"
	"apostas-go/internal/platform/sqsclient"
	"apostas-go/internal/repository/postgres"
	"apostas-go/internal/service"
)

// Tamanho do lote de cada iteração dos workers de referência e de retomada.
const (
	referenceBatchSize = 50
	resumeBatchSize    = 50
)

// ControllerModule oferece as entradas (HTTP e SQS) e os workers, e liga o ciclo de vida
// deles. A ordem de início e de parada é do Runtime, não da ordem dos hooks do Fx.
var ControllerModule = fx.Module(
	"controller",
	fx.Provide(
		newRouter,
		newHTTPServer,
		newConsumer,
		newWorkers,
		newRuntime,
	),
	fx.Invoke(registerRuntime),
)

type routerInputs struct {
	fx.In

	Authenticator *auth.Authenticator
	Wallets       *service.WalletService
	Operations    *service.OperationService
	Queries       *service.QueryService
	Database      *postgres.HealthCheck
	Broker        *sqsclient.HealthCheck
	Metrics       *metrics.Metrics
	Logger        *slog.Logger
}

func newRouter(inputs routerInputs) http.Handler {
	return httpcontroller.NewRouter(httpcontroller.Dependencies{
		Authenticator: inputs.Authenticator,
		Wallets:       inputs.Wallets,
		Operations:    inputs.Operations,
		Queries:       inputs.Queries,
		Readiness:     []port.HealthCheck{inputs.Database, inputs.Broker},
		Logger:        inputs.Logger,
		Observer:      inputs.Metrics,
		Metrics:       inputs.Metrics.Handler(),
	})
}

func newHTTPServer(settings config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              settings.HTTP.Addr,
		Handler:           handler,
		ReadHeaderTimeout: settings.HTTP.ReadHeaderTimeout,
		ReadTimeout:       settings.HTTP.ReadTimeout,
		WriteTimeout:      settings.HTTP.WriteTimeout,
		IdleTimeout:       settings.HTTP.IdleTimeout,
	}
}

func newConsumer(
	client *sqsclient.Client,
	handler *service.MessageHandler,
	queues sqsclient.Queues,
	logger *slog.Logger,
	registry *metrics.Metrics,
) (*sqs.Consumer, error) {
	return sqs.NewConsumer(client, handler, sqs.Config{Queues: queues}, logger, registry)
}

// Workers são os laços periódicos do serviço: publicação da outbox, resolução de
// referências pendentes e retomada de transações abandonadas.
type Workers []*worker.Worker

type workerInputs struct {
	fx.In

	Publisher *service.OutboxPublisher
	Resolver  *service.ReferenceResolver
	Resumer   *service.PendingResumer
	Metrics   *metrics.Metrics
	Logger    *slog.Logger
}

func newWorkers(inputs workerInputs) (Workers, error) {
	definitions := []struct {
		config worker.Config
		task   worker.Task
	}{
		{worker.OutboxPublisherConfig(), worker.OutboxPublisherTask(inputs.Publisher)},
		{
			worker.ReferenceResolverConfig(),
			worker.ReferenceResolverTask(inputs.Resolver, referenceBatchSize),
		},
		{worker.PendingResumerConfig(), worker.PendingResumerTask(inputs.Resumer, resumeBatchSize)},
	}

	workers := make(Workers, 0, len(definitions))

	for _, definition := range definitions {
		definition.config.Observer = inputs.Metrics.WorkerObserver()

		created, err := worker.New(definition.config, definition.task, inputs.Logger)
		if err != nil {
			return nil, err
		}

		workers = append(workers, created)
	}

	return workers, nil
}
