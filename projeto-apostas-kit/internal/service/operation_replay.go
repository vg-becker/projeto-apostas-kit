package service

import (
	"context"
	"errors"
	"fmt"

	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
)

// findReplay decide se a operação já existe. Mesma chave e mesmo hash: devolve o resultado
// persistido. Mesma chave com outro conteúdo, ou o mesmo ID externo sob outra chave:
// ErrIdempotencyConflict, pois uma operação não pode ser reaplicada com outra identidade.
func (service *OperationService) findReplay(
	ctx context.Context,
	plan operationPlan,
) (OperationResult, bool, error) {
	command := plan.command

	existing, err := service.dependencies.Operations.FindByIdempotencyKey(
		ctx,
		command.ProviderID,
		command.IdempotencyKey,
	)
	if err == nil {
		return replayOf(existing, plan)
	}

	if !errors.Is(err, port.ErrNotFound) {
		return OperationResult{}, false, err
	}

	return service.rejectReusedExternalID(ctx, plan)
}

func (service *OperationService) rejectReusedExternalID(
	ctx context.Context,
	plan operationPlan,
) (OperationResult, bool, error) {
	command := plan.command

	_, err := service.dependencies.Operations.FindByProviderExternalID(
		ctx,
		command.ProviderID,
		command.ExternalTransactionID,
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
		command.ProviderID,
		command.ExternalTransactionID,
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
			plan.command.IdempotencyKey,
		)
	}

	return resultOf(existing, true), true, nil
}
