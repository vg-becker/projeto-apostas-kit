package port

import (
	"context"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/wallet"
)

// WalletRepository persiste carteiras. Todas as operações usam a transação do context.
type WalletRepository interface {
	// Create devolve ErrAlreadyExists quando (playerId, currency) já tem carteira.
	Create(ctx context.Context, target *wallet.Wallet) error
	// Get devolve ErrNotFound quando a carteira não existe.
	Get(ctx context.Context, id identity.WalletID) (*wallet.Wallet, error)
	// GetForUpdate bloqueia a linha da carteira (SELECT ... FOR UPDATE) até o fim da transação.
	GetForUpdate(ctx context.Context, id identity.WalletID) (*wallet.Wallet, error)
	// FindByPlayerAndCurrency devolve ErrNotFound quando não há carteira.
	FindByPlayerAndCurrency(
		ctx context.Context,
		playerID identity.PlayerID,
		currency money.Currency,
	) (*wallet.Wallet, error)
	// Update grava o novo saldo e versão; devolve ErrVersionConflict se a versão
	// persistida diferir de expectedVersion.
	Update(ctx context.Context, target *wallet.Wallet, expectedVersion int64) error
}
