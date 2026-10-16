package wager

import (
	"errors"
	"testing"
)

func winParams(t *testing.T, minorUnits int64) ExternalParams {
	t.Helper()

	params := betParams(t)
	params.ExternalTransactionID = "transaction-789"
	params.IdempotencyKey = "provider-a:transaction-789"
	params.Kind = Win
	params.Money = brl(t, minorUnits)
	params.ReferenceExternalTransactionID = "transaction-123"

	return params
}

func TestValidateWinReference(t *testing.T) {
	tests := []struct {
		name          string
		referenceKind Kind
		mutate        func(params *ExternalParams)
		wantCode      FailureCode
	}{
		{name: "bet of the same round", referenceKind: Bet},
		{
			name:          "win amount is free",
			referenceKind: Bet,
			mutate:        func(p *ExternalParams) { p.Money = brl(t, 99900) },
		},
		{name: "referencing a win", referenceKind: Win, wantCode: ReferenceKindInvalid},
		{
			name:          "other round",
			referenceKind: Bet,
			mutate:        func(p *ExternalParams) { p.RoundID = "round-1" },
			wantCode:      ReferenceMismatch,
		},
		{
			name:          "other provider",
			referenceKind: Bet,
			mutate:        func(p *ExternalParams) { p.ProviderID = "provider-b" },
			wantCode:      ReferenceMismatch,
		},
		{
			name:          "other wallet",
			referenceKind: Bet,
			mutate: func(p *ExternalParams) {
				p.WalletID = "0192f291-27dd-7d3f-8071-000000000000"
			},
			wantCode: ReferenceMismatch,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reference := processedReference(t, test.referenceKind, 2500)
			params := winParams(t, 5000)
			if test.mutate != nil {
				test.mutate(&params)
			}

			err := ValidateWinReference(mustExternal(t, params), reference)

			assertRejection(t, err, test.wantCode)
		})
	}
}

func TestValidateWinReferenceWithUnfinishedOrFailedReference(t *testing.T) {
	win := mustExternal(t, winParams(t, 5000))

	pending := mustExternal(t, betParams(t))
	if err := ValidateWinReference(win, pending); !errors.Is(err, ErrReferencePending) {
		t.Fatalf("pending error = %v, want %v", err, ErrReferencePending)
	}

	rejected := mustExternal(t, betParams(t))
	if err := rejected.Reject(InsufficientFunds, later); err != nil {
		t.Fatalf("reject error = %v", err)
	}

	assertRejection(t, ValidateWinReference(win, rejected), ReferenceNotProcessed)
}

func TestValidateWinReferenceRequiresAWin(t *testing.T) {
	bet := mustExternal(t, betParams(t))

	err := ValidateWinReference(bet, processedReference(t, Bet, 2500))

	if !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidKind)
	}
}

func TestCorrelationMetadataIsOptionalAndValidated(t *testing.T) {
	params := betParams(t)
	params.CorrelationID = "correlation-1"
	params.CausationID = "message-1"

	stored := mustExternal(t, params)
	if stored.CorrelationID() != "correlation-1" || stored.CausationID() != "message-1" {
		t.Fatalf("metadata = %q / %q", stored.CorrelationID(), stored.CausationID())
	}

	params.CorrelationID = " "
	if _, err := NewExternal(params); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("blank correlation error = %v, want %v", err, ErrInvalidTransaction)
	}
}
