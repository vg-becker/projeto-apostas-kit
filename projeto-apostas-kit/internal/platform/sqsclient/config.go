package sqsclient

import (
	"errors"
	"fmt"
	"net/url"
	"time"
)

// ErrInvalidConfig indica uma configuração do SQS incompleta ou malformada.
var ErrInvalidConfig = errors.New("sqsclient: invalid configuration")

const defaultRequestTimeout = 10 * time.Second

// Config descreve a conexão com o SQS. Sem AccessKeyID, vale a cadeia padrão de
// credenciais da AWS; com ele (LocalStack, testes), as credenciais são estáticas.
type Config struct {
	Region string
	// Endpoint é opcional; só se informa para o LocalStack ou outro simulador.
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	// RequestTimeout limita cada chamada, além da espera longa do ReceiveMessage.
	RequestTimeout time.Duration
}

// Queues reúne as URLs das filas do serviço.
type Queues struct {
	// Requests recebe as operações dos provedores (wager-transactions.fifo).
	Requests string
	// DeadLetter recebe as mensagens que não podem ser processadas
	// (wager-transactions-dlq.fifo).
	DeadLetter string
	// Events é o destino dos eventos de saída (wager-events.fifo).
	Events string
}

func (config Config) withDefaults() Config {
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = defaultRequestTimeout
	}

	return config
}

// Validate confere a região, o endpoint (se houver) e que as credenciais, se informadas,
// vêm completas.
func (config Config) Validate() error {
	if config.Region == "" {
		return fmt.Errorf("%w: region is required", ErrInvalidConfig)
	}

	if config.Endpoint != "" {
		if err := requireURL("endpoint", config.Endpoint); err != nil {
			return err
		}
	}

	if (config.AccessKeyID == "") != (config.SecretAccessKey == "") {
		return fmt.Errorf("%w: access key and secret must be set together", ErrInvalidConfig)
	}

	return nil
}

// Validate confere que as três filas foram informadas com URLs válidas.
func (queues Queues) Validate() error {
	for name, value := range map[string]string{
		"requests queue":    queues.Requests,
		"dead-letter queue": queues.DeadLetter,
		"events queue":      queues.Events,
	} {
		if err := requireURL(name, value); err != nil {
			return err
		}
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
