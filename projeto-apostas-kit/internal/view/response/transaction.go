package response

// Transaction é o detalhe de uma transação nas consultas. Os campos externos não existem
// na abertura interna, e os de resultado só existem quando aplicáveis.
type Transaction struct {
	TransactionID                  string `json:"transactionId"`
	ProviderID                     string `json:"providerId,omitempty"`
	ExternalTransactionID          string `json:"externalTransactionId,omitempty"`
	WalletID                       string `json:"walletId"`
	PlayerID                       string `json:"playerId"`
	RoundID                        string `json:"roundId,omitempty"`
	GameID                         string `json:"gameId,omitempty"`
	Kind                           string `json:"kind"`
	Money                          Money  `json:"money"`
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId,omitempty"`
	ResolvedReferenceID            string `json:"resolvedReferenceId,omitempty"`
	Status                         string `json:"status"`
	FailureCode                    string `json:"failureCode,omitempty"`
	Correctable                    *bool  `json:"correctable,omitempty"`
	Balance                        *Money `json:"balance,omitempty"`
	CreatedAt                      string `json:"createdAt"`
	UpdatedAt                      string `json:"updatedAt"`
}
