package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"apostas-go/internal/controller/sqs"
	"apostas-go/internal/controller/worker"
	"apostas-go/internal/service"
)

func scrape(t *testing.T, metrics *Metrics) string {
	t.Helper()

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("scrape status = %d", recorder.Code)
	}

	return recorder.Body.String()
}

func TestRequestsAreCountedByRouteAndStatus(t *testing.T) {
	metrics := New()

	metrics.ObserveRequest(http.MethodGet, "/wallets/{walletId}", 200, 20*time.Millisecond)
	metrics.ObserveRequest(http.MethodGet, "/wallets/{walletId}", 200, 30*time.Millisecond)
	metrics.ObserveRequest("BREW", "unmatched", 404, time.Millisecond)

	output := scrape(t, metrics)

	for _, want := range []string{
		`http_requests_total{method="GET",route="/wallets/{walletId}",status="200"} 2`,
		`http_requests_total{method="OTHER",route="unmatched",status="404"} 1`,
		`http_request_duration_seconds_count{method="GET",route="/wallets/{walletId}"} 2`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in:\n%s", want, output)
		}
	}

	if strings.Contains(output, "BREW") {
		t.Fatal("an arbitrary method must not create a series")
	}
}

func TestTheConsumerOutcomesAndDeadLetterReasonsAreCounted(t *testing.T) {
	metrics := New()

	metrics.Observe(sqs.Event{Outcome: sqs.OutcomeApplied, MessageID: "msg-secret-1"})
	metrics.Observe(sqs.Event{Outcome: sqs.OutcomeDeadLettered, Reason: "invalid_json", MessageID: "msg-secret-2"})

	output := scrape(t, metrics)

	for _, want := range []string{
		`sqs_consumer_messages_total{outcome="applied"} 1`,
		`sqs_consumer_messages_total{outcome="dead_lettered"} 1`,
		`sqs_consumer_dead_letters_total{reason="invalid_json"} 1`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in:\n%s", want, output)
		}
	}

	if strings.Contains(output, "msg-secret") {
		t.Fatal("message identifiers must never become labels")
	}
}

func TestWorkerIterationsAreCountedByOutcome(t *testing.T) {
	observer := New()
	workers := observer.WorkerObserver()

	workers.Observe(worker.Iteration{Worker: "outbox-publisher", Duration: time.Millisecond})
	workers.Observe(worker.Iteration{Worker: "outbox-publisher", Failed: true})
	workers.Observe(worker.Iteration{Worker: "outbox-publisher", Failed: true, Panicked: true})

	output := scrape(t, observer)

	for _, want := range []string{
		`worker_iterations_total{outcome="ok",worker="outbox-publisher"} 1`,
		`worker_iterations_total{outcome="error",worker="outbox-publisher"} 1`,
		`worker_iterations_total{outcome="panic",worker="outbox-publisher"} 1`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in:\n%s", want, output)
		}
	}
}

func TestTheSourceGaugesAreReadAtScrapeTime(t *testing.T) {
	metrics := New()
	pending := int64(3)

	err := metrics.RegisterSources(Sources{
		OutboxBacklog: func(context.Context) (int64, time.Duration, error) {
			return pending, 90 * time.Second, nil
		},
		DeadLetterDepth: func(context.Context) (int64, error) { return 7, nil },
	})
	if err != nil {
		t.Fatalf("RegisterSources() = %v", err)
	}

	first := scrape(t, metrics)

	pending = 5

	second := scrape(t, metrics)

	for _, want := range []string{
		"outbox_pending_events 3",
		"outbox_oldest_pending_age_seconds 90",
		"sqs_dead_letter_queue_messages 7",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("missing %q in:\n%s", want, first)
		}
	}

	if !strings.Contains(second, "outbox_pending_events 5") {
		t.Fatal("the gauge must be read again on each scrape")
	}
}

func TestAFailingSourceOnlyOmitsItsOwnMetric(t *testing.T) {
	metrics := New()

	err := metrics.RegisterSources(Sources{
		OutboxBacklog: func(context.Context) (int64, time.Duration, error) {
			return 0, 0, errors.New("database down")
		},
		DeadLetterDepth: func(context.Context) (int64, error) { return 2, nil },
	})
	if err != nil {
		t.Fatalf("RegisterSources() = %v", err)
	}

	output := scrape(t, metrics)

	if strings.Contains(output, "outbox_pending_events") || strings.Contains(output, "database down") {
		t.Fatalf("the failing source must be omitted silently:\n%s", output)
	}

	if !strings.Contains(output, "sqs_dead_letter_queue_messages 2") || !strings.Contains(output, "go_goroutines") {
		t.Fatalf("the other metrics must keep being served:\n%s", output)
	}
}

func TestOperationsAreCountedByPathResultStatusAndFailureCode(t *testing.T) {
	metrics := New()

	for _, observation := range []service.OperationObservation{
		{Source: service.SourceRequest, Kind: "BET", Result: service.ResultApplied, Status: "PROCESSED", Duration: time.Millisecond},
		{Source: service.SourceRequest, Kind: "BET", Result: service.ResultReplay, Status: "PROCESSED"},
		{Source: service.SourceMessage, Kind: "BET", Result: service.ResultApplied, Status: "REJECTED", FailureCode: "INSUFFICIENT_FUNDS"},
		{Source: service.SourceMessage, Kind: "BET", Result: service.ResultDuplicate},
		{Source: service.SourceRequest, Kind: "BET", Result: service.ResultIdempotencyConflict},
		// identificadores nunca viram rótulo
		{Source: service.SourceRequest, Kind: "BET", Result: service.ResultApplied, Status: "PROCESSED", WalletID: "wallet-secret", ProviderID: "provider-secret", TransactionID: "tx-secret"},
	} {
		metrics.ObserveOperation(observation)
	}

	output := scrape(t, metrics)

	for _, want := range []string{
		`operations_total{failure_code="",kind="BET",result="applied",source="request",status="PROCESSED"} 2`,
		`operations_total{failure_code="",kind="BET",result="replay",source="request",status="PROCESSED"} 1`,
		`operations_total{failure_code="INSUFFICIENT_FUNDS",kind="BET",result="applied",source="message",status="REJECTED"} 1`,
		`operations_total{failure_code="",kind="BET",result="duplicate",source="message",status=""} 1`,
		`operations_total{failure_code="",kind="BET",result="idempotency_conflict",source="request",status=""} 1`,
		`operation_duration_seconds_count{result="applied",source="request"} 2`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in:\n%s", want, output)
		}
	}

	for _, forbidden := range []string{"wallet-secret", "provider-secret", "tx-secret"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("identifier %q leaked into the metrics", forbidden)
		}
	}
}

func TestReconciliationDivergencesAreCounted(t *testing.T) {
	metrics := New()

	metrics.ObserveReconciliation(service.ReconciliationObservation{WalletID: "w1", Consistent: true})
	metrics.ObserveReconciliation(service.ReconciliationObservation{WalletID: "w2", Consistent: false})

	output := scrape(t, metrics)

	for _, want := range []string{
		`reconciliations_total{result="consistent"} 1`,
		`reconciliations_total{result="divergent"} 1`,
		"reconciliation_divergences_total 1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in:\n%s", want, output)
		}
	}
}
