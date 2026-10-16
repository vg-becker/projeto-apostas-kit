package port

import (
	"context"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/money"
)

// LedgerQuery pede uma página do ledger; Cursor vazio começa do início.
type LedgerQuery struct {
	WalletID identity.WalletID
	Cursor   string
	Limit    int
}

// LedgerPage é uma página em ordem estável; NextCursor vazio indica o fim.
type LedgerPage struct {
	Entries    []ledger.Entry
	NextCursor string
}

// Reconstruction é o saldo recalculado a partir do ledger, lido junto com o saldo
// armazenado na mesma instrução SQL para que ambos venham do mesmo snapshot.
type Reconstruction struct {
	Balance       money.Money
	StoredBalance money.Money
	EntryCount    int64
}

// LedgerRepository persiste o ledger, que é append-only. Usa a transação do context.
type LedgerRepository interface {
	// Append devolve ErrAlreadyExists quando (walletId, transactionId) já tem lançamento.
	Append(ctx context.Context, entry ledger.Entry) error
	// ListByWallet devolve ErrInvalidCursor para cursor malformado ou de outra carteira.
	ListByWallet(ctx context.Context, query LedgerQuery) (LedgerPage, error)
	// CalculateBalance soma créditos menos débitos em uma visão consistente.
	CalculateBalance(
		ctx context.Context,
		walletID identity.WalletID,
		currency money.Currency,
	) (Reconstruction, error)
}
