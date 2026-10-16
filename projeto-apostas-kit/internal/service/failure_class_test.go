package service

import (
	"errors"
	"fmt"
	"testing"

	"apostas-go/internal/model/money"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want FailureClass
	}{
		{name: "transient", err: port.ErrTransient, want: ClassTransient},
		{name: "wrapped transient", err: fmt.Errorf("db: %w", port.ErrTransient), want: ClassTransient},
		{name: "wallet not found", err: ErrWalletNotFound, want: ClassPermanent},
		{name: "idempotency conflict", err: ErrIdempotencyConflict, want: ClassPermanent},
		{name: "hash conflict", err: ErrInboundHashConflict, want: ClassPermanent},
		{name: "invalid message", err: ErrInvalidMessage, want: ClassPermanent},
		{name: "amount policy", err: wager.ErrAmountPolicy, want: ClassPermanent},
		{name: "money format", err: money.ErrInvalidFormat, want: ClassPermanent},
		{name: "wrapped permanent", err: fmt.Errorf("x: %w", wager.ErrInvalidKind), want: ClassPermanent},
		{name: "unknown", err: errors.New("boom"), want: ClassUnknown},
		{name: "nil", err: nil, want: ClassUnknown},
		{
			name: "transient beats permanent",
			err:  fmt.Errorf("%w while %w", ErrWalletNotFound, port.ErrTransient),
			want: ClassTransient,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Classify(test.err); got != test.want {
				t.Fatalf("Classify(%v) = %d, want %d", test.err, got, test.want)
			}
		})
	}
}

func TestAConflictOfInputIsNeverRetriedAsIfItWereTransient(t *testing.T) {
	for _, err := range permanentErrors {
		if Classify(err) != ClassPermanent {
			t.Fatalf("%v must be permanent", err)
		}
	}
}
