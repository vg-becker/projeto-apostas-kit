package sqs

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"apostas-go/internal/model/money"
	"apostas-go/internal/platform/sqsclient"
	"apostas-go/internal/service"
)

func validConfig() Config {
	return Config{Queues: sqsclient.Queues{
		Requests:   "http://localhost:4566/000000000000/wager-transactions.fifo",
		DeadLetter: "http://localhost:4566/000000000000/wager-transactions-dlq.fifo",
	}}
}

func TestConfigDefaultsAndValidation(t *testing.T) {
	filled := validConfig().withDefaults()

	if filled.MaxInFlight != 20 || filled.BatchSize != 10 || filled.ProcessingTimeout != 30*time.Second ||
		filled.RetryBase != 5*time.Second || filled.RetryMax != 5*time.Minute {
		t.Fatalf("defaults = %+v", filled)
	}

	tests := []struct {
		name   string
		mutate func(config *Config)
		valid  bool
	}{
		{name: "complete", mutate: func(*Config) {}, valid: true},
		{name: "no requests queue", mutate: func(c *Config) { c.Queues.Requests = "" }},
		{name: "no dead-letter queue", mutate: func(c *Config) { c.Queues.DeadLetter = "" }},
		{name: "batch above the broker limit", mutate: func(c *Config) { c.BatchSize = 11 }},
		{name: "long poll above the broker limit", mutate: func(c *Config) { c.WaitSeconds = 21 }},
		{name: "retry base above the maximum", mutate: func(c *Config) {
			c.RetryBase = time.Minute
			c.RetryMax = time.Second
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			test.mutate(&config)

			err := config.withDefaults().Validate()

			if test.valid && err != nil || !test.valid && !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error = %v, want valid = %v", err, test.valid)
			}
		})
	}
}

func TestNewConsumerRejectsAnInvalidConfig(t *testing.T) {
	if _, err := NewConsumer(nil, nil, Config{}, nil, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidConfig)
	}
}

func TestVisibilityBackoffDoublesWithTheReceiveCountUpToTheCap(t *testing.T) {
	config := Config{RetryBase: 5 * time.Second, RetryMax: time.Minute}

	tests := []struct {
		receiveCount int
		want         time.Duration
	}{
		{receiveCount: 0, want: 5 * time.Second},
		{receiveCount: 1, want: 5 * time.Second},
		{receiveCount: 2, want: 10 * time.Second},
		{receiveCount: 3, want: 20 * time.Second},
		{receiveCount: 4, want: 40 * time.Second},
		{receiveCount: 5, want: time.Minute},
		{receiveCount: 50, want: time.Minute},
	}

	for _, test := range tests {
		if got := visibilityBackoff(config, test.receiveCount); got != test.want {
			t.Fatalf("backoff(%d) = %v, want %v", test.receiveCount, got, test.want)
		}
	}

	if got := visibilityBackoff(Config{RetryBase: 200 * time.Millisecond, RetryMax: time.Second}, 1); got != time.Second {
		t.Fatalf("sub-second backoff = %v, the broker works in whole seconds (minimum 1)", got)
	}
}

func message(id string, group string) sqsclient.Message {
	return sqsclient.Message{MessageID: id, GroupID: group}
}

func TestOrderedGroupsKeepTheArrivalOrderAndSeparateTheGroups(t *testing.T) {
	groups := orderedGroups([]sqsclient.Message{
		message("a1", "A"),
		message("b1", "B"),
		message("a2", "A"),
		message("n1", ""),
		message("n2", ""),
		message("a3", "A"),
	})

	var got []string
	for _, group := range groups {
		line := ""
		for _, item := range group {
			line += item.MessageID + " "
		}

		got = append(got, line)
	}

	want := []string{"a1 a2 a3 ", "b1 ", "n1 ", "n2 "}

	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("groups = %q, want %q", got, want)
	}
}

func TestFailureReasonsAreStableAndFreeOfPayload(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "parse failure", err: &ParseError{Reason: ReasonInvalidJSON, Err: errors.New("x")}, want: "invalid_json"},
		{name: "hash conflict", err: fmt.Errorf("%w: m", service.ErrInboundHashConflict), want: "hash_conflict"},
		{name: "idempotency conflict", err: service.ErrIdempotencyConflict, want: "idempotency_conflict"},
		{name: "wallet not found", err: service.ErrWalletNotFound, want: "wallet_not_found"},
		{name: "invalid message", err: service.ErrInvalidMessage, want: "invalid_message"},
		{name: "other input errors", err: money.ErrScaleExceeded, want: "invalid_input"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := failureReason(test.err); got != test.want {
				t.Fatalf("reason = %q, want %q", got, test.want)
			}
		})
	}
}

func TestStopBeforeStartIsHarmlessAndStartTwiceIsRefused(t *testing.T) {
	consumer, err := NewConsumer(nil, nil, validConfig(), nil, nil)
	if err != nil {
		t.Fatalf("NewConsumer error = %v", err)
	}

	if err := consumer.Stop(t.Context()); err != nil {
		t.Fatalf("Stop before Start error = %v", err)
	}
}
