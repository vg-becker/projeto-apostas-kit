package identity

import "errors"

// ErrInvalidID indica um identificador vazio ou fora do formato UUID.
var ErrInvalidID = errors.New("identity: invalid id")
