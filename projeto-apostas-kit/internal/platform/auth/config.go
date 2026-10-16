package auth

import (
	"errors"
	"fmt"
	"net/url"
	"time"
)

// ErrInvalidConfig indica uma configuração de autenticação incompleta ou malformada.
var ErrInvalidConfig = errors.New("auth: invalid configuration")

const (
	defaultInternalScope  = "wallet:internal"
	defaultProviderClaim  = "provider_id"
	defaultRequestTimeout = 5 * time.Second
)

// Config descreve como validar os tokens do IdP. A validação não usa descoberta: o emissor
// esperado e a URL do JWKS são explícitos, então o serviço não depende do IdP para subir.
type Config struct {
	// IssuerURL é o valor exigido no claim iss.
	IssuerURL string
	// JWKSURL é onde as chaves públicas são buscadas; pode ser um endereço interno,
	// diferente do emissor.
	JWKSURL string
	// Audience é o valor exigido no claim aud.
	Audience string
	// InternalScope é o escopo que identifica o serviço interno.
	InternalScope string
	// ProviderClaim é o claim que carrega o providerId autorizado.
	ProviderClaim string
	// RequestTimeout limita cada busca do JWKS.
	RequestTimeout time.Duration
}

func (config Config) withDefaults() Config {
	if config.InternalScope == "" {
		config.InternalScope = defaultInternalScope
	}

	if config.ProviderClaim == "" {
		config.ProviderClaim = defaultProviderClaim
	}

	if config.RequestTimeout <= 0 {
		config.RequestTimeout = defaultRequestTimeout
	}

	return config
}

// Validate confere os campos obrigatórios e o formato das URLs.
func (config Config) Validate() error {
	if err := requireURL("issuer url", config.IssuerURL); err != nil {
		return err
	}

	if err := requireURL("jwks url", config.JWKSURL); err != nil {
		return err
	}

	if config.Audience == "" {
		return fmt.Errorf("%w: audience is required", ErrInvalidConfig)
	}

	return nil
}

func requireURL(name string, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%w: %s must be an absolute http(s) url", ErrInvalidConfig, name)
	}

	return nil
}
