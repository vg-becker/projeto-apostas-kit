package service

import (
	"errors"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
)

// FailureClass diz como o consumidor deve tratar uma mensagem cujo tratamento falhou.
type FailureClass int

// Classes de falha. ClassUnknown é um erro não previsto, tratado como transitório (a opção
// conservadora) para que a mensagem espere e acabe na DLQ pelo broker, em vez de ser
// descartada. ClassTransient é uma indisponibilidade temporária: a mensagem deve ser
// repetida. ClassPermanent é uma mensagem que nunca vai passar: vai para a DLQ.
const (
	ClassUnknown FailureClass = iota
	ClassTransient
	ClassPermanent
)

// permanentErrors são os erros de entrada: os mesmos que o HTTP responde como 4xx. O teste
// de consistência da View garante que as duas listas não divergem.
var permanentErrors = []error{
	ErrWalletExists,
	ErrWalletNotFound,
	ErrTransactionNotFound,
	ErrIdempotencyConflict,
	ErrWalletPlayerMismatch,
	ErrCurrencyMismatch,
	ErrInboundHashConflict,
	ErrInvalidMessage,
	wager.ErrOpeningNotAllowed,
	wager.ErrInvalidKind,
	wager.ErrAmountPolicy,
	wager.ErrInvalidTransaction,
	wallet.ErrInvalidState,
	identity.ErrInvalidID,
	money.ErrInvalidFormat,
	money.ErrScaleExceeded,
	money.ErrNegativeNotAllowed,
	money.ErrOverflow,
	money.ErrUninitialized,
	money.ErrUnknownCurrency,
	money.ErrCurrencyMismatch,
	port.ErrInvalidCursor,
}

// Classify separa transitório, permanente e desconhecido. O transitório tem precedência:
// uma falha de infraestrutura nunca condena a mensagem.
func Classify(err error) FailureClass {
	if IsTransient(err) {
		return ClassTransient
	}

	if IsPermanent(err) {
		return ClassPermanent
	}

	return ClassUnknown
}

// IsPermanent informa se o erro é de entrada: repetir a mesma mensagem não adianta.
func IsPermanent(err error) bool {
	for _, permanent := range permanentErrors {
		if errors.Is(err, permanent) {
			return true
		}
	}

	return false
}
