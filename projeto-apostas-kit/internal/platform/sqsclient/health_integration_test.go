//go:build integration

package sqsclient_test

import (
	"context"
	"testing"
	"time"

	"apostas-go/internal/platform/sqsclient"
)

func TestHealthIsUpWhenTheQueuesAnswer(t *testing.T) {
	client, queues := newClient(t)
	check := sqsclient.NewHealthCheck(client, queues.Requests, queues.Events)

	if check.Name() != "sqs" || check.Check(context.Background()) != nil {
		t.Fatalf("a healthy broker must pass, name = %q", check.Name())
	}
}

func TestHealthIsDownWhenAQueueIsMissing(t *testing.T) {
	client, queues := newClient(t)
	check := sqsclient.NewHealthCheck(client, queues.Requests, broker.QueueURL("gone.fifo"))

	if check.Check(context.Background()) == nil {
		t.Fatal("a missing queue must fail the check")
	}
}

func TestHealthIsDownWhenTheBrokerIsUnreachable(t *testing.T) {
	config := broker.Config()
	config.Endpoint = "http://127.0.0.1:1"
	config.RequestTimeout = time.Second

	client, err := sqsclient.New(context.Background(), config)
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	check := sqsclient.NewHealthCheck(client, broker.QueueURL("any.fifo"))

	if check.Check(context.Background()) == nil {
		t.Fatal("an unreachable broker must fail the check")
	}
}
