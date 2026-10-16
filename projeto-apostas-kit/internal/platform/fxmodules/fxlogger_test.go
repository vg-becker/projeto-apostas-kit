package fxmodules

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"syscall"
	"testing"

	"go.uber.org/fx/fxevent"

	"apostas-go/internal/platform/logging"
)

func fxEvents() []fxevent.Event {
	return []fxevent.Event{
		&fxevent.Provided{ConstructorName: "newPool()", OutputTypeNames: []string{"*pgxpool.Pool"}},
		&fxevent.Run{Name: "newPool()", Kind: "provide"},
		&fxevent.Started{},
		&fxevent.Stopping{Signal: syscall.SIGTERM},
	}
}

func TestFxInternalsStayInDebugWhileStartAndSignalAreInfo(t *testing.T) {
	var noisy bytes.Buffer

	for _, event := range fxEvents() {
		newQuietFxLogger(logging.New(&noisy, slog.LevelDebug)).LogEvent(event)
	}

	if !strings.Contains(noisy.String(), `"msg":"provided"`) || !strings.Contains(noisy.String(), `"msg":"run"`) {
		t.Fatalf("the events used here must produce the noise at debug, or the test proves nothing:\n%s", noisy.String())
	}

	var output bytes.Buffer

	quiet := newQuietFxLogger(logging.New(&output, slog.LevelInfo))

	for _, event := range fxEvents() {
		quiet.LogEvent(event)
	}

	logs := output.String()

	for _, want := range []string{`"msg":"started"`, `"msg":"received signal"`} {
		if !strings.Contains(logs, want) {
			t.Fatalf("missing %s in:\n%s", want, logs)
		}
	}

	for _, noise := range []string{`"msg":"provided"`, `"msg":"run"`, "stacktrace"} {
		if strings.Contains(logs, noise) {
			t.Fatalf("%s must not appear at the info level:\n%s", noise, logs)
		}
	}
}

func TestFxInternalsAreStillAvailableAtDebug(t *testing.T) {
	var output bytes.Buffer

	quiet := newQuietFxLogger(logging.New(&output, slog.LevelDebug))
	quiet.LogEvent(&fxevent.Provided{ConstructorName: "newPool()", OutputTypeNames: []string{"*pgxpool.Pool"}})

	if !strings.Contains(output.String(), `"msg":"provided"`) {
		t.Fatalf("debug level must show the internals:\n%s", output.String())
	}
}

func TestFxFailuresStayAtTheErrorLevel(t *testing.T) {
	var output bytes.Buffer

	quiet := newQuietFxLogger(logging.New(&output, slog.LevelInfo))
	quiet.LogEvent(&fxevent.Started{Err: errors.New("boom")})
	quiet.LogEvent(&fxevent.Provided{ConstructorName: "newPool()", Err: errors.New("cannot build")})

	logs := output.String()
	if strings.Count(logs, `"level":"ERROR"`) != 2 {
		t.Fatalf("both failures must be logged as errors:\n%s", logs)
	}
}
