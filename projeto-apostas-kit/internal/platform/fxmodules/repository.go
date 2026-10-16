package fxmodules

import (
	"log/slog"

	"go.uber.org/fx"

	"apostas-go/internal/platform/config"
	"apostas-go/internal/repository/postgres"
)

// RepositoryModule oferece os repositórios Postgres sobre o pool e, se configurado, aplica
// as migrations antes de qualquer hook de início (um Invoke roda na montagem do grafo).
var RepositoryModule = fx.Module(
	"repository",
	fx.Provide(
		postgres.NewConnection,
		postgres.NewTransactionManager,
		postgres.NewWalletRepository,
		postgres.NewTransactionRepository,
		postgres.NewLedgerRepository,
		postgres.NewInboxRepository,
		postgres.NewOutboxRepository,
		postgres.NewHealthCheck,
	),
	fx.Invoke(migrateOnStart),
)

func migrateOnStart(settings config.Config, logger *slog.Logger) error {
	if !settings.Migrations.OnStart {
		return nil
	}

	directory := settings.Migrations.Directory

	if err := postgres.Migrate(settings.DatabaseURL.Reveal(), directory); err != nil {
		return err
	}

	logger.Info("migrations applied", slog.String("directory", directory))

	return nil
}
