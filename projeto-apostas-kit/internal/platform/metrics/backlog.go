package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// scrapeTimeout limita as leituras feitas a cada coleta, para um banco ou broker lento não
// travar o /metrics.
const scrapeTimeout = 2 * time.Second

// Sources são as leituras feitas a cada coleta. Qualquer uma pode ser nil.
type Sources struct {
	// OutboxBacklog devolve os eventos ainda não publicados e a idade do mais antigo.
	OutboxBacklog func(ctx context.Context) (pending int64, oldest time.Duration, err error)
	// DeadLetterDepth devolve o número de mensagens na DLQ.
	DeadLetterDepth func(ctx context.Context) (int64, error)
}

// RegisterSources registra os gauges de atraso da outbox e de tamanho da DLQ. Os valores
// são lidos a cada coleta; se a leitura falha, a série daquela métrica simplesmente não sai.
func (metrics *Metrics) RegisterSources(sources Sources) error {
	return metrics.registry.Register(&sourceCollector{
		sources:  sources,
		pending:  gauge("outbox_pending_events", "Outbox events not yet published."),
		oldest:   gauge("outbox_oldest_pending_age_seconds", "Age of the oldest pending event."),
		deadDeep: gauge("sqs_dead_letter_queue_messages", "Approximate messages in the DLQ."),
	})
}

func gauge(name string, help string) *prometheus.Desc {
	return prometheus.NewDesc(name, help, nil, nil)
}

type sourceCollector struct {
	sources  Sources
	pending  *prometheus.Desc
	oldest   *prometheus.Desc
	deadDeep *prometheus.Desc
}

func (collector *sourceCollector) Describe(descriptions chan<- *prometheus.Desc) {
	descriptions <- collector.pending
	descriptions <- collector.oldest
	descriptions <- collector.deadDeep
}

func (collector *sourceCollector) Collect(out chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), scrapeTimeout)
	defer cancel()

	collector.collectOutbox(ctx, out)
	collector.collectDeadLetter(ctx, out)
}

func (collector *sourceCollector) collectOutbox(ctx context.Context, out chan<- prometheus.Metric) {
	if collector.sources.OutboxBacklog == nil {
		return
	}

	pending, oldest, err := collector.sources.OutboxBacklog(ctx)
	if err != nil {
		return
	}

	out <- prometheus.MustNewConstMetric(
		collector.pending,
		prometheus.GaugeValue,
		float64(pending),
	)
	out <- prometheus.MustNewConstMetric(
		collector.oldest,
		prometheus.GaugeValue,
		oldest.Seconds(),
	)
}

func (collector *sourceCollector) collectDeadLetter(
	ctx context.Context,
	out chan<- prometheus.Metric,
) {
	if collector.sources.DeadLetterDepth == nil {
		return
	}

	depth, err := collector.sources.DeadLetterDepth(ctx)
	if err != nil {
		return
	}

	out <- prometheus.MustNewConstMetric(
		collector.deadDeep,
		prometheus.GaugeValue,
		float64(depth),
	)
}
