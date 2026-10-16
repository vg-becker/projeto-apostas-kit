package event

import (
	"fmt"

	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/wager"
)

// NewWagerTransactionProcessed cria o evento de uma transação em PROCESSED, incluindo LOSS.
func NewWagerTransactionProcessed(
	header Header,
	transaction *wager.Transaction,
) (Envelope[TransactionProcessedData], error) {
	snapshot := transaction.Snapshot()
	if err := requireStatus(snapshot, wager.Processed); err != nil {
		return Envelope[TransactionProcessedData]{}, err
	}

	return newEnvelope(
		TypeWagerTransactionProcessed,
		snapshot.ID.String(),
		header,
		processedData(snapshot),
	)
}

// NewWagerTransactionRejected cria o evento de uma rejeição definitiva por regra de negócio.
func NewWagerTransactionRejected(
	header Header,
	transaction *wager.Transaction,
) (Envelope[TransactionRejectedData], error) {
	snapshot := transaction.Snapshot()
	if err := requireStatus(snapshot, wager.Rejected); err != nil {
		return Envelope[TransactionRejectedData]{}, err
	}

	return newEnvelope(
		TypeWagerTransactionRejected,
		snapshot.ID.String(),
		header,
		rejectedData(snapshot),
	)
}

// NewWagerTransactionPendingReference cria o evento do registro de espera pela referência.
func NewWagerTransactionPendingReference(
	header Header,
	transaction *wager.Transaction,
) (Envelope[TransactionPendingReferenceData], error) {
	snapshot := transaction.Snapshot()
	if err := requireStatus(snapshot, wager.PendingReference); err != nil {
		return Envelope[TransactionPendingReferenceData]{}, err
	}

	return newEnvelope(
		TypeWagerTransactionPendingReference,
		snapshot.ID.String(),
		header,
		pendingReferenceData(snapshot),
	)
}

// NewWalletBalanceChanged cria o evento de uma alteração efetiva de saldo.
func NewWalletBalanceChanged(
	header Header,
	entry ledger.Entry,
	walletVersion int64,
) (Envelope[BalanceChangedData], error) {
	if !entry.ID().IsValid() {
		return Envelope[BalanceChangedData]{}, invalid("ledger entry is required")
	}

	if walletVersion < 1 {
		return Envelope[BalanceChangedData]{}, invalid("wallet version must be at least 1")
	}

	return newEnvelope(
		TypeWalletBalanceChanged,
		entry.WalletID().String(),
		header,
		balanceChangedData(entry, walletVersion),
	)
}

func requireStatus(snapshot wager.Snapshot, want wager.Status) error {
	if snapshot.Status != want {
		return invalid(fmt.Sprintf("transaction is %s, want %s", snapshot.Status, want))
	}

	return nil
}
