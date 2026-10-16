package event

import (
	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/wager"
)

// MoneyData é o valor monetário no contrato externo: texto decimal e moeda.
type MoneyData struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// TransactionProcessedData é o payload de WagerTransactionProcessed.
// Os campos externos são omitidos quando a origem é a abertura interna.
type TransactionProcessedData struct {
	TransactionID                  string    `json:"transactionId"`
	ProviderID                     string    `json:"providerId,omitempty"`
	ExternalTransactionID          string    `json:"externalTransactionId,omitempty"`
	WalletID                       string    `json:"walletId"`
	PlayerID                       string    `json:"playerId"`
	Kind                           string    `json:"kind"`
	Money                          MoneyData `json:"money"`
	Balance                        MoneyData `json:"balance"`
	ResolvedReferenceID            string    `json:"resolvedReferenceId,omitempty"`
	ReferenceExternalTransactionID string    `json:"referenceExternalTransactionId,omitempty"`
}

// TransactionRejectedData é o payload de WagerTransactionRejected.
type TransactionRejectedData struct {
	TransactionID                  string    `json:"transactionId"`
	ProviderID                     string    `json:"providerId"`
	ExternalTransactionID          string    `json:"externalTransactionId"`
	WalletID                       string    `json:"walletId"`
	PlayerID                       string    `json:"playerId"`
	Kind                           string    `json:"kind"`
	Money                          MoneyData `json:"money"`
	FailureCode                    string    `json:"failureCode"`
	Correctable                    bool      `json:"correctable"`
	ReferenceExternalTransactionID string    `json:"referenceExternalTransactionId,omitempty"`
}

// TransactionPendingReferenceData é o payload de WagerTransactionPendingReference.
type TransactionPendingReferenceData struct {
	TransactionID                  string    `json:"transactionId"`
	ProviderID                     string    `json:"providerId"`
	ExternalTransactionID          string    `json:"externalTransactionId"`
	WalletID                       string    `json:"walletId"`
	PlayerID                       string    `json:"playerId"`
	Kind                           string    `json:"kind"`
	Money                          MoneyData `json:"money"`
	ReferenceExternalTransactionID string    `json:"referenceExternalTransactionId"`
}

// BalanceChangedData é o payload de WalletBalanceChanged.
type BalanceChangedData struct {
	WalletID      string    `json:"walletId"`
	TransactionID string    `json:"transactionId"`
	Direction     string    `json:"direction"`
	Money         MoneyData `json:"money"`
	BalanceBefore MoneyData `json:"balanceBefore"`
	BalanceAfter  MoneyData `json:"balanceAfter"`
	WalletVersion int64     `json:"walletVersion"`
}

func moneyData(amount money.Money) MoneyData {
	return MoneyData{
		Amount:   amount.Amount(),
		Currency: amount.Currency().String(),
	}
}

func processedData(snapshot wager.Snapshot) TransactionProcessedData {
	return TransactionProcessedData{
		TransactionID:                  snapshot.ID.String(),
		ProviderID:                     snapshot.ProviderID,
		ExternalTransactionID:          snapshot.ExternalTransactionID,
		WalletID:                       snapshot.WalletID.String(),
		PlayerID:                       snapshot.PlayerID.String(),
		Kind:                           snapshot.Kind.String(),
		Money:                          moneyData(snapshot.Money),
		Balance:                        moneyData(snapshot.ResultBalance),
		ResolvedReferenceID:            snapshot.ResolvedReferenceID.String(),
		ReferenceExternalTransactionID: snapshot.ReferenceExternalTransactionID,
	}
}

func rejectedData(snapshot wager.Snapshot) TransactionRejectedData {
	return TransactionRejectedData{
		TransactionID:                  snapshot.ID.String(),
		ProviderID:                     snapshot.ProviderID,
		ExternalTransactionID:          snapshot.ExternalTransactionID,
		WalletID:                       snapshot.WalletID.String(),
		PlayerID:                       snapshot.PlayerID.String(),
		Kind:                           snapshot.Kind.String(),
		Money:                          moneyData(snapshot.Money),
		FailureCode:                    snapshot.FailureCode.String(),
		Correctable:                    snapshot.FailureCode.IsCorrectable(),
		ReferenceExternalTransactionID: snapshot.ReferenceExternalTransactionID,
	}
}

func pendingReferenceData(snapshot wager.Snapshot) TransactionPendingReferenceData {
	return TransactionPendingReferenceData{
		TransactionID:                  snapshot.ID.String(),
		ProviderID:                     snapshot.ProviderID,
		ExternalTransactionID:          snapshot.ExternalTransactionID,
		WalletID:                       snapshot.WalletID.String(),
		PlayerID:                       snapshot.PlayerID.String(),
		Kind:                           snapshot.Kind.String(),
		Money:                          moneyData(snapshot.Money),
		ReferenceExternalTransactionID: snapshot.ReferenceExternalTransactionID,
	}
}

func balanceChangedData(entry ledger.Entry, walletVersion int64) BalanceChangedData {
	return BalanceChangedData{
		WalletID:      entry.WalletID().String(),
		TransactionID: entry.TransactionID().String(),
		Direction:     entry.Direction().String(),
		Money:         moneyData(entry.Money()),
		BalanceBefore: moneyData(entry.BalanceBefore()),
		BalanceAfter:  moneyData(entry.BalanceAfter()),
		WalletVersion: walletVersion,
	}
}
