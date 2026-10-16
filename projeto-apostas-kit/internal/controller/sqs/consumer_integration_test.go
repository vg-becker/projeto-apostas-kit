//go:build integration

package sqs_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"apostas-go/internal/controller/sqs"
	"apostas-go/internal/model/port"
	"apostas-go/internal/platform/sqsclient"
	"apostas-go/internal/service"
)

// failingDeleteQueue falha o Delete as primeiras vezes, como uma queda entre o commit e a
// remoção da mensagem.
type failingDeleteQueue struct {
	sqs.Queue
	remaining atomic.Int32
}

func (queue *failingDeleteQueue) Delete(ctx context.Context, queueURL string, handle string) error {
	if queue.remaining.Add(-1) >= 0 {
		return fmt.Errorf("%w: connection reset", port.ErrTransient)
	}

	return queue.Queue.Delete(ctx, queueURL, handle)
}

func TestTheMessageIsDeletedOnlyAfterTheCommit(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})

	f := newFixture(t, fixtureOptions{
		wrapHandler: func(inner sqs.Handler) sqs.Handler {
			return handlerFunc(func(ctx context.Context, message service.InboundMessage) (service.InboundResult, error) {
				result, err := inner.Handle(ctx, message)
				entered <- struct{}{}
				<-release

				return result, err
			})
		},
	})
	wallet := f.openWallet(t, 100000)

	f.publish(t, wallet.id, body("msg-1", wallet, "tx-1", "BET", "25.00"))

	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatalf("the message was never handled\n%s", f.logs.String())
	}

	visible, inFlight := f.depth(t, f.queues.Requests)
	if f.debits(t, wallet.id) != 1 || visible != 0 || inFlight != 1 {
		t.Fatalf("debits = %d, visible = %d, in flight = %d: the commit must precede the delete",
			f.debits(t, wallet.id), visible, inFlight)
	}

	close(release)

	f.eventually(t, 20*time.Second, "the message is deleted after the commit", func() bool {
		return f.queueIsEmpty(t, f.queues.Requests) && f.observer.count(sqs.OutcomeApplied) == 1
	})
}

func TestACrashBetweenCommitAndDeleteRedeliversAsADuplicate(t *testing.T) {
	var flaky *failingDeleteQueue

	f := newFixture(t, fixtureOptions{
		wrapQueue: func(inner sqs.Queue) sqs.Queue {
			flaky = &failingDeleteQueue{Queue: inner}
			flaky.remaining.Store(1)

			return flaky
		},
		configure: func(config *sqs.Config) { config.VisibilityTimeoutSeconds = 2 },
	})
	wallet := f.openWallet(t, 100000)

	f.publish(t, wallet.id, body("msg-1", wallet, "tx-1", "BET", "25.00"))

	f.eventually(t, 40*time.Second, "redelivery after the failed delete, then removal", func() bool {
		return f.observer.count(sqs.OutcomeDuplicate) == 1 && f.queueIsEmpty(t, f.queues.Requests)
	})

	if f.observer.count(sqs.OutcomeDeleteFailed) != 1 || f.observer.count(sqs.OutcomeApplied) != 0 ||
		f.debits(t, wallet.id) != 1 {
		t.Fatalf("delete failed = %d, applied = %d, debits = %d",
			f.observer.count(sqs.OutcomeDeleteFailed), f.observer.count(sqs.OutcomeApplied), f.debits(t, wallet.id))
	}
}

func TestATransientFailureBacksOffAndALaterDeliverySucceeds(t *testing.T) {
	var calls atomic.Int32

	f := newFixture(t, fixtureOptions{
		wrapHandler: func(inner sqs.Handler) sqs.Handler {
			return handlerFunc(func(ctx context.Context, message service.InboundMessage) (service.InboundResult, error) {
				if calls.Add(1) <= 2 {
					return service.InboundResult{}, fmt.Errorf("%w: postgres is down", port.ErrTransient)
				}

				return inner.Handle(ctx, message)
			})
		},
	})
	wallet := f.openWallet(t, 100000)

	f.publish(t, wallet.id, body("msg-1", wallet, "tx-1", "BET", "25.00"))

	f.eventually(t, 40*time.Second, "two postponements and then the application", func() bool {
		return f.observer.count(sqs.OutcomeApplied) == 1 && f.queueIsEmpty(t, f.queues.Requests)
	})

	counts := f.observer.receiveCounts(sqs.OutcomeRetried)
	if len(counts) != 2 || counts[0] != 1 || counts[1] != 2 || f.debits(t, wallet.id) != 1 {
		t.Fatalf("retried receive counts = %v, debits = %d", counts, f.debits(t, wallet.id))
	}
}

