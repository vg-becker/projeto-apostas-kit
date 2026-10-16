//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"apostas-go/internal/model/port"
)

var inboxHash = strings.Repeat("ab", 32)

func inboxMessage(consumer string, messageID string, hash string) port.InboxMessage {
	return port.InboxMessage{ConsumerName: consumer, MessageID: messageID, PayloadHash: hash}
}

// receive registra e conclui a mensagem na mesma transação, como o consumidor SQS fará.
func (f *fixture) receive(message port.InboxMessage) (port.InboxOutcome, error) {
	var outcome port.InboxOutcome

	err := f.manager.WithinTransaction(context.Background(), func(ctx context.Context) error {
		var err error
		outcome, err = f.inbox.Begin(ctx, message, baseTime)
		if err != nil || outcome != port.InboxNew {
			return err
		}

		return f.inbox.Complete(ctx, message.ConsumerName, message.MessageID, baseTime)
	})

	return outcome, err
}

func TestInboxNewThenDuplicate(t *testing.T) {
	f := newFixture(t)
	message := inboxMessage("wager-consumer", "msg-1", inboxHash)

	first, firstErr := f.receive(message)
	second, secondErr := f.receive(message)

	if firstErr != nil || secondErr != nil {
		t.Fatalf("errors = %v, %v", firstErr, secondErr)
	}

	if first != port.InboxNew || second != port.InboxDuplicate {
		t.Fatalf("outcomes = %s, %s, want NEW then DUPLICATE", first, second)
	}
}

func TestInboxDetectsRedeliveryWithDifferentContent(t *testing.T) {
	f := newFixture(t)
	if _, err := f.receive(inboxMessage("wager-consumer", "msg-1", inboxHash)); err != nil {
		t.Fatalf("first receive: %v", err)
	}

	other := strings.Repeat("cd", 32)
	outcome, err := f.receive(inboxMessage("wager-consumer", "msg-1", other))

	if err != nil || outcome != port.InboxHashConflict {
		t.Fatalf("outcome = %s (%v), want HASH_CONFLICT", outcome, err)
	}
}

func TestInboxRollbackReleasesTheMessage(t *testing.T) {
	f := newFixture(t)
	message := inboxMessage("wager-consumer", "msg-1", inboxHash)

	err := f.manager.WithinTransaction(context.Background(), func(ctx context.Context) error {
		if _, err := f.inbox.Begin(ctx, message, baseTime); err != nil {
			return err
		}

		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want %v", err, errBoom)
	}

	outcome, err := f.receive(message)

	if err != nil || outcome != port.InboxNew {
		t.Fatalf("outcome = %s (%v), want NEW after rollback", outcome, err)
	}
}

// deliverConcurrently entrega a mesma mensagem de várias transações ao mesmo tempo; quem
// recebe NEW demora no commit para forçar as demais a esperarem pela unicidade.
func (f *fixture) deliverConcurrently(
	message port.InboxMessage,
	deliveries int,
) ([]port.InboxOutcome, []error) {
	outcomes := make([]port.InboxOutcome, deliveries)
	errs := make([]error, deliveries)
	start := make(chan struct{})

	var group sync.WaitGroup
	for index := range deliveries {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start

			errs[index] = f.manager.WithinTransaction(
				context.Background(),
				func(ctx context.Context) error {
					var err error
					outcomes[index], err = f.inbox.Begin(ctx, message, baseTime)
					if err != nil || outcomes[index] != port.InboxNew {
						return err
					}

					time.Sleep(100 * time.Millisecond)

					return f.inbox.Complete(ctx, message.ConsumerName, message.MessageID, baseTime)
				},
			)
		}()
	}

	close(start)
	group.Wait()

	return outcomes, errs
}

func TestInboxConcurrentDeliveriesYieldExactlyOneNew(t *testing.T) {
	f := newFixture(t)
	const deliveries = 10

	outcomes, errs := f.deliverConcurrently(
		inboxMessage("wager-consumer", "msg-1", inboxHash),
		deliveries,
	)

	counts := map[port.InboxOutcome]int{}
	for index, outcome := range outcomes {
		if errs[index] != nil {
			t.Fatalf("delivery %d error = %v", index, errs[index])
		}

		counts[outcome]++
	}

	if counts[port.InboxNew] != 1 || counts[port.InboxDuplicate] != deliveries-1 {
		t.Fatalf("counts = %v, want 1 NEW and %d DUPLICATE", counts, deliveries-1)
	}
}

func TestInboxIsScopedPerConsumer(t *testing.T) {
	f := newFixture(t)

	first, firstErr := f.receive(inboxMessage("consumer-a", "msg-1", inboxHash))
	second, secondErr := f.receive(inboxMessage("consumer-b", "msg-1", inboxHash))

	if firstErr != nil || secondErr != nil || first != port.InboxNew || second != port.InboxNew {
		t.Fatalf("outcomes = %s, %s (%v, %v)", first, second, firstErr, secondErr)
	}
}

func TestInboxCompleteIsIdempotentAndRejectsUnknownMessages(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	err := f.inbox.Complete(ctx, "wager-consumer", "never-seen", baseTime)
	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("unknown error = %v, want %v", err, port.ErrNotFound)
	}

	if _, err := f.receive(inboxMessage("wager-consumer", "msg-1", inboxHash)); err != nil {
		t.Fatalf("receive: %v", err)
	}

	if err := f.inbox.Complete(ctx, "wager-consumer", "msg-1", baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("second complete error = %v", err)
	}

	var completedAt time.Time
	row := f.pool.QueryRow(ctx, `SELECT completed_at FROM inbox_messages WHERE message_id = 'msg-1'`)
	if err := row.Scan(&completedAt); err != nil || !completedAt.Equal(baseTime) {
		t.Fatalf("completedAt = %v (%v), want the first instant", completedAt, err)
	}
}
