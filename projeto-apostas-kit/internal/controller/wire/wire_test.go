package wire

import (
	"errors"
	"testing"

	"apostas-go/internal/model/money"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/service"
)

func validFields() OperationFields {
	return OperationFields{
		ExternalTransactionID: "transaction-123",
		PlayerID:              "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
		WalletID:              "0192f291-27dd-7d3f-8071-5f8685deef37",
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 &Money{Amount: "25.00", Currency: "BRL"},
	}
}

func TestCommandFromValidFields(t *testing.T) {
	command, err := validFields().Command()
	if err != nil {
		t.Fatalf("Command error = %v", err)
	}

	if command.Kind != wager.Bet || command.Money.Amount() != "25.00" ||
		command.ExternalTransactionID != "transaction-123" || command.ProviderID != "" ||
		command.IdempotencyKey != "" {
		t.Fatalf("command = %+v", command)
	}
}

func TestInvalidFieldsAreRefusedWithTheDomainError(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(fields *OperationFields)
		wantErr error
	}{
		{name: "bad player", mutate: func(f *OperationFields) { f.PlayerID = "nope" }},
		{name: "bad wallet", mutate: func(f *OperationFields) { f.WalletID = "" }},
		{name: "opening", mutate: func(f *OperationFields) { f.Kind = "OPENING" }, wantErr: wager.ErrOpeningNotAllowed},
		{name: "unknown kind", mutate: func(f *OperationFields) { f.Kind = "SPIN" }, wantErr: wager.ErrInvalidKind},
		{name: "missing money", mutate: func(f *OperationFields) { f.Money = nil }, wantErr: wager.ErrInvalidTransaction},
		{name: "negative", mutate: func(f *OperationFields) { f.Money.Amount = "-1.00" }, wantErr: money.ErrNegativeNotAllowed},
		{name: "scale", mutate: func(f *OperationFields) { f.Money.Amount = "1.001" }, wantErr: money.ErrScaleExceeded},
		{name: "scientific", mutate: func(f *OperationFields) { f.Money.Amount = "1e2" }, wantErr: money.ErrInvalidFormat},
		{name: "currency", mutate: func(f *OperationFields) { f.Money.Currency = "XXX" }, wantErr: money.ErrUnknownCurrency},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fields := validFields()
			test.mutate(&fields)

			_, err := fields.Command()

			if err == nil || test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}

			if !service.IsPermanent(err) {
				t.Fatalf("error %v must be permanent: an invalid input never succeeds on retry", err)
			}
		})
	}
}

func TestEquivalentAmountsProduceTheSameHashOnEveryEntry(t *testing.T) {
	var hashes []string

	for _, amount := range []string{"25", "25.0", "25.00"} {
		fields := validFields()
		fields.Money.Amount = amount

		command, err := fields.Command()
		if err != nil {
			t.Fatalf("Command(%q) error = %v", amount, err)
		}

		command.ProviderID = "provider-a"
		command.IdempotencyKey = "k"

		hash, err := service.CanonicalHash(command)
		if err != nil {
			t.Fatalf("hash error = %v", err)
		}

		hashes = append(hashes, hash)
	}

	if hashes[0] != hashes[1] || hashes[1] != hashes[2] {
		t.Fatalf("hashes = %v, equivalent amounts must hash alike", hashes)
	}
}
