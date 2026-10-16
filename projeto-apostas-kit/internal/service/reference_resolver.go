package service

import (
	"context"
	"errors"

	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
)

// ReferenceResolver retoma as operações em PENDING_REFERENCE cujo retry venceu. Pode rodar
// em várias instâncias ao mesmo tempo: cada pendência é reivindicada com lease.
type ReferenceResolver struct {
	*operationEngine
}

// NewReferenceResolver cria o resolver sobre as mesmas dependências das operações.
func NewReferenceResolver(dependencies OperationServiceDependencies) *ReferenceResolver {
	return &ReferenceResolver{operationEngine: newOperationEngine(dependencies)}
}

// ResolutionSummary resume um ciclo do resolver.
type ResolutionSummary struct {
	// Claimed é o total reivindicado por este ciclo.
	Claimed int
	// Finished são as operações que terminaram PROCESSED ou REJECTED.
	Finished int
	// Waiting são as que continuam esperando, com nova tentativa agendada.
	Waiting int
}

// ResolveDue reivindica até limit pendências vencidas e tenta resolver cada uma em uma
// transação própria. Um erro em uma pendência não impede as demais: os erros voltam
// agregados, e uma falha transitória só deixa o lease expirar para outra instância.
func (resolver *ReferenceResolver) ResolveDue(
	ctx context.Context,
	limit int,
) (ResolutionSummary, error) {
	claimed, err := resolver.claimDue(ctx, limit)
	if err != nil {
		return ResolutionSummary{}, err
	}

	summary := ResolutionSummary{Claimed: len(claimed)}

	var failures []error

	for _, pending := range claimed {
		result, found, err := resolver.resolveOne(ctx, pending)
		if err != nil {
			failures = append(failures, err)

			continue
		}

		summary.record(result, found)
	}

	return summary, errors.Join(failures...)
}

func (resolver *ReferenceResolver) claimDue(
	ctx context.Context,
	limit int,
) ([]*wager.Transaction, error) {
	return resolver.dependencies.Operations.ClaimDuePendingReferences(
		ctx,
		port.ClaimParams{
			Limit: limit,
			Now:   resolver.dependencies.Clock.Now(),
			Lease: resolver.policy.ClaimLease,
		},
	)
}

func (summary *ResolutionSummary) record(result OperationResult, resolved bool) {
	switch {
	case !resolved:
	case result.Status == wager.PendingReference:
		summary.Waiting++
	default:
		summary.Finished++
	}
}

func (resolver *ReferenceResolver) resolveOne(
	ctx context.Context,
	pending *wager.Transaction,
) (OperationResult, bool, error) {
	var (
		result   OperationResult
		resolved bool
	)

	err := resolver.dependencies.Transactions.WithinTransaction(
		ctx,
		func(ctx context.Context) error {
			var err error
			result, resolved, err = resolver.resolveInsideTransaction(ctx, pending)

			return err
		},
	)

	return result, resolved, err
}

// resolveInsideTransaction trava a carteira, recarrega a transação e a conclui; se outra
// instância já a concluiu, não faz nada (resolved falso).
func (resolver *ReferenceResolver) resolveInsideTransaction(
	ctx context.Context,
	pending *wager.Transaction,
) (OperationResult, bool, error) {
	target, err := resolver.dependencies.Wallets.GetForUpdate(ctx, pending.WalletID())
	if err != nil {
		return OperationResult{}, false, err
	}

	current, err := resolver.dependencies.Operations.FindByID(ctx, pending.ID())
	if err != nil {
		return OperationResult{}, false, err
	}

	if current.Status() != wager.PendingReference {
		return OperationResult{}, false, nil
	}

	plan, err := resolver.planFromTransaction(current)
	if err != nil {
		return OperationResult{}, false, err
	}

	result, err := resolver.settle(ctx, plan, target)

	return result, err == nil, err
}
