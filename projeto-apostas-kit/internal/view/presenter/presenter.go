package presenter

import (
	"net/http"
	"time"

	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
	"apostas-go/internal/service"
	"apostas-go/internal/view/response"
)

// HealthResult é o resultado de uma verificação de readiness.
type HealthResult struct {
	Name string
	Up   bool
}

// Health monta o corpo do readiness e o seu status: 200 se todas as dependências estão UP,
// 503 se alguma está DOWN. Só os nomes e o estado aparecem, nunca o texto de um erro.
func Health(results []HealthResult) (int, response.Health) {
	body := response.Health{Status: "UP", Checks: make(map[string]string, len(results))}
	status := http.StatusOK

	for _, result := range results {
		if result.Up {
			body.Checks[result.Name] = "UP"

			continue
		}

		body.Checks[result.Name] = "DOWN"
		body.Status = "DOWN"
		status = http.StatusServiceUnavailable
	}

	if len(results) == 0 {
		body.Checks = nil
	}

	return status, body
}

// Money converte um valor monetário para o contrato externo.
func Money(amount money.Money) response.Money {
	return response.Money{
		Amount:   amount.Amount(),
		Currency: amount.Currency().String(),
	}
}

// Wallet converte a carteira.
func Wallet(target *wallet.Wallet) response.Wallet {
	return response.Wallet{
		ID:       target.ID().String(),
		PlayerID: target.PlayerID().String(),
		Balance:  Money(target.Balance()),
		Version:  target.Version(),
	}
}

// Operation converte o resultado de uma operação. O saldo só aparece em PROCESSED; o
// código de falha e se ele é corrigível, só quando há código.
func Operation(result service.OperationResult) response.Operation {
	body := response.Operation{
		TransactionID:    result.TransactionID.String(),
		Status:           result.Status.String(),
		IdempotentReplay: result.IdempotentReplay,
	}

	if result.Status == wager.Processed && result.Balance.IsInitialized() {
		balance := Money(result.Balance)
		body.Balance = &balance
	}

	body.FailureCode, body.Correctable = failure(result.FailureCode)

	return body
}

// OperationStatus devolve o status HTTP do resultado: 200 processada, 202 pendente, 422
// rejeitada por regra de negócio e 500 falha permanente de infraestrutura.
func OperationStatus(result service.OperationResult) int {
	switch result.Status {
	case wager.Processed:
		return http.StatusOK
	case wager.Pending, wager.PendingReference:
		return http.StatusAccepted
	case wager.Rejected:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// Transaction converte o detalhe de uma transação.
func Transaction(transaction *wager.Transaction) response.Transaction {
	snapshot := transaction.Snapshot()

	body := response.Transaction{
		TransactionID: snapshot.ID.String(),
		WalletID:      snapshot.WalletID.String(),
		PlayerID:      snapshot.PlayerID.String(),
		Kind:          snapshot.Kind.String(),
		Money:         Money(snapshot.Money),
		Status:        snapshot.Status.String(),
		CreatedAt:     timestamp(snapshot.CreatedAt),
		UpdatedAt:     timestamp(snapshot.UpdatedAt),
	}

	return withOptionalTransactionFields(body, snapshot)
}

// LedgerPage converte uma página do ledger; a lista nunca é nula.
func LedgerPage(page port.LedgerPage) response.LedgerPage {
	entries := make([]response.LedgerEntry, 0, len(page.Entries))
	for _, entry := range page.Entries {
		entries = append(entries, ledgerEntry(entry))
	}

	return response.LedgerPage{Entries: entries, NextCursor: page.NextCursor}
}

// Reconciliation converte o resultado da reconciliação.
func Reconciliation(result service.ReconciliationResult) response.Reconciliation {
	return response.Reconciliation{
		WalletID:          result.WalletID.String(),
		StoredBalance:     Money(result.StoredBalance),
		CalculatedBalance: Money(result.CalculatedBalance),
		Difference:        Money(result.Difference),
		Consistent:        result.Consistent,
		CheckedEntries:    result.CheckedEntries,
	}
}

func withOptionalTransactionFields(
	body response.Transaction,
	snapshot wager.Snapshot,
) response.Transaction {
	body.ProviderID = snapshot.ProviderID
	body.ExternalTransactionID = snapshot.ExternalTransactionID
	body.RoundID = snapshot.RoundID
	body.GameID = snapshot.GameID
	body.ReferenceExternalTransactionID = snapshot.ReferenceExternalTransactionID
	body.ResolvedReferenceID = snapshot.ResolvedReferenceID.String()
	body.FailureCode, body.Correctable = failure(snapshot.FailureCode)

	if snapshot.ResultBalance.IsInitialized() {
		balance := Money(snapshot.ResultBalance)
		body.Balance = &balance
	}

	return body
}

func ledgerEntry(entry ledger.Entry) response.LedgerEntry {
	return response.LedgerEntry{
		ID:            entry.ID().String(),
		WalletID:      entry.WalletID().String(),
		TransactionID: entry.TransactionID().String(),
		Direction:     entry.Direction().String(),
		Money:         Money(entry.Money()),
		BalanceBefore: Money(entry.BalanceBefore()),
		BalanceAfter:  Money(entry.BalanceAfter()),
		CreatedAt:     timestamp(entry.CreatedAt()),
	}
}

func failure(code wager.FailureCode) (string, *bool) {
	if code == "" {
		return "", nil
	}

	correctable := code.IsCorrectable()

	return code.String(), &correctable
}

func timestamp(instant time.Time) string {
	return instant.UTC().Format(time.RFC3339Nano)
}
