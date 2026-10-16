//go:build integration

package sqs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"apostas-go/internal/controller/sqs"
	"apostas-go/internal/platform/sqsclient"
	"apostas-go/internal/service"
)

func TestAGracefulStopFinishesTheWorkInProgressAndReceivesNothingMore(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	f := newFixture(t, fixtureOptions{
		wrapHandler: func(inner sqs.Handler) sqs.Handler {
			return handlerFunc(func(ctx context.Context, message service.InboundMessage) (service.InboundResult, error) {
				entered <- struct{}{}
				<-release

				return inner.Handle(ctx, message)
			})
		},
	})
	wallet := f.openWallet(t, 100000)

	f.publish(t, wallet.id, body("msg-1", wallet, "tx-1", "BET", "25.00"))

	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatalf("the message was never received\n%s", f.logs.String())
	}

	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		stopped <- f.consumer.Stop(ctx)
	}()

	select {
	case err := <-stopped:
		t.Fatalf("Stop returned (%v) while a message was still in progress", err)
	case <-time.After(500 * time.Millisecond):
	}

	close(release)

	if err := <-stopped; err != nil {
		t.Fatalf("Stop error = %v, want a clean stop", err)
	}

	if f.debits(t, wallet.id) != 1 || f.observer.count(sqs.OutcomeApplied) != 1 {
		t.Fatalf("debits = %d: the message in progress must complete", f.debits(t, wallet.id))
	}

	f.publish(t, wallet.id, body("msg-2", wallet, "tx-2", "BET", "5.00"))
	time.Sleep(3 * time.Second)

	visible, _ := f.depth(t, f.queues.Requests)
	if visible != 1 || f.debits(t, wallet.id) != 1 {
		t.Fatalf("visible = %d, debits = %d: a stopped consumer must not receive anything", visible, f.debits(t, wallet.id))
	}
}

func TestAStopDeadlineAbandonsTheWorkAndReleasesEveryMessage(t *testing.T) {
	entered := make(chan struct{}, 1)

	f := newFixture(t, fixtureOptions{
		wrapHandler: func(_ sqs.Handler) sqs.Handler {
			return handlerFunc(func(ctx context.Context, _ service.InboundMessage) (service.InboundResult, error) {
				entered <- struct{}{}
				<-ctx.Done()

				return service.InboundResult{}, ctx.Err()
			})
		},
		deferStart: true,
	})
	wallet := f.openWallet(t, 100000)

	for index := 1; index <= 3; index++ {
		f.publish(t, wallet.id, body("msg-"+string(rune('0'+index)), wallet, "tx-"+string(rune('0'+index)), "BET", "5.00"))
	}

	f.start(t)

	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatalf("the first message was never received\n%s", f.logs.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := f.consumer.Stop(ctx)

	if !errors.Is(err, sqs.ErrShutdownTimeout) {
		t.Fatalf("Stop error = %v, want %v", err, sqs.ErrShutdownTimeout)
	}

	if f.debits(t, wallet.id) != 0 {
		t.Fatal("abandoned work must leave no effect: the transaction is rolled back")
	}

	f.eventually(t, 20*time.Second, "all three messages are released back to the queue", func() bool {
		visible, inFlight := f.depth(t, f.queues.Requests)

		return visible == 3 && inFlight == 0
	})

	if f.observer.count(sqs.OutcomeReleased) != 3 || f.observer.count(sqs.OutcomeApplied) != 0 {
		t.Fatalf("released = %d, applied = %d", f.observer.count(sqs.OutcomeReleased), f.observer.count(sqs.OutcomeApplied))
	}

	again, err := f.client.Receive(context.Background(), sqsclient.ReceiveParams{
		QueueURL: f.queues.Requests, MaxMessages: 10, WaitSeconds: 1,
	})
	if err != nil || len(again) == 0 || again[0].ReceiveCount < 2 {
		t.Fatalf("redelivery = %+v (%v): a released message must be deliverable at once", again, err)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	f := newFixture(t, fixtureOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := f.consumer.Stop(ctx); err != nil {
		t.Fatalf("first Stop error = %v", err)
	}

	if err := f.consumer.Stop(ctx); err != nil {
		t.Fatalf("second Stop error = %v", err)
	}

	if err := f.consumer.Start(); !errors.Is(err, sqs.ErrAlreadyStarted) {
		t.Fatalf("Start after Stop error = %v, want %v", err, sqs.ErrAlreadyStarted)
	}
}

// Um ReceiveMessage cancelado pelo Stop continua registrado no broker, que entrega a próxima
// mensagem à requisição abandonada: ela fica invisível até o VisibilityTimeout e trava o grupo
// FIFO. O Stop espera a recepção em andamento terminar, então nada é engolido.
func TestAStopWhileIdleDoesNotLeaveALongPollThatSwallowsTheNextMessage(t *testing.T) {
	f := newFixture(t, fixtureOptions{
		configure: func(config *sqs.Config) { config.WaitSeconds = 5 },
	})
	wallet := f.openWallet(t, 100000)

	time.Sleep(time.Second) // o consumidor ocioso já está dentro de uma espera longa

	started := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := f.consumer.Stop(ctx); err != nil {
		t.Fatalf("Stop error = %v", err)
	}

	if took := time.Since(started); took > 8*time.Second {
		t.Fatalf("Stop took %v: it may wait for the long poll (5 s), not longer", took)
	}

	f.publish(t, wallet.id, body("msg-after-stop", wallet, "tx-1", "BET", "5.00"))

	f.eventually(t, 5*time.Second, "the message sent after the stop stays visible", func() bool {
		visible, inFlight := f.depth(t, f.queues.Requests)

		return visible == 1 && inFlight == 0
	})
}
