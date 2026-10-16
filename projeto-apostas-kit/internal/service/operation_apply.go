package service

import (
	"context"
	"errors"
	"time"

	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
)

// movement aplica um débito ou crédito na carteira e devolve o lançamento dela.
type movement func(params wallet.MovementParams) (ledger.Entry, error)

// applyNew grava a transação PENDING e a conclui na mesma transação SQL, sem commit
// intermediário de aceite. A carteira já está bloqueada pelo chamador.
func (service *OperationService) applyNew(
	ctx context.Context,
	plan operationPlan,
	target *wallet.Wallet,
) (OperationResult, error) {
	err := service.dependencies.Operations.Insert(ctx, plan.transaction)
	if err != nil {
		return OperationResult{}, translateOperationInsert(err, plan.command)
	}

	at := appliedAt(service.dependencies.Clock.Now(), plan.receivedAt)

	switch plan.command.Kind {
	case wager.Bet:
		return service.applyMovement(ctx, plan, target, at, target.Debit)
	case wager.Win:
		return service.applyMovement(ctx, plan, target, at, target.Credit)
	default:
		return service.finishProcessed(ctx, plan, target, at, nil)
	}
}

func (service *OperationService) applyMovement(
	ctx context.Context,
	plan operationPlan,
	target *wallet.Wallet,
	at time.Time,
	move movement,
) (OperationResult, error) {
	expectedVersion := target.Version()

	entry, err := move(wallet.MovementParams{
		TransactionID: plan.transaction.ID(),
		EntryID:       plan.entryID,
		Amount:        plan.command.Money,
		Now:           at,
	})
	if errors.Is(err, wallet.ErrInsufficientFunds) {
		return service.reject(ctx, plan, at, wager.InsufficientFunds)
	}

	if err != nil {
		return OperationResult{}, err
	}

	if err := service.persistMovement(ctx, target, entry, expectedVersion); err != nil {
		return OperationResult{}, err
	}

	return service.finishProcessed(ctx, plan, target, at, &entry)
}

func (service *OperationService) persistMovement(
	ctx context.Context,
	target *wallet.Wallet,
	entry ledger.Entry,
	expectedVersion int64,
) error {
	if err := service.dependencies.Ledger.Append(ctx, entry); err != nil {
		return err
	}

	return service.dependencies.Wallets.Update(ctx, target, expectedVersion)
}

// finishProcessed marca a transação como PROCESSED com o saldo observado e enfileira os
// eventos; entry é nulo quando não houve movimentação (LOSS).
func (service *OperationService) finishProcessed(
	ctx context.Context,
	plan operationPlan,
	target *wallet.Wallet,
	at time.Time,
	entry *ledger.Entry,
) (OperationResult, error) {
	balance := target.Balance()

	err := plan.transaction.MarkProcessed(wager.ProcessedResult{Balance: balance}, at)
	if err != nil {
		return OperationResult{}, err
	}

	if err := service.dependencies.Operations.Update(ctx, plan.transaction); err != nil {
		return OperationResult{}, err
	}

	events, err := processedEvents(plan, entry, target.Version(), at)
	if err != nil {
		return OperationResult{}, err
	}

	if err := service.enqueue(ctx, events); err != nil {
		return OperationResult{}, err
	}

	return resultOf(plan.transaction, false), nil
}

func (service *OperationService) reject(
	ctx context.Context,
	plan operationPlan,
	at time.Time,
	code wager.FailureCode,
) (OperationResult, error) {
	if err := plan.transaction.Reject(code, at); err != nil {
		return OperationResult{}, err
	}

	if err := service.dependencies.Operations.Update(ctx, plan.transaction); err != nil {
		return OperationResult{}, err
	}

	events, err := rejectedEvents(plan, at)
	if err != nil {
		return OperationResult{}, err
	}

	if err := service.enqueue(ctx, events); err != nil {
		return OperationResult{}, err
	}

	return resultOf(plan.transaction, false), nil
}
