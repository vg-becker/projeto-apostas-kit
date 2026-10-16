//go:build integration

package postgres_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"apostas-go/internal/repository/postgres"
	"apostas-go/internal/testsupport/postgresdb"
)

func migrationsPath(t *testing.T) string {
	t.Helper()

	path, err := filepath.Abs(filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	return path
}

func TestMigrateBuildsTheSchemaAndIsANoOpTheSecondTime(t *testing.T) {
	databaseURL := postgresdb.StartEmpty(t)

	for attempt := range 2 {
		if err := postgres.Migrate(databaseURL, migrationsPath(t)); err != nil {
			t.Fatalf("Migrate() attempt %d = %v", attempt, err)
		}
	}

	pool, err := postgres.NewPool(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("NewPool() = %v", err)
	}

	defer pool.Close()

	var tables int

	err = pool.QueryRow(context.Background(), `
		SELECT count(*)
		  FROM information_schema.tables
		 WHERE table_schema = 'public'
		   AND table_name IN ('wallets', 'wager_transactions', 'wallet_ledger_entries',
		                      'inbox_messages', 'outbox_events')
	`).Scan(&tables)
	if err != nil || tables != 5 {
		t.Fatalf("tables = %d, %v", tables, err)
	}
}

// Várias instâncias subindo juntas: o lock consultivo faz cada migration rodar uma só vez e
// nenhuma instância falha.
func TestInstancesMigratingAtTheSameTimeAllSucceed(t *testing.T) {
	databaseURL := postgresdb.StartEmpty(t)
	results := make([]error, 4)

	var group sync.WaitGroup

	for index := range results {
		group.Add(1)

		go func() {
			defer group.Done()

			results[index] = postgres.Migrate(databaseURL, migrationsPath(t))
		}()
	}

	group.Wait()

	for index, err := range results {
		if err != nil {
			t.Fatalf("instance %d: %v", index, err)
		}
	}
}

func TestMigrateFailsWithoutLeakingThePasswordWhenTheDatabaseIsUnreachable(t *testing.T) {
	err := postgres.Migrate("postgres://apostas:super-secret-pw@127.0.0.1:1/apostas?sslmode=disable&connect_timeout=2", migrationsPath(t))
	if err == nil {
		t.Fatal("want an error for an unreachable database")
	}

	if strings.Contains(err.Error(), "super-secret-pw") {
		t.Fatalf("the error leaks the password: %v", err)
	}
}
