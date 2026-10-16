package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"apostas-go/internal/model/port"
)

var _ port.HealthCheck = (*HealthCheck)(nil)

// HealthCheck verifica o Postgres com um ping do pool.
type HealthCheck struct {
	pool *pgxpool.Pool
}

// NewHealthCheck cria a verificação de readiness do Postgres.
func NewHealthCheck(pool *pgxpool.Pool) *HealthCheck {
	return &HealthCheck{pool: pool}
}

// Name devolve o nome da dependência no corpo do readiness.
func (check *HealthCheck) Name() string {
	return "postgres"
}

// Check devolve nil se o banco respondeu ao ping.
func (check *HealthCheck) Check(ctx context.Context) error {
	return translateError(check.pool.Ping(ctx))
}
