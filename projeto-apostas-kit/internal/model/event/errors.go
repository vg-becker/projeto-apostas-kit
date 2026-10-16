package event

import "errors"

// ErrInvalidEvent indica dados ou estado de origem incompatíveis com o evento.
var ErrInvalidEvent = errors.New("event: invalid event")
