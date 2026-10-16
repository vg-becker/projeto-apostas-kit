package idgen

import (
	"fmt"

	"github.com/google/uuid"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/port"
)

var _ port.IDGenerator = Generator{}

// Generator implementa port.IDGenerator com UUIDv7, ordenável por tempo.
type Generator struct{}

// NewWalletID gera o identificador de uma carteira.
func (Generator) NewWalletID() (identity.WalletID, error) {
	text, err := newText()

	return identity.WalletID(text), err
}

// NewTransactionID gera o identificador de uma transação.
func (Generator) NewTransactionID() (identity.TransactionID, error) {
	text, err := newText()

	return identity.TransactionID(text), err
}

// NewEntryID gera o identificador de um lançamento do ledger.
func (Generator) NewEntryID() (identity.EntryID, error) {
	text, err := newText()

	return identity.EntryID(text), err
}

// NewEventID gera o identificador de um evento.
func (Generator) NewEventID() (identity.EventID, error) {
	text, err := newText()

	return identity.EventID(text), err
}

func newText() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("idgen: generate uuid v7: %w", err)
	}

	return id.String(), nil
}
