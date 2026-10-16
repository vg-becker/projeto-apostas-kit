package wager

// Status é o estado da transação na máquina de estados.
//
//	PENDING           -> PENDING_REFERENCE | PROCESSED | REJECTED | FAILED
//	PENDING_REFERENCE -> PROCESSED | REJECTED | FAILED
//	PROCESSED, REJECTED, FAILED são terminais.
type Status string

// Estados da transação.
const (
	Pending          Status = "PENDING"
	PendingReference Status = "PENDING_REFERENCE"
	Processed        Status = "PROCESSED"
	Rejected         Status = "REJECTED"
	Failed           Status = "FAILED"
)

// String devolve o texto do estado.
func (status Status) String() string {
	return string(status)
}

// IsTerminal informa se o estado não admite novas transições.
func (status Status) IsTerminal() bool {
	return status == Processed || status == Rejected || status == Failed
}

func (status Status) isValid() bool {
	return status == Pending || status == PendingReference || status.IsTerminal()
}

func (status Status) canMoveTo(next Status) bool {
	switch status {
	case Pending:
		return next == PendingReference || next.IsTerminal()
	case PendingReference:
		return next.IsTerminal()
	default:
		return false
	}
}
