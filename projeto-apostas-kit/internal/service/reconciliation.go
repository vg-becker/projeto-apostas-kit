package service

import (
	"context"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
)

// ReconciliationResult compara o saldo armazenado com o reconstruído pelo ledger.
type ReconciliationResult struct {
	WalletID          identity.WalletID
	StoredBalance     money.Money
	CalculatedBalance money.Money
	// Difference é o saldo armazenado menos o reconstruído.
	Difference     money.Money
	Consistent     bool
	CheckedEntries int64
}

// ReconcileWallet reconstrói o saldo a partir do ledger, incluindo a abertura, e o compara
// com o saldo armazenado. O saldo armazenado e o reconstruído vêm de uma única instrução
// SQL, portanto do mesmo snapshot. Nada é alterado.
func (service *WalletService) ReconcileWallet(
	ctx context.Context,
	id identity.WalletID,
) (ReconciliationResult, error) {
	result, err := service.reconcile(ctx, id)
	if err == nil {
		service.observer.ObserveReconciliation(ReconciliationObservation{
			WalletID:   id.String(),
			Consistent: result.Consistent,
		})
	}

	return result, err
}

func (service *WalletService) reconcile(
	ctx context.Context,
	id identity.WalletID,
) (ReconciliationResult, error) {
	found, err := service.GetWallet(ctx, id)
	if err != nil {
		return ReconciliationResult{}, err
	}

	reconstruction, err := service.dependencies.Ledger.CalculateBalance(ctx, id, found.Currency())
	if err != nil {
		return ReconciliationResult{}, translateWalletLookup(err, id)
	}

	difference, err := reconstruction.StoredBalance.Sub(reconstruction.Balance)
	if err != nil {
		return ReconciliationResult{}, err
	}

	return ReconciliationResult{
		WalletID:          id,
		StoredBalance:     reconstruction.StoredBalance,
		CalculatedBalance: reconstruction.Balance,
		Difference:        difference,
		Consistent:        difference.IsZero(),
		CheckedEntries:    reconstruction.EntryCount,
	}, nil
}
