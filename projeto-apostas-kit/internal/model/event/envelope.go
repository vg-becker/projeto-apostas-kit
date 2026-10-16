package event

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"apostas-go/internal/model/identity"
)

const maxCorrelationLength = 128

// Event é o que a outbox persiste, qualquer que seja o payload.
type Event interface {
	EventID() identity.EventID
	EventType() Type
	AggregateID() string
	OccurredAt() time.Time
	// Marshal devolve o snapshot imutável serializado, estável entre chamadas.
	Marshal() ([]byte, error)
}

// Header reúne os metadados que o chamador fornece; tipo e versão vêm do construtor.
type Header struct {
	EventID       identity.EventID
	CorrelationID string
	CausationID   string
	Now           time.Time
}

// Envelope é o evento de integração com payload tipado.
type Envelope[T any] struct {
	eventID       identity.EventID
	eventType     Type
	aggregateID   string
	correlationID string
	causationID   string
	occurredAt    time.Time
	version       int
	data          T
}

type wire[T any] struct {
	EventID       string `json:"eventId"`
	EventType     string `json:"eventType"`
	AggregateID   string `json:"aggregateId"`
	CorrelationID string `json:"correlationId"`
	CausationID   string `json:"causationId,omitempty"`
	OccurredAt    string `json:"occurredAt"`
	Version       int    `json:"version"`
	Data          T      `json:"data"`
}

// EventID devolve a identidade estável do evento, preservada em republicações.
func (envelope Envelope[T]) EventID() identity.EventID { return envelope.eventID }

// EventType devolve o tipo do evento.
func (envelope Envelope[T]) EventType() Type { return envelope.eventType }

// AggregateID devolve o agregado ao qual o evento pertence.
func (envelope Envelope[T]) AggregateID() string { return envelope.aggregateID }

// CorrelationID devolve o identificador de correlação.
func (envelope Envelope[T]) CorrelationID() string { return envelope.correlationID }

// CausationID devolve o identificador do que causou o evento; vazio se ausente.
func (envelope Envelope[T]) CausationID() string { return envelope.causationID }

// OccurredAt devolve o instante da ocorrência em UTC.
func (envelope Envelope[T]) OccurredAt() time.Time { return envelope.occurredAt }

// Version devolve a versão do contrato do evento.
func (envelope Envelope[T]) Version() int { return envelope.version }

// Data devolve o payload tipado.
func (envelope Envelope[T]) Data() T { return envelope.data }

// Marshal serializa o envelope com timestamp RFC 3339 em UTC.
func (envelope Envelope[T]) Marshal() ([]byte, error) {
	return json.Marshal(wire[T]{
		EventID:       envelope.eventID.String(),
		EventType:     envelope.eventType.String(),
		AggregateID:   envelope.aggregateID,
		CorrelationID: envelope.correlationID,
		CausationID:   envelope.causationID,
		OccurredAt:    envelope.occurredAt.Format(time.RFC3339Nano),
		Version:       envelope.version,
		Data:          envelope.data,
	})
}

func newEnvelope[T any](
	eventType Type,
	aggregateID string,
	header Header,
	data T,
) (Envelope[T], error) {
	if err := validateHeader(header); err != nil {
		return Envelope[T]{}, err
	}

	return Envelope[T]{
		eventID:       header.EventID,
		eventType:     eventType,
		aggregateID:   aggregateID,
		correlationID: header.CorrelationID,
		causationID:   header.CausationID,
		occurredAt:    header.Now.UTC(),
		version:       currentVersion,
		data:          data,
	}, nil
}

func validateHeader(header Header) error {
	if !header.EventID.IsValid() {
		return invalid("event id must be valid")
	}

	if !validCorrelation(header.CorrelationID) {
		return invalid("correlation id is required")
	}

	if header.CausationID != "" && !validCorrelation(header.CausationID) {
		return invalid("causation id is malformed")
	}

	if header.Now.IsZero() {
		return invalid("occurredAt is required")
	}

	return nil
}

func validCorrelation(text string) bool {
	return text != "" && len(text) <= maxCorrelationLength && strings.TrimSpace(text) == text
}

func invalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidEvent, reason)
}
