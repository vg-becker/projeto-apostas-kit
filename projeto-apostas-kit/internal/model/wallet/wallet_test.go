package wallet

import (
	"errors"
	"math"
	"testing"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/money"
)

const (
	walletID      identity.WalletID      = "0192f291-27dd-7d3f-8071-5f8685deef37"
	playerID      identity.PlayerID      = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	transactionID identity.TransactionID = "0192f298-345e-7e38-af88-e43f851a819d"
	entryID       identity.EntryID       = "0192f299-0000-7e38-af88-e43f851a819d"
)

var (
	opened = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	later  = opened.Add(time.Minute)
)

func amount(t *testing.T, minorUnits int64, currency money.Currency) money.Money {
	t.Helper()

	value, err := money.New(minorUnits, currency)
	if err != nil {
		t.Fatalf("money.New error = %v", err)
	}

	return value
}

func openWallet(t *testing.T, minorUnits int64) *Wallet {
	t.Helper()

	result, err := Open(OpenParams{
		ID:                   walletID,
		PlayerID:             playerID,
		InitialBalance:       amount(t, minorUnits, money.BRL),
		OpeningTransactionID: transactionID,
		OpeningEntryID:       entryID,
		Now:                  opened,
	})
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}

	return result.Wallet
}

func movement(t *testing.T, minorUnits int64, currency money.Currency) MovementParams {
	t.Helper()

	return MovementParams{
		TransactionID: transactionID,
		EntryID:       entryID,
		Amount:        amount(t, minorUnits, currency),
		Now:           later,
	}
}

func TestOpenWithPositiveBalanceCreatesOpeningEntry(t *testing.T) {
	result, err := Open(OpenParams{
		ID:                   walletID,
		PlayerID:             playerID,
		InitialBalance:       amount(t, 100000, money.BRL),
		OpeningTransactionID: transactionID,
		OpeningEntryID:       entryID,
		Now:                  opened,
	})
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	entry := result.OpeningEntry
	if entry == nil || entry.Direction() != ledger.Credit {
		t.Fatalf("opening entry = %+v, want a credit", entry)
	}

	if entry.BalanceBefore().Amount() != "0.00" || entry.BalanceAfter().Amount() != "1000.00" {
		t.Fatalf("unexpected opening balances in %+v", entry)
	}

	if result.Wallet.Version() != 1 || result.Wallet.Balance().Amount() != "1000.00" {
		t.Fatalf("wallet = %+v, want version 1 and 1000.00", result.Wallet.Snapshot())
	}
}

func TestOpenWithZeroBalanceCreatesNoEntry(t *testing.T) {
	result, err := Open(OpenParams{
		ID:             walletID,
		PlayerID:       playerID,
		InitialBalance: amount(t, 0, money.BRL),
		Now:            opened,
	})
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	if result.OpeningEntry != nil || result.Wallet.Version() != 1 {
		t.Fatalf("zero opening must have no entry and version 1")
	}
}

func TestOpenRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, params *OpenParams)
	}{
		{name: "negative balance", mutate: func(t *testing.T, p *OpenParams) {
			p.InitialBalance = amount(t, -1, money.BRL)
		}},
		{name: "uninitialized balance", mutate: func(_ *testing.T, p *OpenParams) {
			p.InitialBalance = money.Money{}
		}},
		{name: "missing wallet id", mutate: func(_ *testing.T, p *OpenParams) {
			p.ID = ""
		}},
		{name: "missing player id", mutate: func(_ *testing.T, p *OpenParams) {
			p.PlayerID = ""
		}},
		{name: "missing time", mutate: func(_ *testing.T, p *OpenParams) {
			p.Now = time.Time{}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := OpenParams{
				ID:                   walletID,
				PlayerID:             playerID,
				InitialBalance:       amount(t, 1000, money.BRL),
				OpeningTransactionID: transactionID,
				OpeningEntryID:       entryID,
				Now:                  opened,
			}
			test.mutate(t, &params)

			_, err := Open(params)

			if !errors.Is(err, ErrInvalidState) && !errors.Is(err, money.ErrUninitialized) {
				t.Fatalf("error = %v, want invalid state", err)
			}
		})
	}
}

func assertState(t *testing.T, wallet *Wallet, balance int64, version int64) {
	t.Helper()

	if wallet.Balance().MinorUnits() != balance {
		t.Fatalf("balance = %d, want %d", wallet.Balance().MinorUnits(), balance)
	}

	if wallet.Version() != version {
		t.Fatalf("version = %d, want %d", wallet.Version(), version)
	}
}

