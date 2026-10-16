package service

import (
	"errors"

	"apostas-go/internal/model/port"
)

// Erros dos casos de uso; classifique com errors.Is.
var (
	ErrWalletExists         = errors.New("service: wallet already exists for player and currency")
	ErrWalletNotFound       = errors.New("service: wallet not found")
	ErrTransactionNotFound  = errors.New("service: transaction not found")
	ErrIdempotencyConflict  = errors.New("service: idempotency key reused with different content")
	ErrWalletPlayerMismatch = errors.New("service: wallet belongs to another player")
	ErrCurrencyMismatch     = errors.New("service: operation currency differs from wallet currency")
)

// IsTransient informa se o erro é uma indisponibilidade temporária da infraestrutura:
// o chamador pode repetir o caso de uso com backoff.
func IsTransient(err error) bool {
	return errors.Is(err, port.ErrTransient)
}
