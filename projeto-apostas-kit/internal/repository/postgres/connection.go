package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier é o subconjunto de pgx usado pelos repositórios; o pool e a transação o satisfazem.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row
}

// Connection entrega aos repositórios a transação do context ou, na falta dela, o pool.
type Connection struct {
	pool *pgxpool.Pool
}

// NewPool abre o pool e confirma com um ping que o banco responde.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", translateError(err))
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()

		return nil, fmt.Errorf("postgres: ping: %w", translateError(err))
	}

	return pool, nil
}

// NewConnection cria a fachada de acesso sobre o pool.
func NewConnection(pool *pgxpool.Pool) *Connection {
	return &Connection{pool: pool}
}

// Querier devolve a transação em andamento no context ou o pool.
func (connection *Connection) Querier(ctx context.Context) Querier {
	if transaction, found := transactionFrom(ctx); found {
		return transaction
	}

	return connection.pool
}
