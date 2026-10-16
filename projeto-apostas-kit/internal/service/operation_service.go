package service

import (
	"context"
	"errors"
	"fmt"

	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
)

// OperationServiceDependencies reúne o que OperationService precisa; todas são portas.
type OperationServiceDependencies struct {
	Transactions port.TxManager
	Wallets      port.WalletRepository
	Operations   port.TransactionRepository
	Ledger       port.LedgerRepository
	Outbox       port.OutboxRepository
	Clock        port.Clock
	IDs          port.IDGenerator
}

// OperationService processa as operações dos provedores; HTTP e SQS usam o mesmo caso de uso.
type OperationService struct {
	dependencies OperationServiceDependencies
}

// NewOperationService cria o serviço.
func NewOperationService(dependencies OperationServiceDependencies) *OperationService {
	return &OperationService{dependencies: dependencies}
}

// Process aplica uma operação de forma idempotente. A entrada é validada antes de tocar o
// banco; depois, uma única transação resolve replay ou conflito, bloqueia a carteira, aplica
// a regra do domínio e grava transação, ledger, saldo e eventos da outbox. Rejeições de
// negócio voltam como resultado persistido. Falhas transitórias voltam como erro
// (IsTransient) e podem ser repetidas com a mesma chave.
func (service *OperationService) Process(
	ctx context.Context,
	command ProcessOperationCommand,
) (OperationResult, error) {
	plan, err := service.plan(command)
	if err != nil {
		return OperationResult{}, err
	}

	var result OperationResult

	err = service.dependencies.Transactions.WithinTransaction(
		ctx,
		func(ctx context.Context) error {
			var err error
			result, err = service.processInsideTransaction(ctx, plan)

			return err
		},
	)
	if err != nil {
		return OperationResult{}, err
	}

	return result, nil
}

func (service *OperationService) processInsideTransaction(
	ctx context.Context,
	plan operationPlan,
) (OperationResult, error) {
	replay, found, err := service.findReplay(ctx, plan)
	if err != nil || found {
		return replay, err
	}

	return service.processUnderWalletLock(ctx, plan)
}

// processUnderWalletLock bloqueia só a carteira da operação; a consulta de replay é
// repetida sob o lock, pois outra requisição igual pode ter confirmado nesse intervalo.
func (service *OperationService) processUnderWalletLock(
	ctx context.Context,
	plan operationPlan,
) (OperationResult, error) {
	target, err := service.dependencies.Wallets.GetForUpdate(ctx, plan.command.WalletID)
	if err != nil {
		return OperationResult{}, translateWalletLookup(err, plan.command.WalletID)
	}

	if err := checkWalletMatches(target, plan.command); err != nil {
		return OperationResult{}, err
	}

	replay, found, err := service.findReplay(ctx, plan)
	if err != nil || found {
		return replay, err
	}

	return service.applyNew(ctx, plan, target)
}

func checkWalletMatches(target *wallet.Wallet, command ProcessOperationCommand) error {
	if target.PlayerID() != command.PlayerID {
		return fmt.Errorf("%w: wallet %s", ErrWalletPlayerMismatch, target.ID())
	}

	if target.Currency() != command.Money.Currency() {
		return fmt.Errorf(
			"%w: wallet %s, operation %s",
			ErrCurrencyMismatch,
			target.Currency(),
			command.Money.Currency(),
		)
	}

	return nil
}

func translateOperationInsert(err error, command ProcessOperationCommand) error {
	if errors.Is(err, port.ErrAlreadyExists) {
		return fmt.Errorf(
			"%w: %s/%s: %w",
			ErrIdempotencyConflict,
			command.ProviderID,
			command.ExternalTransactionID,
			err,
		)
	}

	return err
}

func isUnsupported(command ProcessOperationCommand) bool {
	return command.Kind.IsReversal() ||
		(command.Kind == wager.Win && command.ReferenceExternalTransactionID != "")
}
