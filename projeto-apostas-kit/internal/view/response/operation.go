package response

// Operation é o resultado de POST /wagering/transactions. Balance só existe quando a
// operação foi processada; FailureCode e Correctable só quando foi rejeitada ou falhou.
type Operation struct {
	TransactionID    string `json:"transactionId"`
	Status           string `json:"status"`
	Balance          *Money `json:"balance,omitempty"`
	FailureCode      string `json:"failureCode,omitempty"`
	Correctable      *bool  `json:"correctable,omitempty"`
	IdempotentReplay bool   `json:"idempotentReplay"`
}
