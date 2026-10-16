package sqsclient

import (
	"context"
	"errors"

	"apostas-go/internal/model/port"
)

var _ port.HealthCheck = (*HealthCheck)(nil)

// HealthCheck verifica o SQS consultando os atributos das filas do serviço.
type HealthCheck struct {
	client    *Client
	queueURLs []string
}

// NewHealthCheck cria a verificação de readiness sobre as filas dadas.
func NewHealthCheck(client *Client, queueURLs ...string) *HealthCheck {
	return &HealthCheck{client: client, queueURLs: queueURLs}
}

// Name devolve o nome da dependência no corpo do readiness.
func (check *HealthCheck) Name() string {
	return "sqs"
}

// Check devolve nil se todas as filas responderam.
func (check *HealthCheck) Check(ctx context.Context) error {
	var failures []error

	for _, queueURL := range check.queueURLs {
		if _, err := check.client.QueueAttributes(ctx, queueURL); err != nil {
			failures = append(failures, err)
		}
	}

	return errors.Join(failures...)
}
