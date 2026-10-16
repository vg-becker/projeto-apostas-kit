package service

import (
	"context"
	"errors"
	"fmt"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
)

// QueryServiceDependencies reúne as portas de leitura.
type QueryServiceDependencies struct {
	Wallets    port.WalletRepository
	Operations port.TransactionRepository
	Ledger     port.LedgerRepository
}

// QueryService responde as consultas, aplicando o isolamento entre provedores.
type QueryService struct {
	dependencies QueryServiceDependencies
}

// NewQueryService cria o serviço de consultas.
func NewQueryService(dependencies QueryServiceDependencies) *QueryService {
	return &QueryService{dependencies: dependencies}
}

// GetTransaction lê uma transação pelo ID interno. Para um provedor, a transação de outro
// provedor (e a abertura interna) é indistinguível de uma inexistente: ErrTransactionNotFound.
func (service *QueryService) GetTransaction(
	ctx context.Context,
	scope AccessScope,
	id identity.TransactionID,
) (*wager.Transaction, error) {
	if !scope.valid() {
		return nil, ErrAccessDenied
	}

	found, err := service.dependencies.Operations.FindByID(ctx, id)
	if err != nil {
		return nil, translateTransactionLookup(err, id.String())
	}

	if !scope.canSee(found) {
		return nil, fmt.Errorf("%w: %s", ErrTransactionNotFound, id)
	}

	return found, nil
}

// GetProviderTransaction lê uma transação por (provedor, ID externo). Um provedor que peça
// o caminho de outro provedor recebe ErrAccessDenied.
func (service *QueryService) GetProviderTransaction(
	ctx context.Context,
	scope AccessScope,
	providerID string,
	externalTransactionID string,
) (*wager.Transaction, error) {
	if !scope.valid() || (!scope.Internal && scope.ProviderID != providerID) {
		return nil, ErrAccessDenied
	}

	found, err := service.dependencies.Operations.FindByProviderExternalID(
		ctx,
		providerID,
		externalTransactionID,
	)
	if err != nil {
		return nil, translateTransactionLookup(err, providerID+"/"+externalTransactionID)
	}

	return found, nil
}

// ListLedger devolve uma página do ledger da carteira. A restrição dessa consulta ao
// serviço interno é do Controller.
func (service *QueryService) ListLedger(
	ctx context.Context,
	query port.LedgerQuery,
) (port.LedgerPage, error) {
	if _, err := service.dependencies.Wallets.Get(ctx, query.WalletID); err != nil {
		return port.LedgerPage{}, translateWalletLookup(err, query.WalletID)
	}

	return service.dependencies.Ledger.ListByWallet(ctx, query)
}

func translateTransactionLookup(err error, subject string) error {
	if errors.Is(err, port.ErrNotFound) {
		return fmt.Errorf("%w: %s: %w", ErrTransactionNotFound, subject, err)
	}

	return err
}
