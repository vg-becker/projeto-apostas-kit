package fxmodules

import (
	"io"
	"strings"
	"testing"

	"go.uber.org/fx"

	"apostas-go/internal/platform/config"
)

func validSettings(t *testing.T) config.Config {
	t.Helper()

	values := map[string]string{
		"DATABASE_URL":         "postgres://apostas:local@localhost:5432/apostas",
		"AWS_REGION":           "us-east-1",
		"SQS_QUEUE_URL":        "http://localhost:4566/000000000000/wager-transactions.fifo",
		"SQS_DLQ_URL":          "http://localhost:4566/000000000000/wager-transactions-dlq.fifo",
		"SQS_EVENTS_QUEUE_URL": "http://localhost:4566/000000000000/wager-events.fifo",
		"OIDC_ISSUER_URL":      "http://localhost:8080/realms/apostas",
		"OIDC_JWKS_URL":        "http://localhost:8080/realms/apostas/protocol/openid-connect/certs",
		"OIDC_AUDIENCE":        "apostas-api",
	}

	settings, err := config.Load(func(name string) (string, bool) {
		value, found := values[name]

		return value, found
	})
	if err != nil {
		t.Fatalf("config.Load() = %v", err)
	}

	return settings
}

// O grafo inteiro tem de fechar: toda dependência com construtor, sem tipo faltando nem ciclo.
func TestTheApplicationGraphIsComplete(t *testing.T) {
	if err := fx.ValidateApp(ApplicationWith(validSettings(t), io.Discard)); err != nil {
		t.Fatalf("ValidateApp() = %v", err)
	}
}

func TestAnInvalidEnvironmentStopsTheApplicationBeforeAnythingStarts(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	err := fx.New(Application(), fx.NopLogger).Err()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("err = %v", err)
	}
}
