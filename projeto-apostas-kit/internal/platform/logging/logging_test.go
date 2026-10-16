package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestNewWritesOneJSONObjectPerLine(t *testing.T) {
	var output bytes.Buffer

	New(&output, slog.LevelInfo).Info("started", "port", 8081)

	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("output is not JSON: %v: %s", err, output.String())
	}

	if entry["msg"] != "started" || entry["level"] != "INFO" || entry["port"] != float64(8081) {
		t.Fatalf("entry = %v", entry)
	}
}

func TestNewHonorsTheLevel(t *testing.T) {
	var output bytes.Buffer

	logger := New(&output, slog.LevelWarn)
	logger.Info("hidden")
	logger.Warn("shown")

	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("output must hold exactly the warning: %v: %s", err, output.String())
	}

	if entry["msg"] != "shown" {
		t.Fatalf("entry = %v", entry)
	}
}
