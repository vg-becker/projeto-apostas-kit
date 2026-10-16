package http

import (
	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/service"
)

// Os campos monetários são texto: um número JSON é recusado na decodificação, pois dinheiro
// nunca passa por ponto flutuante.
type moneyRequest struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type openWalletRequest struct {
	PlayerID       string        `json:"playerId"`
	InitialBalance *moneyRequest `json:"initialBalance"`
}

type operationRequest struct {
	ProviderID                     string        `json:"providerId"`
	ExternalTransactionID          string        `json:"externalTransactionId"`
	PlayerID                       string        `json:"playerId"`
	WalletID                       string        `json:"walletId"`
	RoundID                        string        `json:"roundId"`
	GameID                         string        `json:"gameId"`
	Kind                           string        `json:"kind"`
	Money                          *moneyRequest `json:"money"`
	ReferenceExternalTransactionID string        `json:"referenceExternalTransactionId"`
}

func (request *moneyRequest) parse(field string) (money.Money, error) {
	if request == nil {
		return money.Money{}, badRequest(field + " is required.")
	}

	currency, err := money.NewCurrency(request.Currency)
	if err != nil {
		return money.Money{}, err
	}

	return money.Parse(request.Amount, currency)
}

func (request openWalletRequest) command(correlationID string) (service.OpenWalletCommand, error) {
	playerID, err := identity.ParsePlayerID(request.PlayerID)
	if err != nil {
		return service.OpenWalletCommand{}, err
	}

	balance, err := request.InitialBalance.parse("initialBalance")
	if err != nil {
		return service.OpenWalletCommand{}, err
	}

	return service.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: balance,
		CorrelationID:  correlationID,
	}, nil
}

// command monta o pedido de operação. O provedor vem da identidade autenticada (providerID),
// e a chave de idempotência é a recebida, sem nenhuma substituição.
func (request operationRequest) command(
	providerID string,
	idempotencyKey string,
	correlationID string,
) (service.ProcessOperationCommand, error) {
	command, err := request.identified()
	if err != nil {
		return service.ProcessOperationCommand{}, err
	}

	command.Money, err = request.Money.parse("money")
	if err != nil {
		return service.ProcessOperationCommand{}, err
	}

	command.ProviderID = providerID
	command.IdempotencyKey = idempotencyKey
	command.CorrelationID = correlationID

	return command, nil
}

// identified converte os campos que identificam a operação (jogador, carteira e tipo).
func (request operationRequest) identified() (service.ProcessOperationCommand, error) {
	playerID, err := identity.ParsePlayerID(request.PlayerID)
	if err != nil {
		return service.ProcessOperationCommand{}, err
	}

	walletID, err := identity.ParseWalletID(request.WalletID)
	if err != nil {
		return service.ProcessOperationCommand{}, err
	}

	kind, err := wager.ParseExternalKind(request.Kind)
	if err != nil {
		return service.ProcessOperationCommand{}, err
	}

	return service.ProcessOperationCommand{
		ExternalTransactionID:          request.ExternalTransactionID,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        request.RoundID,
		GameID:                         request.GameID,
		Kind:                           kind,
		ReferenceExternalTransactionID: request.ReferenceExternalTransactionID,
	}, nil
}
