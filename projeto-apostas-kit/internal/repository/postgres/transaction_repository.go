package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
)

const insertTransaction = `
	INSERT INTO wager_transactions (` + transactionColumnsInsert + `)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
	        $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
`

// transactionColumnsInsert segue a ordem de transactionColumns, sem os casts de leitura.
const transactionColumnsInsert = `
	       id,
	       kind,
	       status,
	       wallet_id,
	       player_id,
	       currency,
	       amount_minor_units,
	       provider_id,
	       external_transaction_id,
	       idempotency_key,
	       payload_hash,
	       round_id,
	       game_id,
	       reference_external_transaction_id,
	       resolved_reference_id,
	       failure_code,
	       result_balance_minor_units,
	       created_at,
	       updated_at,
	       correlation_id,
	       causation_id
`

const updateTransaction = `
	UPDATE wager_transactions
	   SET status = $2,
	       failure_code = $3,
	       result_balance_minor_units = $4,
	       resolved_reference_id = $5,
	       updated_at = $6,
	       lease_until = NULL
	 WHERE id = $1
`

const selectTransaction = `
	SELECT ` + transactionColumns + `
	  FROM wager_transactions
`

const (
	selectTransactionByID = selectTransaction + `
	 WHERE id = $1
`
	selectTransactionByExternalID = selectTransaction + `
	 WHERE provider_id = $1
	   AND external_transaction_id = $2
`
	selectTransactionByIdempotencyKey = selectTransaction + `
	 WHERE provider_id = $1
	   AND idempotency_key = $2
`
)

const existsProcessedReversal = `
	SELECT EXISTS (
	       SELECT 1
	         FROM wager_transactions
	        WHERE resolved_reference_id = $1
	          AND kind IN ('REFUND', 'ROLLBACK')
	          AND status = 'PROCESSED'
	)
`

const claimPending = `
	WITH claimed AS (
	     SELECT id
	       FROM wager_transactions
	      WHERE status = 'PENDING'
	        AND (lease_until IS NULL OR lease_until <= $1)
	      ORDER BY created_at
	      LIMIT $2
	        FOR UPDATE SKIP LOCKED
	)
	UPDATE wager_transactions
	   SET lease_until = $3
	 WHERE id IN (SELECT id FROM claimed)
	RETURNING ` + transactionColumns

const claimDuePendingReferences = `
	WITH claimed AS (
	     SELECT id
	       FROM wager_transactions
	      WHERE status = 'PENDING_REFERENCE'
	        AND (next_attempt_at IS NULL OR next_attempt_at <= $1)
	        AND (lease_until IS NULL OR lease_until <= $1)
	      ORDER BY next_attempt_at NULLS FIRST, created_at
	      LIMIT $2
	        FOR UPDATE SKIP LOCKED
	)
	UPDATE wager_transactions
	   SET lease_until = $3
	 WHERE id IN (SELECT id FROM claimed)
	RETURNING ` + transactionColumns

const selectReferenceAttempts = `
	SELECT reference_attempts
	  FROM wager_transactions
	 WHERE id = $1
`

const scheduleReferenceRetry = `
	UPDATE wager_transactions
	   SET reference_attempts = reference_attempts + 1,
	       next_attempt_at = $2,
	       lease_until = NULL
	 WHERE id = $1
	   AND status = 'PENDING_REFERENCE'
	RETURNING reference_attempts
`

var _ port.TransactionRepository = (*TransactionRepository)(nil)

// TransactionRepository implementa port.TransactionRepository com SQL explícito.
type TransactionRepository struct {
	connection *Connection
}

// NewTransactionRepository cria o repositório sobre a conexão.
func NewTransactionRepository(connection *Connection) *TransactionRepository {
	return &TransactionRepository{connection: connection}
}

// Insert grava a transação; chave de idempotência ou ID externo repetidos
// devolvem ErrAlreadyExists, e a constraint violada fica na mensagem.
func (repository *TransactionRepository) Insert(
	ctx context.Context,
	transaction *wager.Transaction,
) error {
	_, err := repository.connection.Querier(ctx).Exec(
		ctx,
		insertTransaction,
		insertArguments(transaction.Snapshot())...,
	)

	return translateError(err)
}

func insertArguments(snapshot wager.Snapshot) []any {
	return []any{
		snapshot.ID.String(),
		snapshot.Kind.String(),
		snapshot.Status.String(),
		snapshot.WalletID.String(),
		snapshot.PlayerID.String(),
		snapshot.Money.Currency().String(),
		snapshot.Money.MinorUnits(),
		nullableText(snapshot.ProviderID),
		nullableText(snapshot.ExternalTransactionID),
		nullableText(snapshot.IdempotencyKey),
		nullableText(snapshot.PayloadHash),
		nullableText(snapshot.RoundID),
		nullableText(snapshot.GameID),
		nullableText(snapshot.ReferenceExternalTransactionID),
		nullableText(snapshot.ResolvedReferenceID.String()),
		nullableText(snapshot.FailureCode.String()),
		nullableMinorUnits(snapshot.ResultBalance),
		snapshot.CreatedAt,
		snapshot.UpdatedAt,
		nullableText(snapshot.CorrelationID),
		nullableText(snapshot.CausationID),
	}
}

