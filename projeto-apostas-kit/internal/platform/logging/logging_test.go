package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"apostas-go/internal/service"
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

func logLine(t *testing.T, output *bytes.Buffer) map[string]any {
	t.Helper()

	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("output is not one JSON line: %v: %s", err, output.String())
	}

	return entry
}

func TestAnOperationLineCarriesTheTraceIdentifiersAndNothingElse(t *testing.T) {
	var output bytes.Buffer

	NewOperationLogger(New(&output, slog.LevelInfo)).ObserveOperation(service.OperationObservation{
		Source:        service.SourceMessage,
		Kind:          "BET",
		Result:        service.ResultApplied,
		Status:        "PROCESSED",
		ProviderID:    "provider-a",
		WalletID:      "wallet-1",
		TransactionID: "tx-1",
		CorrelationID: "corr-1",
		MessageID:     "msg-1",
		Duration:      42 * time.Millisecond,
	})

	entry := logLine(t, &output)

	for field, want := range map[string]any{
		"msg": "operation", "level": "INFO", "source": "message", "kind": "BET",
		"result": "applied", "status": "PROCESSED", "providerId": "provider-a",
		"walletId": "wallet-1", "transactionId": "tx-1", "correlationId": "corr-1",
		"messageId": "msg-1", "durationMs": float64(42),
	} {
		if entry[field] != want {
			t.Fatalf("%s = %v, want %v (entry %v)", field, entry[field], want, entry)
		}
	}

	if _, present := entry["failureCode"]; present {
		t.Fatalf("an empty failure code must not be logged: %v", entry)
	}
}

func TestTheLogLevelFollowsTheResult(t *testing.T) {
	for result, want := range map[string]string{
		service.ResultApplied:             "INFO",
		service.ResultReplay:              "INFO",
		service.ResultDuplicate:           "INFO",
		service.ResultIdempotencyConflict: "WARN",
		service.ResultInvalid:             "WARN",
		service.ResultTransient:           "WARN",
		service.ResultConcurrencyConflict: "ERROR",
		service.ResultError:               "ERROR",
	} {
		var output bytes.Buffer

		NewOperationLogger(New(&output, slog.LevelDebug)).
			ObserveOperation(service.OperationObservation{Result: result})

		if got := logLine(t, &output)["level"]; got != want {
			t.Fatalf("%s logged at %v, want %s", result, got, want)
		}
	}
}

func TestOnlyADivergentReconciliationIsLogged(t *testing.T) {
	var output bytes.Buffer

	operations := NewOperationLogger(New(&output, slog.LevelDebug))

	operations.ObserveReconciliation(service.ReconciliationObservation{WalletID: "w1", Consistent: true})

	if output.Len() != 0 {
		t.Fatalf("a consistent reconciliation must stay silent: %s", output.String())
	}

	operations.ObserveReconciliation(service.ReconciliationObservation{WalletID: "w2"})

	entry := logLine(t, &output)
	if entry["level"] != "ERROR" || entry["walletId"] != "w2" || entry["msg"] != "reconciliation diverged" {
		t.Fatalf("entry = %v", entry)
	}
}
