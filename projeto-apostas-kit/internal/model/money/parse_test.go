package money

import (
	"errors"
	"math"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr error
	}{
		{name: "two decimals", input: "25.00", want: 2500},
		{name: "zero", input: "0.00", want: 0},
		{name: "integer only", input: "25", want: 2500},
		{name: "one decimal", input: "25.5", want: 2550},
		{name: "cents only", input: "0.05", want: 5},
		{name: "max int64", input: "92233720368547758.07", want: math.MaxInt64},
		{name: "above max int64", input: "92233720368547758.08", wantErr: ErrOverflow},
		{name: "far above max", input: "99999999999999999999.99", wantErr: ErrOverflow},
		{name: "too many decimals", input: "1.001", wantErr: ErrScaleExceeded},
		{name: "scientific notation", input: "1e2", wantErr: ErrInvalidFormat},
		{name: "scientific with point", input: "1.0e2", wantErr: ErrInvalidFormat},
		{name: "empty", input: "", wantErr: ErrInvalidFormat},
		{name: "nan", input: "NaN", wantErr: ErrInvalidFormat},
		{name: "infinity", input: "Infinity", wantErr: ErrInvalidFormat},
		{name: "plus sign", input: "+1.00", wantErr: ErrInvalidFormat},
		{name: "spaces", input: " 1.00", wantErr: ErrInvalidFormat},
		{name: "trailing point", input: "1.", wantErr: ErrInvalidFormat},
		{name: "leading point", input: ".50", wantErr: ErrInvalidFormat},
		{name: "leading zeros", input: "007.00", wantErr: ErrInvalidFormat},
		{name: "comma separator", input: "1,00", wantErr: ErrInvalidFormat},
		{name: "two points", input: "1.0.0", wantErr: ErrInvalidFormat},
		{name: "negative", input: "-1.00", wantErr: ErrNegativeNotAllowed},
		{name: "negative zero", input: "-0.00", wantErr: ErrNegativeNotAllowed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.input, BRL)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}

			if err == nil && got.MinorUnits() != test.want {
				t.Fatalf("minor units = %d, want %d", got.MinorUnits(), test.want)
			}
		})
	}
}

func TestParseSigned(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr error
	}{
		{name: "negative", input: "-1.25", want: -125},
		{name: "negative zero normalizes", input: "-0.00", want: 0},
		{name: "min int64", input: "-92233720368547758.08", want: math.MinInt64},
		{name: "below min int64", input: "-92233720368547758.09", wantErr: ErrOverflow},
		{name: "double sign", input: "--1.00", wantErr: ErrInvalidFormat},
		{name: "only sign", input: "-", wantErr: ErrInvalidFormat},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseSigned(test.input, BRL)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}

			if err == nil && got.MinorUnits() != test.want {
				t.Fatalf("minor units = %d, want %d", got.MinorUnits(), test.want)
			}
		})
	}
}

func TestParseRejectsUnknownCurrency(t *testing.T) {
	_, err := Parse("1.00", Currency("XXX"))

	if !errors.Is(err, ErrUnknownCurrency) {
		t.Fatalf("error = %v, want %v", err, ErrUnknownCurrency)
	}
}

func TestParseNormalizesEquivalentForms(t *testing.T) {
	inputs := []string{"25", "25.0", "25.00"}

	for _, input := range inputs {
		got, err := Parse(input, BRL)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", input, err)
		}

		if got.Amount() != "25.00" {
			t.Fatalf("Parse(%q).Amount() = %q, want 25.00", input, got.Amount())
		}
	}
}

func TestAmountRoundTrip(t *testing.T) {
	values := []int64{0, 5, 2500, -5, -125, math.MaxInt64, math.MinInt64}

	for _, value := range values {
		original := mustNew(t, value, BRL)

		parsed, err := ParseSigned(original.Amount(), BRL)
		if err != nil {
			t.Fatalf("ParseSigned(%q) error = %v", original.Amount(), err)
		}

		if !parsed.Equal(original) {
			t.Fatalf("round trip = %s, want %s", parsed, original)
		}
	}
}
