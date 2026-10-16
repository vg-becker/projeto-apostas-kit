package postgres

import (
	"context"
	"fmt"
	"time"

	"apostas-go/internal/model/port"
)

const insertInboxMessage = `
	INSERT INTO inbox_messages (
	       consumer_name,
	       message_id,
	       payload_hash,
	       received_at
	)
	VALUES ($1, $2, $3, $4)
	ON CONFLICT (consumer_name, message_id) DO NOTHING
`

const selectInboxMessage = `
	SELECT payload_hash,
	       completed_at
	  FROM inbox_messages
	 WHERE consumer_name = $1
	   AND message_id = $2
`

const completeInboxMessage = `
	UPDATE inbox_messages
	   SET completed_at = COALESCE(completed_at, $3)
	 WHERE consumer_name = $1
	   AND message_id = $2
`

var _ port.InboxRepository = (*InboxRepository)(nil)

// InboxRepository implementa port.InboxRepository sobre inbox_messages.
type InboxRepository struct {
	connection *Connection
}

// NewInboxRepository cria o repositório sobre a conexão.
func NewInboxRepository(connection *Connection) *InboxRepository {
	return &InboxRepository{connection: connection}
}

// Begin registra a chegada da mensagem. Duas transações concorrentes com a mesma mensagem
// são serializadas pela unicidade: a segunda espera o commit da primeira e vê DUPLICATE.
// Uma linha existente com o mesmo hash, mas não concluída, devolve NEW: o tratamento
// será refeito em vez de a mensagem ser descartada.
func (repository *InboxRepository) Begin(
	ctx context.Context,
	message port.InboxMessage,
	now time.Time,
) (port.InboxOutcome, error) {
	tag, err := repository.connection.Querier(ctx).Exec(
		ctx,
		insertInboxMessage,
		message.ConsumerName,
		message.MessageID,
		message.PayloadHash,
		now,
	)
	if err != nil {
		return "", translateError(err)
	}

	if tag.RowsAffected() == 1 {
		return port.InboxNew, nil
	}

	return repository.outcomeOfExisting(ctx, message)
}

// Complete marca a conclusão do tratamento; repetir a chamada não altera o instante gravado.
func (repository *InboxRepository) Complete(
	ctx context.Context,
	consumerName string,
	messageID string,
	now time.Time,
) error {
	tag, err := repository.connection.Querier(ctx).Exec(
		ctx,
		completeInboxMessage,
		consumerName,
		messageID,
		now,
	)
	if err != nil {
		return translateError(err)
	}

	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: inbox message %s/%s", port.ErrNotFound, consumerName, messageID)
	}

	return nil
}

func (repository *InboxRepository) outcomeOfExisting(
	ctx context.Context,
	message port.InboxMessage,
) (port.InboxOutcome, error) {
	var (
		storedHash  string
		completedAt *time.Time
	)

	row := repository.connection.Querier(ctx).QueryRow(
		ctx,
		selectInboxMessage,
		message.ConsumerName,
		message.MessageID,
	)
	if err := row.Scan(&storedHash, &completedAt); err != nil {
		return "", translateError(err)
	}

	switch {
	case storedHash != message.PayloadHash:
		return port.InboxHashConflict, nil
	case completedAt == nil:
		return port.InboxNew, nil
	default:
		return port.InboxDuplicate, nil
	}
}