func TestDebit(t *testing.T) {
	tests := []struct {
		name        string
		balance     int64
		debit       int64
		currency    money.Currency
		wantBalance int64
		wantVersion int64
		wantErr     error
	}{
		{name: "partial", balance: 100000, debit: 2500, currency: money.BRL,
			wantBalance: 97500, wantVersion: 2},
		{name: "exact balance", balance: 8000, debit: 8000, currency: money.BRL,
			wantBalance: 0, wantVersion: 2},
		{name: "insufficient funds", balance: 10000, debit: 10001, currency: money.BRL,
			wantBalance: 10000, wantVersion: 1, wantErr: ErrInsufficientFunds},
		{name: "zero amount", balance: 10000, debit: 0, currency: money.BRL,
			wantBalance: 10000, wantVersion: 1, wantErr: ErrAmountNotPositive},
		{name: "negative amount", balance: 10000, debit: -1, currency: money.BRL,
			wantBalance: 10000, wantVersion: 1, wantErr: ErrAmountNotPositive},
		{name: "wrong currency", balance: 10000, debit: 100, currency: money.USD,
			wantBalance: 10000, wantVersion: 1, wantErr: ErrCurrencyMismatch},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wallet := openWallet(t, test.balance)

			entry, err := wallet.Debit(movement(t, test.debit, test.currency))

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}

			assertState(t, wallet, test.wantBalance, test.wantVersion)

			if err == nil && entry.Direction() != ledger.Debit {
				t.Fatalf("direction = %s, want DEBIT", entry.Direction())
			}
		})
	}
}

func TestCredit(t *testing.T) {
	wallet := openWallet(t, 10000)

	entry, err := wallet.Credit(movement(t, 2500, money.BRL))
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	if wallet.Balance().Amount() != "125.00" || wallet.Version() != 2 {
		t.Fatalf("wallet = %+v", wallet.Snapshot())
	}

	if entry.Direction() != ledger.Credit || entry.BalanceBefore().Amount() != "100.00" {
		t.Fatalf("entry = %+v", entry)
	}

	if !wallet.UpdatedAt().Equal(later) {
		t.Fatalf("updatedAt = %v, want %v", wallet.UpdatedAt(), later)
	}
}

func TestCreditRejectsInvalidMovements(t *testing.T) {
	wallet := openWallet(t, 10000)
	huge := MovementParams{
		TransactionID: transactionID,
		EntryID:       entryID,
		Amount:        amount(t, math.MaxInt64, money.BRL),
		Now:           later,
	}

	if _, err := wallet.Credit(huge); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("overflow error = %v, want %v", err, money.ErrOverflow)
	}

	if _, err := wallet.Credit(movement(t, 100, money.USD)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("currency error = %v, want %v", err, ErrCurrencyMismatch)
	}

	if wallet.Version() != 1 || wallet.Balance().MinorUnits() != 10000 {
		t.Fatalf("rejected credits changed state: %+v", wallet.Snapshot())
	}
}

func TestMovementWithInvalidEntryLeavesStateUntouched(t *testing.T) {
	wallet := openWallet(t, 10000)
	params := movement(t, 100, money.BRL)
	params.EntryID = ""

	if _, err := wallet.Debit(params); !errors.Is(err, ledger.ErrInvalidEntry) {
		t.Fatalf("error = %v, want %v", err, ledger.ErrInvalidEntry)
	}

	if wallet.Version() != 1 || wallet.Balance().MinorUnits() != 10000 {
		t.Fatalf("state changed: %+v", wallet.Snapshot())
	}
}

func TestRehydrate(t *testing.T) {
	valid := Snapshot{
		ID:        walletID,
		PlayerID:  playerID,
		Balance:   amount(t, 5000, money.BRL),
		Version:   7,
		CreatedAt: opened,
		UpdatedAt: later,
	}

	tests := []struct {
		name    string
		mutate  func(snapshot *Snapshot)
		wantErr error
	}{
		{name: "valid", mutate: func(*Snapshot) {}},
		{name: "version zero", mutate: func(s *Snapshot) { s.Version = 0 }, wantErr: ErrInvalidState},
		{name: "negative balance", mutate: func(s *Snapshot) {
			s.Balance = amount(t, -1, money.BRL)
		}, wantErr: ErrInvalidState},
		{name: "uninitialized balance", mutate: func(s *Snapshot) {
			s.Balance = money.Money{}
		}, wantErr: ErrInvalidState},
		{name: "updated before created", mutate: func(s *Snapshot) {
			s.UpdatedAt = opened.Add(-time.Second)
		}, wantErr: ErrInvalidState},
		{name: "missing id", mutate: func(s *Snapshot) { s.ID = "" }, wantErr: ErrInvalidState},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := valid
			test.mutate(&snapshot)

			wallet, err := Rehydrate(snapshot)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}

			if err == nil && wallet.Snapshot() != snapshot {
				t.Fatalf("snapshot = %+v, want %+v", wallet.Snapshot(), snapshot)
			}
		})
	}
}
