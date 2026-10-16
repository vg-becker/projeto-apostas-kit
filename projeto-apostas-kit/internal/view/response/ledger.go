package response

// LedgerEntry é um lançamento imutável do ledger.
type LedgerEntry struct {
	ID            string `json:"id"`
	WalletID      string `json:"walletId"`
	TransactionID string `json:"transactionId"`
	Direction     string `json:"direction"`
	Money         Money  `json:"money"`
	BalanceBefore Money  `json:"balanceBefore"`
	BalanceAfter  Money  `json:"balanceAfter"`
	CreatedAt     string `json:"createdAt"`
}

// LedgerPage é uma página do ledger; NextCursor, opaco, só existe se houver mais páginas.
type LedgerPage struct {
	Entries    []LedgerEntry `json:"entries"`
	NextCursor string        `json:"nextCursor,omitempty"`
}
