package service

import (
	"errors"
	"testing"

	"apostas-go/internal/model/money"
	"apostas-go/internal/model/wager"
)

// Vetores calculados por outro programa (Python, chaves ordenadas,
// separadores compactos); se o algoritmo mudar, hashes já persistidos deixam de bater.
const (
	betVector    = "629836932b79106b99523d06a1e7fa80689b0ea1e1c47aa3f0a5a2c87d0c4344"
	refundVector = "f7834fcdc3ad724ef77ba4a3702b85ffe1d0425e088a411a12d447c920436767"
)

func brl(t *testing.T, text string) money.Money {
	t.Helper()

	amount, err := money.Parse(text, money.BRL)
	if err != nil {
		t.Fatalf("money.Parse(%q) error = %v", text, err)
	}

	return amount
}

func betCommand(t *testing.T) ProcessOperationCommand {
	t.Helper()

	return ProcessOperationCommand{
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		PlayerID:              "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
		WalletID:              "0192f291-27dd-7d3f-8071-5f8685deef37",
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  wager.Bet,
		Money:                 brl(t, "25.00"),
		CorrelationID:         "correlation-1",
		CausationID:           "msg-1",
	}
}

func TestCanonicalHashMatchesIndependentVectors(t *testing.T) {
	bet := betCommand(t)

	refund := betCommand(t)
	refund.Kind = wager.Refund
	refund.ReferenceExternalTransactionID = "transaction-1"

	tests := []struct {
		name    string
		command ProcessOperationCommand
		want    string
	}{
		{name: "bet", command: bet, want: betVector},
		{name: "refund with reference", command: refund, want: refundVector},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CanonicalHash(test.command)

			if err != nil || got != test.want {
				t.Fatalf("hash = %q (%v), want %q", got, err, test.want)
			}
		})
	}
}

func TestCanonicalHashIgnoresKeyAndTransportMetadata(t *testing.T) {
	original, _ := CanonicalHash(betCommand(t))

	changed := betCommand(t)
	changed.IdempotencyKey = "another-key"
	changed.CorrelationID = "another-correlation"
	changed.CausationID = "another-message"

	got, err := CanonicalHash(changed)

	if err != nil || got != original {
		t.Fatalf("hash = %q (%v), want the same as %q", got, err, original)
	}
}

func TestCanonicalHashNormalizesEquivalentAmounts(t *testing.T) {
	original, _ := CanonicalHash(betCommand(t))

	for _, text := range []string{"25", "25.0", "25.00"} {
		command := betCommand(t)
		command.Money = brl(t, text)

		got, err := CanonicalHash(command)

		if err != nil || got != original {
			t.Fatalf("amount %q: hash = %q (%v), want %q", text, got, err, original)
		}
	}
}

func TestCanonicalHashChangesWithEveryBusinessField(t *testing.T) {
	original, _ := CanonicalHash(betCommand(t))

	mutations := map[string]func(command *ProcessOperationCommand){
		"provider":  func(c *ProcessOperationCommand) { c.ProviderID = "provider-b" },
		"external":  func(c *ProcessOperationCommand) { c.ExternalTransactionID = "other" },
		"player":    func(c *ProcessOperationCommand) { c.PlayerID = "0192f28f-5dc0-7d58-bdb2-000000000000" },
		"wallet":    func(c *ProcessOperationCommand) { c.WalletID = "0192f291-27dd-7d3f-8071-000000000000" },
		"round":     func(c *ProcessOperationCommand) { c.RoundID = "round-1" },
		"game":      func(c *ProcessOperationCommand) { c.GameID = "other-game" },
		"kind":      func(c *ProcessOperationCommand) { c.Kind = wager.Win },
		"amount":    func(c *ProcessOperationCommand) { c.Money = brl(t, "25.01") },
		"reference": func(c *ProcessOperationCommand) { c.ReferenceExternalTransactionID = "ref" },
	}

	for name, mutate := range mutations {
		command := betCommand(t)
		mutate(&command)

		got, err := CanonicalHash(command)

		if err != nil || got == original {
			t.Fatalf("%s: hash = %q (%v), must differ from the original", name, got, err)
		}
	}
}

func TestCanonicalHashDistinguishesCurrencies(t *testing.T) {
	usd := betCommand(t)
	usdAmount, err := money.Parse("25.00", money.USD)
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}

	usd.Money = usdAmount

	original, _ := CanonicalHash(betCommand(t))
	got, err := CanonicalHash(usd)

	if err != nil || got == original {
		t.Fatalf("hash = %q (%v), the currency must be part of the hash", got, err)
	}
}

func TestCanonicalHashRejectsUninitializedMoney(t *testing.T) {
	command := betCommand(t)
	command.Money = money.Money{}

	_, err := CanonicalHash(command)

	if !errors.Is(err, money.ErrUninitialized) {
		t.Fatalf("error = %v, want %v", err, money.ErrUninitialized)
	}
}

func TestCanonicalHashIsLowercaseHexOfSha256(t *testing.T) {
	got, _ := CanonicalHash(betCommand(t))

	if len(got) != 64 {
		t.Fatalf("hash length = %d, want 64", len(got))
	}

	for _, character := range got {
		if !isLowerHexadecimal(character) {
			t.Fatalf("hash %q is not lowercase hex", got)
		}
	}
}

func isLowerHexadecimal(character rune) bool {
	isDigit := character >= '0' && character <= '9'
	isLetter := character >= 'a' && character <= 'f'

	return isDigit || isLetter
}
