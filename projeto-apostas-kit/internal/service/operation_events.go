package service

import (
	"context"
	"time"

	"apostas-go/internal/model/event"
	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/ledger"
)

// processedEvents monta WagerTransactionProcessed e, quando houve movimentação,
// WalletBalanceChanged com a versão da carteira depois da mudança.
func processedEvents(
	plan operationPlan,
	entry *ledger.Entry,
	walletVersion int64,
	at time.Time,
) ([]event.Event, error) {
	processed, err := event.NewWagerTransactionProcessed(
		plan.header(plan.eventIDs[0], plan.command.CausationID, at),
		plan.transaction,
	)
	if err != nil {
		return nil, err
	}

	events := []event.Event{processed}
	if entry == nil {
		return events, nil
	}

	changed, err := event.NewWalletBalanceChanged(
		plan.header(plan.eventIDs[1], plan.transaction.ID().String(), at),
		*entry,
		walletVersion,
	)
	if err != nil {
		return nil, err
	}

	return append(events, changed), nil
}

func rejectedEvents(plan operationPlan, at time.Time) ([]event.Event, error) {
	rejected, err := event.NewWagerTransactionRejected(
		plan.header(plan.eventIDs[0], plan.command.CausationID, at),
		plan.transaction,
	)
	if err != nil {
		return nil, err
	}

	return []event.Event{rejected}, nil
}

func (plan operationPlan) header(
	eventID identity.EventID,
	causationID string,
	at time.Time,
) event.Header {
	return event.Header{
		EventID:       eventID,
		CorrelationID: plan.correlationID,
		CausationID:   causationID,
		Now:           at,
	}
}

func (service *OperationService) enqueue(ctx context.Context, events []event.Event) error {
	for _, pending := range events {
		if err := service.dependencies.Outbox.Add(ctx, pending); err != nil {
			return err
		}
	}

	return nil
}
