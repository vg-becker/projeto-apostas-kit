package service

import (
	"errors"
	"fmt"
	"time"

	"apostas-go/internal/model/event"
	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
)

// openingIdentifiers são os IDs de tudo que uma abertura pode gravar.
type openingIdentifiers struct {
	walletID         identity.WalletID
	transactionID    identity.TransactionID
	entryID          identity.EntryID
	processedEventID identity.EventID
	balanceEventID   identity.EventID
}

func (service *WalletService) buildOpening(command OpenWalletCommand) (openingPlan, error) {
	identifiers, err := service.newOpeningIdentifiers()
	if err != nil {
		return openingPlan{}, err
	}

	now := service.dependencies.Clock.Now()

	opened, err := wallet.Open(wallet.OpenParams{
		ID:                   identifiers.walletID,
		PlayerID:             command.PlayerID,
		InitialBalance:       command.InitialBalance,
		OpeningTransactionID: identifiers.transactionID,
		OpeningEntryID:       identifiers.entryID,
		Now:                  now,
	})
	if err != nil {
		return openingPlan{}, err
	}

	if opened.OpeningEntry == nil {
		return openingPlan{opened: opened}, nil
	}

	return buildOpeningRecords(command, identifiers, opened, now)
}

func buildOpeningRecords(
	command OpenWalletCommand,
	identifiers openingIdentifiers,
	opened wallet.Opened,
	now time.Time,
) (openingPlan, error) {
	transaction, err := wager.NewOpening(wager.OpeningParams{
		ID:       identifiers.transactionID,
		WalletID: opened.Wallet.ID(),
		PlayerID: opened.Wallet.PlayerID(),
		Money:    command.InitialBalance,
		Now:      now,
	})
	if err != nil {
		return openingPlan{}, err
	}

	events, err := buildOpeningEvents(command, identifiers, opened, transaction, now)
	if err != nil {
		return openingPlan{}, err
	}

	return openingPlan{opened: opened, transaction: transaction, events: events}, nil
}

func buildOpeningEvents(
	command OpenWalletCommand,
	identifiers openingIdentifiers,
	opened wallet.Opened,
	transaction *wager.Transaction,
	now time.Time,
) ([]event.Event, error) {
	correlationID := command.CorrelationID
	if correlationID == "" {
		correlationID = transaction.ID().String()
	}

	processed, err := event.NewWagerTransactionProcessed(
		event.Header{EventID: identifiers.processedEventID, CorrelationID: correlationID, Now: now},
		transaction,
	)
	if err != nil {
		return nil, err
	}

	changed, err := newBalanceChangedEvent(identifiers, opened, transaction, correlationID, now)
	if err != nil {
		return nil, err
	}

	return []event.Event{processed, changed}, nil
}

func newBalanceChangedEvent(
	identifiers openingIdentifiers,
	opened wallet.Opened,
	transaction *wager.Transaction,
	correlationID string,
	now time.Time,
) (event.Event, error) {
	return event.NewWalletBalanceChanged(
		event.Header{
			EventID:       identifiers.balanceEventID,
			CorrelationID: correlationID,
			CausationID:   transaction.ID().String(),
			Now:           now,
		},
		*opened.OpeningEntry,
		opened.Wallet.Version(),
	)
}

func (service *WalletService) newOpeningIdentifiers() (openingIdentifiers, error) {
	ids := service.dependencies.IDs

	walletID, err := ids.NewWalletID()
	if err != nil {
		return openingIdentifiers{}, err
	}

	transactionID, err := ids.NewTransactionID()
	if err != nil {
		return openingIdentifiers{}, err
	}

	entryID, err := ids.NewEntryID()
	if err != nil {
		return openingIdentifiers{}, err
	}

	identifiers := openingIdentifiers{
		walletID:      walletID,
		transactionID: transactionID,
		entryID:       entryID,
	}

	return service.withEventIdentifiers(identifiers)
}

func (service *WalletService) withEventIdentifiers(
	identifiers openingIdentifiers,
) (openingIdentifiers, error) {
	var err error

	identifiers.processedEventID, err = service.dependencies.IDs.NewEventID()
	if err != nil {
		return openingIdentifiers{}, err
	}

	identifiers.balanceEventID, err = service.dependencies.IDs.NewEventID()
	if err != nil {
		return openingIdentifiers{}, err
	}

	return identifiers, nil
}

func translateWalletCreation(err error, target *wallet.Wallet) error {
	if errors.Is(err, port.ErrAlreadyExists) {
		return fmt.Errorf("%w: player %s: %w", ErrWalletExists, target.PlayerID(), err)
	}

	return err
}

func translateWalletLookup(err error, id identity.WalletID) error {
	if errors.Is(err, port.ErrNotFound) {
		return fmt.Errorf("%w: %s: %w", ErrWalletNotFound, id, err)
	}

	return err
}
