package money

import "fmt"

// Currency é um código ISO 4217 cuja unidade menor tem exatamente duas casas.
type Currency string

// Moedas suportadas.
const (
	BRL Currency = "BRL"
	USD Currency = "USD"
	EUR Currency = "EUR"
)

// NewCurrency valida o código recebido de uma fronteira externa.
func NewCurrency(code string) (Currency, error) {
	currency := Currency(code)
	if !currency.isSupported() {
		return "", fmt.Errorf("%w: %q", ErrUnknownCurrency, code)
	}

	return currency, nil
}

// String devolve o código ISO 4217.
func (currency Currency) String() string {
	return string(currency)
}

func (currency Currency) isSupported() bool {
	switch currency {
	case BRL, USD, EUR:
		return true
	default:
		return false
	}
}
