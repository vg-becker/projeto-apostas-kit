package service

import (
	"context"

	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
)

// PendingResumer retoma as transações PENDING confirmadas e não concluídas, por exemplo
// quando o processo que as aceitou caiu antes de executá-las. Outra instância as assume
// assim que o lease da reivindicação anterior vence.
type PendingResumer struct {
	*operationEngine
}

// NewPendingResumer cria o resumer sobre as mesmas dependências das operações.
func NewPendingResumer(dependencies OperationServiceDependencies) *PendingResumer {
	return &PendingResumer{operationEngine: newOperationEngine(dependencies)}
}

// ResumeAbandoned reivindica até limit transações PENDING sem lease ativo e conclui cada uma
// com a mesma regra de Process. Os erros por item voltam agregados.
func (resumer *PendingResumer) ResumeAbandoned(
	ctx context.Context,
	limit int,
) (ResolutionSummary, error) {
	claimed, err := resumer.dependencies.Operations.ClaimPending(
		ctx,
		port.ClaimParams{
			Limit: limit,
			Now:   resumer.dependencies.Clock.Now(),
			Lease: resumer.policy.ClaimLease,
		},
	)
	if err != nil {
		return ResolutionSummary{}, err
	}

	return resumer.processClaimed(ctx, claimed, wager.Pending)
}
