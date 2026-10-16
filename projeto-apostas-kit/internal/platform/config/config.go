package config

import (
	"errors"
	"log/slog"
	"net"
	"net/url"
	"os"
	"time"

	"apostas-go/internal/platform/auth"
	"apostas-go/internal/platform/sqsclient"
)

// ErrInvalidConfig indica que o ambiente não descreve uma configuração utilizável.
var ErrInvalidConfig = errors.New("config: invalid configuration")

const (
	defaultHTTPAddr        = ":8081"
	defaultShutdownTimeout = 30 * time.Second
	defaultLogLevel        = "info"

	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 15 * time.Second
	defaultWriteTimeout      = 30 * time.Second
	defaultIdleTimeout       = 60 * time.Second
)

// Config reúne tudo o que o serviço lê do ambiente.
type Config struct {
	HTTP        HTTP
	LogLevel    slog.Level
	DatabaseURL Secret
	SQS         sqsclient.Config
	Queues      sqsclient.Queues
	Auth        auth.Config
}

// HTTP configura o servidor. Os timeouts protegem contra conexões lentas; o
// ShutdownTimeout é o prazo total do desligamento, dividido entre as peças pelo Fx.
type HTTP struct {
	Addr              string
	ShutdownTimeout   time.Duration
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// FromEnvironment lê a configuração das variáveis de ambiente do processo.
func FromEnvironment() (Config, error) {
	return Load(os.LookupEnv)
}

// Load monta e valida a configuração. Variável vazia vale como ausente. O erro lista todos
// tudo o que está errado, sem repetir valores.
func Load(lookup LookupFunc) (Config, error) {
	source := &reader{lookup: lookup}

	config := Config{
		HTTP:        loadHTTP(source),
		LogLevel:    loadLogLevel(source),
		DatabaseURL: loadDatabaseURL(source),
		SQS:         loadSQS(source),
		Queues:      loadQueues(source),
		Auth:        loadAuth(source),
	}

	source.failWith("sqs", config.SQS.Validate())
	source.failWith("sqs", config.Queues.Validate())
	source.failWith("oidc", config.Auth.Validate())

	if err := source.err(); err != nil {
		return Config{}, err
	}

	return config, nil
}

func loadHTTP(source *reader) HTTP {
	addr := source.text("APP_HTTP_ADDR", defaultHTTPAddr)

	if _, _, err := net.SplitHostPort(addr); err != nil {
		source.fail(errors.New("APP_HTTP_ADDR must be host:port, such as :8081"))
	}

	return HTTP{
		Addr:              addr,
		ShutdownTimeout:   source.duration("APP_SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
	}
}

func loadLogLevel(source *reader) slog.Level {
	var level slog.Level

	raw := source.text("APP_LOG_LEVEL", defaultLogLevel)
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		source.fail(errors.New("APP_LOG_LEVEL must be debug, info, warn or error"))
	}

	return level
}

// loadDatabaseURL só aceita o formato postgres://; o erro de url.Parse é descartado porque
// repete a URL, que carrega a senha.
func loadDatabaseURL(source *reader) Secret {
	raw := source.required("DATABASE_URL")
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil || !isPostgresScheme(parsed.Scheme) || parsed.Host == "" {
		source.fail(errors.New("DATABASE_URL must be a postgres:// url with a host"))
	}

	return Secret(raw)
}

func isPostgresScheme(scheme string) bool {
	return scheme == "postgres" || scheme == "postgresql"
}

func loadSQS(source *reader) sqsclient.Config {
	return sqsclient.Config{
		Region:          source.required("AWS_REGION"),
		Endpoint:        source.text("SQS_ENDPOINT", ""),
		AccessKeyID:     source.text("AWS_ACCESS_KEY_ID", ""),
		SecretAccessKey: source.text("AWS_SECRET_ACCESS_KEY", ""),
	}
}

func loadQueues(source *reader) sqsclient.Queues {
	return sqsclient.Queues{
		Requests:   source.required("SQS_QUEUE_URL"),
		DeadLetter: source.required("SQS_DLQ_URL"),
		Events:     source.required("SQS_EVENTS_QUEUE_URL"),
	}
}

func loadAuth(source *reader) auth.Config {
	return auth.Config{
		IssuerURL:     source.required("OIDC_ISSUER_URL"),
		JWKSURL:       source.required("OIDC_JWKS_URL"),
		Audience:      source.required("OIDC_AUDIENCE"),
		InternalScope: source.text("OIDC_INTERNAL_SCOPE", ""),
	}
}
