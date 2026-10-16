package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"apostas-go/internal/controller/sqs"
	"apostas-go/internal/controller/worker"
)

// otherMethod agrupa os métodos HTTP desconhecidos: um cliente não pode criar séries novas
// enviando métodos inventados.
const otherMethod = "OTHER"

// Todos os rótulos têm valores de um conjunto pequeno e fixo: método, padrão de rota (nunca
// o caminho com IDs), status, desfecho, motivo de DLQ e nome do worker. Nunca entram
// walletId, providerId, messageId nem chave de idempotência.
var requestDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Metrics reúne as métricas do serviço sobre um registro próprio (não o global).
type Metrics struct {
	registry   *prometheus.Registry
	http       httpMetrics
	consumer   consumerMetrics
	workers    workerMetrics
	operations operationMetrics
}

type httpMetrics struct {
	requests *prometheus.CounterVec
	latency  *prometheus.HistogramVec
}

type consumerMetrics struct {
	messages    *prometheus.CounterVec
	deadLetters *prometheus.CounterVec
}

type workerMetrics struct {
	iterations *prometheus.CounterVec
	duration   *prometheus.HistogramVec
}

// New cria o registro com as métricas do processo (Go e sistema) e as do serviço.
func New() *Metrics {
	metrics := &Metrics{
		registry:   prometheus.NewRegistry(),
		http:       newHTTPMetrics(),
		consumer:   newConsumerMetrics(),
		workers:    newWorkerMetrics(),
		operations: newOperationMetrics(),
	}

	metrics.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		metrics.http.requests,
		metrics.http.latency,
		metrics.consumer.messages,
		metrics.consumer.deadLetters,
		metrics.workers.iterations,
		metrics.workers.duration,
	)
	metrics.registry.MustRegister(metrics.operations.collectors()...)

	return metrics
}

func newHTTPMetrics() httpMetrics {
	return httpMetrics{
		requests: newCounter(
			"http_requests_total",
			"HTTP requests handled, by method, route pattern and status.",
			"method", "route", "status",
		),
		latency: newDurationHistogram(
			"http_request_duration_seconds",
			"HTTP request duration.",
			"method", "route",
		),
	}
}

func newConsumerMetrics() consumerMetrics {
	return consumerMetrics{
		messages: newCounter(
			"sqs_consumer_messages_total",
			"Messages handled by the SQS consumer, by outcome.",
			"outcome",
		),
		deadLetters: newCounter(
			"sqs_consumer_dead_letters_total",
			"Messages sent to the dead-letter queue, by reason.",
			"reason",
		),
	}
}

func newWorkerMetrics() workerMetrics {
	return workerMetrics{
		iterations: newCounter(
			"worker_iterations_total",
			"Worker iterations, by outcome (ok, error, panic).",
			"worker", "outcome",
		),
		duration: newDurationHistogram(
			"worker_iteration_duration_seconds",
			"Worker iteration duration.",
			"worker",
		),
	}
}

func newCounter(name string, help string, labels ...string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
}

func newDurationHistogram(name string, help string, labels ...string) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(
		prometheus.HistogramOpts{Name: name, Help: help, Buckets: requestDurationBuckets},
		labels,
	)
}

// Handler serve as métricas no formato de texto do Prometheus. Uma fonte que falha na
// leitura só omite a sua métrica; as demais continuam saindo.
func (metrics *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(metrics.registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	})
}

// ObserveRequest implementa controller/http.RequestObserver.
func (metrics *Metrics) ObserveRequest(
	method string,
	route string,
	status int,
	duration time.Duration,
) {
	method = knownMethod(method)

	metrics.http.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	metrics.http.latency.WithLabelValues(method, route).Observe(duration.Seconds())
}

// Observe implementa controller/sqs.Observer.
func (metrics *Metrics) Observe(event sqs.Event) {
	metrics.consumer.messages.WithLabelValues(string(event.Outcome)).Inc()

	if event.Outcome == sqs.OutcomeDeadLettered {
		metrics.consumer.deadLetters.WithLabelValues(event.Reason).Inc()
	}
}

// WorkerObserver devolve o observador dos workers. É um tipo à parte porque o método
// Observe de Metrics já é o do consumidor SQS.
func (metrics *Metrics) WorkerObserver() worker.Observer {
	return workerObserver{metrics: metrics}
}

type workerObserver struct {
	metrics *Metrics
}

func (observer workerObserver) Observe(iteration worker.Iteration) {
	outcome := "ok"

	switch {
	case iteration.Panicked:
		outcome = "panic"
	case iteration.Failed:
		outcome = "error"
	}

	observer.metrics.workers.iterations.WithLabelValues(iteration.Worker, outcome).Inc()
	observer.metrics.workers.duration.WithLabelValues(iteration.Worker).
		Observe(iteration.Duration.Seconds())
}

func knownMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	default:
		return otherMethod
	}
}
