//go:build integration

package sqsclient_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"apostas-go/internal/model/port"
	"apostas-go/internal/platform/sqsclient"
	"apostas-go/internal/testsupport/localstack"
)

func newClient(t *testing.T) (*sqsclient.Client, sqsclient.Queues) {
	t.Helper()

	client, err := sqsclient.New(context.Background(), broker.Config())
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	return client, broker.Isolated(t, client)
}

func send(t *testing.T, client *sqsclient.Client, queueURL string, group string, dedup string, body string) {
	t.Helper()

	_, err := client.Send(context.Background(), sqsclient.SendParams{
		QueueURL:        queueURL,
		Body:            body,
		GroupID:         group,
		DeduplicationID: dedup,
	})
	if err != nil {
		t.Fatalf("send %s: %v", dedup, err)
	}
}

func receive(t *testing.T, client *sqsclient.Client, queueURL string, max int, visibility int) []sqsclient.Message {
	t.Helper()

	messages, err := client.Receive(context.Background(), sqsclient.ReceiveParams{
		QueueURL:                 queueURL,
		MaxMessages:              max,
		WaitSeconds:              1,
		VisibilityTimeoutSeconds: visibility,
	})
	if err != nil {
		t.Fatalf("receive: %v", err)
	}

	return messages
}

func queueAttributes(t *testing.T, client *sqsclient.Client, queueURL string) map[string]string {
	t.Helper()

	attributes, err := client.QueueAttributes(context.Background(), queueURL)
	if err != nil {
		t.Fatalf("attributes of %s: %v", queueURL, err)
	}

	return attributes
}

func TestTheProvisioningScriptCreatesTheContractQueues(t *testing.T) {
	client, err := sqsclient.New(context.Background(), broker.Config())
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	queues := broker.Provisioned()
	requests := queueAttributes(t, client, queues.Requests)
	deadLetter := queueAttributes(t, client, queues.DeadLetter)
	events := queueAttributes(t, client, queues.Events)

	for name, attributes := range map[string]map[string]string{"requests": requests, "dlq": deadLetter, "events": events} {
		if attributes["FifoQueue"] != "true" || attributes["ContentBasedDeduplication"] != "false" {
			t.Fatalf("%s queue must be FIFO with explicit deduplication ids: %v", name, attributes)
		}
	}

	assertRedrive(t, requests, deadLetter["QueueArn"])

	if !strings.Contains(requests["Policy"], "TrustedAccountOnly") {
		t.Fatalf("the access policy must be declared on the requests queue: %s", requests["Policy"])
	}
}

func assertRedrive(t *testing.T, requests map[string]string, deadLetterARN string) {
	t.Helper()

	redrive := requests["RedrivePolicy"]
	quoted := strings.Contains(redrive, fmt.Sprintf(`"maxReceiveCount":"%d"`, localstack.MaxReceiveCount))
	numeric := strings.Contains(redrive, fmt.Sprintf(`"maxReceiveCount":%d`, localstack.MaxReceiveCount))

	if requests["VisibilityTimeout"] != fmt.Sprint(localstack.VisibilityTimeout) ||
		!strings.Contains(redrive, deadLetterARN) || !quoted && !numeric {
		t.Fatalf("visibility = %s, redrive = %s", requests["VisibilityTimeout"], redrive)
	}
}

func TestSendReceiveAndDelete(t *testing.T) {
	client, queues := newClient(t)
	send(t, client, queues.Requests, "wallet-1", "m-1", `{"hello":"world"}`)

	received := receive(t, client, queues.Requests, 10, 0)

	if len(received) != 1 || received[0].Body != `{"hello":"world"}` ||
		received[0].GroupID != "wallet-1" || received[0].ReceiveCount != 1 {
		t.Fatalf("received = %+v", received)
	}

	if err := client.Delete(context.Background(), queues.Requests, received[0].ReceiptHandle); err != nil {
		t.Fatalf("Delete error = %v", err)
	}

	if again := receive(t, client, queues.Requests, 10, 0); len(again) != 0 {
		t.Fatalf("a deleted message came back: %+v", again)
	}
}

func TestAnUndeletedMessageComesBackWithAHigherReceiveCount(t *testing.T) {
	client, queues := newClient(t)
	send(t, client, queues.Requests, "wallet-1", "m-1", "body")

	first := receive(t, client, queues.Requests, 1, 1)
	time.Sleep(2 * time.Second)
	second := receive(t, client, queues.Requests, 1, 1)

	if len(first) != 1 || len(second) != 1 || first[0].MessageID != second[0].MessageID ||
		second[0].ReceiveCount != 2 {
		t.Fatalf("first = %+v, second = %+v", first, second)
	}
}

func TestChangeVisibilityReleasesOrPostponesAMessage(t *testing.T) {
	client, queues := newClient(t)
	send(t, client, queues.Requests, "wallet-1", "m-1", "body")

	held := receive(t, client, queues.Requests, 1, 30)
	if len(held) != 1 {
		t.Fatalf("held = %+v", held)
	}

	if hidden := receive(t, client, queues.Requests, 1, 0); len(hidden) != 0 {
		t.Fatalf("a message in flight must not be delivered: %+v", hidden)
	}

	if err := client.ChangeVisibility(context.Background(), queues.Requests, held[0].ReceiptHandle, 0); err != nil {
		t.Fatalf("release error = %v", err)
	}

	released := receive(t, client, queues.Requests, 1, 30)

	if len(released) != 1 || released[0].MessageID != held[0].MessageID {
		t.Fatalf("a released message must be deliverable at once: %+v", released)
	}
}

