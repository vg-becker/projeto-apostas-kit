package port

import "errors"

// Erros que os repositórios devolvem; classifique com errors.Is.
var (
	ErrNotFound        = errors.New("port: not found")
	ErrAlreadyExists   = errors.New("port: already exists")
	ErrVersionConflict = errors.New("port: version conflict")
	ErrInvalidCursor   = errors.New("port: invalid cursor")
	ErrTransient       = errors.New("port: transient infrastructure failure")
)
