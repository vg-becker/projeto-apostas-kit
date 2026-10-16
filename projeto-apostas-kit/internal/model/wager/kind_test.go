package wager

import (
	"errors"
	"testing"

	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/money"
)

func brl(t *testing.T, minorUnits int64) money.Money {
	t.Helper()

	amount, err := money.New(minorUnits, money.BRL)
	if err != nil {
		t.Fatalf("money.New error = %v", err)
	}

	return amount
}

func TestParseExternalKind(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Kind
		wantErr error
	}{
		{name: "bet", input: "BET", want: Bet},
		{name: "win", input: "WIN", want: Win},
		{name: "loss", input: "LOSS", want: Loss},
		{name: "refund", input: "REFUND", want: Refund},
		{name: "rollback", input: "ROLLBACK", want: Rollback},
		{name: "opening is internal", input: "OPENING", wantErr: ErrOpeningNotAllowed},
		{name: "lowercase", input: "bet", wantErr: ErrInvalidKind},
		{name: "empty", input: "", wantErr: ErrInvalidKind},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseExternalKind(test.input)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}

			if got != test.want {
				t.Fatalf("kind = %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidateAmountPolicy(t *testing.T) {
	tests := []struct {
		name    string
		kind    Kind
		minor   int64
		wantErr error
	}{
		{name: "bet positive", kind: Bet, minor: 100},
		{name: "bet zero", kind: Bet, minor: 0, wantErr: ErrAmountPolicy},
		{name: "bet negative", kind: Bet, minor: -100, wantErr: ErrAmountPolicy},
		{name: "win zero", kind: Win, minor: 0, wantErr: ErrAmountPolicy},
		{name: "refund zero", kind: Refund, minor: 0, wantErr: ErrAmountPolicy},
		{name: "rollback zero", kind: Rollback, minor: 0, wantErr: ErrAmountPolicy},
		{name: "opening positive", kind: Opening, minor: 100},
		{name: "opening zero", kind: Opening, minor: 0, wantErr: ErrAmountPolicy},
		{name: "loss zero", kind: Loss, minor: 0},
		{name: "loss positive", kind: Loss, minor: 1, wantErr: ErrAmountPolicy},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.kind.ValidateAmount(brl(t, test.minor))

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateAmountRejectsUninitialized(t *testing.T) {
	err := Bet.ValidateAmount(money.Money{})

	if !errors.Is(err, money.ErrUninitialized) {
		t.Fatalf("error = %v, want %v", err, money.ErrUninitialized)
	}
}

func TestKindDirection(t *testing.T) {
	tests := []struct {
		name          string
		kind          Kind
		wantDirection ledger.Direction
		wantOK        bool
	}{
		{name: "bet debits", kind: Bet, wantDirection: ledger.Debit, wantOK: true},
		{name: "win credits", kind: Win, wantDirection: ledger.Credit, wantOK: true},
		{name: "refund credits", kind: Refund, wantDirection: ledger.Credit, wantOK: true},
		{name: "opening credits", kind: Opening, wantDirection: ledger.Credit, wantOK: true},
		{name: "loss has none", kind: Loss},
		{name: "rollback depends on reference", kind: Rollback},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			direction, ok := test.kind.Direction()

			if direction != test.wantDirection || ok != test.wantOK {
				t.Fatalf("got (%q, %v)", direction, ok)
			}
		})
	}
}

func TestReferencePolicyPerKind(t *testing.T) {
	tests := []struct {
		kind      Kind
		required  bool
		permitted bool
	}{
		{kind: Bet},
		{kind: Loss},
		{kind: Win, permitted: true},
		{kind: Refund, required: true, permitted: true},
		{kind: Rollback, required: true, permitted: true},
	}

	for _, test := range tests {
		t.Run(test.kind.String(), func(t *testing.T) {
			if test.kind.RequiresReference() != test.required ||
				test.kind.AllowsReference() != test.permitted {
				t.Fatalf("reference policy wrong for %s", test.kind)
			}
		})
	}
}

func TestFailureCodes(t *testing.T) {
	codes := []FailureCode{
		InsufficientFunds,
		ReversalInsufficientFunds,
		ReferenceNotFound,
		ReferenceNotProcessed,
		ReferenceMismatch,
		ReferenceKindInvalid,
		AmountMismatch,
		AlreadyReversed,
		CurrencyMismatch,
		InfrastructureFailure,
	}

	for _, code := range codes {
		if !code.IsValid() {
			t.Fatalf("%s must be valid", code)
		}
	}

	if FailureCode("SOMETHING").IsValid() || FailureCode("").IsValid() {
		t.Fatal("unknown codes must be invalid")
	}

	if InsufficientFunds == ReversalInsufficientFunds {
		t.Fatal("bet and reversal insufficient funds must differ")
	}

	if !CurrencyMismatch.IsCorrectable() || InsufficientFunds.IsCorrectable() {
		t.Fatal("only currency mismatch is correctable")
	}
}
