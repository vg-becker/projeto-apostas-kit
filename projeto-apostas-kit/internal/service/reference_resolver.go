package service

import (
	"context"

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

// ResolveDue reivindica até limit pendências vencidas e tenta resolver cada uma em uma
// transação própria. Os erros por item voltam agregados, sem interromper as demais.
func (resolver *ReferenceResolver) ResolveDue(
	ctx context.Context,
	limit int,
) (ResolutionSummary, error) {
	claimed, err := resolver.dependencies.Operations.ClaimDuePendingReferences(
		ctx,
		port.ClaimParams{
			Limit: limit,
			Now:   resolver.dependencies.Clock.Now(),
			Lease: resolver.policy.ClaimLease,
		},
	)
	if err != nil {
		return ResolutionSummary{}, err
	}

	return resolver.processClaimed(ctx, claimed, wager.PendingReference)
}
