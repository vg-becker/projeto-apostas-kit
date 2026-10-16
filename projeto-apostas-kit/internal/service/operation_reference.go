package service

import (
	"context"
	"errors"
	"time"

	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
)

// settleWithReference resolve a referência de um WIN, REFUND ou ROLLBACK. A referência
// ausente ou ainda pendente leva à espera; uma que terminou sem sucesso, ou que diverge, à
// rejeição; a válida, ao movimento.
func (engine *operationEngine) settleWithReference(
	ctx context.Context,
	plan operationPlan,
	target *wallet.Wallet,
	at time.Time,
) (OperationResult, error) {
	transaction := plan.transaction

	reference, err := engine.dependencies.Operations.FindByProviderExternalID(
		ctx,
		transaction.ProviderID(),
		transaction.ReferenceExternalTransactionID(),
	)
	if errors.Is(err, port.ErrNotFound) {
		return engine.awaitReference(ctx, plan, at, wager.ReferenceNotFound)
	}

	if err != nil {
		return OperationResult{}, err
	}

	return engine.decideWithReference(ctx, plan, target, at, reference)
}

func (engine *operationEngine) decideWithReference(
	ctx context.Context,
	plan operationPlan,
	target *wallet.Wallet,
	at time.Time,
	reference *wager.Transaction,
) (OperationResult, error) {
	err := engine.validateReference(ctx, plan.transaction, reference)

	var rejection *wager.Rejection

	switch {
	case err == nil:
		return engine.applyReferenced(ctx, plan, target, at, reference)
	case errors.Is(err, wager.ErrReferencePending):
		return engine.awaitReference(ctx, plan, at, wager.ReferenceNotProcessed)
	case errors.As(err, &rejection):
		return engine.reject(ctx, plan, at, rejection.Code)
	default:
		return OperationResult{}, err
	}
}

func (engine *operationEngine) validateReference(
	ctx context.Context,
	transaction *wager.Transaction,
	reference *wager.Transaction,
) error {
	if transaction.Kind() == wager.Win {
		return wager.ValidateWinReference(transaction, reference)
	}

	alreadyReversed, err := engine.dependencies.Operations.HasProcessedReversal(
		ctx,
		reference.ID(),
	)
	if err != nil {
		return err
	}

	return wager.ValidateReversal(transaction, reference, alreadyReversed)
}

// applyReferenced aplica o movimento de uma operação com referência válida: o WIN credita;
// a reversão credita ou debita conforme o tipo da referência, e um débito sem saldo usa um
// código próprio, distinto do de uma aposta sem saldo.
func (engine *operationEngine) applyReferenced(
	ctx context.Context,
	plan operationPlan,
	target *wallet.Wallet,
	at time.Time,
	reference *wager.Transaction,
) (OperationResult, error) {
	spec := movementSpec{
		move:         target.Credit,
		insufficient: wager.ReversalInsufficientFunds,
		resolved:     reference.ID(),
	}

	if plan.transaction.Kind().IsReversal() {
		direction, err := wager.ReversalDirection(reference.Kind())
		if err != nil {
			return OperationResult{}, err
		}

		if direction == ledger.Debit {
			spec.move = target.Debit
		}
	}

	return engine.applyMovement(ctx, plan, target, at, spec)
}

// awaitReference registra a espera pela referência e agenda a próxima tentativa, com um
// atraso que dobra a cada vez. Esgotado o limite de tentativas ou o TTL, a operação é
// rejeitada com expiredCode: REFERENCE_NOT_FOUND se a referência nunca apareceu, ou
// REFERENCE_NOT_PROCESSED se ela existe mas nunca terminou.
func (engine *operationEngine) awaitReference(
	ctx context.Context,
	plan operationPlan,
	at time.Time,
	expiredCode wager.FailureCode,
) (OperationResult, error) {
	transaction := plan.transaction

	attempts, err := engine.dependencies.Operations.ReferenceAttempts(ctx, transaction.ID())
	if err != nil {
		return OperationResult{}, err
	}

	createdAt := transaction.Snapshot().CreatedAt
	if engine.policy.expired(createdAt, attempts, at) {
		return engine.reject(ctx, plan, at, expiredCode)
	}

	if err := engine.scheduleWait(ctx, plan, at, attempts); err != nil {
		return OperationResult{}, err
	}

	return resultOf(transaction, false), nil
}

// scheduleWait garante o estado PENDING_REFERENCE e agenda a próxima tentativa.
func (engine *operationEngine) scheduleWait(
	ctx context.Context,
	plan operationPlan,
	at time.Time,
	attempts int,
) error {
	if plan.transaction.Status() == wager.Pending {
		if err := engine.registerWait(ctx, plan, at); err != nil {
			return err
		}
	}

	_, err := engine.dependencies.Operations.ScheduleReferenceRetry(
		ctx,
		plan.transaction.ID(),
		at.Add(engine.policy.backoff(attempts)),
	)

	return err
}

// registerWait move a transação para PENDING_REFERENCE e emite o evento, uma única vez.
func (engine *operationEngine) registerWait(
	ctx context.Context,
	plan operationPlan,
	at time.Time,
) error {
	if err := plan.transaction.AwaitReference(at); err != nil {
		return err
	}

	if err := engine.dependencies.Operations.Update(ctx, plan.transaction); err != nil {
		return err
	}

	events, err := pendingReferenceEvents(plan, at)
	if err != nil {
		return err
	}

	return engine.enqueue(ctx, events)
}
