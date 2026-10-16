package service

import (
	"fmt"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/wager"
)

// operationPlan é tudo que a operação precisa, calculado antes de abrir a transação SQL.
type operationPlan struct {
	command       ProcessOperationCommand
	hash          string
	transaction   *wager.Transaction
	entryID       identity.EntryID
	eventIDs      [2]identity.EventID
	correlationID string
	receivedAt    time.Time
}

// plan valida a entrada e prepara a transação PENDING e os identificadores. Nada aqui
// toca o banco: entrada inválida ou não suportada não deixa rastro.
func (service *OperationService) plan(command ProcessOperationCommand) (operationPlan, error) {
	hash, err := CanonicalHash(command)
	if err != nil {
		return operationPlan{}, err
	}

	transactionID, err := service.dependencies.IDs.NewTransactionID()
	if err != nil {
		return operationPlan{}, err
	}

	receivedAt := service.dependencies.Clock.Now()

	transaction, err := wager.NewExternal(externalParams(command, transactionID, hash, receivedAt))
	if err != nil {
		return operationPlan{}, err
	}

	if isUnsupported(command) {
		return operationPlan{}, fmt.Errorf("%w: %s", ErrUnsupportedOperation, command.Kind)
	}

	return service.completePlan(operationPlan{
		command:       command,
		hash:          hash,
		transaction:   transaction,
		correlationID: correlationOf(command, transactionID),
		receivedAt:    receivedAt,
	})
}

func (service *OperationService) completePlan(plan operationPlan) (operationPlan, error) {
	ids := service.dependencies.IDs

	entryID, err := ids.NewEntryID()
	if err != nil {
		return operationPlan{}, err
	}

	plan.entryID = entryID

	for index := range plan.eventIDs {
		plan.eventIDs[index], err = ids.NewEventID()
		if err != nil {
			return operationPlan{}, err
		}
	}

	return plan, nil
}

func externalParams(
	command ProcessOperationCommand,
	transactionID identity.TransactionID,
	hash string,
	now time.Time,
) wager.ExternalParams {
	return wager.ExternalParams{
		ID:                             transactionID,
		ProviderID:                     command.ProviderID,
		ExternalTransactionID:          command.ExternalTransactionID,
		IdempotencyKey:                 command.IdempotencyKey,
		PayloadHash:                    hash,
		WalletID:                       command.WalletID,
		PlayerID:                       command.PlayerID,
		RoundID:                        command.RoundID,
		GameID:                         command.GameID,
		Kind:                           command.Kind,
		Money:                          command.Money,
		ReferenceExternalTransactionID: command.ReferenceExternalTransactionID,
		Now:                            now,
	}
}

func correlationOf(command ProcessOperationCommand, id identity.TransactionID) string {
	if command.CorrelationID != "" {
		return command.CorrelationID
	}

	return id.String()
}

// appliedAt devolve o instante da aplicação, nunca anterior ao do recebimento, mesmo que o
// relógio de parede tenha recuado.
func appliedAt(now time.Time, receivedAt time.Time) time.Time {
	if now.Before(receivedAt) {
		return receivedAt
	}

	return now
}
