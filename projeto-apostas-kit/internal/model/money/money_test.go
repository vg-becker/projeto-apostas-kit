package money

import (
	"errors"
	"math"
	"testing"
)

func mustNew(t *testing.T, minorUnits int64, currency Currency) Money {
	t.Helper()

	money, err := New(minorUnits, currency)
	if err != nil {
		t.Fatalf("New(%d, %s) error = %v", minorUnits, currency, err)
	}

	return money
}

func TestNewCurrency(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		wantErr error
	}{
		{name: "brl", code: "BRL"},
		{name: "lowercase", code: "brl", wantErr: ErrUnknownCurrency},
		{name: "unknown", code: "XXX", wantErr: ErrUnknownCurrency},
		{name: "empty", code: "", wantErr: ErrUnknownCurrency},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewCurrency(test.code)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestZeroValueIsUninitialized(t *testing.T) {
	var uninitialized Money
	valid := mustNew(t, 100, BRL)

	if uninitialized.IsInitialized() {
		t.Fatal("zero value must not be initialized")
	}

	if _, err := valid.Add(uninitialized); !errors.Is(err, ErrUninitialized) {
		t.Fatalf("Add error = %v, want %v", err, ErrUninitialized)
	}

	if _, err := uninitialized.Negate(); !errors.Is(err, ErrUninitialized) {
		t.Fatalf("Negate error = %v, want %v", err, ErrUninitialized)
	}
}

func TestZeroAndNewRejectUnknownCurrency(t *testing.T) {
	if _, err := Zero(Currency("")); !errors.Is(err, ErrUnknownCurrency) {
		t.Fatalf("Zero error = %v, want %v", err, ErrUnknownCurrency)
	}

	if _, err := New(1, Currency("XXX")); !errors.Is(err, ErrUnknownCurrency) {
		t.Fatalf("New error = %v, want %v", err, ErrUnknownCurrency)
	}
}

func TestAdd(t *testing.T) {
	tests := []struct {
		name    string
		left    int64
		right   int64
		want    int64
		wantErr error
	}{
		{name: "simple", left: 2500, right: 500, want: 3000},
		{name: "negative operand", left: 100, right: -300, want: -200},
		{name: "to max", left: math.MaxInt64 - 1, right: 1, want: math.MaxInt64},
		{name: "overflow", left: math.MaxInt64, right: 1, wantErr: ErrOverflow},
		{name: "underflow", left: math.MinInt64, right: -1, wantErr: ErrOverflow},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left := mustNew(t, test.left, BRL)
			right := mustNew(t, test.right, BRL)

			got, err := left.Add(right)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}

			if err == nil && got.MinorUnits() != test.want {
				t.Fatalf("sum = %d, want %d", got.MinorUnits(), test.want)
			}
		})
	}
}

func TestSub(t *testing.T) {
	tests := []struct {
		name    string
		left    int64
		right   int64
		want    int64
		wantErr error
	}{
		{name: "simple", left: 2500, right: 500, want: 2000},
		{name: "goes negative", left: 100, right: 300, want: -200},
		{name: "to min", left: math.MinInt64 + 1, right: 1, want: math.MinInt64},
		{name: "underflow", left: math.MinInt64, right: 1, wantErr: ErrOverflow},
		{name: "overflow", left: math.MaxInt64, right: -1, wantErr: ErrOverflow},
		{name: "negative from zero", left: 0, right: math.MinInt64, wantErr: ErrOverflow},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left := mustNew(t, test.left, BRL)
			right := mustNew(t, test.right, BRL)

			got, err := left.Sub(right)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}

			if err == nil && got.MinorUnits() != test.want {
				t.Fatalf("difference = %d, want %d", got.MinorUnits(), test.want)
			}
		})
	}
}

func TestNegate(t *testing.T) {
	tests := []struct {
		name    string
		input   int64
		want    int64
		wantErr error
	}{
		{name: "positive", input: 500, want: -500},
		{name: "negative", input: -500, want: 500},
		{name: "zero", input: 0, want: 0},
		{name: "min int64", input: math.MinInt64, wantErr: ErrOverflow},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := mustNew(t, test.input, BRL).Negate()

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}

			if err == nil && got.MinorUnits() != test.want {
				t.Fatalf("negated = %d, want %d", got.MinorUnits(), test.want)
			}
		})
	}
}

func TestCurrencyMismatch(t *testing.T) {
	brl := mustNew(t, 100, BRL)
	usd := mustNew(t, 100, USD)

	if _, err := brl.Add(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Add error = %v, want %v", err, ErrCurrencyMismatch)
	}

	if _, err := brl.Sub(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Sub error = %v, want %v", err, ErrCurrencyMismatch)
	}

	if _, err := brl.Compare(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Compare error = %v, want %v", err, ErrCurrencyMismatch)
	}

	if brl.Equal(usd) {
		t.Fatal("same amount in different currencies must not be equal")
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		name  string
		left  int64
		right int64
		want  int
	}{
		{name: "less", left: 100, right: 200, want: -1},
		{name: "equal", left: 200, right: 200, want: 0},
		{name: "greater", left: 300, right: 200, want: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := mustNew(t, test.left, BRL).Compare(mustNew(t, test.right, BRL))
			if err != nil {
				t.Fatalf("error = %v", err)
			}

			if got != test.want {
				t.Fatalf("compare = %d, want %d", got, test.want)
			}
		})
	}
}

func TestOperationsDoNotMutateReceiver(t *testing.T) {
	original := mustNew(t, 1000, BRL)
	other := mustNew(t, 250, BRL)

	if _, err := original.Add(other); err != nil {
		t.Fatalf("Add error = %v", err)
	}

	if _, err := original.Sub(other); err != nil {
		t.Fatalf("Sub error = %v", err)
	}

	if original.MinorUnits() != 1000 {
		t.Fatalf("receiver changed to %d", original.MinorUnits())
	}
}

func TestSignPredicates(t *testing.T) {
	tests := []struct {
		name         string
		input        int64
		wantZero     bool
		wantPositive bool
		wantNegative bool
	}{
		{name: "zero", input: 0, wantZero: true},
		{name: "positive", input: 1, wantPositive: true},
		{name: "negative", input: -1, wantNegative: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			money := mustNew(t, test.input, BRL)

			if money.IsZero() != test.wantZero ||
				money.IsPositive() != test.wantPositive ||
				money.IsNegative() != test.wantNegative {
				t.Fatalf("predicates wrong for %s", money)
			}
		})
	}
}

func TestAmountAndString(t *testing.T) {
	tests := []struct {
		name       string
		input      int64
		wantAmount string
	}{
		{name: "whole", input: 2500, wantAmount: "25.00"},
		{name: "cents", input: 5, wantAmount: "0.05"},
		{name: "negative cents", input: -5, wantAmount: "-0.05"},
		{name: "min int64", input: math.MinInt64, wantAmount: "-92233720368547758.08"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			money := mustNew(t, test.input, BRL)

			if money.Amount() != test.wantAmount {
				t.Fatalf("amount = %q, want %q", money.Amount(), test.wantAmount)
			}

			if money.String() != test.wantAmount+" BRL" {
				t.Fatalf("string = %q", money.String())
			}
		})
	}
}
