package fxmodules

import (
	"log/slog"

	"go.uber.org/fx/fxevent"
)

// quietFxLogger registra os eventos do Fx no mesmo logger JSON do serviço, mas só os que
// interessam a quem opera (a partida concluída e o sinal de desligamento) em nível info. Os
// demais (cada construtor fornecido ou executado, com pilha) ficam em debug, onde não
// afogam os registros úteis; as falhas continuam em nível de erro.
type quietFxLogger struct {
	important fxevent.Logger
	verbose   fxevent.Logger
}

func newQuietFxLogger(logger *slog.Logger) fxevent.Logger {
	verbose := &fxevent.SlogLogger{Logger: logger}
	verbose.UseLogLevel(slog.LevelDebug)

	return quietFxLogger{
		important: &fxevent.SlogLogger{Logger: logger},
		verbose:   verbose,
	}
}

func (quiet quietFxLogger) LogEvent(event fxevent.Event) {
	switch event.(type) {
	case *fxevent.Started, *fxevent.Stopping:
		quiet.important.LogEvent(event)
	default:
		quiet.verbose.LogEvent(event)
	}
}
