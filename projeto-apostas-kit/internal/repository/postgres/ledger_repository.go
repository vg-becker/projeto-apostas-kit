package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/port"
)

const (
	defaultLedgerPageSize = 50
	maxLedgerPageSize     = 200
)

const insertLedgerEntry = `
	INSERT INTO wallet_ledger_entries (
	       id,
	       wallet_id,
	       transaction_id,
	       direction,
	       currency,
	       amount_minor_units,
	       balance_before_minor_units,
	       balance_after_minor_units,
	       created_at
	)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
`

const selectLedgerPage = `
	SELECT id::text,
	       wallet_id::text,
	       transaction_id::text,
	       direction,
	       currency,
	       amount_minor_units,
	       balance_before_minor_units,
	       balance_after_minor_units,
	       created_at
	  FROM wallet_ledger_entries
	 WHERE wallet_id = $1
	   AND ($2::timestamptz IS NULL OR (created_at, id) > ($2::timestamptz, $3::uuid))
	 ORDER BY created_at, id
	 LIMIT $4
`

const reconstructBalance = `
	SELECT w.balance_minor_units,
	       COALESCE(
	              SUM(
	                  CASE l.direction
	                       WHEN 'CREDIT' THEN l.amount_minor_units
	                       ELSE -l.amount_minor_units
	                  END
	              ),
	              0
	       )::bigint,
	       COUNT(l.id)
	  FROM wallets AS w
	  LEFT JOIN wallet_ledger_entries AS l
	    ON l.wallet_id = w.id
	 WHERE w.id = $1
	   AND w.currency = $2
	 GROUP BY w.id
`

var _ port.LedgerRepository = (*LedgerRepository)(nil)

// LedgerRepository implementa port.LedgerRepository; o ledger é append-only.
type LedgerRepository struct {
	connection *Connection
}

// NewLedgerRepository cria o repositório sobre a conexão.
func NewLedgerRepository(connection *Connection) *LedgerRepository {
	return &LedgerRepository{connection: connection}
}

// Append insere o lançamento; (walletId, transactionId) repetido devolve ErrAlreadyExists.
func (repository *LedgerRepository) Append(ctx context.Context, entry ledger.Entry) error {
	_, err := repository.connection.Querier(ctx).Exec(
		ctx,
		insertLedgerEntry,
		entry.ID().String(),
		entry.WalletID().String(),
		entry.TransactionID().String(),
		entry.Direction().String(),
		entry.Money().Currency().String(),
		entry.Money().MinorUnits(),
		entry.BalanceBefore().MinorUnits(),
		entry.BalanceAfter().MinorUnits(),
		entry.CreatedAt(),
	)

	return translateError(err)
}

// ListByWallet devolve uma página em ordem estável (created_at, id).
func (repository *LedgerRepository) ListByWallet(
	ctx context.Context,
	query port.LedgerQuery,
) (port.LedgerPage, error) {
	after, err := cursorArguments(query)
	if err != nil {
		return port.LedgerPage{}, err
	}

	limit := normalizeLimit(query.Limit)

	rows, err := repository.connection.Querier(ctx).Query(
		ctx,
		selectLedgerPage,
		query.WalletID.String(),
		after.createdAt,
		after.entryID,
		limit+1,
	)
	if err != nil {
		return port.LedgerPage{}, translateError(err)
	}
	defer rows.Close()

	entries, err := collectEntries(rows)
	if err != nil {
		return port.LedgerPage{}, err
	}

	return buildPage(query.WalletID, entries, limit)
}

// CalculateBalance reconstrói o saldo pelo ledger e lê o saldo armazenado na mesma
// instrução SQL, garantindo um único snapshot para a reconciliação.
func (repository *LedgerRepository) CalculateBalance(
	ctx context.Context,
	walletID identity.WalletID,
	currency money.Currency,
) (port.Reconstruction, error) {
	var stored, calculated, entryCount int64

	row := repository.connection.Querier(ctx).QueryRow(
		ctx,
		reconstructBalance,
		walletID.String(),
		currency.String(),
	)
	if err := row.Scan(&stored, &calculated, &entryCount); err != nil {
		return port.Reconstruction{}, translateError(err)
	}

	return newReconstruction(currency, stored, calculated, entryCount)
}

type cursorPosition struct {
	createdAt *time.Time
	entryID   *string
}

func cursorArguments(query port.LedgerQuery) (cursorPosition, error) {
	if query.Cursor == "" {
		return cursorPosition{}, nil
	}

	cursor, err := decodeCursor(query.Cursor, query.WalletID)
	if err != nil {
		return cursorPosition{}, err
	}

	return cursorPosition{createdAt: &cursor.CreatedAt, entryID: &cursor.EntryID}, nil
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return defaultLedgerPageSize
	}

	return min(limit, maxLedgerPageSize)
}

func buildPage(
	walletID identity.WalletID,
	entries []ledger.Entry,
	limit int,
) (port.LedgerPage, error) {
	if len(entries) <= limit {
		return port.LedgerPage{Entries: entries}, nil
	}

	last := entries[limit-1]
	next, err := encodeCursor(walletID, last.CreatedAt(), last.ID().String())
	if err != nil {
		return port.LedgerPage{}, err
	}

	return port.LedgerPage{Entries: entries[:limit], NextCursor: next}, nil
}

func collectEntries(rows pgx.Rows) ([]ledger.Entry, error) {
	var entries []ledger.Entry

	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}

		entries = append(entries, entry)
	}

	return entries, translateError(rows.Err())
}

// entryRow recebe uma linha de wallet_ledger_entries.
type entryRow struct {
	id            string
	walletID      string
	transactionID string
	direction     string
	currency      string
	amount        int64
	before        int64
	after         int64
	createdAt     time.Time
}

func (row *entryRow) destinations() []any {
	return []any{
		&row.id,
		&row.walletID,
		&row.transactionID,
		&row.direction,
		&row.currency,
		&row.amount,
		&row.before,
		&row.after,
		&row.createdAt,
	}
}

func scanEntry(scanner pgx.Row) (ledger.Entry, error) {
	var row entryRow
	if err := scanner.Scan(row.destinations()...); err != nil {
		return ledger.Entry{}, translateError(err)
	}

	return row.entry()
}

func (row *entryRow) entry() (ledger.Entry, error) {
	values := make([]money.Money, 0, 3)
	for _, minorUnits := range []int64{row.amount, row.before, row.after} {
		value, err := money.New(minorUnits, money.Currency(row.currency))
		if err != nil {
			return ledger.Entry{}, fmt.Errorf("postgres: ledger entry %s: %w", row.id, err)
		}

		values = append(values, value)
	}

	return ledger.Rehydrate(ledger.EntryParams{
		ID:            identity.EntryID(row.id),
		WalletID:      identity.WalletID(row.walletID),
		TransactionID: identity.TransactionID(row.transactionID),
		Direction:     ledger.Direction(row.direction),
		Money:         values[0],
		BalanceBefore: values[1],
		BalanceAfter:  values[2],
		CreatedAt:     row.createdAt,
	})
}

func newReconstruction(
	currency money.Currency,
	stored int64,
	calculated int64,
	entryCount int64,
) (port.Reconstruction, error) {
	storedBalance, err := money.New(stored, currency)
	if err != nil {
		return port.Reconstruction{}, err
	}

	balance, err := money.New(calculated, currency)
	if err != nil {
		return port.Reconstruction{}, err
	}

	return port.Reconstruction{
		Balance:       balance,
		StoredBalance: storedBalance,
		EntryCount:    entryCount,
	}, nil
}
