package wager

import "errors"

// Erros de domínio de WagerTransaction; classifique com errors.Is.
var (
	ErrInvalidTransaction = errors.New("wager: invalid transaction")
	ErrInvalidKind        = errors.New("wager: invalid kind")
	ErrOpeningNotAllowed  = errors.New("wager: OPENING is reserved for internal wallet opening")
	ErrAmountPolicy       = errors.New("wager: amount violates the policy of the kind")
	ErrInvalidTransition  = errors.New("wager: invalid state transition")
	ErrTerminalState      = errors.New("wager: transaction is in a terminal state")
	ErrInvalidFailureCode = errors.New("wager: invalid failure code")
	ErrReferencePending   = errors.New("wager: reference exists but is not finished yet")
)
