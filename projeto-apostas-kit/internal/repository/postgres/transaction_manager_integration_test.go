//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"apostas-go/internal/repository/postgres"
)

var errBoom = errors.New("boom")

func newProbe(t *testing.T) (*pgxpool.Pool, *postgres.TransactionManager, *postgres.Connection) {
	t.Helper()

	pool := startDatabase(t)
	_, err := pool.Exec(
		context.Background(),
		`CREATE TABLE tx_probe (id integer PRIMARY KEY, value integer NOT NULL)`,
	)
	if err != nil {
		t.Fatalf("create probe table: %v", err)
	}

	return pool, postgres.NewTransactionManager(pool), postgres.NewConnection(pool)
}

func countProbe(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()

	var count int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM tx_probe`).Scan(&count)
	if err != nil {
		t.Fatalf("count probe: %v", err)
	}

	return count
}

func insertProbe(ctx context.Context, connection *postgres.Connection, id int) error {
	_, err := connection.Querier(ctx).Exec(
		ctx,
		`INSERT INTO tx_probe (id, value) VALUES ($1, 0)`,
		id,
	)

	return err
}

func TestCommitPersistsWork(t *testing.T) {
	pool, manager, connection := newProbe(t)

	err := manager.WithinTransaction(context.Background(), func(ctx context.Context) error {
		return insertProbe(ctx, connection, 1)
	})

	if err != nil || countProbe(context.Background(), t, pool) != 1 {
		t.Fatalf("error = %v, rows = %d, want committed row", err, countProbe(context.Background(), t, pool))
	}
}

func TestErrorRollsBackAndIsReturned(t *testing.T) {
	pool, manager, connection := newProbe(t)

	err := manager.WithinTransaction(context.Background(), func(ctx context.Context) error {
		if err := insertProbe(ctx, connection, 1); err != nil {
			return err
		}

		return errBoom
	})

	if !errors.Is(err, errBoom) || countProbe(context.Background(), t, pool) != 0 {
		t.Fatalf("error = %v, rows = %d, want rollback", err, countProbe(context.Background(), t, pool))
	}
}

func TestPanicRollsBackAndPropagates(t *testing.T) {
	pool, manager, connection := newProbe(t)

	recovered := func() (value any) {
		defer func() { value = recover() }()

		_ = manager.WithinTransaction(context.Background(), func(ctx context.Context) error {
			_ = insertProbe(ctx, connection, 1)
			panic("kaboom")
		})

		return nil
	}()

	if recovered != "kaboom" || countProbe(context.Background(), t, pool) != 0 {
		t.Fatalf("recovered = %v, rows = %d", recovered, countProbe(context.Background(), t, pool))
	}
}

func TestCancellationRollsBackAndReleasesConnection(t *testing.T) {
	pool, manager, connection := newProbe(t)
	ctx, cancel := context.WithCancel(context.Background())

	err := manager.WithinTransaction(ctx, func(ctx context.Context) error {
		if err := insertProbe(ctx, connection, 1); err != nil {
			return err
		}

		cancel()

		return ctx.Err()
	})

	if !errors.Is(err, context.Canceled) || countProbe(context.Background(), t, pool) != 0 {
		t.Fatalf("error = %v, rows = %d", err, countProbe(context.Background(), t, pool))
	}

	if pool.Stat().AcquiredConns() != 0 {
		t.Fatalf("acquired connections = %d, want 0", pool.Stat().AcquiredConns())
	}
}

func TestNestedCallJoinsOuterTransaction(t *testing.T) {
	pool, manager, connection := newProbe(t)

	err := manager.WithinTransaction(context.Background(), func(ctx context.Context) error {
		inner := manager.WithinTransaction(ctx, func(ctx context.Context) error {
			return insertProbe(ctx, connection, 1)
		})
		if inner != nil {
			return inner
		}

		if countProbe(ctx, t, pool) != 0 {
			return errors.New("inner work must stay invisible before the outer commit")
		}

		return errBoom
	})

	if !errors.Is(err, errBoom) || countProbe(context.Background(), t, pool) != 0 {
		t.Fatalf("error = %v, rows = %d, want full rollback", err, countProbe(context.Background(), t, pool))
	}
}

func TestQuerierUsesTransactionOnlyInsideIt(t *testing.T) {
	pool, manager, connection := newProbe(t)

	if err := insertProbe(context.Background(), connection, 1); err != nil {
		t.Fatalf("insert outside transaction: %v", err)
	}

	_ = manager.WithinTransaction(context.Background(), func(ctx context.Context) error {
		_ = insertProbe(ctx, connection, 2)

		return errBoom
	})

	if countProbe(context.Background(), t, pool) != 1 {
		t.Fatalf("rows = %d, want only the autocommitted row", countProbe(context.Background(), t, pool))
	}
}

func TestConcurrentLockedUpdatesDoNotLoseWrites(t *testing.T) {
	pool, manager, connection := newProbe(t)
	if err := insertProbe(context.Background(), connection, 1); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const workers = 20

	var group sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			errs <- manager.WithinTransaction(context.Background(), incrementProbe(connection))
		}()
	}

	group.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("worker error = %v", err)
		}
	}

	var value int
	row := pool.QueryRow(context.Background(), `SELECT value FROM tx_probe WHERE id = 1`)
	if err := row.Scan(&value); err != nil || value != workers {
		t.Fatalf("value = %d (%v), want %d", value, err, workers)
	}
}

func incrementProbe(connection *postgres.Connection) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		querier := connection.Querier(ctx)

		var value int
		row := querier.QueryRow(ctx, `SELECT value FROM tx_probe WHERE id = 1 FOR UPDATE`)
		if err := row.Scan(&value); err != nil {
			return fmt.Errorf("lock row: %w", err)
		}

		_, err := querier.Exec(ctx, `UPDATE tx_probe SET value = $1 WHERE id = 1`, value+1)

		return err
	}
}
