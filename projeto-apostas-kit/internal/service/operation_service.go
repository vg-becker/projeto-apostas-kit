package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wallet"
)

// OperationServiceDependencies reúne o que os casos de uso de operação precisam; todas
// são portas do model. ReferencePolicy é opcional (campos zerados assumem os padrões).
type OperationServiceDependencies struct {
	Transactions    port.TxManager
	Wallets         port.WalletRepository
	Operations      port.TransactionRepository
	Ledger          port.LedgerRepository
	Outbox          port.OutboxRepository
	Clock           port.Clock
	IDs             port.IDGenerator
	ReferencePolicy ReferencePolicy
	// Observer é opcional e recebe o resultado de cada processamento.
	Observer Observer
}

// operationEngine concentra a regra de aplicação de uma operação sobre uma carteira já
// bloqueada; OperationService e ReferenceResolver a compartilham.
type operationEngine struct {
	dependencies OperationServiceDependencies
	policy       ReferencePolicy
	observer     Observer
}

func newOperationEngine(dependencies OperationServiceDependencies) *operationEngine {
	return &operationEngine{
		dependencies: dependencies,
		policy:       dependencies.ReferencePolicy.withDefaults(),
		observer:     observerOrNoop(dependencies.Observer),
	}
}

// OperationService processa as operações dos provedores; HTTP e SQS usam o mesmo caso de uso.
type OperationService struct {
	*operationEngine
}

// NewOperationService cria o serviço.
func NewOperationService(dependencies OperationServiceDependencies) *OperationService {
	return &OperationService{operationEngine: newOperationEngine(dependencies)}
}

// Process aplica uma operação de forma idempotente. A entrada é validada antes de tocar o
// banco; depois, uma única transação resolve replay ou conflito, bloqueia a carteira, aplica
// a regra do domínio e grava transação, ledger, saldo e eventos da outbox. Rejeições de
// negócio voltam como resultado persistido, e uma referência ainda indisponível volta como
// PENDING_REFERENCE. Falhas transitórias voltam como erro (IsTransient) e podem ser
// repetidas com a mesma chave.
func (service *OperationService) Process(
	ctx context.Context,
	command ProcessOperationCommand,
) (OperationResult, error) {
	started := time.Now()
	result, err := service.process(ctx, command)

	observeCommand(service.observer, SourceRequest, command, started, result, err)

	return result, err
}

// process é Process sem a observação: o tratamento de mensagens a usa dentro da transação da
// inbox e observa só depois do commit.
func (service *OperationService) process(
	ctx context.Context,
	command ProcessOperationCommand,
) (OperationResult, error) {
	plan, err := service.planFromCommand(command)
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

func (engine *operationEngine) processInsideTransaction(
	ctx context.Context,
	plan operationPlan,
) (OperationResult, error) {
	replay, found, err := engine.findReplayByKey(ctx, plan)
	if err != nil || found {
		return replay, err
	}

	return engine.processUnderWalletLock(ctx, plan)
}

// processUnderWalletLock bloqueia só a carteira da operação; a consulta de replay é
// repetida sob o lock, pois outra requisição igual pode ter confirmado nesse intervalo.
func (engine *operationEngine) processUnderWalletLock(
	ctx context.Context,
	plan operationPlan,
) (OperationResult, error) {
	walletID := plan.transaction.WalletID()

	target, err := engine.dependencies.Wallets.GetForUpdate(ctx, walletID)
	if err != nil {
		return OperationResult{}, translateWalletLookup(err, walletID)
	}

	if err := checkWalletMatches(target, plan); err != nil {
		return OperationResult{}, err
	}

	replay, found, err := engine.findReplay(ctx, plan)
	if err != nil || found {
		return replay, err
	}

	return engine.applyNew(ctx, plan, target)
}

func checkWalletMatches(target *wallet.Wallet, plan operationPlan) error {
	transaction := plan.transaction

	if target.PlayerID() != transaction.PlayerID() {
		return fmt.Errorf("%w: wallet %s", ErrWalletPlayerMismatch, target.ID())
	}

	if target.Currency() != transaction.Money().Currency() {
		return fmt.Errorf(
			"%w: wallet %s, operation %s",
			ErrCurrencyMismatch,
			target.Currency(),
			transaction.Money().Currency(),
		)
	}

	return nil
}

func translateOperationInsert(err error, plan operationPlan) error {
	if errors.Is(err, port.ErrAlreadyExists) {
		return fmt.Errorf(
			"%w: %s/%s: %w",
			ErrIdempotencyConflict,
			plan.transaction.ProviderID(),
			plan.transaction.ExternalTransactionID(),
			err,
		)
	}

	return err
}
