package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wallet"
)

const insertWallet = `
	INSERT INTO wallets (
	       id,
	       player_id,
	       currency,
	       balance_minor_units,
	       version,
	       created_at,
	       updated_at
	)
	VALUES ($1, $2, $3, $4, $5, $6, $7)
`

const walletColumns = `
	SELECT id::text,
	       player_id::text,
	       currency,
	       balance_minor_units,
	       version,
	       created_at,
	       updated_at
	  FROM wallets
`

const (
	selectWalletByID = walletColumns + `
	 WHERE id = $1
`
	lockWalletByID = walletColumns + `
	 WHERE id = $1
	   FOR UPDATE
`
	selectWalletByPlayerAndCurrency = walletColumns + `
	 WHERE player_id = $1
	   AND currency = $2
`
)

const updateWallet = `
	UPDATE wallets
	   SET balance_minor_units = $2,
	       version = $3,
	       updated_at = $4
	 WHERE id = $1
	   AND version = $5
`

var _ port.WalletRepository = (*WalletRepository)(nil)

// WalletRepository implementa port.WalletRepository com SQL explícito.
type WalletRepository struct {
	connection *Connection
}

// NewWalletRepository cria o repositório sobre a conexão.
func NewWalletRepository(connection *Connection) *WalletRepository {
	return &WalletRepository{connection: connection}
}

// Create insere a carteira; (playerId, currency) repetido devolve ErrAlreadyExists.
func (repository *WalletRepository) Create(ctx context.Context, target *wallet.Wallet) error {
	snapshot := target.Snapshot()

	_, err := repository.connection.Querier(ctx).Exec(
		ctx,
		insertWallet,
		snapshot.ID.String(),
		snapshot.PlayerID.String(),
		snapshot.Balance.Currency().String(),
		snapshot.Balance.MinorUnits(),
		snapshot.Version,
		snapshot.CreatedAt,
		snapshot.UpdatedAt,
	)

	return translateError(err)
}

// Get lê a carteira sem bloqueá-la.
func (repository *WalletRepository) Get(
	ctx context.Context,
	id identity.WalletID,
) (*wallet.Wallet, error) {
	row := repository.connection.Querier(ctx).QueryRow(ctx, selectWalletByID, id.String())

	return scanWallet(row)
}

// GetForUpdate bloqueia a linha da carteira até o fim da transação.
func (repository *WalletRepository) GetForUpdate(
	ctx context.Context,
	id identity.WalletID,
) (*wallet.Wallet, error) {
	row := repository.connection.Querier(ctx).QueryRow(ctx, lockWalletByID, id.String())

	return scanWallet(row)
}

// FindByPlayerAndCurrency busca a carteira única do par (jogador, moeda).
func (repository *WalletRepository) FindByPlayerAndCurrency(
	ctx context.Context,
	playerID identity.PlayerID,
	currency money.Currency,
) (*wallet.Wallet, error) {
	row := repository.connection.Querier(ctx).QueryRow(
		ctx,
		selectWalletByPlayerAndCurrency,
		playerID.String(),
		currency.String(),
	)

	return scanWallet(row)
}

// Update grava saldo e versão se a versão persistida ainda for expectedVersion. Sem linha
// afetada devolve ErrVersionConflict: a versão divergiu ou a carteira não existe.
func (repository *WalletRepository) Update(
	ctx context.Context,
	target *wallet.Wallet,
	expectedVersion int64,
) error {
	snapshot := target.Snapshot()

	tag, err := repository.connection.Querier(ctx).Exec(
		ctx,
		updateWallet,
		snapshot.ID.String(),
		snapshot.Balance.MinorUnits(),
		snapshot.Version,
		snapshot.UpdatedAt,
		expectedVersion,
	)
	if err != nil {
		return translateError(err)
	}

	if tag.RowsAffected() == 0 {
		return fmt.Errorf(
			"%w: wallet %s at version %d",
			port.ErrVersionConflict,
			snapshot.ID,
			expectedVersion,
		)
	}

	return nil
}

func scanWallet(row pgx.Row) (*wallet.Wallet, error) {
	var (
		id        string
		playerID  string
		currency  string
		balance   int64
		version   int64
		createdAt time.Time
		updatedAt time.Time
	)

	err := row.Scan(&id, &playerID, &currency, &balance, &version, &createdAt, &updatedAt)
	if err != nil {
		return nil, translateError(err)
	}

	amount, err := money.New(balance, money.Currency(currency))
	if err != nil {
		return nil, fmt.Errorf("postgres: wallet %s balance: %w", id, err)
	}

	return wallet.Rehydrate(wallet.Snapshot{
		ID:        identity.WalletID(id),
		PlayerID:  identity.PlayerID(playerID),
		Balance:   amount,
		Version:   version,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	})
}