func TestExhaustedRetriesEndUpInTheDeadLetterQueueByTheBroker(t *testing.T) {
	var calls atomic.Int32

	f := newFixture(t, fixtureOptions{
		wrapHandler: func(sqs.Handler) sqs.Handler {
			return handlerFunc(func(context.Context, service.InboundMessage) (service.InboundResult, error) {
				calls.Add(1)

				return service.InboundResult{}, fmt.Errorf("%w: postgres is down", port.ErrTransient)
			})
		},
	})
	wallet := f.openWallet(t, 100000)
	payload := body("msg-1", wallet, "tx-1", "BET", "25.00")

	f.publish(t, wallet.id, payload)

	dead := f.drainDeadLetters(t, 1, 90*time.Second)

	if len(dead) != 1 || dead[0].Body != payload {
		t.Fatalf("dead letters = %+v\nlogs:\n%s", dead, f.logs.String())
	}

	if calls.Load() < 5 || f.debits(t, wallet.id) != 0 || f.observer.count(sqs.OutcomeApplied) != 0 {
		t.Fatalf("handler calls = %d, debits = %d: the message must have been tried to the limit with no effect",
			calls.Load(), f.debits(t, wallet.id))
	}
}

func TestPermanentErrorsGoStraightToTheDeadLetterQueueWithTheirReason(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	wallet := f.openWallet(t, 100000)
	stranger := walletRef{id: "0192f291-27dd-7d3f-8071-000000000000", playerID: wallet.playerID}

	good := body("msg-conflict", wallet, "tx-1", "BET", "25.00")
	conflicting := body("msg-conflict", wallet, "tx-1", "BET", "99.00")
	cases := map[string]struct {
		payload string
		reason  string
	}{
		"malformed json":   {payload: "{not json", reason: "invalid_json"},
		"unsupported type": {payload: strings.Replace(good, "WagerTransactionRequested", "Other", 1), reason: "unsupported_type"},
		"opening kind":     {payload: body("m-open", wallet, "tx-2", "OPENING", "10.00"), reason: "invalid_field"},
		"invalid amount":   {payload: body("m-amount", wallet, "tx-3", "BET", "-5.00"), reason: "invalid_field"},
		"unknown wallet":   {payload: body("m-wallet", stranger, "tx-4", "BET", "10.00"), reason: "wallet_not_found"},
		"reused message":   {payload: conflicting, reason: "hash_conflict"},
	}

	f.publish(t, wallet.id, good)
	f.eventually(t, 30*time.Second, "the first message is applied", func() bool {
		return f.observer.count(sqs.OutcomeApplied) == 1
	})

	for _, test := range cases {
		f.publish(t, wallet.id, test.payload)
	}

	dead := f.drainDeadLetters(t, len(cases), 60*time.Second)

	if len(dead) != len(cases) {
		t.Fatalf("got %d dead letters, want %d\nlogs:\n%s", len(dead), len(cases), f.logs.String())
	}

	for name, test := range cases {
		assertDeadLetter(t, dead, test.payload, test.reason, name)
	}

	f.eventually(t, 20*time.Second, "every original is removed", func() bool {
		return f.queueIsEmpty(t, f.queues.Requests)
	})

	if f.debits(t, wallet.id) != 1 {
		t.Fatalf("debits = %d: only the first message may have moved money", f.debits(t, wallet.id))
	}
}

func assertDeadLetter(t *testing.T, dead []sqsclient.Message, payload string, reason string, name string) {
	t.Helper()

	for _, message := range dead {
		if message.Body != payload {
			continue
		}

		if message.Attributes["failureReason"] != reason || message.Attributes["receiveCount"] != "1" ||
			message.Attributes["originalMessageId"] == "" {
			t.Fatalf("%s: attributes = %v, want reason %s on the first attempt", name, message.Attributes, reason)
		}

		return
	}

	t.Fatalf("%s: the message is not in the dead-letter queue", name)
}

func TestBusinessRejectionsAndPendingReferencesAreDeleted(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	wallet := f.openWallet(t, 10000)

	f.publish(t, wallet.id, body("m-broke", wallet, "tx-1", "BET", "999999.00"))
	f.publish(t, wallet.id, strings.Replace(
		body("m-refund", wallet, "refund-1", "REFUND", "10.00"),
		`"money"`, `"referenceExternalTransactionId":"bet-9","money"`, 1))

	f.eventually(t, 30*time.Second, "both messages are concluded and deleted", func() bool {
		return f.observer.count(sqs.OutcomeApplied) == 2 && f.queueIsEmpty(t, f.queues.Requests)
	})

	rejected := f.count(t, `SELECT count(*) FROM wager_transactions WHERE status = 'REJECTED' AND failure_code = 'INSUFFICIENT_FUNDS'`)
	pending := f.count(t, `SELECT count(*) FROM wager_transactions WHERE status = 'PENDING_REFERENCE'`)

	if rejected != 1 || pending != 1 || f.observer.count(sqs.OutcomeDeadLettered) != 0 {
		t.Fatalf("rejected = %d, pending = %d: a business outcome is terminal, not a failure", rejected, pending)
	}
}

