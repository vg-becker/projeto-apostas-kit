package response

// Reconciliation compara o saldo armazenado com o reconstruído pelo ledger.
type Reconciliation struct {
	WalletID          string `json:"walletId"`
	StoredBalance     Money  `json:"storedBalance"`
	CalculatedBalance Money  `json:"calculatedBalance"`
	Difference        Money  `json:"difference"`
	Consistent        bool   `json:"consistent"`
	CheckedEntries    int64  `json:"checkedEntries"`
}
