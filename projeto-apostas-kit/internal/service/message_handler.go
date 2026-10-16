package service

import (
	"context"
	"fmt"
	"strings"

	"apostas-go/internal/model/port"
)

const maxMessageIDLength = 128

// MessageHandlerDependencies reúne o que o tratamento de mensagens precisa.
type MessageHandlerDependencies struct {
	Transactions port.TxManager
	Inbox        port.InboxRepository
	Operations   *OperationService
	Clock        port.Clock
	// ConsumerName identifica este consumidor na inbox: (consumerName, messageId) é único.
	ConsumerName string
}

// MessageHandler trata as operações que chegam por mensageria com as mesmas garantias do HTTP.
type MessageHandler struct {
	dependencies MessageHandlerDependencies
}

// NewMessageHandler cria o tratador.
func NewMessageHandler(dependencies MessageHandlerDependencies) *MessageHandler {
	return &MessageHandler{dependencies: dependencies}
}

// Handle registra a mensagem na inbox, executa a operação e conclui a inbox, tudo na mesma
// transação SQL: ou os efeitos de domínio, o ledger, os eventos e a inbox são confirmados
// juntos, ou nenhum. A mensagem só pode ser removida da fila depois que Handle devolve nil.
//
// Uma reentrega do mesmo messageId e conteúdo devolve InboundDuplicate sem reaplicar nada;
// o mesmo messageId com outro conteúdo é ErrInboundHashConflict. Rejeição de negócio e
// referência pendente são resultados concluídos (InboundApplied). Use Classify para decidir
// o que fazer com um erro.
func (handler *MessageHandler) Handle(
	ctx context.Context,
	message InboundMessage,
) (InboundResult, error) {
	if err := validateMessageID(message.MessageID); err != nil {
		return InboundResult{}, err
	}

	hash, err := CanonicalHash(message.Command)
	if err != nil {
		return InboundResult{}, fmt.Errorf("%w: %w", ErrInvalidMessage, err)
	}

	var result InboundResult

	err = handler.dependencies.Transactions.WithinTransaction(
		ctx,
		func(ctx context.Context) error {
			var err error
			result, err = handler.handleInsideTransaction(ctx, message, hash)

			return err
		},
	)
	if err != nil {
		return InboundResult{}, err
	}

	return result, nil
}

func (handler *MessageHandler) handleInsideTransaction(
	ctx context.Context,
	message InboundMessage,
	hash string,
) (InboundResult, error) {
	outcome, err := handler.dependencies.Inbox.Begin(
		ctx,
		port.InboxMessage{
			ConsumerName: handler.dependencies.ConsumerName,
			MessageID:    message.MessageID,
			PayloadHash:  hash,
		},
		handler.dependencies.Clock.Now(),
	)
	if err != nil {
		return InboundResult{}, err
	}

	switch outcome {
	case port.InboxDuplicate:
		return InboundResult{Outcome: InboundDuplicate}, nil
	case port.InboxHashConflict:
		return InboundResult{}, fmt.Errorf("%w: %s", ErrInboundHashConflict, message.MessageID)
	default:
		return handler.apply(ctx, message)
	}
}

// apply executa a operação, que se junta à transação em curso, e conclui a inbox.
func (handler *MessageHandler) apply(
	ctx context.Context,
	message InboundMessage,
) (InboundResult, error) {
	operation, err := handler.dependencies.Operations.Process(ctx, message.Command)
	if err != nil {
		return InboundResult{}, err
	}

	err = handler.dependencies.Inbox.Complete(
		ctx,
		handler.dependencies.ConsumerName,
		message.MessageID,
		handler.dependencies.Clock.Now(),
	)
	if err != nil {
		return InboundResult{}, err
	}

	return InboundResult{Outcome: InboundApplied, Operation: operation}, nil
}

func validateMessageID(messageID string) error {
	if messageID == "" || len(messageID) > maxMessageIDLength ||
		strings.TrimSpace(messageID) != messageID {
		return fmt.Errorf("%w: message id must be 1 to %d characters without surrounding spaces",
			ErrInvalidMessage, maxMessageIDLength)
	}

	return nil
}
