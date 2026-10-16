package sqsclient

import (
	"errors"
	"testing"
)

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		valid  bool
	}{
		{name: "region only", config: Config{Region: "us-east-1"}, valid: true},
		{
			name: "localstack",
			config: Config{
				Region: "us-east-1", Endpoint: "http://localhost:4566",
				AccessKeyID: "test", SecretAccessKey: "test",
			},
			valid: true,
		},
		{name: "missing region", config: Config{}},
		{name: "bad endpoint", config: Config{Region: "us-east-1", Endpoint: "localhost:4566"}},
		{name: "key without secret", config: Config{Region: "us-east-1", AccessKeyID: "test"}},
		{name: "secret without key", config: Config{Region: "us-east-1", SecretAccessKey: "test"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()

			if test.valid && err != nil || !test.valid && !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error = %v, want valid = %v", err, test.valid)
			}
		})
	}
}

func TestQueuesValidation(t *testing.T) {
	valid := Queues{
		Requests:   "http://localhost:4566/000000000000/wager-transactions.fifo",
		DeadLetter: "http://localhost:4566/000000000000/wager-transactions-dlq.fifo",
		Events:     "http://localhost:4566/000000000000/wager-events.fifo",
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("complete queues error = %v", err)
	}

	for _, broken := range []Queues{
		{DeadLetter: valid.DeadLetter, Events: valid.Events},
		{Requests: valid.Requests, Events: valid.Events},
		{Requests: valid.Requests, DeadLetter: valid.DeadLetter},
		{Requests: "nope", DeadLetter: valid.DeadLetter, Events: valid.Events},
	} {
		if err := broken.Validate(); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("queues %+v: error = %v, want %v", broken, err, ErrInvalidConfig)
		}
	}
}

func TestNewRejectsAnInvalidConfigWithoutTouchingTheNetwork(t *testing.T) {
	if _, err := New(t.Context(), Config{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidConfig)
	}
}

func TestTheDefaultTimeoutIsFilledIn(t *testing.T) {
	if got := (Config{Region: "x"}).withDefaults().RequestTimeout; got != defaultRequestTimeout {
		t.Fatalf("timeout = %v", got)
	}
}
