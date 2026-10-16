//go:build integration

package sqsclient_test

import (
	"context"
	"testing"
	"time"

	"apostas-go/internal/model/event"
	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/port"
	"apostas-go/internal/platform/sqsclient"
)

func outboxRecord(eventID string) port.OutboxRecord {
	return port.OutboxRecord{
		EventID:     identity.EventID(eventID),
		AggregateID: "0192f291-27dd-7d3f-8071-5f8685deef37",
		EventType:   event.TypeWalletBalanceChanged,
		Payload:     []byte(`{"eventId":"` + eventID + `","data":{"walletVersion":2}}`),
		OccurredAt:  time.Now(),
	}
}

func TestPublishDeliversTheSnapshotWithTheRoutingContract(t *testing.T) {
	client, queues := newClient(t)
	publisher := sqsclient.NewEventPublisher(client, queues.Events)
	record := outboxRecord("0192f2a0-0000-7e38-af88-e43f851a819d")

	if err := publisher.Publish(context.Background(), record); err != nil {
		t.Fatalf("Publish error = %v", err)
	}

	received := receive(t, client, queues.Events, 10, 0)

	if len(received) != 1 || received[0].Body != string(record.Payload) ||
		received[0].GroupID != record.AggregateID ||
		received[0].Attributes["eventType"] != "WalletBalanceChanged" ||
		received[0].Attributes["eventId"] != record.EventID.String() {
		t.Fatalf("received = %+v", received)
	}
}

func TestRepublishingTheSameEventIDDoesNotDuplicateTheMessage(t *testing.T) {
	client, queues := newClient(t)
	publisher := sqsclient.NewEventPublisher(client, queues.Events)
	record := outboxRecord("0192f2a0-0000-7e38-af88-e43f851a8000")

	for range 3 {
		if err := publisher.Publish(context.Background(), record); err != nil {
			t.Fatalf("Publish error = %v", err)
		}
	}

	if received := receive(t, client, queues.Events, 10, 0); len(received) != 1 {
		t.Fatalf("received %d copies, the broker must deduplicate by event id", len(received))
	}
}

func TestPublishingToAMissingQueueIsAPermanentError(t *testing.T) {
	client, _ := newClient(t)
	publisher := sqsclient.NewEventPublisher(client, broker.QueueURL("gone.fifo"))

	err := publisher.Publish(context.Background(), outboxRecord("0192f2a0-0000-7e38-af88-e43f851a8001"))

	if err == nil {
		t.Fatal("publishing to a missing queue must fail")
	}
}
