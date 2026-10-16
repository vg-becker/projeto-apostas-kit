package auth

import (
	"errors"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		IssuerURL: "https://idp.example/realms/apostas",
		JWKSURL:   "https://idp.example/realms/apostas/protocol/openid-connect/certs",
		Audience:  "apostas-api",
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(config *Config)
		valid  bool
	}{
		{name: "complete", mutate: func(*Config) {}, valid: true},
		{name: "plain http is allowed for local development", mutate: func(c *Config) {
			c.IssuerURL = "http://localhost:8080/realms/apostas"
		}, valid: true},
		{name: "missing issuer", mutate: func(c *Config) { c.IssuerURL = "" }},
		{name: "missing jwks url", mutate: func(c *Config) { c.JWKSURL = "" }},
		{name: "missing audience", mutate: func(c *Config) { c.Audience = "" }},
		{name: "relative issuer", mutate: func(c *Config) { c.IssuerURL = "/realms/apostas" }},
		{name: "unsupported scheme", mutate: func(c *Config) { c.JWKSURL = "ftp://idp.example/keys" }},
		{name: "host missing", mutate: func(c *Config) { c.JWKSURL = "https:///keys" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			test.mutate(&config)

			err := config.Validate()

			if test.valid && err != nil || !test.valid && !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error = %v, want valid = %v", err, test.valid)
			}
		})
	}
}

func TestDefaultsFillTheOptionalFields(t *testing.T) {
	config := validConfig().withDefaults()

	if config.InternalScope != "wallet:internal" || config.ProviderClaim != "provider_id" ||
		config.RequestTimeout != 5*time.Second {
		t.Fatalf("config = %+v", config)
	}

	custom := validConfig()
	custom.InternalScope = "x:internal"

	if custom.withDefaults().InternalScope != "x:internal" {
		t.Fatal("explicit values must be kept")
	}
}

func TestNewVerifierRejectsAnInvalidConfig(t *testing.T) {
	if _, err := NewVerifier(Config{}, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidConfig)
	}
}
