package service

import (
	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/wager"
)

// OperationResult é o desfecho persistido de uma operação. Uma rejeição de negócio é um
// resultado (Status REJECTED com FailureCode), não um erro.
type OperationResult struct {
	TransactionID identity.TransactionID
	Status        wager.Status
	// Balance é o saldo observado no processamento original; só existe em PROCESSED.
	Balance          money.Money
	FailureCode      wager.FailureCode
	IdempotentReplay bool
}

func resultOf(transaction *wager.Transaction, replay bool) OperationResult {
	return OperationResult{
		TransactionID:    transaction.ID(),
		Status:           transaction.Status(),
		Balance:          transaction.ResultBalance(),
		FailureCode:      transaction.FailureCode(),
		IdempotentReplay: replay,
	}
}
