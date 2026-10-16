package service

import (
	"context"
	"errors"
	"time"

	"apostas-go/internal/model/event"
	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
)

// movement aplica um débito ou crédito na carteira e devolve o lançamento dela.
type movement func(params wallet.MovementParams) (ledger.Entry, error)

// movementSpec descreve como uma operação movimenta a carteira: a função, o código usado
// quando o débito não tem saldo e a referência interna resolvida, se houver.
type movementSpec struct {
	move         movement
	insufficient wager.FailureCode
	resolved     identity.TransactionID
}

// applyNew grava a transação PENDING e a conclui na mesma transação SQL, sem commit
// intermediário de aceite. A carteira já está bloqueada pelo chamador.
func (engine *operationEngine) applyNew(
	ctx context.Context,
	plan operationPlan,
	target *wallet.Wallet,
) (OperationResult, error) {
	err := engine.dependencies.Operations.Insert(ctx, plan.transaction)
	if err != nil {
		return OperationResult{}, translateOperationInsert(err, plan)
	}

	return engine.settle(ctx, plan, target)
}

// settle conclui uma transação PENDING ou PENDING_REFERENCE com a carteira já bloqueada:
// aplica a regra do tipo, ou resolve a referência quando há uma.
func (engine *operationEngine) settle(
	ctx context.Context,
	plan operationPlan,
	target *wallet.Wallet,
) (OperationResult, error) {
	at := appliedAt(engine.dependencies.Clock.Now(), plan.transaction.Snapshot().UpdatedAt)

	if plan.transaction.ReferenceExternalTransactionID() != "" {
		return engine.settleWithReference(ctx, plan, target, at)
	}

	switch plan.transaction.Kind() {
	case wager.Bet:
		spec := movementSpec{move: target.Debit, insufficient: wager.InsufficientFunds}

		return engine.applyMovement(ctx, plan, target, at, spec)
	case wager.Win:
		return engine.applyMovement(ctx, plan, target, at, movementSpec{move: target.Credit})
	default:
		return engine.finishProcessed(ctx, plan, target, at, nil, "")
	}
}

func (engine *operationEngine) applyMovement(
	ctx context.Context,
	plan operationPlan,
	target *wallet.Wallet,
	at time.Time,
	spec movementSpec,
) (OperationResult, error) {
	expectedVersion := target.Version()

	entry, err := spec.move(wallet.MovementParams{
		TransactionID: plan.transaction.ID(),
		EntryID:       plan.entryID,
		Amount:        plan.transaction.Money(),
		Now:           at,
	})
	if errors.Is(err, wallet.ErrInsufficientFunds) {
		return engine.reject(ctx, plan, at, spec.insufficient)
	}

	if err != nil {
		return OperationResult{}, err
	}

	if err := engine.persistMovement(ctx, target, entry, expectedVersion); err != nil {
		return OperationResult{}, err
	}

	return engine.finishProcessed(ctx, plan, target, at, &entry, spec.resolved)
}

func (engine *operationEngine) persistMovement(
	ctx context.Context,
	target *wallet.Wallet,
	entry ledger.Entry,
	expectedVersion int64,
) error {
	if err := engine.dependencies.Ledger.Append(ctx, entry); err != nil {
		return err
	}

	return engine.dependencies.Wallets.Update(ctx, target, expectedVersion)
}

// finishProcessed marca a transação como PROCESSED com o saldo observado e enfileira os
// eventos; entry é nulo quando não houve movimentação (LOSS).
func (engine *operationEngine) finishProcessed(
	ctx context.Context,
	plan operationPlan,
	target *wallet.Wallet,
	at time.Time,
	entry *ledger.Entry,
	resolved identity.TransactionID,
) (OperationResult, error) {
	err := plan.transaction.MarkProcessed(
		wager.ProcessedResult{Balance: target.Balance(), ResolvedReferenceID: resolved},
		at,
	)
	if err != nil {
		return OperationResult{}, err
	}

	if err := engine.dependencies.Operations.Update(ctx, plan.transaction); err != nil {
		return OperationResult{}, err
	}

	events, err := processedEvents(plan, entry, target.Version(), at)
	if err != nil {
		return OperationResult{}, err
	}

	return engine.concluded(ctx, plan, events)
}

func (engine *operationEngine) reject(
	ctx context.Context,
	plan operationPlan,
	at time.Time,
	code wager.FailureCode,
) (OperationResult, error) {
	if err := plan.transaction.Reject(code, at); err != nil {
		return OperationResult{}, err
	}

	if err := engine.dependencies.Operations.Update(ctx, plan.transaction); err != nil {
		return OperationResult{}, err
	}

	events, err := rejectedEvents(plan, at)
	if err != nil {
		return OperationResult{}, err
	}

	return engine.concluded(ctx, plan, events)
}

func (engine *operationEngine) concluded(
	ctx context.Context,
	plan operationPlan,
	events []event.Event,
) (OperationResult, error) {
	if err := engine.enqueue(ctx, events); err != nil {
		return OperationResult{}, err
	}

	return resultOf(plan.transaction, false), nil
}
