package http

import (
	"context"
	"net/http"
	"sync"
	"time"

	"apostas-go/internal/model/port"
	"apostas-go/internal/view/presenter"
	"apostas-go/internal/view/response"
)

const defaultReadinessTimeout = 2 * time.Second

// live responde 200 enquanto o processo atende; nunca consulta dependências.
func (handler *controller) live(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, response.Health{Status: "UP"})
}

// ready consulta cada dependência com prazo próprio e responde 503 se alguma falhar. O
// corpo traz só UP ou DOWN; o erro vai para o log.
func (handler *controller) ready(writer http.ResponseWriter, request *http.Request) {
	status, body := presenter.Health(handler.runReadiness(request.Context()))

	writeJSON(writer, status, body)
}

// runReadiness executa as verificações em paralelo, preservando a ordem configurada.
func (handler *controller) runReadiness(ctx context.Context) []presenter.HealthResult {
	timeout := handler.ReadinessTimeout
	if timeout <= 0 {
		timeout = defaultReadinessTimeout
	}

	results := make([]presenter.HealthResult, len(handler.Readiness))

	var group sync.WaitGroup
	for index, check := range handler.Readiness {
		group.Add(1)
		go func() {
			defer group.Done()

			results[index] = presenter.HealthResult{
				Name: check.Name(),
				Up:   handler.passes(ctx, check, timeout),
			}
		}()
	}

	group.Wait()

	return results
}

// passes dá à verificação até timeout para responder; uma dependência travada, mesmo que
// ignore o contexto, conta como DOWN dentro do prazo.
func (handler *controller) passes(
	ctx context.Context,
	check port.HealthCheck,
	timeout time.Duration,
) bool {
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	outcome := make(chan error, 1)
	go func() { outcome <- check.Check(deadline) }()

	select {
	case err := <-outcome:
		if err != nil {
			handler.logger.Warn("readiness check failed", "dependency", check.Name(), "error", err)
		}

		return err == nil
	case <-deadline.Done():
		handler.logger.Warn("readiness check timed out", "dependency", check.Name())

		return false
	}
}
