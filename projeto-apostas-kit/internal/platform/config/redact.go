package config

import (
	"fmt"
	"log/slog"
)

const redacted = "[redacted]"

// Secret é um valor que nunca deve aparecer em log, erro ou impressão. Só Reveal o devolve.
type Secret string

// Reveal devolve o valor; chame só onde ele é de fato usado (a conexão com o banco).
func (secret Secret) Reveal() string {
	return string(secret)
}

// String implementa fmt.Stringer.
func (Secret) String() string {
	return redacted
}

// GoString implementa fmt.GoStringer, para o verbo %#v.
func (Secret) GoString() string {
	return redacted
}

// LogValue implementa slog.LogValuer.
func (Secret) LogValue() slog.Value {
	return slog.StringValue(redacted)
}

// MarshalText cobre JSON e outros codificadores.
func (Secret) MarshalText() ([]byte, error) {
	return []byte(redacted), nil
}

// String resume a configuração para diagnóstico, sem nenhum segredo (nem a senha do banco,
// nem a chave de acesso da AWS, que vive em um campo de texto comum do sqsclient).
func (config Config) String() string {
	return fmt.Sprintf(
		"http=%s log=%s sqs.region=%s sqs.endpoint=%s queues=%s,%s,%s oidc.issuer=%s audience=%s",
		config.HTTP.Addr,
		config.LogLevel,
		config.SQS.Region,
		config.SQS.Endpoint,
		config.Queues.Requests,
		config.Queues.DeadLetter,
		config.Queues.Events,
		config.Auth.IssuerURL,
		config.Auth.Audience,
	)
}

// LogValue deixa o slog registrar a configuração com o mesmo resumo seguro.
func (config Config) LogValue() slog.Value {
	return slog.StringValue(config.String())
}
