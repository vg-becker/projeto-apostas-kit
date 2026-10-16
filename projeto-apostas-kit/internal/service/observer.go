package service

import (
	"errors"
	"time"

	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
)

// Origem do processamento de uma operação: o caminho pelo qual ela chegou, não o transporte.
const (
	// SourceRequest é a chamada síncrona de OperationService.Process.
	SourceRequest = "request"
	// SourceMessage é o tratamento de uma mensagem pela inbox.
	SourceMessage = "message"
	// SourcePendingReference é a retomada de uma PENDING_REFERENCE pelo resolver.
	SourcePendingReference = "pending_reference"
	// SourcePending é a retomada de uma PENDING abandonada pelo resumer.
	SourcePending = "pending"
)

// Resultado de um processamento. Um resultado de negócio (inclusive REJECTED) é "applied": o
// status e o código de falha dizem qual.
const (
	ResultApplied             = "applied"
	ResultReplay              = "replay"
	ResultDuplicate           = "duplicate"
	ResultIdempotencyConflict = "idempotency_conflict"
	// ResultConcurrencyConflict deve ficar sempre em zero: a carteira é bloqueada antes de
	// ser lida, então um conflito de versão indica um defeito.
	ResultConcurrencyConflict = "concurrency_conflict"
	ResultInvalid             = "invalid"
	ResultTransient           = "transient"
	ResultError               = "error"
)

// OperationObservation descreve um processamento já confirmado (ou que falhou), sem valores
// monetários nem o corpo da operação: só identificadores e classificações.
type OperationObservation struct {
	Source      string
	Kind        string
	Result      string
	Status      string
	FailureCode string

	ProviderID    string
	WalletID      string
	TransactionID string
	CorrelationID string
	MessageID     string

	Duration time.Duration
}

// ReconciliationObservation é o resultado de uma conferência de saldo contra o ledger.
type ReconciliationObservation struct {
	WalletID   string
	Consistent bool
}

// Observer recebe o que o Service fez, para métricas e logs. É opcional, e a implementação
// deve ser rápida e segura para uso concorrente. Uma operação de mensagem é observada só
// depois do commit da transação da inbox.
type Observer interface {
	ObserveOperation(observation OperationObservation)
	ObserveReconciliation(observation ReconciliationObservation)
}

type noopObserver struct{}

func (noopObserver) ObserveOperation(OperationObservation)           {}
func (noopObserver) ObserveReconciliation(ReconciliationObservation) {}

func observerOrNoop(observer Observer) Observer {
	if observer == nil {
		return noopObserver{}
	}

	return observer
}

// CombineObservers entrega cada observação a todos os observers, na ordem dada.
func CombineObservers(observers ...Observer) Observer {
	return combinedObserver(observers)
}

type combinedObserver []Observer

func (combined combinedObserver) ObserveOperation(observation OperationObservation) {
	for _, observer := range combined {
		observer.ObserveOperation(observation)
	}
}

func (combined combinedObserver) ObserveReconciliation(observation ReconciliationObservation) {
	for _, observer := range combined {
		observer.ObserveReconciliation(observation)
	}
}

// resultOfError classifica uma falha. Conflitos de idempotência e de concorrência vêm antes
// das classes gerais, e o transitório tem precedência sobre o permanente (como em Classify).
func resultOfError(err error) string {
	switch {
	case errors.Is(err, ErrIdempotencyConflict), errors.Is(err, ErrInboundHashConflict):
		return ResultIdempotencyConflict
	case errors.Is(err, port.ErrVersionConflict):
		return ResultConcurrencyConflict
	}

	switch Classify(err) {
	case ClassTransient:
		return ResultTransient
	case ClassPermanent:
		return ResultInvalid
	default:
		return ResultError
	}
}

// observeCommand observa uma operação que chegou como pedido (requisição ou mensagem).
func observeCommand(
	observer Observer,
	source string,
	command ProcessOperationCommand,
	started time.Time,
	result OperationResult,
	err error,
) {
	observation := OperationObservation{
		Source:        source,
		Kind:          string(command.Kind),
		ProviderID:    command.ProviderID,
		WalletID:      command.WalletID.String(),
		CorrelationID: command.CorrelationID,
		Duration:      time.Since(started),
	}

	if source == SourceMessage {
		observation.MessageID = command.CausationID
	}

	fillOutcome(&observation, result, err)
	observer.ObserveOperation(observation)
}

func fillOutcome(observation *OperationObservation, result OperationResult, err error) {
	if err != nil {
		observation.Result = resultOfError(err)

		return
	}

	observation.Result = ResultApplied
	observation.Status = string(result.Status)
	observation.FailureCode = string(result.FailureCode)
	observation.TransactionID = result.TransactionID.String()

	if result.IdempotentReplay {
		observation.Result = ResultReplay
	}
}

// observeClaimed observa a retomada de uma transação reivindicada por um worker.
func observeClaimed(
	observer Observer,
	source string,
	pending *wager.Transaction,
	started time.Time,
	result OperationResult,
	err error,
) {
	observation := OperationObservation{
		Source:        source,
		Kind:          string(pending.Kind()),
		ProviderID:    pending.ProviderID(),
		WalletID:      pending.WalletID().String(),
		TransactionID: pending.ID().String(),
		CorrelationID: pending.CorrelationID(),
		Duration:      time.Since(started),
	}

	fillOutcome(&observation, result, err)
	observer.ObserveOperation(observation)
}
