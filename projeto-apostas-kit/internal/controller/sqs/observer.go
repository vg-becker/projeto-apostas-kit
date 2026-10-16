package sqs

// Outcome é o desfecho de uma mensagem, para métricas e testes.
type Outcome string

// Desfechos possíveis.
const (
	// OutcomeApplied: a operação foi tratada e a mensagem, apagada.
	OutcomeApplied Outcome = "applied"
	// OutcomeDuplicate: o messageId já tinha sido concluído; a mensagem foi apagada.
	OutcomeDuplicate Outcome = "duplicate"
	// OutcomeRetried: falha transitória; a mensagem foi adiada com backoff.
	OutcomeRetried Outcome = "retried"
	// OutcomeDeadLettered: mensagem que nunca passaria; enviada à DLQ e apagada.
	OutcomeDeadLettered Outcome = "dead_lettered"
	// OutcomeReleased: devolvida à fila na hora, sem ser tratada (desligamento).
	OutcomeReleased Outcome = "released"
	// OutcomeDeleteFailed: o commit foi feito, mas a remoção falhou; a mensagem voltará e
	// será tratada como duplicata.
	OutcomeDeleteFailed Outcome = "delete_failed"
)

// Event descreve um desfecho, sem o corpo da mensagem.
type Event struct {
	Outcome      Outcome
	MessageID    string
	GroupID      string
	ReceiveCount int
	// Reason só existe em OutcomeDeadLettered.
	Reason string
}

// Observer recebe cada desfecho. A implementação deve ser rápida e segura para uso
// concorrente.
type Observer interface {
	Observe(event Event)
}

type noopObserver struct{}

func (noopObserver) Observe(Event) {}
