package fxmodules

import (
	"go.uber.org/fx"

	"apostas-go/internal/repository/postgres"
)

// RepositoryModule oferece os repositórios Postgres sobre o pool.
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
)
