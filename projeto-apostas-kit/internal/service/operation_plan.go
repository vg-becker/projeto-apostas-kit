package service

import (
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/wager"
)

// operationPlan é tudo que a operação precisa para ser aplicada: a transação (nova ou já
// persistida), o hash do conteúdo e os identificadores dos registros que ela pode gerar.
type operationPlan struct {
	transaction *wager.Transaction
	hash        string
	entryID     identity.EntryID
	eventIDs    [2]identity.EventID
}

// planFromCommand valida a entrada e prepara a transação PENDING. Nada aqui toca o banco:
// entrada inválida não deixa rastro.
func (engine *operationEngine) planFromCommand(
	command ProcessOperationCommand,
) (operationPlan, error) {
	hash, err := CanonicalHash(command)
	if err != nil {
		return operationPlan{}, err
	}

	transactionID, err := engine.dependencies.IDs.NewTransactionID()
	if err != nil {
		return operationPlan{}, err
	}

	transaction, err := wager.NewExternal(
		externalParams(command, transactionID, hash, engine.dependencies.Clock.Now()),
	)
	if err != nil {
		return operationPlan{}, err
	}

	return engine.newPlan(transaction, hash)
}

// planFromTransaction prepara a retomada de uma transação já persistida.
func (engine *operationEngine) planFromTransaction(
	transaction *wager.Transaction,
) (operationPlan, error) {
	return engine.newPlan(transaction, transaction.Snapshot().PayloadHash)
}

func (engine *operationEngine) newPlan(
	transaction *wager.Transaction,
	hash string,
) (operationPlan, error) {
	ids := engine.dependencies.IDs
	plan := operationPlan{transaction: transaction, hash: hash}

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

// correlationID devolve o identificador de correlação original ou, na falta dele, o ID da
// transação.
func (plan operationPlan) correlationID() string {
	if correlation := plan.transaction.CorrelationID(); correlation != "" {
		return correlation
	}

	return plan.transaction.ID().String()
}

func (plan operationPlan) causationID() string {
	return plan.transaction.CausationID()
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
		CorrelationID:                  command.CorrelationID,
		CausationID:                    command.CausationID,
		Now:                            now,
	}
}

// appliedAt devolve o instante da aplicação, nunca anterior à última atualização da
// transação, mesmo que o relógio de parede tenha recuado.
func appliedAt(now time.Time, lastUpdate time.Time) time.Time {
	if now.Before(lastUpdate) {
		return lastUpdate
	}

	return now
}
