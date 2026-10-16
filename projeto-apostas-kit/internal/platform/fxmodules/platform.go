package fxmodules

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"apostas-go/internal/model/port"
	"apostas-go/internal/platform/auth"
	"apostas-go/internal/platform/clock"
	"apostas-go/internal/platform/config"
	"apostas-go/internal/platform/idgen"
	"apostas-go/internal/platform/metrics"
	"apostas-go/internal/platform/sqsclient"
	"apostas-go/internal/repository/postgres"
)

const databaseConnectTimeout = 10 * time.Second

// PlatformModule oferece o relógio, os identificadores e as conexões de infraestrutura. O
// pool do banco é fechado por um OnStop próprio: como tudo o que usa o pool é criado depois
// dele, o Fx o executa por último.
var PlatformModule = fx.Module(
	"platform",
	fx.Provide(
		newClock,
		newIDs,
		newPool,
		newQueues,
		newSQSClient,
		newSQSHealthCheck,
		newAuthenticator,
		newMetrics,
	),
)

func newClock() port.Clock {
	return clock.System{}
}

func newIDs() port.IDGenerator {
	return idgen.Generator{}
}

func newPool(lifecycle fx.Lifecycle, settings config.Config) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), databaseConnectTimeout)
	defer cancel()

	pool, err := postgres.NewPool(ctx, settings.DatabaseURL.Reveal())
	if err != nil {
		return nil, err
	}

	lifecycle.Append(fx.Hook{
		OnStop: func(context.Context) error {
			pool.Close()

			return nil
		},
	})

	return pool, nil
}

func newQueues(settings config.Config) sqsclient.Queues {
	return settings.Queues
}

func newSQSClient(settings config.Config) (*sqsclient.Client, error) {
	return sqsclient.New(context.Background(), settings.SQS)
}

func newSQSHealthCheck(client *sqsclient.Client, settings config.Config) *sqsclient.HealthCheck {
	return sqsclient.NewHealthCheck(client, settings.Queues.Requests, settings.Queues.Events)
}

func newAuthenticator(settings config.Config, logger *slog.Logger) (*auth.Authenticator, error) {
	return auth.New(settings.Auth, logger)
}

// newMetrics cria o registro e liga as leituras da coleta: o atraso da outbox (no banco) e o
// tamanho da DLQ (no broker).
func newMetrics(
	outbox *postgres.OutboxRepository,
	client *sqsclient.Client,
	queues sqsclient.Queues,
) (*metrics.Metrics, error) {
	registry := metrics.New()

	err := registry.RegisterSources(metrics.Sources{
		OutboxBacklog: outbox.Backlog,
		DeadLetterDepth: func(ctx context.Context) (int64, error) {
			return client.ApproximateMessages(ctx, queues.DeadLetter)
		},
	})
	if err != nil {
		return nil, err
	}

	return registry, nil
}
