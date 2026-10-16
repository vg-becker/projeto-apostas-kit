package http

import (
	"apostas-go/internal/controller/wire"
	"apostas-go/internal/model/identity"
	"apostas-go/internal/service"
)

type openWalletRequest struct {
	PlayerID       string      `json:"playerId"`
	InitialBalance *wire.Money `json:"initialBalance"`
}

// operationRequest é o corpo de POST /wagering/transactions: o providerId, que precisa
// coincidir com a identidade autenticada, mais os campos de negócio compartilhados com o SQS.
type operationRequest struct {
	ProviderID string `json:"providerId"`
	wire.OperationFields
}

func (request openWalletRequest) command(correlationID string) (service.OpenWalletCommand, error) {
	playerID, err := identity.ParsePlayerID(request.PlayerID)
	if err != nil {
		return service.OpenWalletCommand{}, err
	}

	balance, err := request.InitialBalance.Parse("initialBalance")
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
	command, err := request.Command()
	if err != nil {
		return service.ProcessOperationCommand{}, err
	}

	command.ProviderID = providerID
	command.IdempotencyKey = idempotencyKey
	command.CorrelationID = correlationID

	return command, nil
}