func TestFIFOKeepsTheOrderInAGroupAndBlocksItWhileOneMessageIsInFlight(t *testing.T) {
	client, queues := newClient(t)
	for index := 1; index <= 3; index++ {
		send(t, client, queues.Requests, "group-a", fmt.Sprintf("a-%d", index), fmt.Sprintf("a%d", index))
	}

	send(t, client, queues.Requests, "group-b", "b-1", "b1")

	first := receive(t, client, queues.Requests, 1, 30)
	if len(first) != 1 || first[0].Body != "a1" {
		t.Fatalf("the first message of the group must come first: %+v", first)
	}

	others := receive(t, client, queues.Requests, 10, 30)

	for _, message := range others {
		if message.GroupID == "group-a" {
			t.Fatalf("group-a has a message in flight, its next one must wait: %+v", others)
		}
	}

	if len(others) != 1 || others[0].Body != "b1" {
		t.Fatalf("another group must keep flowing: %+v", others)
	}
}

func TestTheBrokerDeduplicatesTheSameDeduplicationID(t *testing.T) {
	client, queues := newClient(t)
	send(t, client, queues.Requests, "wallet-1", "same-id", "body")
	send(t, client, queues.Requests, "wallet-1", "same-id", "body")

	received := receive(t, client, queues.Requests, 10, 0)

	if len(received) != 1 {
		t.Fatalf("received %d messages, the broker must deduplicate: %+v", len(received), received)
	}
}

// releaseInFlight devolve imediatamente à fila tudo que o consumidor acabou de receber.
func releaseInFlight(t *testing.T, client *sqsclient.Client, queueURL string) {
	t.Helper()

	for _, message := range receive(t, client, queueURL, 1, 30) {
		if err := client.ChangeVisibility(context.Background(), queueURL, message.ReceiptHandle, 0); err != nil {
			t.Fatalf("release: %v", err)
		}
	}
}

func TestAMessageThatIsNeverDeletedEndsUpInTheDeadLetterQueue(t *testing.T) {
	client, queues := newClient(t)
	send(t, client, queues.Requests, "wallet-1", "m-1", "poison")

	for range localstack.MaxReceiveCount + 3 {
		releaseInFlight(t, client, queues.Requests)

		if dead := receive(t, client, queues.DeadLetter, 1, 0); len(dead) == 1 {
			if dead[0].Body != "poison" {
				t.Fatalf("dead letter = %+v", dead)
			}

			return
		}
	}

	t.Fatal("the message never reached the dead-letter queue after exceeding maxReceiveCount")
}

func TestMessageAttributesTravelWithTheMessage(t *testing.T) {
	client, queues := newClient(t)

	_, err := client.Send(context.Background(), sqsclient.SendParams{
		QueueURL: queues.Events, Body: "{}", GroupID: "g", DeduplicationID: "d",
		Attributes: map[string]string{"eventType": "WalletBalanceChanged"},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	received := receive(t, client, queues.Events, 1, 0)

	if len(received) != 1 || received[0].Attributes["eventType"] != "WalletBalanceChanged" {
		t.Fatalf("received = %+v", received)
	}
}

func TestErrorsAreClassifiedAsPermanentOrTransient(t *testing.T) {
	client, queues := newClient(t)
	missing := broker.QueueURL("does-not-exist.fifo")

	_, missingErr := client.Send(context.Background(), sqsclient.SendParams{
		QueueURL: missing, Body: "x", GroupID: "g", DeduplicationID: "d",
	})
	_, oversizedErr := client.Send(context.Background(), sqsclient.SendParams{
		QueueURL: queues.Requests, Body: strings.Repeat("x", 300<<10), GroupID: "g", DeduplicationID: "d",
	})

	if !errors.Is(missingErr, sqsclient.ErrQueueNotFound) || errors.Is(missingErr, port.ErrTransient) {
		t.Fatalf("missing queue error = %v, want a permanent %v", missingErr, sqsclient.ErrQueueNotFound)
	}

	if !errors.Is(oversizedErr, sqsclient.ErrRejected) || errors.Is(oversizedErr, port.ErrTransient) {
		t.Fatalf("a message above 256 KiB must be refused permanently: %v", oversizedErr)
	}

	unreachable := broker.Config()
	unreachable.Endpoint = "http://127.0.0.1:1"
	unreachable.RequestTimeout = 2 * time.Second

	lost, err := sqsclient.New(context.Background(), unreachable)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	_, networkErr := lost.Send(context.Background(), sqsclient.SendParams{
		QueueURL: queues.Requests, Body: "x", GroupID: "g", DeduplicationID: "d",
	})

	if !errors.Is(networkErr, port.ErrTransient) {
		t.Fatalf("an unreachable broker must be transient: %v", networkErr)
	}
}

func TestApproximateMessagesReportsTheQueueDepth(t *testing.T) {
	client, queues := newClient(t)

	for index, group := range []string{"g1", "g2", "g3"} {
		send(t, client, queues.DeadLetter, group, "dedup-"+group, "body-"+string(rune('a'+index)))
	}

	deadline := time.Now().Add(15 * time.Second)

	for {
		depth, err := client.ApproximateMessages(context.Background(), queues.DeadLetter)
		if err != nil {
			t.Fatalf("ApproximateMessages error = %v", err)
		}

		if depth == 3 {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("depth = %d, want 3", depth)
		}

		time.Sleep(200 * time.Millisecond)
	}
}

func TestApproximateMessagesFailsForAMissingQueue(t *testing.T) {
	client, queues := newClient(t)

	if _, err := client.ApproximateMessages(context.Background(), queues.Requests+"-missing"); err == nil {
		t.Fatal("want an error for a queue that does not exist")
	}
}
