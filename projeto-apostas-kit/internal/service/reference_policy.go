package service

import "time"

// ReferencePolicy controla a espera por uma referência ainda indisponível: o atraso que
// dobra entre tentativas (backoff), o limite de tentativas e o TTL total. Campos zerados
// assumem os padrões de DefaultReferencePolicy.
type ReferencePolicy struct {
	MaxAttempts    int
	TTL            time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	// ClaimLease é por quanto tempo uma instância reserva uma pendência que reivindicou.
	ClaimLease time.Duration
}

// DefaultReferencePolicy devolve os padrões: 10 tentativas, TTL de 10 minutos e backoff
// de 2 segundos, dobrando até 1 minuto.
func DefaultReferencePolicy() ReferencePolicy {
	return ReferencePolicy{
		MaxAttempts:    10,
		TTL:            10 * time.Minute,
		InitialBackoff: 2 * time.Second,
		MaxBackoff:     time.Minute,
		ClaimLease:     time.Minute,
	}
}

func (policy ReferencePolicy) withDefaults() ReferencePolicy {
	defaults := DefaultReferencePolicy()

	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = defaults.MaxAttempts
	}

	if policy.TTL <= 0 {
		policy.TTL = defaults.TTL
	}

	if policy.InitialBackoff <= 0 {
		policy.InitialBackoff = defaults.InitialBackoff
	}

	if policy.MaxBackoff <= 0 {
		policy.MaxBackoff = defaults.MaxBackoff
	}

	if policy.ClaimLease <= 0 {
		policy.ClaimLease = defaults.ClaimLease
	}

	return policy
}

// expired informa se a espera esgotou o limite de tentativas ou o TTL.
func (policy ReferencePolicy) expired(createdAt time.Time, attempts int, now time.Time) bool {
	return attempts >= policy.MaxAttempts || now.Sub(createdAt) >= policy.TTL
}

// backoff devolve o atraso antes da próxima tentativa: InitialBackoff dobrado a cada
// tentativa já feita, limitado a MaxBackoff.
func (policy ReferencePolicy) backoff(attempts int) time.Duration {
	return doublingDelay(policy.InitialBackoff, policy.MaxBackoff, attempts)
}

// doublingDelay dobra o atraso inicial a cada tentativa, sem passar do máximo.
func doublingDelay(initial time.Duration, maximum time.Duration, attempts int) time.Duration {
	delay := initial
	for range attempts {
		delay *= 2
		if delay >= maximum {
			return maximum
		}
	}

	return min(delay, maximum)
}
