package money

import (
	"math"
	"strings"
)

const negativeSign = "-"

// Parse lê uma entrada financeira externa: decimal com até duas casas, sem sinal.
// "25", "25.5" e "25.00" são formas equivalentes e normalizam para 25.00;
// o hash de idempotência deve usar a forma normalizada (Amount).
func Parse(text string, currency Currency) (Money, error) {
	money, err := ParseSigned(text, currency)
	if err != nil {
		return Money{}, err
	}

	if strings.HasPrefix(text, negativeSign) {
		return Money{}, ErrNegativeNotAllowed
	}

	return money, nil
}

// ParseSigned aceita sinal negativo, para valores internos como diferenças.
func ParseSigned(text string, currency Currency) (Money, error) {
	if !currency.isSupported() {
		return Money{}, ErrUnknownCurrency
	}

	unsigned := strings.TrimPrefix(text, negativeSign)
	whole, fraction, err := splitDecimal(unsigned)
	if err != nil {
		return Money{}, err
	}

	negative := strings.HasPrefix(text, negativeSign)
	minorUnits, err := toMinorUnits(whole+fraction, negative)
	if err != nil {
		return Money{}, err
	}

	return Money{minorUnits: minorUnits, currency: currency}, nil
}

// splitDecimal valida a gramática e devolve a parte inteira e a fração com 2 dígitos.
func splitDecimal(text string) (string, string, error) {
	whole, fraction, hasPoint := strings.Cut(text, ".")
	if !isDigits(whole) || hasLeadingZero(whole) {
		return "", "", ErrInvalidFormat
	}

	if !hasPoint {
		return whole, "00", nil
	}

	if !isDigits(fraction) {
		return "", "", ErrInvalidFormat
	}

	if len(fraction) > decimalPlaces {
		return "", "", ErrScaleExceeded
	}

	return whole, fraction + strings.Repeat("0", decimalPlaces-len(fraction)), nil
}

// toMinorUnits acumula dígito a dígito em uint64, detectando overflow antes de somar.
func toMinorUnits(digits string, negative bool) (int64, error) {
	limit := uint64(math.MaxInt64)
	if negative {
		limit++
	}

	var value uint64
	for _, digit := range digits {
		next := uint64(digit - '0')
		if value > (limit-next)/10 {
			return 0, ErrOverflow
		}

		value = value*10 + next
	}

	return signed(value, negative), nil
}

func signed(value uint64, negative bool) int64 {
	if !negative || value == 0 {
		return int64(value)
	}

	return -int64(value-1) - 1
}

func isDigits(text string) bool {
	if text == "" {
		return false
	}

	for _, character := range text {
		if character < '0' || character > '9' {
			return false
		}
	}

	return true
}

func hasLeadingZero(whole string) bool {
	return len(whole) > 1 && whole[0] == '0'
}
