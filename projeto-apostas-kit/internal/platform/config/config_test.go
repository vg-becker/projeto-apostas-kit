package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

const (
	secretPassword = "s3cr3t-db-password"
	secretKey      = "s3cr3t-aws-key"
)

func validEnvironment() map[string]string {
	return map[string]string{
		"DATABASE_URL":          "postgres://apostas:" + secretPassword + "@localhost:5432/apostas",
		"AWS_REGION":            "us-east-1",
		"AWS_ACCESS_KEY_ID":     "test",
		"AWS_SECRET_ACCESS_KEY": secretKey,
		"SQS_ENDPOINT":          "http://localhost:4566",
		"SQS_QUEUE_URL":         "http://localhost:4566/000000000000/wager-transactions.fifo",
		"SQS_DLQ_URL":           "http://localhost:4566/000000000000/wager-transactions-dlq.fifo",
		"SQS_EVENTS_QUEUE_URL":  "http://localhost:4566/000000000000/wager-events.fifo",
		"OIDC_ISSUER_URL":       "http://localhost:8080/realms/apostas",
		"OIDC_JWKS_URL":         "http://keycloak:8080/realms/apostas/protocol/openid-connect/certs",
		"OIDC_AUDIENCE":         "apostas-api",
	}
}

func lookupOf(values map[string]string) LookupFunc {
	return func(name string) (string, bool) {
		value, found := values[name]

		return value, found
	}
}

func TestLoadAppliesDefaultsToAMinimalEnvironment(t *testing.T) {
	config, err := Load(lookupOf(validEnvironment()))
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	if config.HTTP.Addr != ":8081" || config.HTTP.ShutdownTimeout != 30*time.Second {
		t.Fatalf("http = %+v", config.HTTP)
	}

	if config.LogLevel != slog.LevelInfo {
		t.Fatalf("level = %v", config.LogLevel)
	}

	if config.HTTP.ReadHeaderTimeout <= 0 || config.HTTP.WriteTimeout <= 0 || config.HTTP.IdleTimeout <= 0 {
		t.Fatalf("http timeouts must have defaults: %+v", config.HTTP)
	}

	if config.DatabaseURL.Reveal() != validEnvironment()["DATABASE_URL"] {
		t.Fatal("database url was altered")
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	values := validEnvironment()
	values["APP_HTTP_ADDR"] = "127.0.0.1:9000"
	values["APP_SHUTDOWN_TIMEOUT"] = "45s"
	values["APP_LOG_LEVEL"] = "DEBUG"
	values["OIDC_INTERNAL_SCOPE"] = "wallet:ops"

	config, err := Load(lookupOf(values))
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	if config.HTTP.Addr != "127.0.0.1:9000" || config.HTTP.ShutdownTimeout != 45*time.Second {
		t.Fatalf("http = %+v", config.HTTP)
	}

	if config.LogLevel != slog.LevelDebug || config.Auth.InternalScope != "wallet:ops" {
		t.Fatalf("level = %v, scope = %q", config.LogLevel, config.Auth.InternalScope)
	}
}

func TestMigrationsAreOffByDefaultAndConfigurable(t *testing.T) {
	config, err := Load(lookupOf(validEnvironment()))
	if err != nil || config.Migrations.OnStart || config.Migrations.Directory != "migrations" {
		t.Fatalf("defaults = %+v, %v", config.Migrations, err)
	}

	values := validEnvironment()
	values["APP_MIGRATE_ON_START"] = "true"
	values["APP_MIGRATIONS_DIR"] = "/migrations"

	config, err = Load(lookupOf(values))
	if err != nil || !config.Migrations.OnStart || config.Migrations.Directory != "/migrations" {
		t.Fatalf("configured = %+v, %v", config.Migrations, err)
	}

	values["APP_MIGRATE_ON_START"] = "maybe"

	if _, err := Load(lookupOf(values)); err == nil || !strings.Contains(err.Error(), "APP_MIGRATE_ON_START") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadTreatsABlankVariableAsMissing(t *testing.T) {
	values := validEnvironment()
	values["APP_HTTP_ADDR"] = "   "
	values["AWS_REGION"] = ""

	_, err := Load(lookupOf(values))
	if err == nil || !strings.Contains(err.Error(), "AWS_REGION is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name     string
		variable string
		value    string
		wantText string
	}{
		{"address without port", "APP_HTTP_ADDR", "localhost", "APP_HTTP_ADDR"},
		{"zero timeout", "APP_SHUTDOWN_TIMEOUT", "0s", "APP_SHUTDOWN_TIMEOUT"},
		{"malformed timeout", "APP_SHUTDOWN_TIMEOUT", "soon", "APP_SHUTDOWN_TIMEOUT"},
		{"unknown level", "APP_LOG_LEVEL", "loud", "APP_LOG_LEVEL"},
		{"database url scheme", "DATABASE_URL", "mysql://u:p@h/db", "DATABASE_URL"},
		{"database url without host", "DATABASE_URL", "postgres:///db", "DATABASE_URL"},
		{"queue url", "SQS_QUEUE_URL", "wager-transactions", "requests queue"},
		{"jwks url", "OIDC_JWKS_URL", "keycloak", "jwks url"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			values := validEnvironment()
			values[testCase.variable] = testCase.value

			_, err := Load(lookupOf(values))
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), testCase.wantText) {
				t.Fatalf("err = %v, want it to mention %q", err, testCase.wantText)
			}
		})
	}
}

func TestLoadReportsEveryMissingVariableAtOnce(t *testing.T) {
	_, err := Load(lookupOf(map[string]string{}))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v", err)
	}

	for _, name := range []string{
		"DATABASE_URL", "AWS_REGION", "SQS_QUEUE_URL", "SQS_DLQ_URL",
		"SQS_EVENTS_QUEUE_URL", "OIDC_ISSUER_URL", "OIDC_JWKS_URL", "OIDC_AUDIENCE",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error does not mention %s: %v", name, err)
		}
	}
}

func TestErrorsNeverEchoSecretValues(t *testing.T) {
	values := validEnvironment()
	values["DATABASE_URL"] = "mysql://apostas:" + secretPassword + "@localhost/db"
	values["AWS_ACCESS_KEY_ID"] = ""

	_, err := Load(lookupOf(values))
	if err == nil {
		t.Fatal("want an error")
	}

	for _, secret := range []string{secretPassword, secretKey} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks a secret: %v", err)
		}
	}
}

func TestSecretsAreRedactedEverywhereThePrintersLook(t *testing.T) {
	config, err := Load(lookupOf(validEnvironment()))
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	asJSON, err := json.Marshal(config.DatabaseURL)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}

	renderings := []string{
		fmt.Sprint(config),
		fmt.Sprintf("%v", config),
		fmt.Sprintf("%+v", config),
		config.DatabaseURL.String(),
		fmt.Sprintf("%#v", config.DatabaseURL),
		string(asJSON),
		config.LogValue().String(),
	}

	for _, rendering := range renderings {
		for _, secret := range []string{secretPassword, secretKey} {
			if strings.Contains(rendering, secret) {
				t.Fatalf("rendering leaks a secret: %s", rendering)
			}
		}
	}
}
