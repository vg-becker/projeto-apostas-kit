package postgres

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/port"
)

// ledgerCursor marca a última linha entregue; é opaco para o cliente e não é assinado.
type ledgerCursor struct {
	CreatedAt time.Time `json:"t"`
	EntryID   string    `json:"i"`
	WalletID  string    `json:"w"`
}

func encodeCursor(walletID identity.WalletID, createdAt time.Time, entryID string) (string, error) {
	raw, err := json.Marshal(ledgerCursor{
		CreatedAt: createdAt.UTC(),
		EntryID:   entryID,
		WalletID:  walletID.String(),
	})
	if err != nil {
		return "", fmt.Errorf("postgres: encode cursor: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCursor(text string, walletID identity.WalletID) (ledgerCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return ledgerCursor{}, fmt.Errorf("%w: %w", port.ErrInvalidCursor, err)
	}

	var cursor ledgerCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return ledgerCursor{}, fmt.Errorf("%w: %w", port.ErrInvalidCursor, err)
	}

	if cursor.CreatedAt.IsZero() || cursor.EntryID == "" || cursor.WalletID != walletID.String() {
		return ledgerCursor{}, fmt.Errorf("%w: wrong shape or wallet", port.ErrInvalidCursor)
	}

	return cursor, nil
}
