package sqs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"apostas-go/internal/controller/wire"
	"apostas-go/internal/service"
)

const (
	// WagerRequestedType é o único tipo de mensagem aceito na fila de operações.
	WagerRequestedType = "WagerTransactionRequested"

	maxBodyBytes = 64 << 10
)

// Reason é o motivo estável de uma recusa, gravado na DLQ para quem for investigar.
type Reason string

// Motivos de recusa de uma mensagem.
const (
	ReasonInvalidJSON     Reason = "invalid_json"
	ReasonTooLarge        Reason = "too_large"
	ReasonUnsupportedType Reason = "unsupported_type"
	ReasonInvalidField    Reason = "invalid_field"
)

var (
	errTooLarge     = errors.New("message body too large")
	errTrailingData = errors.New("unexpected data after the JSON body")
	errMissingData  = errors.New("data, providerId and idempotencyKey are required")
)

// ParseError é uma mensagem que nunca vai passar: o consumidor a envia à DLQ.
type ParseError struct {
	Reason Reason
	Err    error
}

func (failure *ParseError) Error() string {
	return fmt.Sprintf("sqs: %s: %v", failure.Reason, failure.Err)
}

func (failure *ParseError) Unwrap() error {
	return failure.Err
}

type envelope struct {
	MessageID  string         `json:"messageId"`
	Type       string         `json:"type"`
	OccurredAt string         `json:"occurredAt"`
	Data       *operationData `json:"data"`
}

// operationData é o corpo da operação: os campos de negócio compartilhados com o HTTP, mais o
// provedor e a chave de idempotência, que no SQS vêm no envelope (não há token).
type operationData struct {
	ProviderID     string `json:"providerId"`
	IdempotencyKey string `json:"idempotencyKey"`
	wire.OperationFields
}

// Parse valida o formato do envelope com a mesma rigidez do HTTP (JSON estrito, valores
// monetários em texto, tipo de operação permitido) e devolve a mensagem pronta para o
// tratamento. O messageId vira a correlação e a causação dos eventos. O erro é sempre um
// *ParseError.
func Parse(body string) (service.InboundMessage, error) {
	if len(body) > maxBodyBytes {
		return service.InboundMessage{}, refuse(ReasonTooLarge, errTooLarge)
	}

	decoded, err := decode(body)
	if err != nil {
		return service.InboundMessage{}, err
	}

	if err := validateEnvelope(decoded); err != nil {
		return service.InboundMessage{}, err
	}

	command, err := decoded.Data.Command()
	if err != nil {
		return service.InboundMessage{}, refuse(ReasonInvalidField, err)
	}

	command.ProviderID = decoded.Data.ProviderID
	command.IdempotencyKey = decoded.Data.IdempotencyKey
	command.CorrelationID = decoded.MessageID
	command.CausationID = decoded.MessageID

	return service.InboundMessage{MessageID: decoded.MessageID, Command: command}, nil
}

func decode(body string) (envelope, error) {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()

	var decoded envelope
	if err := decoder.Decode(&decoded); err != nil {
		return envelope{}, refuse(ReasonInvalidJSON, err)
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return envelope{}, refuse(ReasonInvalidJSON, errTrailingData)
	}

	return decoded, nil
}

func validateEnvelope(decoded envelope) error {
	if decoded.Type != WagerRequestedType {
		return refuse(ReasonUnsupportedType, fmt.Errorf("type %q is not supported", decoded.Type))
	}

	if decoded.MessageID == "" {
		return refuse(ReasonInvalidField, errors.New("messageId is required"))
	}

	if _, err := time.Parse(time.RFC3339Nano, decoded.OccurredAt); err != nil {
		return refuse(ReasonInvalidField, fmt.Errorf("occurredAt must be RFC 3339: %w", err))
	}

	if decoded.Data == nil || decoded.Data.ProviderID == "" || decoded.Data.IdempotencyKey == "" {
		return refuse(ReasonInvalidField, errMissingData)
	}

	return nil
}

func refuse(reason Reason, err error) error {
	return &ParseError{Reason: reason, Err: err}
}
