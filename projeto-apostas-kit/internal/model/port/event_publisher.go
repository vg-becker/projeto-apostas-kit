package port

import "context"

// EventPublisher entrega um registro da outbox ao destino dos eventos de saída. A entrega é
// at-least-once: o mesmo EventID pode chegar mais de uma vez, e o destino deve deduplicar
// por ele. Devolver nil significa que o destino confirmou o recebimento.
type EventPublisher interface {
	Publish(ctx context.Context, record OutboxRecord) error
}
