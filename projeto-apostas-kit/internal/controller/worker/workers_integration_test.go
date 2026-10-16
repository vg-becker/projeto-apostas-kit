//go:build integration

package worker_test

import (
	"context"
	"testing"
	"time"

	"apostas-go/internal/controller/worker"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/service"
)

func TestTheOutboxWorkerDeliversEveryEventToTheQueue(t *testing.T) {
	f := newFixture(t)
	target, total := f.walletWithBets(t, 3)
	startWorker(t, f.publisherWorker(t, f.outbox, f.queues.Events, fastPolicy))

	f.eventually(t, 30*time.Second, "the whole outbox is published", func() bool {
		return f.unpublished(t) == 0
	})

	delivered := f.drain(t, f.queues.Events, total, 30*time.Second)

	if len(delivered) != total {
		t.Fatalf("delivered %d messages, want %d", len(delivered), total)
	}

	f.assertDeliveredOnce(t, delivered)

	lastVersion := 0.0
	for _, message := range delivered {
		if message.GroupID != target.id.String() || message.Attributes["eventType"] != "WalletBalanceChanged" {
			continue
		}

		version := walletVersion(t, message.Body)
		if version <= lastVersion {
			t.Fatalf("wallet version %v after %v: events of one aggregate must stay in order", version, lastVersion)
		}

		lastVersion = version
	}

	if lastVersion != 4 {
		t.Fatalf("last wallet version = %v, want 4 (opening and three bets)", lastVersion)
	}
}

func TestALostConfirmationRepublishesAndTheDestinationStillHoldsOneCopy(t *testing.T) {
	f := newFixture(t)
	_, total := f.walletWithBets(t, 2)
	crashing := &lossyConfirmation{OutboxRepository: f.outbox, lose: total}
	startWorker(t, f.publisherWorker(t, crashing, f.queues.Events, fastPolicy))

	f.eventually(t, 40*time.Second, "events are republished after the lease and finally confirmed", func() bool {
		return f.unpublished(t) == 0
	})

	delivered := f.drain(t, f.queues.Events, total+3, 10*time.Second)

	if len(delivered) != total {
		t.Fatalf("destination holds %d messages, want %d: the broker must drop the republished copies", len(delivered), total)
	}

	f.assertDeliveredOnce(t, delivered)
}

func TestTwoPublisherWorkersNeverDuplicateDeliveries(t *testing.T) {
	f := newFixture(t)
	_, first := f.walletWithBets(t, 3)
	_, second := f.walletWithBets(t, 3)
	startWorker(t, f.publisherWorker(t, f.outbox, f.queues.Events, fastPolicy))
	startWorker(t, f.publisherWorker(t, f.outbox, f.queues.Events, fastPolicy))

	f.eventually(t, 30*time.Second, "everything is published", func() bool { return f.unpublished(t) == 0 })

	delivered := f.drain(t, f.queues.Events, first+second+4, 10*time.Second)

	if len(delivered) != first+second {
		t.Fatalf("destination holds %d messages, want %d", len(delivered), first+second)
	}

	f.assertDeliveredOnce(t, delivered)
}

func TestAnUnavailableDestinationLosesNothingAndRecovers(t *testing.T) {
	f := newFixture(t)
	_, total := f.walletWithBets(t, 2)
	missing := "recovered-later.fifo"
	startWorker(t, f.publisherWorker(t, f.outbox, broker.QueueURL(missing), fastPolicy))

	f.eventually(t, 30*time.Second, "the publisher keeps trying and backing off", func() bool {
		return f.count(t, `SELECT count(*) FROM outbox_events WHERE attempts >= 2`) == total
	})

	if f.unpublished(t) != total {
		t.Fatalf("unpublished = %d, no event may be lost while the destination is down", f.unpublished(t))
	}

	if _, err := f.client.CreateFIFOQueue(context.Background(), missing, nil); err != nil {
		t.Fatalf("create the destination: %v", err)
	}

	f.eventually(t, 40*time.Second, "everything is delivered once the destination exists", func() bool {
		return f.unpublished(t) == 0
	})

	delivered := f.drain(t, broker.QueueURL(missing), total+3, 10*time.Second)

	if len(delivered) != total {
		t.Fatalf("delivered %d messages, want %d", len(delivered), total)
	}

	f.assertDeliveredOnce(t, delivered)
}

func TestTheReferenceResolverWorkerFinishesPendingOperationsByItself(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)

	refund := f.command(t, target, wager.Refund, "refund-1", 2500)
	refund.ReferenceExternalTransactionID = "bet-1"

	waiting, err := f.messages.Process(context.Background(), refund)
	if err != nil || waiting.Status != wager.PendingReference {
		t.Fatalf("waiting = %+v (%v)", waiting, err)
	}

	f.bet(t, target, "bet-1", 2500)

	resolver := service.NewReferenceResolver(f.ops)
	running, err := worker.New(
		worker.Config{Name: "reference-resolver", Interval: 100 * time.Millisecond},
		worker.ReferenceResolverTask(resolver, 10),
		nil,
	)
	if err != nil {
		t.Fatalf("worker.New error = %v", err)
	}

	startWorker(t, running)

	f.eventually(t, 30*time.Second, "the refund is resolved without any manual call", func() bool {
		return f.count(t, `SELECT count(*) FROM wager_transactions WHERE kind = 'REFUND' AND status = 'PROCESSED'`) == 1
	})
}

func TestThePendingResumerWorkerRetakesAnAcceptedOperation(t *testing.T) {
	f := newFixture(t)
	target := f.openWallet(t, 100000)
	accepted := f.accept(t, f.command(t, target, wager.Bet, "bet-1", 2500))

	resumer := service.NewPendingResumer(f.ops)
	running, err := worker.New(
		worker.Config{Name: "pending-resumer", Interval: 100 * time.Millisecond},
		worker.PendingResumerTask(resumer, 10),
		nil,
	)
	if err != nil {
		t.Fatalf("worker.New error = %v", err)
	}

	startWorker(t, running)

	f.eventually(t, 30*time.Second, "the accepted bet is applied by the worker", func() bool {
		return f.count(t, `SELECT count(*) FROM wager_transactions WHERE id = $1 AND status = 'PROCESSED'`, accepted.String()) == 1
	})
}

func TestAStoppedWorkerDoesNothingMoreAndItsTerminationIsObservable(t *testing.T) {
	f := newFixture(t)
	running := f.publisherWorker(t, f.outbox, f.queues.Events, fastPolicy)

	if err := running.Start(); err != nil {
		t.Fatalf("Start error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := running.Stop(ctx); err != nil {
		t.Fatalf("Stop error = %v", err)
	}

	select {
	case <-running.Done():
	default:
		t.Fatal("Done must be closed after Stop")
	}

	f.walletWithBets(t, 1)
	time.Sleep(2 * time.Second)

	if f.unpublished(t) == 0 {
		t.Fatal("a stopped publisher must not publish anything")
	}
}
