//go:build integration

package postgresdb

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"apostas-go/internal/repository/postgres"
)

// Start sobe um Postgres 17, aplica as migrations reais e devolve o pool, fechado ao fim
// do teste. O container é removido pelo testcontainers.
func Start(t testing.TB) *pgxpool.Pool {
	t.Helper()

	databaseURL := startContainer(t)
	applyMigrations(t, databaseURL)

	pool, err := postgres.NewPool(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("postgresdb: NewPool: %v", err)
	}

	t.Cleanup(pool.Close)

	return pool
}

// StartEmpty sobe um Postgres 17 sem nenhuma migration e devolve a URL de conexão, para os
// testes que precisam aplicar as migrations por conta própria (a partida do serviço).
func StartEmpty(t testing.TB) string {
	t.Helper()

	return startContainer(t)
}

func startContainer(t testing.TB) string {
	t.Helper()

	ctx := context.Background()
	container, err := tcpostgres.Run(
		ctx,
		"postgres:17",
		tcpostgres.WithDatabase("apostas"),
		tcpostgres.WithUsername("apostas"),
		tcpostgres.WithPassword("apostas_test"),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("postgresdb: start container: %v", err)
	}

	databaseURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgresdb: connection string: %v", err)
	}

	return databaseURL
}

func applyMigrations(t testing.TB, databaseURL string) {
	t.Helper()

	migrator, err := migrate.New(
		"file://"+migrationsDirectory(t),
		strings.Replace(databaseURL, "postgres://", "pgx5://", 1),
	)
	if err != nil {
		t.Fatalf("postgresdb: create migrator: %v", err)
	}

	defer func() { _, _ = migrator.Close() }()

	if err := migrator.Up(); err != nil {
		t.Fatalf("postgresdb: migrate up: %v", err)
	}
}

// migrationsDirectory resolve o diretório a partir deste arquivo, não do diretório de
// trabalho, para funcionar a partir de qualquer pacote de teste.
func migrationsDirectory(t testing.TB) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("postgresdb: cannot locate source file")
	}

	return filepath.Join(filepath.Dir(file), "..", "..", "..", "migrations")
}
