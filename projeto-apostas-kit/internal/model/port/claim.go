package port

import "time"

// ClaimParams controla a reivindicação de trabalho por uma instância: o que já está
// vencido em Now pode ser assumido, e o lease reserva o item por Lease contra outras instâncias.
type ClaimParams struct {
	Limit int
	Now   time.Time
	Lease time.Duration
}
