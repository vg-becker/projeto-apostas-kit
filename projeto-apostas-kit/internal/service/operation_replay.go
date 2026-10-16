package service

import (
	"context"
	"errors"
	"fmt"

	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
)

// findReplayByKey procura a operação pela chave de idempotência. Mesma chave e mesmo hash:
// devolve o resultado persistido. Mesma chave com outro conteúdo: ErrIdempotencyConflict.
// Sem lock da carteira, só esta consulta é segura: o ID externo é verificado depois, sob o
// lock, onde as duas leituras enxergam o mesmo estado confirmado.
func (engine *operationEngine) findReplayByKey(
	ctx context.Context,
	plan operationPlan,
) (OperationResult, bool, error) {
	transaction := plan.transaction

	existing, err := engine.dependencies.Operations.FindByIdempotencyKey(
		ctx,
		transaction.ProviderID(),
		transaction.Snapshot().IdempotencyKey,
	)
	if errors.Is(err, port.ErrNotFound) {
		return OperationResult{}, false, nil
	}

	if err != nil {
		return OperationResult{}, false, err
	}

	return replayOf(existing, plan)
}

// findReplay acrescenta à consulta por chave a regra de que o mesmo ID externo não pode ser
// reaplicado sob outra chave (ErrIdempotencyConflict). Deve rodar com a carteira bloqueada:
// as operações da mesma carteira são serializadas, então não há confirmação concorrente
// entre as duas leituras.
func (engine *operationEngine) findReplay(
	ctx context.Context,
	plan operationPlan,
) (OperationResult, bool, error) {
	result, found, err := engine.findReplayByKey(ctx, plan)
	if err != nil || found {
		return result, found, err
	}

	return engine.rejectReusedExternalID(ctx, plan)
}

func (engine *operationEngine) rejectReusedExternalID(
	ctx context.Context,
	plan operationPlan,
) (OperationResult, bool, error) {
	transaction := plan.transaction

	_, err := engine.dependencies.Operations.FindByProviderExternalID(
		ctx,
		transaction.ProviderID(),
		transaction.ExternalTransactionID(),
	)
	if errors.Is(err, port.ErrNotFound) {
		return OperationResult{}, false, nil
	}

	if err != nil {
		return OperationResult{}, false, err
	}

	return OperationResult{}, false, fmt.Errorf(
		"%w: %s/%s already exists under another idempotency key",
		ErrIdempotencyConflict,
		transaction.ProviderID(),
		transaction.ExternalTransactionID(),
	)
}

func replayOf(
	existing *wager.Transaction,
	plan operationPlan,
) (OperationResult, bool, error) {
	if existing.Snapshot().PayloadHash != plan.hash {
		return OperationResult{}, false, fmt.Errorf(
			"%w: key %s",
			ErrIdempotencyConflict,
			plan.transaction.Snapshot().IdempotencyKey,
		)
	}

	return resultOf(existing, true), true, nil
}
