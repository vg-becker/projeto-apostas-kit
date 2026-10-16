package service

import (
	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/wager"
)

// OpenWalletCommand pede a abertura interna de uma carteira.
type OpenWalletCommand struct {
	PlayerID       identity.PlayerID
	InitialBalance money.Money
	// CorrelationID é opcional; na falta dele, vale o ID da transação de abertura.
	CorrelationID string
}

// ProcessOperationCommand é a operação de um provedor, igual para HTTP e SQS. O
// ProviderID vem da identidade autenticada, nunca do corpo da requisição.
type ProcessOperationCommand struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       identity.PlayerID
	WalletID                       identity.WalletID
	RoundID                        string
	GameID                         string
	Kind                           wager.Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	// CorrelationID e CausationID são metadados de transporte: ficam fora do hash.
	CorrelationID string
	CausationID   string
}
