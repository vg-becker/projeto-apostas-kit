package sqs

import (
	"errors"
	"fmt"
	"time"

	"apostas-go/internal/platform/sqsclient"
)

// ErrInvalidConfig indica uma configuração do consumidor incompleta ou fora dos limites.
var ErrInvalidConfig = errors.New("sqs: invalid consumer configuration")

const (
	defaultMaxInFlight       = 20
	defaultBatchSize         = 10
	defaultWaitSeconds       = 10
	defaultProcessingTimeout = 30 * time.Second
	defaultRetryBase         = 5 * time.Second
	defaultRetryMax          = 5 * time.Minute
	defaultReceiveBackoff    = time.Second
	defaultReceiveBackoffMax = 30 * time.Second
	maxSQSBatch              = 10
	maxSQSWaitSeconds        = 20
)

// Config controla o consumidor. Campos zerados assumem os padrões.
type Config struct {
	// Queues: o consumidor lê de Requests e envia à DeadLetter as mensagens que não passam.
	Queues sqsclient.Queues
	// MaxInFlight limita as mensagens recebidas e ainda não concluídas.
	MaxInFlight int
	// BatchSize é o máximo por recepção (1 a 10).
	BatchSize int
	// WaitSeconds é a espera longa de cada recepção (0 a 20).
	WaitSeconds int
	// VisibilityTimeoutSeconds, se maior que zero, substitui o da fila a cada recepção.
	VisibilityTimeoutSeconds int
	// ProcessingTimeout limita o tratamento de uma mensagem; deve ficar abaixo do
	// VisibilityTimeout da fila (60 s), senão a mensagem pode ser reentregue durante o
	// tratamento (a inbox torna isso inofensivo, mas é trabalho perdido).
	ProcessingTimeout time.Duration
	// RetryBase e RetryMax definem o backoff da visibilidade depois de uma falha transitória.
	RetryBase time.Duration
	RetryMax  time.Duration
	// ReceiveBackoff e ReceiveBackoffMax definem a espera depois de um erro de recepção.
	ReceiveBackoff    time.Duration
	ReceiveBackoffMax time.Duration
}

func (config Config) withDefaults() Config {
	config.MaxInFlight = orDefault(config.MaxInFlight, defaultMaxInFlight)
	config.BatchSize = orDefault(config.BatchSize, defaultBatchSize)
	config.WaitSeconds = orDefault(config.WaitSeconds, defaultWaitSeconds)
	config.ProcessingTimeout = orDefaultDuration(config.ProcessingTimeout, defaultProcessingTimeout)
	config.RetryBase = orDefaultDuration(config.RetryBase, defaultRetryBase)
	config.RetryMax = orDefaultDuration(config.RetryMax, defaultRetryMax)
	config.ReceiveBackoff = orDefaultDuration(config.ReceiveBackoff, defaultReceiveBackoff)
	config.ReceiveBackoffMax = orDefaultDuration(config.ReceiveBackoffMax, defaultReceiveBackoffMax)

	return config
}

func orDefault(value int, fallback int) int {
	if value <= 0 {
		return fallback
	}

	return value
}

func orDefaultDuration(value time.Duration, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}

	return value
}

// Validate confere as filas e os limites do SQS.
func (config Config) Validate() error {
	if config.Queues.Requests == "" || config.Queues.DeadLetter == "" {
		return fmt.Errorf("%w: the requests and dead-letter queues are required", ErrInvalidConfig)
	}

	if config.BatchSize > maxSQSBatch {
		return fmt.Errorf("%w: batch size is at most %d", ErrInvalidConfig, maxSQSBatch)
	}

	if config.WaitSeconds > maxSQSWaitSeconds {
		return fmt.Errorf("%w: long polling is at most %d s", ErrInvalidConfig, maxSQSWaitSeconds)
	}

	if config.RetryMax > 0 && config.RetryBase > config.RetryMax {
		return fmt.Errorf("%w: retry base is above the retry maximum", ErrInvalidConfig)
	}

	return nil
}
