package logging

import (
	"context"
	"log/slog"

	"apostas-go/internal/service"
)

var _ service.Observer = (*OperationLogger)(nil)

// OperationLogger registra uma linha por operação processada, com os identificadores que
// permitem rastreá-la (correlationId, messageId, transactionId, walletId, providerId).
// Nunca registra valores monetários, saldos, a chave de idempotência nem o corpo.
type OperationLogger struct {
	logger *slog.Logger
}

// NewOperationLogger cria o registrador sobre o logger dado.
func NewOperationLogger(logger *slog.Logger) *OperationLogger {
	return &OperationLogger{logger: logger}
}

// ObserveOperation implementa service.Observer.
func (operations *OperationLogger) ObserveOperation(observation service.OperationObservation) {
	attributes := []slog.Attr{
		slog.String("source", observation.Source),
		slog.String("result", observation.Result),
		slog.Int64("durationMs", observation.Duration.Milliseconds()),
	}

	for name, value := range map[string]string{
		"kind":          observation.Kind,
		"status":        observation.Status,
		"failureCode":   observation.FailureCode,
		"providerId":    observation.ProviderID,
		"walletId":      observation.WalletID,
		"transactionId": observation.TransactionID,
		"correlationId": observation.CorrelationID,
		"messageId":     observation.MessageID,
	} {
		if value != "" {
			attributes = append(attributes, slog.String(name, value))
		}
	}

	operations.logger.LogAttrs(
		context.Background(),
		levelOf(observation.Result),
		"operation",
		attributes...,
	)
}

// ObserveReconciliation implementa service.Observer. Só a divergência é registrada: é uma
// quebra de integridade financeira e merece um registro de erro.
func (operations *OperationLogger) ObserveReconciliation(
	observation service.ReconciliationObservation,
) {
	if observation.Consistent {
		return
	}

	operations.logger.Error(
		"reconciliation diverged",
		slog.String("walletId", observation.WalletID),
	)
}

func levelOf(result string) slog.Level {
	switch result {
	case service.ResultApplied, service.ResultReplay, service.ResultDuplicate:
		return slog.LevelInfo
	case service.ResultConcurrencyConflict, service.ResultError:
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}
