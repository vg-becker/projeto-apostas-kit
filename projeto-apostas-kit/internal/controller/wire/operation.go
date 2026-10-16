package wire

import (
	"fmt"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/service"
)

// Money é o valor monetário do contrato externo. Os dois campos são texto: um número JSON é
// recusado na decodificação, pois dinheiro nunca passa por ponto flutuante.
type Money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// OperationFields são os campos de negócio de uma operação, iguais no corpo HTTP e na
// mensagem SQS. O provedor e a chave de idempotência ficam de fora: cada entrada os obtém
// do seu jeito (identidade autenticada, cabeçalho ou envelope).
type OperationFields struct {
	ExternalTransactionID          string `json:"externalTransactionId"`
	PlayerID                       string `json:"playerId"`
	WalletID                       string `json:"walletId"`
	RoundID                        string `json:"roundId"`
	GameID                         string `json:"gameId"`
	Kind                           string `json:"kind"`
	Money                          *Money `json:"money"`
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId"`
}

// Parse converte o valor com a mesma rigidez em toda entrada: até duas casas, sem sinal,
// sem notação científica e com moeda conhecida. Um valor ausente é entrada inválida.
func (value *Money) Parse(field string) (money.Money, error) {
	if value == nil {
		return money.Money{}, fmt.Errorf("%w: %s is required", wager.ErrInvalidTransaction, field)
	}

	currency, err := money.NewCurrency(value.Currency)
	if err != nil {
		return money.Money{}, err
	}

	return money.Parse(value.Amount, currency)
}

// Command monta o pedido de operação a partir dos campos de negócio. O chamador completa
// ProviderID, IdempotencyKey, CorrelationID e CausationID.
func (fields OperationFields) Command() (service.ProcessOperationCommand, error) {
	playerID, err := identity.ParsePlayerID(fields.PlayerID)
	if err != nil {
		return service.ProcessOperationCommand{}, err
	}

	walletID, err := identity.ParseWalletID(fields.WalletID)
	if err != nil {
		return service.ProcessOperationCommand{}, err
	}

	kind, err := wager.ParseExternalKind(fields.Kind)
	if err != nil {
		return service.ProcessOperationCommand{}, err
	}

	amount, err := fields.Money.Parse("money")
	if err != nil {
		return service.ProcessOperationCommand{}, err
	}

	return service.ProcessOperationCommand{
		ExternalTransactionID:          fields.ExternalTransactionID,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        fields.RoundID,
		GameID:                         fields.GameID,
		Kind:                           kind,
		Money:                          amount,
		ReferenceExternalTransactionID: fields.ReferenceExternalTransactionID,
	}, nil
}
