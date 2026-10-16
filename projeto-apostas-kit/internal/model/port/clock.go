package port

import (
	"time"

	"apostas-go/internal/model/identity"
)

// Clock fornece o instante atual, injetável para tornar o tempo testável.
type Clock interface {
	Now() time.Time
}

// IDGenerator gera identificadores de domínio (UUIDv7 na implementação).
type IDGenerator interface {
	NewWalletID() (identity.WalletID, error)
	NewTransactionID() (identity.TransactionID, error)
	NewEntryID() (identity.EntryID, error)
	NewEventID() (identity.EventID, error)
}
