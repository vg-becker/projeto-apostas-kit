package event

// Type é o nome do evento no contrato de saída.
type Type string

// Tipos de evento exigidos pelo enunciado.
const (
	TypeWagerTransactionProcessed        Type = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         Type = "WagerTransactionRejected"
	TypeWalletBalanceChanged             Type = "WalletBalanceChanged"
	TypeWagerTransactionPendingReference Type = "WagerTransactionPendingReference"
)

// currentVersion é a versão do contrato de todos os eventos atuais.
const currentVersion = 1

// String devolve o nome do evento.
func (eventType Type) String() string {
	return string(eventType)
}
