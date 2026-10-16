package fxmodules

import (
	"go.uber.org/fx"

	"apostas-go/internal/model/port"
	"apostas-go/internal/platform/sqsclient"
	"apostas-go/internal/repository/postgres"
	"apostas-go/internal/service"
)

// consumerName identifica este consumidor na inbox: (consumerName, messageId) é único.
const consumerName = "wager-transactions"

// ServiceModule oferece os casos de uso. As dependências dos serviços são as portas do model,
// ligadas aqui às implementações concretas: este é o único lugar que as conhece.
var ServiceModule = fx.Module(
	"service",
	fx.Provide(
		newOperationDependencies,
		service.NewOperationService,
		service.NewReferenceResolver,
		service.NewPendingResumer,
		newWalletService,
		newQueryService,
		newMessageHandler,
		newEventPublisher,
		newOutboxPublisher,
	),
)

type operationInputs struct {
	fx.In

	Transactions *postgres.TransactionManager
	Wallets      *postgres.WalletRepository
	Operations   *postgres.TransactionRepository
	Ledger       *postgres.LedgerRepository
	Outbox       *postgres.OutboxRepository
	Clock        port.Clock
	IDs          port.IDGenerator
}

func newOperationDependencies(inputs operationInputs) service.OperationServiceDependencies {
	return service.OperationServiceDependencies{
		Transactions: inputs.Transactions,
		Wallets:      inputs.Wallets,
		Operations:   inputs.Operations,
		Ledger:       inputs.Ledger,
		Outbox:       inputs.Outbox,
		Clock:        inputs.Clock,
		IDs:          inputs.IDs,
	}
}

func newWalletService(shared service.OperationServiceDependencies) *service.WalletService {
	return service.NewWalletService(service.WalletServiceDependencies{
		Transactions: shared.Transactions,
		Wallets:      shared.Wallets,
		Operations:   shared.Operations,
		Ledger:       shared.Ledger,
		Outbox:       shared.Outbox,
		Clock:        shared.Clock,
		IDs:          shared.IDs,
	})
}

func newQueryService(shared service.OperationServiceDependencies) *service.QueryService {
	return service.NewQueryService(service.QueryServiceDependencies{
		Wallets:    shared.Wallets,
		Operations: shared.Operations,
		Ledger:     shared.Ledger,
	})
}

func newMessageHandler(
	shared service.OperationServiceDependencies,
	operations *service.OperationService,
	inbox *postgres.InboxRepository,
) *service.MessageHandler {
	return service.NewMessageHandler(service.MessageHandlerDependencies{
		Transactions: shared.Transactions,
		Inbox:        inbox,
		Operations:   operations,
		Clock:        shared.Clock,
		ConsumerName: consumerName,
	})
}

func newEventPublisher(client *sqsclient.Client, queues sqsclient.Queues) port.EventPublisher {
	return sqsclient.NewEventPublisher(client, queues.Events)
}

func newOutboxPublisher(
	shared service.OperationServiceDependencies,
	destination port.EventPublisher,
) *service.OutboxPublisher {
	return service.NewOutboxPublisher(service.OutboxPublisherDependencies{
		Outbox:    shared.Outbox,
		Publisher: destination,
		Clock:     shared.Clock,
	})
}
