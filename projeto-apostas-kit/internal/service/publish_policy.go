package service

import "time"

// PublishPolicy controla o publisher da outbox. Campos zerados assumem os padrões de
// DefaultPublishPolicy.
type PublishPolicy struct {
	// BatchSize é quantos eventos cada ciclo reivindica.
	BatchSize int
	// Lease é por quanto tempo um evento reivindicado fica reservado; se o publisher morrer,
	// outra instância o assume quando o lease vence.
	Lease          time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// DefaultPublishPolicy devolve os padrões: lotes de 50, lease de 30 segundos e atraso de
// 2 segundos dobrando até 1 minuto.
func DefaultPublishPolicy() PublishPolicy {
	return PublishPolicy{
		BatchSize:      50,
		Lease:          30 * time.Second,
		InitialBackoff: 2 * time.Second,
		MaxBackoff:     time.Minute,
	}
}

func (policy PublishPolicy) withDefaults() PublishPolicy {
	defaults := DefaultPublishPolicy()

	if policy.BatchSize <= 0 {
		policy.BatchSize = defaults.BatchSize
	}

	if policy.Lease <= 0 {
		policy.Lease = defaults.Lease
	}

	if policy.InitialBackoff <= 0 {
		policy.InitialBackoff = defaults.InitialBackoff
	}

	if policy.MaxBackoff <= 0 {
		policy.MaxBackoff = defaults.MaxBackoff
	}

	return policy
}

func (policy PublishPolicy) backoff(attempts int) time.Duration {
	return doublingDelay(policy.InitialBackoff, policy.MaxBackoff, attempts)
}
