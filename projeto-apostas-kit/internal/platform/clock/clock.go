package clock

import (
	"time"

	"apostas-go/internal/model/port"
)

var (
	_ port.Clock = System{}
	_ port.Clock = Fixed{}
)

// System lê o relógio do sistema e devolve sempre UTC.
type System struct{}

// Now devolve o instante atual em UTC.
func (System) Now() time.Time {
	return time.Now().UTC()
}

// Fixed devolve sempre o mesmo instante, para testes determinísticos.
type Fixed struct {
	Instant time.Time
}

// Now devolve o instante configurado em UTC.
func (clock Fixed) Now() time.Time {
	return clock.Instant.UTC()
}
