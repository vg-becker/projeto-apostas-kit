package sqs

import (
	"context"
	"time"

	"apostas-go/internal/platform/sqsclient"
	"apostas-go/internal/service"
)

// dispatch separa o lote em grupos, preservando a ordem de chegada, e trata cada grupo em
// sequência numa goroutine própria: a ordem dentro da carteira é mantida e carteiras
// diferentes andam em paralelo.
func (consumer *Consumer) dispatch(
	runContext context.Context,
	workContext context.Context,
	messages []sqsclient.Message,
) {
	for _, group := range orderedGroups(messages) {
		consumer.adjustInflight(len(group))
		consumer.groups.Add(1)

		go consumer.processGroup(runContext, workContext, group)
	}
}

// orderedGroups agrupa por MessageGroupId mantendo a ordem do lote; uma mensagem sem grupo
// forma um grupo só dela.
func orderedGroups(messages []sqsclient.Message) [][]sqsclient.Message {
	var groups [][]sqsclient.Message

	position := map[string]int{}

	for _, message := range messages {
		key := message.GroupID
		if key == "" {
			groups = append(groups, []sqsclient.Message{message})

			continue
		}

		index, seen := position[key]
		if !seen {
			position[key] = len(groups)
			groups = append(groups, []sqsclient.Message{message})

			continue
		}

		groups[index] = append(groups[index], message)
	}

	return groups
}

func (consumer *Consumer) processGroup(
	runContext context.Context,
	workContext context.Context,
	group []sqsclient.Message,
) {
	defer consumer.groups.Done()

	for index, message := range group {
		if runContext.Err() != nil {
			consumer.releaseAll(workContext, group[index:])
			consumer.adjustInflight(-len(group[index:]))

			return
		}

		consumer.processMessage(workContext, message)
		consumer.adjustInflight(-1)
	}
}

// processMessage decide o destino de uma mensagem. O Delete só vem depois que Handle
// devolve nil, isto é, depois do commit.
func (consumer *Consumer) processMessage(workContext context.Context, message sqsclient.Message) {
	ctx, cancel := context.WithTimeout(workContext, consumer.config.ProcessingTimeout)
	defer cancel()

	parsed, err := Parse(message.Body)
	if err != nil {
		consumer.deadLetter(workContext, message, failureReason(err))

		return
	}

	result, err := consumer.handler.Handle(ctx, parsed)

	switch {
	case err == nil:
		consumer.finish(workContext, message, outcomeOf(result))
	case workContext.Err() != nil:
		consumer.releaseAll(workContext, []sqsclient.Message{message})
	case service.Classify(err) == service.ClassPermanent:
		consumer.deadLetter(workContext, message, failureReason(err))
	default:
		consumer.retryLater(workContext, message, err)
	}
}

func outcomeOf(result service.InboundResult) Outcome {
	if result.Outcome == service.InboundDuplicate {
		return OutcomeDuplicate
	}

	return OutcomeApplied
}

// finish apaga a mensagem já confirmada. Uma falha aqui é inofensiva: a mensagem volta, a
// inbox a reconhece como duplicata e ela é apagada então.
func (consumer *Consumer) finish(ctx context.Context, message sqsclient.Message, outcome Outcome) {
	if err := consumer.delete(ctx, message); err != nil {
		consumer.warn("delete failed after commit", message, err)
		consumer.observe(OutcomeDeleteFailed, message, "")

		return
	}

	consumer.observe(outcome, message, "")
}

// retryLater adia a mensagem com backoff pela contagem de recebimentos. Esgotadas as
// tentativas, o próprio broker a move para a DLQ.
func (consumer *Consumer) retryLater(ctx context.Context, message sqsclient.Message, cause error) {
	delay := visibilityBackoff(consumer.config, message.ReceiveCount)

	cleanup, cancel := cleanupContext(ctx)
	defer cancel()

	err := consumer.queue.ChangeVisibility(
		cleanup,
		consumer.config.Queues.Requests,
		message.ReceiptHandle,
		int(delay/time.Second),
	)
	if err != nil {
		consumer.warn("could not postpone message", message, err)
	}

	consumer.logger.Warn(
		"message postponed",
		"messageId", message.MessageID,
		"groupId", message.GroupID,
		"receiveCount", message.ReceiveCount,
		"retryInSeconds", int(delay/time.Second),
		"error", cause,
	)
	consumer.observe(OutcomeRetried, message, "")
}

// releaseAll devolve as mensagens à fila na hora, para reentrega imediata e segura.
func (consumer *Consumer) releaseAll(ctx context.Context, messages []sqsclient.Message) {
	for _, message := range messages {
		cleanup, cancel := cleanupContext(ctx)

		err := consumer.queue.ChangeVisibility(
			cleanup,
			consumer.config.Queues.Requests,
			message.ReceiptHandle,
			0,
		)

		cancel()

		if err != nil {
			consumer.warn("could not release message", message, err)
		}

		consumer.observe(OutcomeReleased, message, "")
	}
}

func (consumer *Consumer) delete(ctx context.Context, message sqsclient.Message) error {
	cleanup, cancel := cleanupContext(ctx)
	defer cancel()

	return consumer.queue.Delete(cleanup, consumer.config.Queues.Requests, message.ReceiptHandle)
}

func (consumer *Consumer) warn(text string, message sqsclient.Message, err error) {
	consumer.logger.Warn(text, "messageId", message.MessageID, "error", err)
}

func (consumer *Consumer) observe(outcome Outcome, message sqsclient.Message, reason string) {
	consumer.observer.Observe(Event{
		Outcome:      outcome,
		MessageID:    message.MessageID,
		GroupID:      message.GroupID,
		ReceiveCount: message.ReceiveCount,
		Reason:       reason,
	})
}

// cleanupContext dá às operações finais (Delete, Send à DLQ, liberar) um prazo próprio, que
// não é cancelado pelo desligamento: depois de um commit, apagar a mensagem tem de acontecer.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}
