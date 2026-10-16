package port

import (
	"context"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/wager"
)

// TransactionRepository persiste WagerTransaction. Usa a transação do context.
type TransactionRepository interface {
	// Insert devolve ErrAlreadyExists se (provider, key) ou (provider, externalId) já existir.
	Insert(ctx context.Context, transaction *wager.Transaction) error
	// Update grava o estado da transação; devolve ErrNotFound se ela não existir.
	Update(ctx context.Context, transaction *wager.Transaction) error
	// As buscas devolvem ErrNotFound quando não há resultado.
	FindByID(ctx context.Context, id identity.TransactionID) (*wager.Transaction, error)
	FindByProviderExternalID(
		ctx context.Context,
		providerID string,
		externalTransactionID string,
	) (*wager.Transaction, error)
	FindByIdempotencyKey(
		ctx context.Context,
		providerID string,
		idempotencyKey string,
	) (*wager.Transaction, error)
	// HasProcessedReversal informa se a referência já recebeu uma reversão PROCESSED.
	HasProcessedReversal(ctx context.Context, reference identity.TransactionID) (bool, error)
	// ClaimPending assume transações PENDING abandonadas, para retomada por outra instância.
	ClaimPending(ctx context.Context, params ClaimParams) ([]*wager.Transaction, error)
	// ClaimDuePendingReferences assume PENDING_REFERENCE cujo próximo retry venceu.
	ClaimDuePendingReferences(
		ctx context.Context,
		params ClaimParams,
	) ([]*wager.Transaction, error)
	// ReferenceAttempts devolve quantas vezes a espera pela referência foi reagendada;
	// o Service usa o total para definir o atraso e aplicar o limite de tentativas.
	ReferenceAttempts(ctx context.Context, id identity.TransactionID) (int, error)
	// ScheduleReferenceRetry agenda a próxima tentativa e devolve o total de tentativas,
	// para o Service aplicar o limite de tentativas e o TTL.
	ScheduleReferenceRetry(
		ctx context.Context,
		id identity.TransactionID,
		nextAttemptAt time.Time,
	) (int, error)
}
