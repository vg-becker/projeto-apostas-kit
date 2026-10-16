package sqs

import (
	"context"
	"errors"
	"strconv"
	"time"

	"apostas-go/internal/platform/sqsclient"
	"apostas-go/internal/service"
)

const unknownGroup = "unknown"

// deadLetter envia a mensagem à DLQ com o motivo e só então a apaga. Se o envio falhar, a
// mensagem é tratada como falha transitória: não é apagada, e o broker a move para a DLQ
// ao esgotar os recebimentos.
func (consumer *Consumer) deadLetter(
	ctx context.Context,
	message sqsclient.Message,
	reason string,
) {
	if err := consumer.sendToDeadLetter(ctx, message, reason); err != nil {
		consumer.warn("dead-letter send failed", message, err)
		consumer.retryLater(ctx, message, err)

		return
	}

	consumer.logger.Warn(
		"message dead-lettered",
		"messageId", message.MessageID,
		"groupId", message.GroupID,
		"reason", reason,
	)

	if err := consumer.delete(ctx, message); err != nil {
		consumer.warn("delete failed after dead-lettering", message, err)
	}

	consumer.observe(OutcomeDeadLettered, message, reason)
}

// sendToDeadLetter preserva o corpo e o grupo originais. Os atributos trazem só códigos e
// identificadores, nunca texto livre de erro nem dados do payload.
func (consumer *Consumer) sendToDeadLetter(
	ctx context.Context,
	message sqsclient.Message,
	reason string,
) error {
	cleanup, cancel := cleanupContext(ctx)
	defer cancel()

	group := message.GroupID
	if group == "" {
		group = unknownGroup
	}

	_, err := consumer.queue.Send(cleanup, sqsclient.SendParams{
		QueueURL:        consumer.config.Queues.DeadLetter,
		Body:            message.Body,
		GroupID:         group,
		DeduplicationID: message.MessageID,
		Attributes: map[string]string{
			"failureReason":     reason,
			"originalMessageId": message.MessageID,
			"receiveCount":      strconv.Itoa(message.ReceiveCount),
		},
	})

	return err
}

// failureReason devolve o motivo estável de uma recusa permanente.
func failureReason(err error) string {
	var parseFailure *ParseError
	if errors.As(err, &parseFailure) {
		return string(parseFailure.Reason)
	}

	switch {
	case errors.Is(err, service.ErrInboundHashConflict):
		return "hash_conflict"
	case errors.Is(err, service.ErrIdempotencyConflict):
		return "idempotency_conflict"
	case errors.Is(err, service.ErrWalletNotFound):
		return "wallet_not_found"
	case errors.Is(err, service.ErrInvalidMessage):
		return "invalid_message"
	default:
		return "invalid_input"
	}
}

// visibilityBackoff é o atraso antes de a mensagem voltar: RetryBase dobrado a cada
// recebimento já feito, limitado a RetryMax, em segundos inteiros de no mínimo 1.
func visibilityBackoff(config Config, receiveCount int) time.Duration {
	delay := doubling(config.RetryBase, config.RetryMax, max(receiveCount, 1)-1)

	return max(delay.Truncate(time.Second), time.Second)
}

// doubling dobra o atraso inicial a cada tentativa, sem passar do máximo.
func doubling(initial time.Duration, maximum time.Duration, attempts int) time.Duration {
	delay := initial

	for range attempts {
		delay *= 2
		if delay >= maximum {
			return maximum
		}
	}

	return min(delay, maximum)
}
