package logging

import (
	"io"
	"log/slog"
)

// New cria o logger JSON do serviço. Todo log sai em uma linha de JSON no writer dado
// (stdout em produção). Os campos de negócio (messageId, providerId, correlationId) entram
// por quem registra; este pacote não conhece payload financeiro.
func New(output io.Writer, level slog.Level) *slog.Logger {
	handler := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level})

	return slog.New(handler)
}
