package wager

import (
	"fmt"

	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/money"
)

// Kind é o tipo da transação.
type Kind string

// Tipos de transação. Opening é interno; os demais chegam por HTTP ou SQS.
const (
	Opening  Kind = "OPENING"
	Bet      Kind = "BET"
	Win      Kind = "WIN"
	Loss     Kind = "LOSS"
	Refund   Kind = "REFUND"
	Rollback Kind = "ROLLBACK"
)

// ParseExternalKind valida um tipo recebido de HTTP ou SQS e rejeita OPENING.
func ParseExternalKind(text string) (Kind, error) {
	kind := Kind(text)
	if kind == Opening {
		return "", ErrOpeningNotAllowed
	}

	if !kind.isValid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidKind, text)
	}

	return kind, nil
}

// String devolve o texto do tipo.
func (kind Kind) String() string {
	return string(kind)
}

// IsReversal informa se o tipo desfaz uma transação anterior.
func (kind Kind) IsReversal() bool {
	return kind == Refund || kind == Rollback
}

// RequiresReference informa se referenceExternalTransactionId é obrigatório.
func (kind Kind) RequiresReference() bool {
	return kind.IsReversal()
}

// AllowsReference informa se o tipo pode carregar uma referência.
func (kind Kind) AllowsReference() bool {
	return kind.IsReversal() || kind == Win
}

// Direction devolve a direção fixa do lançamento; false quando o tipo não tem
// direção própria (LOSS não movimenta; ROLLBACK depende da referência).
func (kind Kind) Direction() (ledger.Direction, bool) {
	switch kind {
	case Bet:
		return ledger.Debit, true
	case Win, Refund, Opening:
		return ledger.Credit, true
	default:
		return "", false
	}
}

// ValidateAmount aplica a política de zero: LOSS exige 0.00; os demais, valor positivo.
func (kind Kind) ValidateAmount(amount money.Money) error {
	if !amount.IsInitialized() {
		return money.ErrUninitialized
	}

	if kind == Loss && !amount.IsZero() {
		return fmt.Errorf("%w: LOSS requires 0.00, got %s", ErrAmountPolicy, amount)
	}

	if kind != Loss && !amount.IsPositive() {
		return fmt.Errorf(
			"%w: %s requires a positive amount, got %s",
			ErrAmountPolicy,
			kind,
			amount,
		)
	}

	return nil
}

func (kind Kind) isValid() bool {
	switch kind {
	case Opening, Bet, Win, Loss, Refund, Rollback:
		return true
	default:
		return false
	}
}
