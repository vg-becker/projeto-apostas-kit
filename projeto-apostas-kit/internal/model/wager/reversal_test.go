package wager

import (
	"errors"
	"testing"

	"apostas-go/internal/model/ledger"
)

func processedReference(t *testing.T, kind Kind, minorUnits int64) *Transaction {
	t.Helper()

	params := betParams(t)
	params.Kind = kind
	params.Money = brl(t, minorUnits)
	if kind == Loss {
		params.Money = brl(t, 0)
	}

	if kind.IsReversal() {
		params.ReferenceExternalTransactionID = "transaction-0"
	}

	transaction := mustExternal(t, params)
	result := ProcessedResult{Balance: brl(t, 100000)}
	if kind.IsReversal() {
		result.ResolvedReferenceID = referenceID
	}

	if err := transaction.MarkProcessed(result, later); err != nil {
		t.Fatalf("MarkProcessed error = %v", err)
	}

	return transaction
}

func TestValidateReversal(t *testing.T) {
	tests := []struct {
		name            string
		reversalKind    Kind
		referenceKind   Kind
		reverseAmount   int64
		alreadyReversed bool
		mutate          func(params *ExternalParams)
		wantCode        FailureCode
	}{
		{name: "refund of bet", reversalKind: Refund, referenceKind: Bet, reverseAmount: 2500},
		{name: "rollback of bet", reversalKind: Rollback, referenceKind: Bet, reverseAmount: 2500},
		{name: "rollback of win", reversalKind: Rollback, referenceKind: Win, reverseAmount: 2500},
		{name: "rollback of refund", reversalKind: Rollback, referenceKind: Refund,
			reverseAmount: 2500},
		{name: "refund of win", reversalKind: Refund, referenceKind: Win, reverseAmount: 2500,
			wantCode: ReferenceKindInvalid},
		{name: "refund of loss", reversalKind: Refund, referenceKind: Loss, reverseAmount: 2500,
			wantCode: ReferenceKindInvalid},
		{name: "rollback of rollback", reversalKind: Rollback, referenceKind: Rollback,
			reverseAmount: 2500, wantCode: ReferenceKindInvalid},
		{name: "partial amount", reversalKind: Refund, referenceKind: Bet, reverseAmount: 1000,
			wantCode: AmountMismatch},
		{name: "already reversed by refund or rollback", reversalKind: Rollback,
			referenceKind: Bet, reverseAmount: 2500, alreadyReversed: true,
			wantCode: AlreadyReversed},
		{name: "different provider", reversalKind: Refund, referenceKind: Bet,
			reverseAmount: 2500, wantCode: ReferenceMismatch,
			mutate: func(p *ExternalParams) { p.ProviderID = "provider-b" }},
		{name: "different round", reversalKind: Refund, referenceKind: Bet,
			reverseAmount: 2500, wantCode: ReferenceMismatch,
			mutate: func(p *ExternalParams) { p.RoundID = "round-1" }},
		{name: "different player", reversalKind: Refund, referenceKind: Bet,
			reverseAmount: 2500, wantCode: ReferenceMismatch,
			mutate: func(p *ExternalParams) {
				p.PlayerID = "0192f28f-5dc0-7d58-bdb2-000000000000"
			}},
		{name: "different wallet", reversalKind: Refund, referenceKind: Bet,
			reverseAmount: 2500, wantCode: ReferenceMismatch,
			mutate: func(p *ExternalParams) {
				p.WalletID = "0192f291-27dd-7d3f-8071-000000000000"
			}},
		{name: "different reference id", reversalKind: Refund, referenceKind: Bet,
			reverseAmount: 2500, wantCode: ReferenceMismatch,
			mutate: func(p *ExternalParams) { p.ReferenceExternalTransactionID = "other" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reference := processedReference(t, test.referenceKind, 2500)
			params := reversalParams(t, test.reversalKind)
			params.Money = brl(t, test.reverseAmount)
			if test.mutate != nil {
				test.mutate(&params)
			}

			err := ValidateReversal(mustExternal(t, params), reference, test.alreadyReversed)

			assertRejection(t, err, test.wantCode)
		})
	}
}

func assertRejection(t *testing.T, err error, wantCode FailureCode) {
	t.Helper()

	if wantCode == "" {
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}

		return
	}

	var rejection *Rejection
	if !errors.As(err, &rejection) || rejection.Code != wantCode {
		t.Fatalf("error = %v, want rejection %s", err, wantCode)
	}
}

func TestValidateReversalWithUnfinishedReference(t *testing.T) {
	reference := mustExternal(t, betParams(t))
	reversal := mustExternal(t, reversalParams(t, Refund))

	err := ValidateReversal(reversal, reference, false)

	if !errors.Is(err, ErrReferencePending) {
		t.Fatalf("error = %v, want %v", err, ErrReferencePending)
	}
}

func TestValidateReversalWithUnsuccessfulReference(t *testing.T) {
	rejected := mustExternal(t, betParams(t))
	if err := rejected.Reject(InsufficientFunds, later); err != nil {
		t.Fatalf("reject error = %v", err)
	}

	failed := mustExternal(t, betParams(t))
	if err := failed.Fail(later); err != nil {
		t.Fatalf("fail error = %v", err)
	}

	for _, reference := range []*Transaction{rejected, failed} {
		reversal := mustExternal(t, reversalParams(t, Refund))

		assertRejection(t, ValidateReversal(reversal, reference, false), ReferenceNotProcessed)
	}
}

func TestValidateReversalRequiresReversalKind(t *testing.T) {
	bet := mustExternal(t, betParams(t))

	err := ValidateReversal(bet, processedReference(t, Bet, 2500), false)

	if !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidKind)
	}
}

func TestReversalDirection(t *testing.T) {
	tests := []struct {
		name      string
		reference Kind
		want      ledger.Direction
		wantErr   error
	}{
		{name: "bet is credited back", reference: Bet, want: ledger.Credit},
		{name: "win is debited back", reference: Win, want: ledger.Debit},
		{name: "refund is debited back", reference: Refund, want: ledger.Debit},
		{name: "loss has no movement", reference: Loss, wantErr: ErrInvalidKind},
		{name: "rollback is not reversible", reference: Rollback, wantErr: ErrInvalidKind},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ReversalDirection(test.reference)

			if !errors.Is(err, test.wantErr) || got != test.want {
				t.Fatalf("got (%q, %v)", got, err)
			}
		})
	}
}
