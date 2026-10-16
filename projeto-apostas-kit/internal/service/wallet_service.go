package service

import (
	"context"
	"errors"
	"fmt"

	"apostas-go/internal/model/event"
	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
)

// WalletServiceDependencies reúne o que WalletService precisa; todas são portas do model.
type WalletServiceDependencies struct {
	Transactions port.TxManager
	Wallets      port.WalletRepository
	Operations   port.TransactionRepository
	Ledger       port.LedgerRepository
	Outbox       port.OutboxRepository
	Clock        port.Clock
	IDs          port.IDGenerator
	// Observer é opcional e recebe o resultado de cada reconciliação.
	Observer Observer
}

// WalletService executa os casos de uso de carteira: abertura, leitura e reconciliação.
type WalletService struct {
	dependencies WalletServiceDependencies
	observer     Observer
}

// NewWalletService cria o serviço.
func NewWalletService(dependencies WalletServiceDependencies) *WalletService {
	return &WalletService{
		dependencies: dependencies,
		observer:     observerOrNoop(dependencies.Observer),
	}
}

// openingPlan é tudo que a abertura grava, montado antes de abrir a transação SQL.
type openingPlan struct {
	opened      wallet.Opened
	transaction *wager.Transaction
	events      []event.Event
}

// OpenWallet abre a carteira. Com saldo inicial positivo, grava no mesmo commit a
// transação OPENING, o lançamento de crédito e os eventos da outbox; com saldo zero,
// grava só a carteira. Um jogador e moeda já com carteira devolvem ErrWalletExists.
func (service *WalletService) OpenWallet(
	ctx context.Context,
	command OpenWalletCommand,
) (*wallet.Wallet, error) {
	plan, err := service.buildOpening(command)
	if err != nil {
		return nil, err
	}

	err = service.dependencies.Transactions.WithinTransaction(
		ctx,
		func(ctx context.Context) error { return service.persistOpening(ctx, plan) },
	)
	if err != nil {
		return nil, err
	}

	return plan.opened.Wallet, nil
}

// GetWallet lê a carteira; inexistente devolve ErrWalletNotFound.
func (service *WalletService) GetWallet(
	ctx context.Context,
	id identity.WalletID,
) (*wallet.Wallet, error) {
	found, err := service.dependencies.Wallets.Get(ctx, id)
	if err != nil {
		return nil, translateWalletLookup(err, id)
	}

	return found, nil
}

func (service *WalletService) persistOpening(ctx context.Context, plan openingPlan) error {
	if err := service.ensureWalletAbsent(ctx, plan.opened.Wallet); err != nil {
		return err
	}

	if err := service.dependencies.Wallets.Create(ctx, plan.opened.Wallet); err != nil {
		return translateWalletCreation(err, plan.opened.Wallet)
	}

	if plan.opened.OpeningEntry == nil {
		return nil
	}

	return service.persistOpeningRecords(ctx, plan)
}

func (service *WalletService) persistOpeningRecords(ctx context.Context, plan openingPlan) error {
	if err := service.dependencies.Operations.Insert(ctx, plan.transaction); err != nil {
		return err
	}

	if err := service.dependencies.Ledger.Append(ctx, *plan.opened.OpeningEntry); err != nil {
		return err
	}

	for _, pending := range plan.events {
		if err := service.dependencies.Outbox.Add(ctx, pending); err != nil {
			return err
		}
	}

	return nil
}

func (service *WalletService) ensureWalletAbsent(ctx context.Context, target *wallet.Wallet) error {
	_, err := service.dependencies.Wallets.FindByPlayerAndCurrency(
		ctx,
		target.PlayerID(),
		target.Currency(),
	)
	if err == nil {
		return fmt.Errorf("%w: player %s", ErrWalletExists, target.PlayerID())
	}

	if errors.Is(err, port.ErrNotFound) {
		return nil
	}

	return err
}
