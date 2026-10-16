package postgres

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"apostas-go/internal/model/event"
	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/port"
)

const insertOutboxEvent = `
	INSERT INTO outbox_events (
	       event_id,
	       aggregate_id,
	       event_type,
	       payload,
	       occurred_at,
	       next_attempt_at
	)
	VALUES ($1, $2, $3, $4::jsonb, $5, $5)
`

const claimOutboxEvents = `
	WITH claimed AS (
	     SELECT event_id
	       FROM outbox_events
	      WHERE published_at IS NULL
	        AND next_attempt_at <= $1
	        AND (lease_until IS NULL OR lease_until <= $1)
	      ORDER BY occurred_at, event_id
	      LIMIT $2
	        FOR UPDATE SKIP LOCKED
	)
	UPDATE outbox_events
	   SET lease_until = $3
	 WHERE event_id IN (SELECT event_id FROM claimed)
	RETURNING event_id::text,
	          aggregate_id,
	          event_type,
	          payload::text,
	          occurred_at,
	          attempts
`

const markOutboxPublished = `
	UPDATE outbox_events
	   SET published_at = COALESCE(published_at, $2),
	       lease_until = NULL
	 WHERE event_id = $1
`

const markOutboxFailed = `
	UPDATE outbox_events
	   SET attempts = attempts + 1,
	       next_attempt_at = $2,
	       lease_until = NULL
	 WHERE event_id = $1
	   AND published_at IS NULL
`

const outboxBacklog = `
	SELECT count(*),
	       COALESCE(EXTRACT(EPOCH FROM (now() - min(occurred_at))), 0)::float8
	  FROM outbox_events
	 WHERE published_at IS NULL
`

var _ port.OutboxRepository = (*OutboxRepository)(nil)

// OutboxRepository implementa port.OutboxRepository sobre outbox_events.
type OutboxRepository struct {
	connection *Connection
}

// NewOutboxRepository cria o repositório sobre a conexão.
func NewOutboxRepository(connection *Connection) *OutboxRepository {
	return &OutboxRepository{connection: connection}
}

// Add grava o snapshot do evento na transação do context; eventId repetido devolve
// ErrAlreadyExists. O evento só fica visível aos publishers depois do commit.
func (repository *OutboxRepository) Add(ctx context.Context, evt event.Event) error {
	payload, err := evt.Marshal()
	if err != nil {
		return fmt.Errorf("postgres: marshal event %s: %w", evt.EventID(), err)
	}

	_, err = repository.connection.Querier(ctx).Exec(
		ctx,
		insertOutboxEvent,
		evt.EventID().String(),
		evt.AggregateID(),
		evt.EventType().String(),
		string(payload),
		evt.OccurredAt(),
	)

	return translateError(err)
}

// Claim reserva eventos pendentes ou com lease vencido, pulando os travados por outro
// publisher; um publisher que morreu tem seu trabalho assumido quando o lease vence.
func (repository *OutboxRepository) Claim(
	ctx context.Context,
	params port.ClaimParams,
) ([]port.OutboxRecord, error) {
	if params.Limit <= 0 || params.Lease <= 0 {
		return nil, fmt.Errorf("postgres: claim needs a positive limit and lease")
	}

	rows, err := repository.connection.Querier(ctx).Query(
		ctx,
		claimOutboxEvents,
		params.Now,
		params.Limit,
		params.Now.Add(params.Lease),
	)
	if err != nil {
		return nil, translateError(err)
	}
	defer rows.Close()

	return collectOutboxRecords(rows)
}

// MarkPublished conclui o registro; repetir a chamada preserva o instante original.
func (repository *OutboxRepository) MarkPublished(
	ctx context.Context,
	id identity.EventID,
	now time.Time,
) error {
	tag, err := repository.connection.Querier(ctx).Exec(ctx, markOutboxPublished, id.String(), now)
	if err != nil {
		return translateError(err)
	}

	return requireOneRow(tag.RowsAffected(), id)
}

// MarkFailed soma uma tentativa e agenda a próxima; o evento volta a ser reivindicável
// quando nextAttemptAt vencer.
func (repository *OutboxRepository) MarkFailed(
	ctx context.Context,
	id identity.EventID,
	nextAttemptAt time.Time,
) error {
	tag, err := repository.connection.Querier(ctx).Exec(
		ctx,
		markOutboxFailed,
		id.String(),
		nextAttemptAt,
	)
	if err != nil {
		return translateError(err)
	}

	return requireOneRow(tag.RowsAffected(), id)
}

func requireOneRow(affected int64, id identity.EventID) error {
	if affected == 0 {
		return fmt.Errorf("%w: outbox event %s", port.ErrNotFound, id)
	}

	return nil
}

type outboxRow struct {
	id          string
	aggregateID string
	eventType   string
	payload     string
	occurredAt  time.Time
	attempts    int
}

func (row *outboxRow) destinations() []any {
	return []any{
		&row.id,
		&row.aggregateID,
		&row.eventType,
		&row.payload,
		&row.occurredAt,
		&row.attempts,
	}
}

func (row *outboxRow) record() port.OutboxRecord {
	return port.OutboxRecord{
		EventID:     identity.EventID(row.id),
		AggregateID: row.aggregateID,
		EventType:   event.Type(row.eventType),
		Payload:     []byte(row.payload),
		OccurredAt:  row.occurredAt.UTC(),
		Attempts:    row.attempts,
	}
}

func collectOutboxRecords(rows pgx.Rows) ([]port.OutboxRecord, error) {
	var records []port.OutboxRecord

	for rows.Next() {
		var row outboxRow
		if err := rows.Scan(row.destinations()...); err != nil {
			return nil, translateError(err)
		}

		records = append(records, row.record())
	}

	if err := rows.Err(); err != nil {
		return nil, translateError(err)
	}

	return inOrderOfOccurrence(records), nil
}

// inOrderOfOccurrence ordena os eventos por (occurred_at, event_id). O UPDATE ... RETURNING
// não garante a ordem das linhas devolvidas, e o publisher precisa enviar os eventos de um
// agregado na ordem em que aconteceram.
func inOrderOfOccurrence(records []port.OutboxRecord) []port.OutboxRecord {
	sort.SliceStable(records, func(left int, right int) bool {
		if !records[left].OccurredAt.Equal(records[right].OccurredAt) {
			return records[left].OccurredAt.Before(records[right].OccurredAt)
		}

		return records[left].EventID < records[right].EventID
	})

	return records
}

// Backlog informa quantos eventos ainda não foram publicados e há quanto tempo o mais
// antigo espera, para métricas. É uma leitura do estado, sem lock nem efeito.
func (repository *OutboxRepository) Backlog(
	ctx context.Context,
) (pending int64, oldest time.Duration, err error) {
	var oldestSeconds float64

	row := repository.connection.Querier(ctx).QueryRow(ctx, outboxBacklog)
	if err := row.Scan(&pending, &oldestSeconds); err != nil {
		return 0, 0, translateError(err)
	}

	return pending, time.Duration(oldestSeconds * float64(time.Second)), nil
}
