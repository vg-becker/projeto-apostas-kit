package port

import (
	"context"
	"time"

	"apostas-go/internal/model/event"
	"apostas-go/internal/model/identity"
)

// OutboxRecord é um evento pendente de publicação; Payload é o snapshot imutável.
type OutboxRecord struct {
	EventID     identity.EventID
	AggregateID string
	EventType   event.Type
	Payload     []byte
	OccurredAt  time.Time
	Attempts    int
}

// OutboxRepository guarda os eventos na mesma transação do domínio e os entrega aos
// publishers. Add usa a transação do context; as demais operações são do publisher.
type OutboxRepository interface {
	// Add devolve ErrAlreadyExists se o eventId já foi registrado.
	Add(ctx context.Context, event event.Event) error
	// Claim reserva registros pendentes ou com lease vencido (FOR UPDATE SKIP LOCKED),
	// permitindo vários publishers e recuperação de trabalho abandonado.
	Claim(ctx context.Context, params ClaimParams) ([]OutboxRecord, error)
	// MarkPublished conclui o registro depois da confirmação do destino.
	MarkPublished(ctx context.Context, id identity.EventID, now time.Time) error
	// MarkFailed incrementa as tentativas e agenda a próxima com backoff.
	MarkFailed(ctx context.Context, id identity.EventID, nextAttemptAt time.Time) error
}
