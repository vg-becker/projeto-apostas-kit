package worker

import (
	"context"
	"time"

	"apostas-go/internal/service"
)

// Nomes e esperas padrão dos workers do serviço.
const (
	OutboxPublisherName   = "outbox-publisher"
	ReferenceResolverName = "reference-resolver"
	PendingResumerName    = "pending-resumer"

	OutboxPublisherInterval   = 500 * time.Millisecond
	ReferenceResolverInterval = 2 * time.Second
	PendingResumerInterval    = 5 * time.Second
)

// OutboxPublisherConfig é a configuração padrão do worker que publica a outbox.
func OutboxPublisherConfig() Config {
	return Config{
		Name:     OutboxPublisherName,
		Interval: OutboxPublisherInterval,
		Jitter:   DefaultJitter,
	}
}

// ReferenceResolverConfig é a configuração padrão do worker de referências pendentes.
func ReferenceResolverConfig() Config {
	return Config{
		Name:     ReferenceResolverName,
		Interval: ReferenceResolverInterval,
		Jitter:   DefaultJitter,
	}
}

// PendingResumerConfig é a configuração padrão do worker que retoma transações PENDING.
func PendingResumerConfig() Config {
	return Config{
		Name:     PendingResumerName,
		Interval: PendingResumerInterval,
		Jitter:   DefaultJitter,
	}
}

// OutboxPublisherTask publica um lote da outbox por iteração e repete na hora enquanto
// houver eventos reivindicados. Falhas do destino voltam como erro, e o backoff próprio de
// cada evento (na outbox) impede o laço de martelar o destino.
func OutboxPublisherTask(publisher *service.OutboxPublisher) Task {
	return func(ctx context.Context) (bool, error) {
		summary, err := publisher.PublishPending(ctx)

		return err == nil && summary.Claimed > 0, err
	}
}

// ReferenceResolverTask resolve até limit referências vencidas por iteração e repete na
// hora enquanto o lote vier cheio.
func ReferenceResolverTask(resolver *service.ReferenceResolver, limit int) Task {
	return func(ctx context.Context) (bool, error) {
		summary, err := resolver.ResolveDue(ctx, limit)

		return err == nil && summary.Claimed >= limit, err
	}
}

// PendingResumerTask retoma até limit transações PENDING por iteração e repete na hora
// enquanto o lote vier cheio.
func PendingResumerTask(resumer *service.PendingResumer, limit int) Task {
	return func(ctx context.Context) (bool, error) {
		summary, err := resumer.ResumeAbandoned(ctx, limit)

		return err == nil && summary.Claimed >= limit, err
	}
}
