package identity

import (
	"errors"
	"testing"
)

func TestParseWalletID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    WalletID
		wantErr error
	}{
		{
			name:  "canonical",
			input: "0192f291-27dd-7d3f-8071-5f8685deef37",
			want:  "0192f291-27dd-7d3f-8071-5f8685deef37",
		},
		{
			name:  "uppercase is normalized",
			input: "0192F291-27DD-7D3F-8071-5F8685DEEF37",
			want:  "0192f291-27dd-7d3f-8071-5f8685deef37",
		},
		{name: "empty", input: "", wantErr: ErrInvalidID},
		{name: "too short", input: "0192f291", wantErr: ErrInvalidID},
		{
			name:    "missing hyphens",
			input:   "0192f29127dd7d3f80715f8685deef37abcd",
			wantErr: ErrInvalidID,
		},
		{
			name:    "non hexadecimal",
			input:   "0192f291-27dd-7d3f-8071-5f8685deef3z",
			wantErr: ErrInvalidID,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseWalletID(test.input)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}

			if got != test.want {
				t.Fatalf("id = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAllIdentifierKindsShareValidation(t *testing.T) {
	const valid = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"

	if _, err := ParsePlayerID(valid); err != nil {
		t.Fatalf("player id error = %v", err)
	}

	if _, err := ParseTransactionID(valid); err != nil {
		t.Fatalf("transaction id error = %v", err)
	}

	if _, err := ParseEventID(valid); err != nil {
		t.Fatalf("event id error = %v", err)
	}

	if _, err := ParseEntryID(valid); err != nil {
		t.Fatalf("entry id error = %v", err)
	}

	if _, err := ParseEntryID("nope"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidID)
	}
}

func TestIsValid(t *testing.T) {
	tests := []struct {
		name  string
		input WalletID
		want  bool
	}{
		{name: "zero value", input: "", want: false},
		{name: "canonical", input: "0192f291-27dd-7d3f-8071-5f8685deef37", want: true},
		{name: "uppercase is not canonical", input: "0192F291-27DD-7D3F-8071-5F8685DEEF37"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.input.IsValid() != test.want {
				t.Fatalf("IsValid(%q) = %v, want %v", test.input, !test.want, test.want)
			}
		})
	}
}
