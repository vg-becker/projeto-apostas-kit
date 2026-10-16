package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"apostas-go/internal/model/port"
)

const rollbackTimeout = 5 * time.Second

var _ port.TxManager = (*TransactionManager)(nil)

type transactionKey struct{}

// TransactionManager implementa port.TxManager em READ COMMITTED. A consistência por
// carteira vem de SELECT ... FOR UPDATE e das constraints, não de um isolamento maior.
type TransactionManager struct {
	pool *pgxpool.Pool
}

// NewTransactionManager cria o gerenciador sobre o pool.
func NewTransactionManager(pool *pgxpool.Pool) *TransactionManager {
	return &TransactionManager{pool: pool}
}

// WithinTransaction executa o trabalho em uma transação: commit se retornar nil;
// erro ou panic desfazem tudo. Se já houver transação no context, ela é reaproveitada.
func (manager *TransactionManager) WithinTransaction(
	ctx context.Context,
	work func(ctx context.Context) error,
) error {
	if _, joined := transactionFrom(ctx); joined {
		return work(ctx)
	}

	transaction, err := manager.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return translateError(err)
	}

	return runTransaction(ctx, transaction, work)
}

func runTransaction(
	ctx context.Context,
	transaction pgx.Tx,
	work func(ctx context.Context) error,
) error {
	committed := false
	defer func() {
		if !committed {
			rollback(ctx, transaction)
		}
	}()

	if err := work(context.WithValue(ctx, transactionKey{}, transaction)); err != nil {
		return err
	}

	if err := transaction.Commit(ctx); err != nil {
		return translateError(err)
	}

	committed = true

	return nil
}

// rollback ignora o cancelamento do context original: a transação não pode ficar aberta.
func rollback(ctx context.Context, transaction pgx.Tx) {
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()

	_ = transaction.Rollback(rollbackContext)
}

func transactionFrom(ctx context.Context) (pgx.Tx, bool) {
	transaction, found := ctx.Value(transactionKey{}).(pgx.Tx)

	return transaction, found
}
