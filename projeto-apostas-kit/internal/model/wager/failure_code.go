package wager

// FailureCode é o código estável de rejeição ou falha, exposto ao provedor.
type FailureCode string

// Códigos de falha. Não renomeie: fazem parte do contrato externo.
const (
	InsufficientFunds         FailureCode = "INSUFFICIENT_FUNDS"
	ReversalInsufficientFunds FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	ReferenceNotFound         FailureCode = "REFERENCE_NOT_FOUND"
	ReferenceNotProcessed     FailureCode = "REFERENCE_NOT_PROCESSED"
	ReferenceMismatch         FailureCode = "REFERENCE_MISMATCH"
	ReferenceKindInvalid      FailureCode = "REFERENCE_KIND_INVALID"
	AmountMismatch            FailureCode = "AMOUNT_MISMATCH"
	AlreadyReversed           FailureCode = "ALREADY_REVERSED"
	CurrencyMismatch          FailureCode = "CURRENCY_MISMATCH"
	InfrastructureFailure     FailureCode = "INFRASTRUCTURE_FAILURE"
)

// String devolve o texto do código.
func (code FailureCode) String() string {
	return string(code)
}

// IsCorrectable informa se o provedor pode corrigir a entrada e reenviar;
// os demais códigos encerram a operação de forma final.
func (code FailureCode) IsCorrectable() bool {
	return code == CurrencyMismatch
}

// IsValid informa se o código pertence ao contrato.
func (code FailureCode) IsValid() bool {
	switch code {
	case InsufficientFunds,
		ReversalInsufficientFunds,
		ReferenceNotFound,
		ReferenceNotProcessed,
		ReferenceMismatch,
		ReferenceKindInvalid,
		AmountMismatch,
		AlreadyReversed,
		CurrencyMismatch,
		InfrastructureFailure:
		return true
	default:
		return false
	}
}

func (code FailureCode) isRejection() bool {
	return code.IsValid() && code != InfrastructureFailure
}
