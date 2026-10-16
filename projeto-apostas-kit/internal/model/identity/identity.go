package identity

import (
	"fmt"
	"strings"
)

const uuidLength = 36

// WalletID identifica uma carteira.
type WalletID string

// PlayerID identifica um jogador.
type PlayerID string

// TransactionID identifica uma transação interna (WagerTransaction).
type TransactionID string

// EntryID identifica um lançamento do ledger.
type EntryID string

// EventID identifica um evento de integração.
type EventID string

// ParseWalletID valida e normaliza o texto para minúsculas.
func ParseWalletID(text string) (WalletID, error) {
	return parse[WalletID](text)
}

// ParsePlayerID valida e normaliza o texto para minúsculas.
func ParsePlayerID(text string) (PlayerID, error) {
	return parse[PlayerID](text)
}

// ParseTransactionID valida e normaliza o texto para minúsculas.
func ParseTransactionID(text string) (TransactionID, error) {
	return parse[TransactionID](text)
}

// ParseEntryID valida e normaliza o texto para minúsculas.
func ParseEntryID(text string) (EntryID, error) {
	return parse[EntryID](text)
}

// ParseEventID valida e normaliza o texto para minúsculas.
func ParseEventID(text string) (EventID, error) {
	return parse[EventID](text)
}

// String devolve o UUID em texto.
func (id WalletID) String() string { return string(id) }

// String devolve o UUID em texto.
func (id PlayerID) String() string { return string(id) }

// String devolve o UUID em texto.
func (id TransactionID) String() string { return string(id) }

// String devolve o UUID em texto.
func (id EntryID) String() string { return string(id) }

// String devolve o UUID em texto.
func (id EventID) String() string { return string(id) }

// IsValid informa se o identificador está no formato canônico.
func (id EventID) IsValid() bool { return isCanonical(string(id)) }

// IsValid informa se o identificador está no formato canônico.
func (id WalletID) IsValid() bool { return isCanonical(string(id)) }

// IsValid informa se o identificador está no formato canônico.
func (id PlayerID) IsValid() bool { return isCanonical(string(id)) }

// IsValid informa se o identificador está no formato canônico.
func (id TransactionID) IsValid() bool { return isCanonical(string(id)) }

// IsValid informa se o identificador está no formato canônico.
func (id EntryID) IsValid() bool { return isCanonical(string(id)) }

func parse[T ~string](text string) (T, error) {
	canonical, err := normalize(text)
	if err != nil {
		return "", err
	}

	return T(canonical), nil
}

func isCanonical(text string) bool {
	canonical, err := normalize(text)

	return err == nil && canonical == text
}

func normalize(text string) (string, error) {
	if len(text) != uuidLength {
		return "", fmt.Errorf("%w: %q", ErrInvalidID, text)
	}

	for index, character := range text {
		if !isValidCharacter(index, character) {
			return "", fmt.Errorf("%w: %q", ErrInvalidID, text)
		}
	}

	return strings.ToLower(text), nil
}

func isValidCharacter(index int, character rune) bool {
	switch index {
	case 8, 13, 18, 23:
		return character == '-'
	default:
		return isHexadecimal(character)
	}
}

func isHexadecimal(character rune) bool {
	return character >= '0' && character <= '9' ||
		character >= 'a' && character <= 'f' ||
		character >= 'A' && character <= 'F'
}