func TestMessagesOfOneWalletAreHandledInOrder(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	wallet := f.openWallet(t, 100000)

	for index := 1; index <= 5; index++ {
		f.publish(t, wallet.id, body(fmt.Sprintf("msg-%d", index), wallet, fmt.Sprintf("tx-%d", index), "BET", "1.00"))
	}

	f.eventually(t, 40*time.Second, "five debits", func() bool { return f.debits(t, wallet.id) == 5 })

	rows, err := f.pool.Query(context.Background(),
		`SELECT t.external_transaction_id
		   FROM wallet_ledger_entries l
		   JOIN wager_transactions t ON t.id = l.transaction_id
		  WHERE l.wallet_id = $1 AND l.direction = 'DEBIT'
		  ORDER BY l.created_at, l.id`, wallet.id)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var order []string
	for rows.Next() {
		var external string
		if err := rows.Scan(&external); err != nil {
			t.Fatalf("scan: %v", err)
		}

		order = append(order, external)
	}

	if fmt.Sprint(order) != "[tx-1 tx-2 tx-3 tx-4 tx-5]" {
		t.Fatalf("ledger order = %v, want the order of the messages", order)
	}
}

func TestADifferentWalletIsNotHeldUpByASlowOne(t *testing.T) {
	var slowWallet atomic.Value

	release := make(chan struct{})

	f := newFixture(t, fixtureOptions{
		wrapHandler: func(inner sqs.Handler) sqs.Handler {
			return handlerFunc(func(ctx context.Context, message service.InboundMessage) (service.InboundResult, error) {
				if message.Command.WalletID.String() == slowWallet.Load() {
					select {
					case <-release:
					case <-ctx.Done():
					}
				}

				return inner.Handle(ctx, message)
			})
		},
		deferStart: true,
	})
	slow := f.openWallet(t, 100000)
	fast := f.openWallet(t, 100000)
	slowWallet.Store(slow.id)

	f.publish(t, slow.id, body("m-slow", slow, "tx-slow", "BET", "5.00"))
	f.publish(t, fast.id, body("m-fast", fast, "tx-fast", "BET", "5.00"))
	f.start(t)

	f.eventually(t, 40*time.Second, "the fast wallet is processed while the slow one is held", func() bool {
		return f.debits(t, fast.id) == 1
	})

	if f.debits(t, slow.id) != 0 {
		t.Fatal("the slow wallet must still be held")
	}

	close(release)

	f.eventually(t, 30*time.Second, "the slow wallet finishes once released", func() bool {
		return f.debits(t, slow.id) == 1
	})
}

func TestAPoisonMessageDoesNotBlockTheNextOneOfTheSameGroup(t *testing.T) {
	f := newFixture(t, fixtureOptions{deferStart: true})
	wallet := f.openWallet(t, 100000)

	f.publish(t, wallet.id, "{poison")
	f.publish(t, wallet.id, body("m-good", wallet, "tx-1", "BET", "5.00"))
	f.start(t)

	f.eventually(t, 40*time.Second, "the poison is dead-lettered and the next message applied", func() bool {
		return f.observer.count(sqs.OutcomeDeadLettered) == 1 && f.observer.count(sqs.OutcomeApplied) == 1
	})

	if f.debits(t, wallet.id) != 1 {
		t.Fatalf("debits = %d", f.debits(t, wallet.id))
	}
}

func TestTheSameMessageDeliveredTwiceByTheBrokerIsAppliedOnce(t *testing.T) {
	f := newFixture(t, fixtureOptions{deferStart: true})
	wallet := f.openWallet(t, 100000)
	payload := body("msg-1", wallet, "tx-1", "BET", "25.00")

	f.publish(t, wallet.id, payload)
	f.publish(t, wallet.id, payload)
	f.start(t)

	f.eventually(t, 40*time.Second, "one application, one duplicate, both deleted", func() bool {
		return f.observer.count(sqs.OutcomeApplied) == 1 && f.observer.count(sqs.OutcomeDuplicate) == 1 &&
			f.queueIsEmpty(t, f.queues.Requests)
	})

	if f.debits(t, wallet.id) != 1 {
		t.Fatalf("debits = %d, want a single debit", f.debits(t, wallet.id))
	}
}

func TestAMissingQueueDoesNotKillTheConsumerAndStopIsPrompt(t *testing.T) {
	f := newFixture(t, fixtureOptions{
		deferStart: true,
		configure: func(config *sqs.Config) {
			config.Queues.Requests = broker.QueueURL("does-not-exist.fifo")
		},
	})
	f.start(t)

	time.Sleep(1500 * time.Millisecond)

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := f.consumer.Stop(ctx); err != nil || time.Since(started) > 4*time.Second {
		t.Fatalf("Stop = %v after %v", err, time.Since(started))
	}

	if !strings.Contains(f.logs.String(), "receive failed") {
		t.Fatal("the receive failures must be logged")
	}
}
