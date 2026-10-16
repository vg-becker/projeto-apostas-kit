package postgres

import (
	"fmt"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/wager"
)

// transactionColumns é a lista de colunas lida por todas as consultas de transação,
// na mesma ordem de transactionRow.destinations.
const transactionColumns = `
	       id::text,
	       kind,
	       status,
	       wallet_id::text,
	       player_id::text,
	       currency,
	       amount_minor_units,
	       provider_id,
	       external_transaction_id,
	       idempotency_key,
	       payload_hash,
	       round_id,
	       game_id,
	       reference_external_transaction_id,
	       resolved_reference_id::text,
	       failure_code,
	       result_balance_minor_units,
	       created_at,
	       updated_at,
	       correlation_id,
	       causation_id
`

// transactionRow recebe uma linha de wager_transactions; colunas anuláveis usam ponteiros.
type transactionRow struct {
	id                  string
	kind                string
	status              string
	walletID            string
	playerID            string
	currency            string
	amount              int64
	providerID          *string
	externalID          *string
	idempotencyKey      *string
	payloadHash         *string
	roundID             *string
	gameID              *string
	referenceExternalID *string
	resolvedReferenceID *string
	failureCode         *string
	resultBalance       *int64
	createdAt           time.Time
	updatedAt           time.Time
	correlationID       *string
	causationID         *string
}

func (row *transactionRow) destinations() []any {
	return []any{
		&row.id,
		&row.kind,
		&row.status,
		&row.walletID,
		&row.playerID,
		&row.currency,
		&row.amount,
		&row.providerID,
		&row.externalID,
		&row.idempotencyKey,
		&row.payloadHash,
		&row.roundID,
		&row.gameID,
		&row.referenceExternalID,
		&row.resolvedReferenceID,
		&row.failureCode,
		&row.resultBalance,
		&row.createdAt,
		&row.updatedAt,
		&row.correlationID,
		&row.causationID,
	}
}

func (row *transactionRow) transaction() (*wager.Transaction, error) {
	amount, err := money.New(row.amount, money.Currency(row.currency))
	if err != nil {
		return nil, fmt.Errorf("postgres: transaction %s amount: %w", row.id, err)
	}

	resultBalance, err := row.resultBalanceMoney()
	if err != nil {
		return nil, err
	}

	return wager.Rehydrate(row.snapshot(amount, resultBalance))
}

func (row *transactionRow) snapshot(amount money.Money, resultBalance money.Money) wager.Snapshot {
	resolved := identity.TransactionID(textOrEmpty(row.resolvedReferenceID))

	return wager.Snapshot{
		ID:                             identity.TransactionID(row.id),
		ProviderID:                     textOrEmpty(row.providerID),
		ExternalTransactionID:          textOrEmpty(row.externalID),
		IdempotencyKey:                 textOrEmpty(row.idempotencyKey),
		PayloadHash:                    textOrEmpty(row.payloadHash),
		WalletID:                       identity.WalletID(row.walletID),
		PlayerID:                       identity.PlayerID(row.playerID),
		RoundID:                        textOrEmpty(row.roundID),
		GameID:                         textOrEmpty(row.gameID),
		Kind:                           wager.Kind(row.kind),
		Money:                          amount,
		ReferenceExternalTransactionID: textOrEmpty(row.referenceExternalID),
		ResolvedReferenceID:            resolved,
		Status:                         wager.Status(row.status),
		FailureCode:                    wager.FailureCode(textOrEmpty(row.failureCode)),
		ResultBalance:                  resultBalance,
		CorrelationID:                  textOrEmpty(row.correlationID),
		CausationID:                    textOrEmpty(row.causationID),
		CreatedAt:                      row.createdAt,
		UpdatedAt:                      row.updatedAt,
	}
}

func (row *transactionRow) resultBalanceMoney() (money.Money, error) {
	if row.resultBalance == nil {
		return money.Money{}, nil
	}

	balance, err := money.New(*row.resultBalance, money.Currency(row.currency))
	if err != nil {
		return money.Money{}, fmt.Errorf("postgres: transaction %s result: %w", row.id, err)
	}

	return balance, nil
}

func textOrEmpty(text *string) string {
	if text == nil {
		return ""
	}

	return *text
}

func nullableText(text string) *string {
	if text == "" {
		return nil
	}

	return &text
}

func nullableMinorUnits(amount money.Money) *int64 {
	if !amount.IsInitialized() {
		return nil
	}

	minorUnits := amount.MinorUnits()

	return &minorUnits
}
