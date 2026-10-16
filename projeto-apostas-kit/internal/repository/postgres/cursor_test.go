package postgres

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/port"
)

const (
	cursorWallet identity.WalletID = "0192f291-27dd-7d3f-8071-5f8685deef37"
	cursorEntry  string            = "0192f299-0000-7e38-af88-e43f851a819d"
)

func TestCursorRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, 9, 8, 12, 0, 0, 123456000, time.UTC)

	text, err := encodeCursor(cursorWallet, createdAt, cursorEntry)
	if err != nil {
		t.Fatalf("encode error = %v", err)
	}

	cursor, err := decodeCursor(text, cursorWallet)
	if err != nil {
		t.Fatalf("decode error = %v", err)
	}

	if !cursor.CreatedAt.Equal(createdAt) || cursor.EntryID != cursorEntry {
		t.Fatalf("cursor = %+v", cursor)
	}
}

func TestDecodeCursorRejectsInvalidInput(t *testing.T) {
	valid, err := encodeCursor(cursorWallet, time.Now(), cursorEntry)
	if err != nil {
		t.Fatalf("encode error = %v", err)
	}

	tests := []struct {
		name     string
		text     string
		walletID identity.WalletID
	}{
		{name: "not base64", text: "***", walletID: cursorWallet},
		{name: "not json", text: base64.RawURLEncoding.EncodeToString([]byte("nope")), walletID: cursorWallet},
		{name: "empty json", text: base64.RawURLEncoding.EncodeToString([]byte("{}")), walletID: cursorWallet},
		{name: "other wallet", text: valid, walletID: "0192f291-27dd-7d3f-8071-000000000000"},
		{name: "empty", text: "", walletID: cursorWallet},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeCursor(test.text, test.walletID)

			if !errors.Is(err, port.ErrInvalidCursor) {
				t.Fatalf("error = %v, want %v", err, port.ErrInvalidCursor)
			}
		})
	}
}
