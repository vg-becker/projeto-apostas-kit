package money

import (
	"fmt"
	"math"
)

const (
	decimalPlaces     = 2
	minorUnitsPerUnit = 100
)

// Money é um value object imutável: centavos em int64 mais a moeda.
// O zero value não é válido; construa com New, Zero ou Parse.
type Money struct {
	minorUnits int64
	currency   Currency
}

// New cria um valor, que pode ser negativo, a partir de unidades menores.
func New(minorUnits int64, currency Currency) (Money, error) {
	if !currency.isSupported() {
		return Money{}, fmt.Errorf("%w: %q", ErrUnknownCurrency, currency)
	}

	return Money{minorUnits: minorUnits, currency: currency}, nil
}

// Zero cria o valor zero da moeda.
func Zero(currency Currency) (Money, error) {
	return New(0, currency)
}

// MinorUnits devolve o valor em centavos.
func (money Money) MinorUnits() int64 {
	return money.minorUnits
}

// Currency devolve a moeda.
func (money Money) Currency() Currency {
	return money.currency
}

// IsInitialized informa se o valor foi criado por um construtor.
func (money Money) IsInitialized() bool {
	return money.currency.isSupported()
}

// IsZero informa se o valor é exatamente zero.
func (money Money) IsZero() bool {
	return money.minorUnits == 0
}

// IsPositive informa se o valor é maior que zero.
func (money Money) IsPositive() bool {
	return money.minorUnits > 0
}

// IsNegative informa se o valor é menor que zero.
func (money Money) IsNegative() bool {
	return money.minorUnits < 0
}

// Add soma dois valores da mesma moeda.
func (money Money) Add(other Money) (Money, error) {
	if err := money.requireSameCurrency(other); err != nil {
		return Money{}, err
	}

	if addOverflows(money.minorUnits, other.minorUnits) {
		return Money{}, ErrOverflow
	}

	return Money{money.minorUnits + other.minorUnits, money.currency}, nil
}

// Sub subtrai other de money; o resultado pode ser negativo.
func (money Money) Sub(other Money) (Money, error) {
	if err := money.requireSameCurrency(other); err != nil {
		return Money{}, err
	}

	if subOverflows(money.minorUnits, other.minorUnits) {
		return Money{}, ErrOverflow
	}

	return Money{money.minorUnits - other.minorUnits, money.currency}, nil
}

// Negate inverte o sinal do valor.
func (money Money) Negate() (Money, error) {
	if !money.IsInitialized() {
		return Money{}, ErrUninitialized
	}

	if money.minorUnits == math.MinInt64 {
		return Money{}, ErrOverflow
	}

	return Money{-money.minorUnits, money.currency}, nil
}

// Compare devolve -1, 0 ou 1 para valores da mesma moeda.
func (money Money) Compare(other Money) (int, error) {
	if err := money.requireSameCurrency(other); err != nil {
		return 0, err
	}

	switch {
	case money.minorUnits < other.minorUnits:
		return -1, nil
	case money.minorUnits > other.minorUnits:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal informa igualdade de valor e moeda.
func (money Money) Equal(other Money) bool {
	return money == other
}

// Amount devolve o valor decimal canônico, como "25.00" ou "-0.05".
func (money Money) Amount() string {
	magnitude := absolute(money.minorUnits)
	sign := ""
	if money.minorUnits < 0 {
		sign = "-"
	}

	return fmt.Sprintf(
		"%s%d.%02d",
		sign,
		magnitude/minorUnitsPerUnit,
		magnitude%minorUnitsPerUnit,
	)
}

// String devolve o valor seguido da moeda, para logs e mensagens de erro.
func (money Money) String() string {
	return money.Amount() + " " + money.currency.String()
}

func (money Money) requireSameCurrency(other Money) error {
	if !money.IsInitialized() || !other.IsInitialized() {
		return ErrUninitialized
	}

	if money.currency != other.currency {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, money.currency, other.currency)
	}

	return nil
}

func addOverflows(left int64, right int64) bool {
	if right > 0 {
		return left > math.MaxInt64-right
	}

	return left < math.MinInt64-right
}

func subOverflows(left int64, right int64) bool {
	if right > 0 {
		return left < math.MinInt64+right
	}

	return left > math.MaxInt64+right
}

func absolute(value int64) uint64 {
	if value < 0 {
		return uint64(-(value + 1)) + 1
	}

	return uint64(value)
}
