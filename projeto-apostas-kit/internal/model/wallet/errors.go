package wallet

import (
	"errors"
	"fmt"

	"apostas-go/internal/model/money"
)

// Erros de domínio da carteira; classifique com errors.Is.
var (
	ErrInsufficientFunds = errors.New("wallet: insufficient funds")
	ErrAmountNotPositive = errors.New("wallet: amount must be positive")
	ErrInvalidState      = errors.New("wallet: invalid state")
	ErrCurrencyMismatch  = fmt.Errorf("wallet: %w", money.ErrCurrencyMismatch)
)
