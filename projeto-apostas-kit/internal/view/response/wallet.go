package response

// Wallet é a carteira devolvida por POST /wallets e GET /wallets/{walletId}.
type Wallet struct {
	ID       string `json:"id"`
	PlayerID string `json:"playerId"`
	Balance  Money  `json:"balance"`
	Version  int64  `json:"version"`
}
