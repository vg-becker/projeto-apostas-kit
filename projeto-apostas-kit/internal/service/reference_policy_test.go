package service

import (
	"testing"
	"time"
)

func TestBackoffDoublesUntilTheCap(t *testing.T) {
	policy := ReferencePolicy{InitialBackoff: 2 * time.Second, MaxBackoff: 10 * time.Second}.
		withDefaults()

	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 0, want: 2 * time.Second},
		{attempts: 1, want: 4 * time.Second},
		{attempts: 2, want: 8 * time.Second},
		{attempts: 3, want: 10 * time.Second},
		{attempts: 40, want: 10 * time.Second},
	}

	for _, test := range tests {
		if got := policy.backoff(test.attempts); got != test.want {
			t.Fatalf("backoff(%d) = %v, want %v", test.attempts, got, test.want)
		}
	}
}

func TestExpiryByAttemptsOrTTL(t *testing.T) {
	policy := ReferencePolicy{MaxAttempts: 3, TTL: time.Minute}.withDefaults()
	created := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		attempts int
		elapsed  time.Duration
		want     bool
	}{
		{name: "fresh", attempts: 0, elapsed: 0, want: false},
		{name: "below both limits", attempts: 2, elapsed: 59 * time.Second, want: false},
		{name: "attempts exhausted", attempts: 3, elapsed: time.Second, want: true},
		{name: "ttl reached", attempts: 1, elapsed: time.Minute, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := policy.expired(created, test.attempts, created.Add(test.elapsed))

			if got != test.want {
				t.Fatalf("expired = %v, want %v", got, test.want)
			}
		})
	}
}

func TestZeroPolicyFallsBackToDefaults(t *testing.T) {
	got := ReferencePolicy{}.withDefaults()

	if got != DefaultReferencePolicy() {
		t.Fatalf("policy = %+v, want the defaults %+v", got, DefaultReferencePolicy())
	}
}
