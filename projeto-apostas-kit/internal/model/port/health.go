package port

import "context"

// HealthCheck verifica uma dependência externa para o readiness (Postgres, SQS). Check
// devolve nil quando a dependência responde; o erro é para log e nunca vai ao corpo da
// resposta.
type HealthCheck interface {
	Name() string
	Check(ctx context.Context) error
}
