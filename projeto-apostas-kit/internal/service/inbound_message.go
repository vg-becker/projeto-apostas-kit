package service

// InboundMessage é uma operação que chegou por mensageria. O MessageID é a identidade
// durável da mensagem para a inbox; o pedido já traz o provedor e a chave de idempotência
// do envelope.
type InboundMessage struct {
	MessageID string
	Command   ProcessOperationCommand
}

// InboundOutcome diz o que o tratamento fez com a mensagem.
type InboundOutcome string

// Resultados do tratamento de uma mensagem.
const (
	// InboundApplied: a mensagem era nova e a operação foi tratada e persistida; o resultado
	// pode ser PROCESSED, REJECTED (terminal) ou PENDING_REFERENCE (a pendência está
	// persistida e o worker de referências assume).
	InboundApplied InboundOutcome = "APPLIED"
	// InboundDuplicate: o mesmo messageId, com o mesmo conteúdo, já tinha sido concluído.
	InboundDuplicate InboundOutcome = "DUPLICATE"
)

// InboundResult é o desfecho do tratamento. Operation só vale quando Outcome é InboundApplied.
type InboundResult struct {
	Outcome   InboundOutcome
	Operation OperationResult
}
