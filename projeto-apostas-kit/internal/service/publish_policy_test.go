package service

import (
	"testing"
	"time"
)

func TestPublishBackoffDoublesUntilTheCap(t *testing.T) {
	policy := PublishPolicy{InitialBackoff: time.Second, MaxBackoff: 5 * time.Second}.withDefaults()

	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 0, want: time.Second},
		{attempts: 1, want: 2 * time.Second},
		{attempts: 2, want: 4 * time.Second},
		{attempts: 3, want: 5 * time.Second},
		{attempts: 100, want: 5 * time.Second},
	}

	for _, test := range tests {
		if got := policy.backoff(test.attempts); got != test.want {
			t.Fatalf("backoff(%d) = %v, want %v", test.attempts, got, test.want)
		}
	}
}

func TestZeroPublishPolicyFallsBackToDefaults(t *testing.T) {
	if got := (PublishPolicy{}).withDefaults(); got != DefaultPublishPolicy() {
		t.Fatalf("policy = %+v, want %+v", got, DefaultPublishPolicy())
	}
}
