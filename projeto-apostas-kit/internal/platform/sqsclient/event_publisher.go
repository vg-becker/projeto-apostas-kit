package sqsclient

import (
	"context"

	"apostas-go/internal/model/port"
)

var _ port.EventPublisher = (*EventPublisher)(nil)

// EventPublisher entrega os registros da outbox à fila de eventos de saída. O contrato de
// roteamento: MessageGroupId = aggregateId (ordem por agregado) e MessageDeduplicationId =
// eventId (o broker descarta o mesmo evento republicado dentro de 5 minutos; o consumidor
// deve deduplicar por eventId, porque a entrega é at-least-once).
type EventPublisher struct {
	client   *Client
	queueURL string
}

// NewEventPublisher cria o publicador sobre a fila de eventos.
func NewEventPublisher(client *Client, queueURL string) *EventPublisher {
	return &EventPublisher{client: client, queueURL: queueURL}
}

// Publish envia o snapshot imutável do evento; devolver nil significa que o broker o aceitou.
func (publisher *EventPublisher) Publish(ctx context.Context, record port.OutboxRecord) error {
	_, err := publisher.client.Send(ctx, SendParams{
		QueueURL:        publisher.queueURL,
		Body:            string(record.Payload),
		GroupID:         record.AggregateID,
		DeduplicationID: record.EventID.String(),
		Attributes: map[string]string{
			"eventType": record.EventType.String(),
			"eventId":   record.EventID.String(),
		},
	})

	return err
}
