package service

import (
	"context"
	"errors"

	"apostas-go/internal/model/wager"
)

// ResolutionSummary resume um ciclo de retomada (resolver de referências ou resumer).
type ResolutionSummary struct {
	// Claimed é o total reivindicado por este ciclo.
	Claimed int
	// Finished são as operações que terminaram PROCESSED, REJECTED ou FAILED.
	Finished int
	// Waiting são as que continuam esperando, com nova tentativa agendada.
	Waiting int
}

func (summary *ResolutionSummary) record(result OperationResult, concluded bool) {
	switch {
	case !concluded:
	case result.Status == wager.PendingReference:
		summary.Waiting++
	default:
		summary.Finished++
	}
}

// processClaimed conclui cada transação reivindicada em uma transação SQL própria. Um erro
// em uma delas não impede as demais: os erros voltam agregados, e uma falha transitória só
// deixa o lease expirar para outra instância tentar.
func (engine *operationEngine) processClaimed(
	ctx context.Context,
	claimed []*wager.Transaction,
	expected wager.Status,
) (ResolutionSummary, error) {
	summary := ResolutionSummary{Claimed: len(claimed)}

	var failures []error

	for _, pending := range claimed {
		result, concluded, err := engine.completeClaimed(ctx, pending, expected)
		if err != nil {
			failures = append(failures, err)

			continue
		}

		summary.record(result, concluded)
	}

	return summary, errors.Join(failures...)
}

func (engine *operationEngine) completeClaimed(
	ctx context.Context,
	pending *wager.Transaction,
	expected wager.Status,
) (OperationResult, bool, error) {
	var (
		result    OperationResult
		concluded bool
	)

	err := engine.dependencies.Transactions.WithinTransaction(
		ctx,
		func(ctx context.Context) error {
			var err error
			result, concluded, err = engine.completeInsideTransaction(ctx, pending, expected)

			return err
		},
	)

	return result, concluded, err
}

// completeInsideTransaction conclui a transação com a carteira bloqueada; se outra instância
// já a levou adiante (o estado não é mais o esperado), não faz nada. Uma divergência
// permanente entre a transação e a carteira vira FAILED, para auditoria.
func (engine *operationEngine) completeInsideTransaction(
	ctx context.Context,
	pending *wager.Transaction,
	expected wager.Status,
) (OperationResult, bool, error) {
	target, err := engine.dependencies.Wallets.GetForUpdate(ctx, pending.WalletID())
	if err != nil {
		return OperationResult{}, false, err
	}

	plan, stillExpected, err := engine.reloadPlan(ctx, pending, expected)
	if err != nil || !stillExpected {
		return OperationResult{}, false, err
	}

	if err := checkWalletMatches(target, plan); err != nil {
		result, failErr := engine.failPermanently(ctx, plan)

		return result, failErr == nil, failErr
	}

	result, err := engine.settle(ctx, plan, target)

	return result, err == nil, err
}

// reloadPlan recarrega a transação sob o lock da carteira e informa se ela ainda está no
// estado esperado.
func (engine *operationEngine) reloadPlan(
	ctx context.Context,
	pending *wager.Transaction,
	expected wager.Status,
) (operationPlan, bool, error) {
	current, err := engine.dependencies.Operations.FindByID(ctx, pending.ID())
	if err != nil {
		return operationPlan{}, false, err
	}

	if current.Status() != expected {
		return operationPlan{}, false, nil
	}

	plan, err := engine.planFromTransaction(current)

	return plan, err == nil, err
}

// failPermanently registra uma falha permanente de infraestrutura para auditoria, sem evento.
func (engine *operationEngine) failPermanently(
	ctx context.Context,
	plan operationPlan,
) (OperationResult, error) {
	at := appliedAt(engine.dependencies.Clock.Now(), plan.transaction.Snapshot().UpdatedAt)

	if err := plan.transaction.Fail(at); err != nil {
		return OperationResult{}, err
	}

	if err := engine.dependencies.Operations.Update(ctx, plan.transaction); err != nil {
		return OperationResult{}, err
	}

	return resultOf(plan.transaction, false), nil
}
