package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"apostas-go/internal/model/money"
)

// CanonicalHash calcula o hash determinístico dos campos de negócio da operação:
// SHA-256, em hex minúsculo, de um JSON com chaves ordenadas. A chave de idempotência e
// os metadados de transporte ficam de fora, então HTTP e SQS produzem o mesmo hash. O valor
// entra na forma normalizada (Money.Amount), de modo que "25", "25.5" e "25.00" equivalem.
func CanonicalHash(command ProcessOperationCommand) (string, error) {
	if !command.Money.IsInitialized() {
		return "", money.ErrUninitialized
	}

	encoded, err := canonicalJSON(businessFields(command))
	if err != nil {
		return "", err
	}

	digest := sha256.Sum256(encoded)

	return hex.EncodeToString(digest[:]), nil
}

func businessFields(command ProcessOperationCommand) map[string]any {
	fields := map[string]any{
		"providerId":            command.ProviderID,
		"externalTransactionId": command.ExternalTransactionID,
		"playerId":              command.PlayerID.String(),
		"walletId":              command.WalletID.String(),
		"roundId":               command.RoundID,
		"gameId":                command.GameID,
		"kind":                  command.Kind.String(),
		"money": map[string]string{
			"amount":   command.Money.Amount(),
			"currency": command.Money.Currency().String(),
		},
	}

	if command.ReferenceExternalTransactionID != "" {
		fields["referenceExternalTransactionId"] = command.ReferenceExternalTransactionID
	}

	return fields
}

// canonicalJSON serializa sem escape de HTML; o encoder do Go ordena as chaves dos mapas.
func canonicalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer

	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("service: canonical json: %w", err)
	}

	return []byte(strings.TrimSuffix(buffer.String(), "\n")), nil
}
