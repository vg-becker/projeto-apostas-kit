//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"

	"apostas-go/internal/service"
)

func TestReconcileConsistentWallet(t *testing.T) {
	h := newHarness(t)
	opened, err := h.open(t, h.newPlayerID(t).String(), 100000)
	if err != nil {
		t.Fatalf("open error = %v", err)
	}

	result, err := h.wallets.ReconcileWallet(context.Background(), opened.ID())

	if err != nil || !result.Consistent || result.CheckedEntries != 1 {
		t.Fatalf("result = %+v (%v)", result, err)
	}

	if result.StoredBalance.Amount() != "1000.00" || result.CalculatedBalance.Amount() != "1000.00" ||
		result.Difference.Amount() != "0.00" {
		t.Fatalf("balances = %+v", result)
	}
}

func TestReconcileDetectsDivergenceWithoutChangingTheBalance(t *testing.T) {
	h := newHarness(t)
	opened, err := h.open(t, h.newPlayerID(t).String(), 100000)
	if err != nil {
		t.Fatalf("open error = %v", err)
	}

	_, err = h.pool.Exec(
		context.Background(),
		`UPDATE wallets SET balance_minor_units = balance_minor_units + 500 WHERE id = $1`,
		opened.ID().String(),
	)
	if err != nil {
		t.Fatalf("tamper: %v", err)
	}

	result, err := h.wallets.ReconcileWallet(context.Background(), opened.ID())
	if err != nil || result.Consistent {
		t.Fatalf("result = %+v (%v), want a divergence", result, err)
	}

	if result.Difference.Amount() != "5.00" || result.StoredBalance.Amount() != "1005.00" ||
		result.CalculatedBalance.Amount() != "1000.00" {
		t.Fatalf("result = %+v", result)
	}

	after, err := h.wallets.GetWallet(context.Background(), opened.ID())
	if err != nil || after.Balance().Amount() != "1005.00" || after.Version() != 1 {
		t.Fatalf("reconciliation changed the wallet: %+v (%v)", after, err)
	}
}

func TestReconcileWalletWithoutEntries(t *testing.T) {
	h := newHarness(t)
	opened, err := h.open(t, h.newPlayerID(t).String(), 0)
	if err != nil {
		t.Fatalf("open error = %v", err)
	}

	result, err := h.wallets.ReconcileWallet(context.Background(), opened.ID())

	if err != nil || !result.Consistent || result.CheckedEntries != 0 {
		t.Fatalf("result = %+v (%v)", result, err)
	}
}

func TestReconcileUnknownWallet(t *testing.T) {
	h := newHarness(t)
	missing, _ := h.ids.NewWalletID()

	_, err := h.wallets.ReconcileWallet(context.Background(), missing)

	if !errors.Is(err, service.ErrWalletNotFound) {
		t.Fatalf("error = %v, want %v", err, service.ErrWalletNotFound)
	}
}
