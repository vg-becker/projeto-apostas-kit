package wager

import (
	"errors"
	"strings"
	"testing"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
)

const (
	transactionID identity.TransactionID = "0192f298-345e-7e38-af88-e43f851a819d"
	referenceID   identity.TransactionID = "0192f298-0000-7e38-af88-e43f851a819d"
	walletID      identity.WalletID      = "0192f291-27dd-7d3f-8071-5f8685deef37"
	playerID      identity.PlayerID      = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
)

var (
	created = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	later   = created.Add(time.Minute)
	hash    = strings.Repeat("ab", 32)
)

func betParams(t *testing.T) ExternalParams {
	t.Helper()

	return ExternalParams{
		ID:                    transactionID,
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		PayloadHash:           hash,
		WalletID:              walletID,
		PlayerID:              playerID,
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  Bet,
		Money:                 brl(t, 2500),
		Now:                   created,
	}
}

func reversalParams(t *testing.T, kind Kind) ExternalParams {
	t.Helper()

	params := betParams(t)
	params.ExternalTransactionID = "transaction-456"
	params.IdempotencyKey = "provider-a:transaction-456"
	params.Kind = kind
	params.ReferenceExternalTransactionID = "transaction-123"

	return params
}

func mustExternal(t *testing.T, params ExternalParams) *Transaction {
	t.Helper()

	transaction, err := NewExternal(params)
	if err != nil {
		t.Fatalf("NewExternal error = %v", err)
	}

	return transaction
}

func TestNewExternalStartsPending(t *testing.T) {
	transaction := mustExternal(t, betParams(t))

	if transaction.Status() != Pending || transaction.FailureCode() != "" {
		t.Fatalf("snapshot = %+v", transaction.Snapshot())
	}

	if transaction.ResultBalance().IsInitialized() {
		t.Fatal("pending transaction must have no result balance")
	}
}

func TestNewExternalValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(t *testing.T, params *ExternalParams)
		wantErr error
	}{
		{name: "opening", mutate: func(_ *testing.T, p *ExternalParams) {
			p.Kind = Opening
		}, wantErr: ErrOpeningNotAllowed},
		{name: "unknown kind", mutate: func(_ *testing.T, p *ExternalParams) {
			p.Kind = "SPIN"
		}, wantErr: ErrInvalidTransaction},
		{name: "loss with amount", mutate: func(t *testing.T, p *ExternalParams) {
			p.Kind = Loss
			p.Money = brl(t, 100)
		}, wantErr: ErrAmountPolicy},
		{name: "bet zero", mutate: func(t *testing.T, p *ExternalParams) {
			p.Money = brl(t, 0)
		}, wantErr: ErrAmountPolicy},
		{name: "missing provider", mutate: func(_ *testing.T, p *ExternalParams) {
			p.ProviderID = ""
		}, wantErr: ErrInvalidTransaction},
		{name: "blank round", mutate: func(_ *testing.T, p *ExternalParams) {
			p.RoundID = " "
		}, wantErr: ErrInvalidTransaction},
		{name: "oversized game", mutate: func(_ *testing.T, p *ExternalParams) {
			p.GameID = strings.Repeat("g", 129)
		}, wantErr: ErrInvalidTransaction},
		{name: "short hash", mutate: func(_ *testing.T, p *ExternalParams) {
			p.PayloadHash = "abc"
		}, wantErr: ErrInvalidTransaction},
		{name: "uppercase hash", mutate: func(_ *testing.T, p *ExternalParams) {
			p.PayloadHash = strings.Repeat("AB", 32)
		}, wantErr: ErrInvalidTransaction},
		{name: "bet with reference", mutate: func(_ *testing.T, p *ExternalParams) {
			p.ReferenceExternalTransactionID = "transaction-1"
		}, wantErr: ErrInvalidTransaction},
		{name: "refund without reference", mutate: func(_ *testing.T, p *ExternalParams) {
			p.Kind = Refund
		}, wantErr: ErrInvalidTransaction},
		{name: "rollback without reference", mutate: func(_ *testing.T, p *ExternalParams) {
			p.Kind = Rollback
		}, wantErr: ErrInvalidTransaction},
		{name: "self reference", mutate: func(_ *testing.T, p *ExternalParams) {
			p.Kind = Refund
			p.ReferenceExternalTransactionID = p.ExternalTransactionID
		}, wantErr: ErrInvalidTransaction},
		{name: "missing time", mutate: func(_ *testing.T, p *ExternalParams) {
			p.Now = time.Time{}
		}, wantErr: ErrInvalidTransaction},
		{name: "win with optional reference", mutate: func(_ *testing.T, p *ExternalParams) {
			p.Kind = Win
			p.ReferenceExternalTransactionID = "transaction-1"
		}},
		{name: "win without reference", mutate: func(_ *testing.T, p *ExternalParams) {
			p.Kind = Win
		}},
		{name: "loss at zero", mutate: func(t *testing.T, p *ExternalParams) {
			p.Kind = Loss
			p.Money = brl(t, 0)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := betParams(t)
			test.mutate(t, &params)

			_, err := NewExternal(params)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestNewOpening(t *testing.T) {
	transaction, err := NewOpening(OpeningParams{
		ID:       transactionID,
		WalletID: walletID,
		PlayerID: playerID,
		Money:    brl(t, 100000),
		Now:      created,
	})
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	snapshot := transaction.Snapshot()
	if snapshot.Status != Processed || snapshot.Kind != Opening {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	if snapshot.ProviderID != "" || snapshot.IdempotencyKey != "" || snapshot.RoundID != "" {
		t.Fatalf("opening must not carry external metadata: %+v", snapshot)
	}

	if snapshot.ResultBalance.Amount() != "1000.00" {
		t.Fatalf("result balance = %s", snapshot.ResultBalance)
	}
}

func TestNewOpeningRejectsZeroAmount(t *testing.T) {
	_, err := NewOpening(OpeningParams{
		ID:       transactionID,
		WalletID: walletID,
		PlayerID: playerID,
		Money:    brl(t, 0),
		Now:      created,
	})

	if !errors.Is(err, ErrAmountPolicy) {
		t.Fatalf("error = %v, want %v", err, ErrAmountPolicy)
	}
}

func TestMarkProcessed(t *testing.T) {
	transaction := mustExternal(t, betParams(t))

	err := transaction.MarkProcessed(ProcessedResult{Balance: brl(t, 97500)}, later)
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	if transaction.Status() != Processed || transaction.ResultBalance().Amount() != "975.00" {
		t.Fatalf("snapshot = %+v", transaction.Snapshot())
	}

	if !transaction.Snapshot().UpdatedAt.Equal(later) {
		t.Fatalf("updatedAt = %v", transaction.Snapshot().UpdatedAt)
	}
}

func TestMarkProcessedValidatesResult(t *testing.T) {
	usd, err := money.New(100, money.USD)
	if err != nil {
		t.Fatalf("money.New error = %v", err)
	}

	tests := []struct {
		name   string
		params ExternalParams
		result ProcessedResult
	}{
		{name: "uninitialized balance", params: betParams(t)},
		{name: "wrong currency", params: betParams(t), result: ProcessedResult{Balance: usd}},
		{
			name:   "reversal without resolved",
			params: reversalParams(t, Refund),
			result: ProcessedResult{Balance: brl(t, 100)},
		},
		{
			name:   "bet with resolved",
			params: betParams(t),
			result: ProcessedResult{Balance: brl(t, 100), ResolvedReferenceID: referenceID},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transaction := mustExternal(t, test.params)

			err := transaction.MarkProcessed(test.result, later)

			if !errors.Is(err, ErrInvalidTransaction) || transaction.Status() != Pending {
				t.Fatalf("error = %v, status = %s", err, transaction.Status())
			}
		})
	}
}

func TestReversalProcessedKeepsResolvedReference(t *testing.T) {
	transaction := mustExternal(t, reversalParams(t, Refund))

	err := transaction.MarkProcessed(
		ProcessedResult{Balance: brl(t, 100000), ResolvedReferenceID: referenceID},
		later,
	)
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	if transaction.ResolvedReferenceID() != referenceID {
		t.Fatalf("resolved = %s", transaction.ResolvedReferenceID())
	}
}

func TestAwaitReference(t *testing.T) {
	waiting := mustExternal(t, reversalParams(t, Rollback))
	if err := waiting.AwaitReference(later); err != nil {
		t.Fatalf("error = %v", err)
	}

	if waiting.Status() != PendingReference {
		t.Fatalf("status = %s", waiting.Status())
	}

	if err := waiting.AwaitReference(later); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second await error = %v, want %v", err, ErrInvalidTransition)
	}

	bet := mustExternal(t, betParams(t))
	if err := bet.AwaitReference(later); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("bet await error = %v, want %v", err, ErrInvalidTransition)
	}
}

func TestPendingReferenceCanFinish(t *testing.T) {
	transaction := mustExternal(t, reversalParams(t, Refund))
	if err := transaction.AwaitReference(later); err != nil {
		t.Fatalf("await error = %v", err)
	}

	if err := transaction.Reject(ReferenceNotFound, later.Add(time.Hour)); err != nil {
		t.Fatalf("reject error = %v", err)
	}

	if transaction.Status() != Rejected || transaction.FailureCode() != ReferenceNotFound {
		t.Fatalf("snapshot = %+v", transaction.Snapshot())
	}
}

func TestRejectAndFail(t *testing.T) {
	rejected := mustExternal(t, betParams(t))
	if err := rejected.Reject(InsufficientFunds, later); err != nil {
		t.Fatalf("reject error = %v", err)
	}

	if rejected.FailureCode() != InsufficientFunds {
		t.Fatalf("code = %s", rejected.FailureCode())
	}

	failed := mustExternal(t, betParams(t))
	if err := failed.Fail(later); err != nil {
		t.Fatalf("fail error = %v", err)
	}

	if failed.Status() != Failed || failed.FailureCode() != InfrastructureFailure {
		t.Fatalf("snapshot = %+v", failed.Snapshot())
	}
}

func TestRejectValidatesCode(t *testing.T) {
	codes := []FailureCode{"", "UNKNOWN", InfrastructureFailure}

	for _, code := range codes {
		transaction := mustExternal(t, betParams(t))

		err := transaction.Reject(code, later)

		if !errors.Is(err, ErrInvalidFailureCode) || transaction.Status() != Pending {
			t.Fatalf("code %q: error = %v, status = %s", code, err, transaction.Status())
		}
	}
}

func TestTerminalStatesRejectAnyTransition(t *testing.T) {
	finishers := map[string]func(transaction *Transaction) error{
		"processed": func(transaction *Transaction) error {
			return transaction.MarkProcessed(ProcessedResult{Balance: transaction.Money()}, later)
		},
		"rejected": func(transaction *Transaction) error {
			return transaction.Reject(InsufficientFunds, later)
		},
		"failed": func(transaction *Transaction) error {
			return transaction.Fail(later)
		},
	}

	for name, finish := range finishers {
		t.Run(name, func(t *testing.T) {
			transaction := mustExternal(t, betParams(t))
			if err := finish(transaction); err != nil {
				t.Fatalf("finish error = %v", err)
			}

			assertFrozen(t, transaction)
		})
	}
}

func assertFrozen(t *testing.T, transaction *Transaction) {
	t.Helper()

	before := transaction.Snapshot()
	errs := []error{
		transaction.Reject(InsufficientFunds, later),
		transaction.Fail(later),
		transaction.MarkProcessed(ProcessedResult{Balance: transaction.Money()}, later),
		transaction.AwaitReference(later),
	}

	for _, err := range errs {
		if !errors.Is(err, ErrTerminalState) && !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("error = %v, want terminal state", err)
		}
	}

	if transaction.Snapshot() != before {
		t.Fatalf("terminal transaction changed: %+v", transaction.Snapshot())
	}
}

