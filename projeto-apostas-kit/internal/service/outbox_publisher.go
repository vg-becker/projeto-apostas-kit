package service

import (
	"context"
	"errors"
	"time"

	"apostas-go/internal/model/port"
)

const confirmationTimeout = 5 * time.Second

// OutboxPublisherDependencies reúne as portas do publisher.
type OutboxPublisherDependencies struct {
	Outbox    port.OutboxRepository
	Publisher port.EventPublisher
	Clock     port.Clock
	Policy    PublishPolicy
}

// OutboxPublisher entrega à saída os eventos já confirmados no banco. Várias instâncias
// podem rodar ao mesmo tempo: cada evento é reivindicado com lease. A entrega é
// at-least-once e o eventId é estável, então o destino deduplica por ele.
type OutboxPublisher struct {
	dependencies OutboxPublisherDependencies
	policy       PublishPolicy
}

// NewOutboxPublisher cria o publisher.
func NewOutboxPublisher(dependencies OutboxPublisherDependencies) *OutboxPublisher {
	return &OutboxPublisher{
		dependencies: dependencies,
		policy:       dependencies.Policy.withDefaults(),
	}
}

// PublishSummary resume um ciclo de publicação.
type PublishSummary struct {
	Claimed int
	// Published são os eventos entregues e confirmados na outbox.
	Published int
	// Failed são as entregas recusadas pelo destino; o evento volta com backoff.
	Failed int
	// Unconfirmed foram entregues, mas a confirmação na outbox falhou: o evento será
	// republicado, com o mesmo eventId, quando o lease vencer.
	Unconfirmed int
}

// PublishPending reivindica um lote e publica cada evento em ordem de ocorrência. Uma
// recusa do destino agenda a nova tentativa com backoff; um evento nunca é descartado, só
// repetido. Os erros voltam agregados, sem interromper o restante do lote.
func (publisher *OutboxPublisher) PublishPending(ctx context.Context) (PublishSummary, error) {
	records, err := publisher.dependencies.Outbox.Claim(ctx, port.ClaimParams{
		Limit: publisher.policy.BatchSize,
		Now:   publisher.dependencies.Clock.Now(),
		Lease: publisher.policy.Lease,
	})
	if err != nil {
		return PublishSummary{}, err
	}

	summary := PublishSummary{Claimed: len(records)}

	var failures []error

	for _, record := range records {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)

			break
		}

		if err := publisher.publishOne(ctx, record, &summary); err != nil {
			failures = append(failures, err)
		}
	}

	return summary, errors.Join(failures...)
}

func (publisher *OutboxPublisher) publishOne(
	ctx context.Context,
	record port.OutboxRecord,
	summary *PublishSummary,
) error {
	confirmation, cancel := context.WithTimeout(context.WithoutCancel(ctx), confirmationTimeout)
	defer cancel()

	if err := publisher.dependencies.Publisher.Publish(ctx, record); err != nil {
		summary.Failed++

		return errors.Join(err, publisher.scheduleRetry(confirmation, record))
	}

	err := publisher.dependencies.Outbox.MarkPublished(
		confirmation,
		record.EventID,
		publisher.dependencies.Clock.Now(),
	)
	if err != nil {
		summary.Unconfirmed++

		return err
	}

	summary.Published++

	return nil
}

func (publisher *OutboxPublisher) scheduleRetry(
	ctx context.Context,
	record port.OutboxRecord,
) error {
	retryAt := publisher.dependencies.Clock.Now().Add(publisher.policy.backoff(record.Attempts))

	return publisher.dependencies.Outbox.MarkFailed(ctx, record.EventID, retryAt)
}
