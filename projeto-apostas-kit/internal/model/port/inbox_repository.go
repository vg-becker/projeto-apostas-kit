package port

import (
	"context"
	"time"
)

// InboxOutcome é o resultado de registrar a chegada de uma mensagem.
type InboxOutcome string

// Resultados possíveis de InboxRepository.Begin.
const (
	InboxNew          InboxOutcome = "NEW"
	InboxDuplicate    InboxOutcome = "DUPLICATE"
	InboxHashConflict InboxOutcome = "HASH_CONFLICT"
)

// InboxMessage identifica uma mensagem de um consumidor; o hash detecta reentrega alterada.
type InboxMessage struct {
	ConsumerName string
	MessageID    string
	PayloadHash  string
}

// InboxRepository deduplica mensagens por (consumerName, messageId), na mesma
// transação das alterações de domínio.
type InboxRepository interface {
	// Begin registra a mensagem: NEW se é a primeira vez; DUPLICATE se já foi concluída
	// com o mesmo hash; HASH_CONFLICT se o messageId voltou com outro conteúdo.
	Begin(ctx context.Context, message InboxMessage, now time.Time) (InboxOutcome, error)
	// Complete marca a conclusão durável do tratamento.
	Complete(ctx context.Context, consumerName string, messageID string, now time.Time) error
}
