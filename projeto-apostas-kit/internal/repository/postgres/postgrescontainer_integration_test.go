//go:build integration

package postgres_test

import (
	"context"
	"path/filepath"
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

const migrationsDirectory = "../../../migrations"

// startDatabase sobe um Postgres real, aplica as migrations e devolve o pool.
func startDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()

	databaseURL := startContainer(t)
	applyMigrations(t, databaseURL)

	pool, err := postgres.NewPool(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("NewPool error = %v", err)
	}

	t.Cleanup(pool.Close)

	return pool
}

func startContainer(t *testing.T) string {
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
		t.Fatalf("start postgres container: %v", err)
	}

	databaseURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	return databaseURL
}

func newMigrator(t *testing.T, databaseURL string) *migrate.Migrate {
	t.Helper()

	absolute, err := filepath.Abs(migrationsDirectory)
	if err != nil {
		t.Fatalf("absolute migrations path: %v", err)
	}

	migrator, err := migrate.New(
		"file://"+absolute,
		strings.Replace(databaseURL, "postgres://", "pgx5://", 1),
	)
	if err != nil {
		t.Fatalf("create migrator: %v", err)
	}

	return migrator
}

func applyMigrations(t *testing.T, databaseURL string) {
	t.Helper()

	migrator := newMigrator(t, databaseURL)
	defer func() { _, _ = migrator.Close() }()

	if err := migrator.Up(); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
}

func TestMigrationsUpDownUp(t *testing.T) {
	databaseURL := startContainer(t)
	migrator := newMigrator(t, databaseURL)
	defer func() { _, _ = migrator.Close() }()

	steps := []struct {
		name string
		run  func() error
	}{
		{name: "up", run: migrator.Up},
		{name: "down", run: migrator.Down},
		{name: "up again", run: migrator.Up},
	}

	for _, step := range steps {
		if err := step.run(); err != nil {
			t.Fatalf("migrate %s: %v", step.name, err)
		}
	}
}
