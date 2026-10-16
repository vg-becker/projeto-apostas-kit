package money

import "errors"

// Erros de domínio de Money; classifique com errors.Is.
var (
	ErrInvalidFormat      = errors.New("money: invalid decimal format")
	ErrScaleExceeded      = errors.New("money: more than two decimal places")
	ErrNegativeNotAllowed = errors.New("money: negative amount not allowed")
	ErrCurrencyMismatch   = errors.New("money: currency mismatch")
	ErrOverflow           = errors.New("money: int64 overflow")
	ErrUnknownCurrency    = errors.New("money: unknown currency")
	ErrUninitialized      = errors.New("money: uninitialized value")
)