// Update grava o desfecho da transação e libera o lease.
func (repository *TransactionRepository) Update(
	ctx context.Context,
	transaction *wager.Transaction,
) error {
	snapshot := transaction.Snapshot()

	tag, err := repository.connection.Querier(ctx).Exec(
		ctx,
		updateTransaction,
		snapshot.ID.String(),
		snapshot.Status.String(),
		nullableText(snapshot.FailureCode.String()),
		nullableMinorUnits(snapshot.ResultBalance),
		nullableText(snapshot.ResolvedReferenceID.String()),
		snapshot.UpdatedAt,
	)
	if err != nil {
		return translateError(err)
	}

	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: transaction %s", port.ErrNotFound, snapshot.ID)
	}

	return nil
}

// FindByID busca pela identidade interna.
func (repository *TransactionRepository) FindByID(
	ctx context.Context,
	id identity.TransactionID,
) (*wager.Transaction, error) {
	row := repository.connection.Querier(ctx).QueryRow(ctx, selectTransactionByID, id.String())

	return scanTransaction(row)
}

// FindByProviderExternalID busca por (provedor, ID externo).
func (repository *TransactionRepository) FindByProviderExternalID(
	ctx context.Context,
	providerID string,
	externalTransactionID string,
) (*wager.Transaction, error) {
	row := repository.connection.Querier(ctx).QueryRow(
		ctx,
		selectTransactionByExternalID,
		providerID,
		externalTransactionID,
	)

	return scanTransaction(row)
}

// FindByIdempotencyKey busca por (provedor, chave de idempotência).
func (repository *TransactionRepository) FindByIdempotencyKey(
	ctx context.Context,
	providerID string,
	idempotencyKey string,
) (*wager.Transaction, error) {
	row := repository.connection.Querier(ctx).QueryRow(
		ctx,
		selectTransactionByIdempotencyKey,
		providerID,
		idempotencyKey,
	)

	return scanTransaction(row)
}

// HasProcessedReversal informa se a referência já recebeu uma reversão PROCESSED.
func (repository *TransactionRepository) HasProcessedReversal(
	ctx context.Context,
	reference identity.TransactionID,
) (bool, error) {
	var exists bool

	row := repository.connection.Querier(ctx).QueryRow(
		ctx,
		existsProcessedReversal,
		reference.String(),
	)
	if err := row.Scan(&exists); err != nil {
		return false, translateError(err)
	}

	return exists, nil
}

// ClaimPending assume PENDING sem lease ou com lease vencido, ignorando os já travados.
func (repository *TransactionRepository) ClaimPending(
	ctx context.Context,
	params port.ClaimParams,
) ([]*wager.Transaction, error) {
	return repository.claim(ctx, claimPending, params)
}

// ClaimDuePendingReferences assume PENDING_REFERENCE com retry vencido e sem lease ativo.
func (repository *TransactionRepository) ClaimDuePendingReferences(
	ctx context.Context,
	params port.ClaimParams,
) ([]*wager.Transaction, error) {
	return repository.claim(ctx, claimDuePendingReferences, params)
}

// ReferenceAttempts devolve o total de tentativas já agendadas para a referência.
func (repository *TransactionRepository) ReferenceAttempts(
	ctx context.Context,
	id identity.TransactionID,
) (int, error) {
	var attempts int

	row := repository.connection.Querier(ctx).QueryRow(ctx, selectReferenceAttempts, id.String())
	if err := row.Scan(&attempts); err != nil {
		return 0, translateError(err)
	}

	return attempts, nil
}

// ScheduleReferenceRetry registra mais uma tentativa e devolve o total acumulado.
func (repository *TransactionRepository) ScheduleReferenceRetry(
	ctx context.Context,
	id identity.TransactionID,
	nextAttemptAt time.Time,
) (int, error) {
	var attempts int

	row := repository.connection.Querier(ctx).QueryRow(
		ctx,
		scheduleReferenceRetry,
		id.String(),
		nextAttemptAt,
	)
	if err := row.Scan(&attempts); err != nil {
		return 0, translateError(err)
	}

	return attempts, nil
}

func (repository *TransactionRepository) claim(
	ctx context.Context,
	query string,
	params port.ClaimParams,
) ([]*wager.Transaction, error) {
	if params.Limit <= 0 || params.Lease <= 0 {
		return nil, fmt.Errorf("postgres: claim needs a positive limit and lease")
	}

	rows, err := repository.connection.Querier(ctx).Query(
		ctx,
		query,
		params.Now,
		params.Limit,
		params.Now.Add(params.Lease),
	)
	if err != nil {
		return nil, translateError(err)
	}
	defer rows.Close()

	return collectTransactions(rows)
}

func collectTransactions(rows pgx.Rows) ([]*wager.Transaction, error) {
	var transactions []*wager.Transaction

	for rows.Next() {
		var row transactionRow
		if err := rows.Scan(row.destinations()...); err != nil {
			return nil, translateError(err)
		}

		transaction, err := row.transaction()
		if err != nil {
			return nil, err
		}

		transactions = append(transactions, transaction)
	}

	return transactions, translateError(rows.Err())
}

func scanTransaction(scanner pgx.Row) (*wager.Transaction, error) {
	var row transactionRow
	if err := scanner.Scan(row.destinations()...); err != nil {
		return nil, translateError(err)
	}

	return row.transaction()
}
