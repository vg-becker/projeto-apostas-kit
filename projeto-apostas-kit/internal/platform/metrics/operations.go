package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"apostas-go/internal/service"
)

var _ service.Observer = (*Metrics)(nil)

type operationMetrics struct {
	results   *prometheus.CounterVec
	duration  *prometheus.HistogramVec
	reconcile *prometheus.CounterVec
	diverged  prometheus.Counter
}

func newOperationMetrics() operationMetrics {
	return operationMetrics{
		results: newCounter(
			"operations_total",
			"Operations processed, by path, kind, result, status and failure code.",
			"source", "kind", "result", "status", "failure_code",
		),
		duration: newDurationHistogram(
			"operation_duration_seconds",
			"Operation processing time (including the transaction), by path and result.",
			"source", "result",
		),
		reconcile: newCounter(
			"reconciliations_total",
			"Wallet reconciliations, by result (consistent or divergent).",
			"result",
		),
		diverged: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reconciliation_divergences_total",
			Help: "Reconciliations where the stored balance differs from the ledger. Must be zero.",
		}),
	}
}

func (operations operationMetrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{
		operations.results,
		operations.duration,
		operations.reconcile,
		operations.diverged,
	}
}

// ObserveOperation implementa service.Observer. Os rótulos são enumerações fixas (caminho,
// tipo, resultado, status e código de falha); identificadores nunca viram rótulo.
func (metrics *Metrics) ObserveOperation(observation service.OperationObservation) {
	metrics.operations.results.WithLabelValues(
		observation.Source,
		observation.Kind,
		observation.Result,
		observation.Status,
		observation.FailureCode,
	).Inc()

	metrics.operations.duration.
		WithLabelValues(observation.Source, observation.Result).
		Observe(observation.Duration.Seconds())
}

// ObserveReconciliation implementa service.Observer.
func (metrics *Metrics) ObserveReconciliation(observation service.ReconciliationObservation) {
	if observation.Consistent {
		metrics.operations.reconcile.WithLabelValues("consistent").Inc()

		return
	}

	metrics.operations.reconcile.WithLabelValues("divergent").Inc()
	metrics.operations.diverged.Inc()
}
