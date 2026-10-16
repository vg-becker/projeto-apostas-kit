package ledger

import (
	"errors"
	"testing"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
)

const (
	entryID       identity.EntryID       = "0192f298-345e-7e38-af88-e43f851a819d"
	walletID      identity.WalletID      = "0192f291-27dd-7d3f-8071-5f8685deef37"
	transactionID identity.TransactionID = "0192f298-0000-7e38-af88-e43f851a819d"
)

func brl(t *testing.T, minorUnits int64) money.Money {
	t.Helper()

	amount, err := money.New(minorUnits, money.BRL)
	if err != nil {
		t.Fatalf("money.New error = %v", err)
	}

	return amount
}

func validParams(t *testing.T) EntryParams {
	t.Helper()

	return EntryParams{
		ID:            entryID,
		WalletID:      walletID,
		TransactionID: transactionID,
		Direction:     Debit,
		Money:         brl(t, 2500),
		BalanceBefore: brl(t, 100000),
		BalanceAfter:  brl(t, 97500),
		CreatedAt:     time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
	}
}

func TestNewEntry(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(t *testing.T, params *EntryParams)
		wantErr error
	}{
		{name: "valid debit", mutate: func(*testing.T, *EntryParams) {}},
		{
			name: "valid credit",
			mutate: func(t *testing.T, params *EntryParams) {
				params.Direction = Credit
				params.BalanceAfter = brl(t, 102500)
			},
		},
		{
			name: "debit that empties the wallet",
			mutate: func(t *testing.T, params *EntryParams) {
				params.Money = brl(t, 100000)
				params.BalanceAfter = brl(t, 0)
			},
		},
		{
			name: "wrong arithmetic",
			mutate: func(t *testing.T, params *EntryParams) {
				params.BalanceAfter = brl(t, 97000)
			},
			wantErr: ErrBalanceMismatch,
		},
		{
			name: "credit direction with debit arithmetic",
			mutate: func(_ *testing.T, params *EntryParams) {
				params.Direction = Credit
			},
			wantErr: ErrBalanceMismatch,
		},
		{
			name: "zero money",
			mutate: func(t *testing.T, params *EntryParams) {
				params.Money = brl(t, 0)
				params.BalanceAfter = brl(t, 100000)
			},
			wantErr: ErrInvalidEntry,
		},
		{
			name: "negative balance after",
			mutate: func(t *testing.T, params *EntryParams) {
				params.BalanceBefore = brl(t, 1000)
				params.BalanceAfter = brl(t, -1500)
			},
			wantErr: ErrInvalidEntry,
		},
		{
			name: "mixed currencies",
			mutate: func(t *testing.T, params *EntryParams) {
				usd, err := money.New(97500, money.USD)
				if err != nil {
					t.Fatalf("money.New error = %v", err)
				}

				params.BalanceAfter = usd
			},
			wantErr: ErrInvalidEntry,
		},
		{
			name: "uninitialized money",
			mutate: func(_ *testing.T, params *EntryParams) {
				params.Money = money.Money{}
			},
			wantErr: ErrInvalidEntry,
		},
		{
			name: "unknown direction",
			mutate: func(_ *testing.T, params *EntryParams) {
				params.Direction = "SIDEWAYS"
			},
			wantErr: ErrInvalidEntry,
		},
		{
			name: "missing wallet id",
			mutate: func(_ *testing.T, params *EntryParams) {
				params.WalletID = ""
			},
			wantErr: ErrInvalidEntry,
		},
		{
			name: "missing created at",
			mutate: func(_ *testing.T, params *EntryParams) {
				params.CreatedAt = time.Time{}
			},
			wantErr: ErrInvalidEntry,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := validParams(t)
			test.mutate(t, &params)

			_, err := NewEntry(params)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestEntryExposesGivenValues(t *testing.T) {
	params := validParams(t)
	params.CreatedAt = time.Date(2026, 9, 8, 9, 0, 0, 0, time.FixedZone("BRT", -3*3600))

	entry, err := NewEntry(params)
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	if entry.Direction() != Debit || entry.WalletID() != walletID {
		t.Fatalf("unexpected entry %+v", entry)
	}

	if entry.BalanceAfter().Amount() != "975.00" || entry.Money().Amount() != "25.00" {
		t.Fatalf("unexpected amounts in %+v", entry)
	}

	if entry.CreatedAt().Location() != time.UTC {
		t.Fatalf("createdAt must be UTC, got %v", entry.CreatedAt().Location())
	}
}

func TestRehydrateValidatesSamePersistedInvariants(t *testing.T) {
	params := validParams(t)
	params.BalanceAfter = brl(t, 1)

	if _, err := Rehydrate(params); !errors.Is(err, ErrBalanceMismatch) {
		t.Fatalf("error = %v, want %v", err, ErrBalanceMismatch)
	}

	if _, err := Rehydrate(validParams(t)); err != nil {
		t.Fatalf("valid rehydrate error = %v", err)
	}
}

func TestParseDirection(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{name: "debit", input: "DEBIT"},
		{name: "credit", input: "CREDIT"},
		{name: "lowercase", input: "debit", wantErr: ErrInvalidEntry},
		{name: "empty", input: "", wantErr: ErrInvalidEntry},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseDirection(test.input)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}
