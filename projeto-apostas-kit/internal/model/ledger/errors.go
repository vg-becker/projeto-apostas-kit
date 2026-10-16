package ledger

import "errors"

// Erros de domínio do ledger; classifique com errors.Is.
var (
	ErrInvalidEntry    = errors.New("ledger: invalid entry")
	ErrBalanceMismatch = errors.New("ledger: balanceAfter does not match balanceBefore and money")
)