func TestTransitionCannotMoveTimeBackwards(t *testing.T) {
	transaction := mustExternal(t, betParams(t))

	err := transaction.Reject(InsufficientFunds, created.Add(-time.Second))

	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidTransition)
	}
}

func TestRehydrateDoesNotApplyTransitions(t *testing.T) {
	original := mustExternal(t, betParams(t))
	if err := original.Reject(InsufficientFunds, later); err != nil {
		t.Fatalf("reject error = %v", err)
	}

	restored, err := Rehydrate(original.Snapshot())
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	if restored.Snapshot() != original.Snapshot() {
		t.Fatalf("restored = %+v, want %+v", restored.Snapshot(), original.Snapshot())
	}
}

func TestRehydrateRejectsIncoherentSnapshots(t *testing.T) {
	base := mustExternal(t, betParams(t)).Snapshot()

	tests := []struct {
		name   string
		mutate func(t *testing.T, snapshot *Snapshot)
	}{
		{name: "processed without result", mutate: func(_ *testing.T, s *Snapshot) {
			s.Status = Processed
		}},
		{name: "rejected without code", mutate: func(_ *testing.T, s *Snapshot) {
			s.Status = Rejected
		}},
		{name: "rejected with infrastructure code", mutate: func(_ *testing.T, s *Snapshot) {
			s.Status = Rejected
			s.FailureCode = InfrastructureFailure
		}},
		{name: "failed with business code", mutate: func(_ *testing.T, s *Snapshot) {
			s.Status = Failed
			s.FailureCode = InsufficientFunds
		}},
		{name: "pending with code", mutate: func(_ *testing.T, s *Snapshot) {
			s.FailureCode = InsufficientFunds
		}},
		{name: "pending reference without reference", mutate: func(_ *testing.T, s *Snapshot) {
			s.Status = PendingReference
		}},
		{name: "unknown status", mutate: func(_ *testing.T, s *Snapshot) {
			s.Status = "DONE"
		}},
		{name: "opening with provider", mutate: func(t *testing.T, s *Snapshot) {
			s.Kind = Opening
			s.Status = Processed
			s.ResultBalance = brl(t, 2500)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := base
			test.mutate(t, &snapshot)

			_, err := Rehydrate(snapshot)

			if !errors.Is(err, ErrInvalidTransaction) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidTransaction)
			}
		})
	}
}
