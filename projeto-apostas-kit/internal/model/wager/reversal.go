package wager

import (
	"fmt"

	"apostas-go/internal/model/ledger"
)

// Rejection é a recusa de uma reversão por regra de negócio; extraia com errors.As.
type Rejection struct {
	Code   FailureCode
	Reason string
}

// Error descreve a recusa.
func (rejection *Rejection) Error() string {
	return fmt.Sprintf("wager: rejected with %s: %s", rejection.Code, rejection.Reason)
}

// ValidateReversal aplica a política de reversões a REFUND e ROLLBACK.
//
// Política: REFUND só referencia BET; ROLLBACK referencia BET, WIN ou REFUND. Cada
// referência recebe no máximo uma reversão bem-sucedida, de qualquer tipo
// (alreadyReversed). Uma referência ainda pendente devolve ErrReferencePending, e o
// chamador espera; uma referência REJECTED ou FAILED é recusada de imediato.
func ValidateReversal(
	reversal *Transaction,
	reference *Transaction,
	alreadyReversed bool,
) error {
	if !reversal.snapshot.Kind.IsReversal() {
		return fmt.Errorf("%w: %s is not a reversal", ErrInvalidKind, reversal.snapshot.Kind)
	}

	if err := checkReferenceStatus(reference); err != nil {
		return err
	}

	if err := checkReferenceIdentity(reversal, reference); err != nil {
		return err
	}

	if alreadyReversed {
		return reject(AlreadyReversed, "the reference already received a reversal")
	}

	return checkReferenceAmount(reversal, reference)
}

// ReversalDirection devolve a direção do lançamento de uma reversão bem-sucedida:
// REFUND e ROLLBACK de BET creditam; ROLLBACK de WIN ou REFUND debita.
func ReversalDirection(reference Kind) (ledger.Direction, error) {
	switch reference {
	case Bet:
		return ledger.Credit, nil
	case Win, Refund:
		return ledger.Debit, nil
	default:
		return "", fmt.Errorf("%w: %s cannot be reversed", ErrInvalidKind, reference)
	}
}

func checkReferenceStatus(reference *Transaction) error {
	switch reference.snapshot.Status {
	case Processed:
		return nil
	case Pending, PendingReference:
		return ErrReferencePending
	default:
		return reject(
			ReferenceNotProcessed,
			"the reference ended as "+reference.snapshot.Status.String(),
		)
	}
}

func checkReferenceIdentity(reversal *Transaction, reference *Transaction) error {
	if !referencesMatch(reversal.snapshot, reference.snapshot) {
		return reject(
			ReferenceMismatch,
			"provider, reference id, player, wallet, round or currency differ",
		)
	}

	if !allowedReference(reversal.snapshot.Kind, reference.snapshot.Kind) {
		return reject(
			ReferenceKindInvalid,
			fmt.Sprintf("%s cannot reference %s", reversal.snapshot.Kind, reference.snapshot.Kind),
		)
	}

	return nil
}

func checkReferenceAmount(reversal *Transaction, reference *Transaction) error {
	if !reversal.snapshot.Money.Equal(reference.snapshot.Money) {
		return reject(AmountMismatch, "reversal amount must equal the referenced amount")
	}

	return nil
}

func referencesMatch(reversal Snapshot, reference Snapshot) bool {
	return reference.ProviderID == reversal.ProviderID &&
		reference.ExternalTransactionID == reversal.ReferenceExternalTransactionID &&
		reference.PlayerID == reversal.PlayerID &&
		reference.WalletID == reversal.WalletID &&
		reference.RoundID == reversal.RoundID &&
		reference.Money.Currency() == reversal.Money.Currency()
}

func allowedReference(reversal Kind, reference Kind) bool {
	switch reversal {
	case Refund:
		return reference == Bet
	case Rollback:
		return reference == Bet || reference == Win || reference == Refund
	default:
		return false
	}
}

func reject(code FailureCode, reason string) error {
	return &Rejection{Code: code, Reason: reason}
}
