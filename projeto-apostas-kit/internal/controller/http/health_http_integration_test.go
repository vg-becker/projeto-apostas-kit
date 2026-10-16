//go:build integration

package http_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"apostas-go/internal/model/port"
)

func TestLivenessIsPublicAndIgnoresDependencies(t *testing.T) {
	f := newFixture(t)
	f.sqs.down.Store(true)
	f.pool.Close()

	response := f.call(t, http.MethodGet, "/health/live", "", nil, "")

	if response.status != http.StatusOK || string(response.body) != `{"status":"UP"}` {
		t.Fatalf("live = %d %s", response.status, response.body)
	}
}

func TestReadinessReportsEachDependency(t *testing.T) {
	f := newFixture(t)

	ready := f.call(t, http.MethodGet, "/health/ready", "", nil, "")
	if ready.status != http.StatusOK ||
		string(ready.body) != `{"status":"UP","checks":{"postgres":"UP","sqs":"UP"}}` {
		t.Fatalf("ready = %d %s", ready.status, ready.body)
	}

	f.sqs.down.Store(true)
	sqsDown := f.call(t, http.MethodGet, "/health/ready", "", nil, "")
	if sqsDown.status != http.StatusServiceUnavailable ||
		string(sqsDown.body) != `{"status":"DOWN","checks":{"postgres":"UP","sqs":"DOWN"}}` {
		t.Fatalf("sqs down = %d %s", sqsDown.status, sqsDown.body)
	}

	f.sqs.down.Store(false)
	f.pool.Close()

	databaseDown := f.call(t, http.MethodGet, "/health/ready", "", nil, "")
	if databaseDown.status != http.StatusServiceUnavailable ||
		string(databaseDown.body) != `{"status":"DOWN","checks":{"postgres":"DOWN","sqs":"UP"}}` {
		t.Fatalf("database down = %d %s", databaseDown.status, databaseDown.body)
	}
}

func TestReadinessNeverLeaksTheErrorText(t *testing.T) {
	f := newFixture(t)
	f.sqs.down.Store(true)

	response := f.call(t, http.MethodGet, "/health/ready", "", nil, "")

	if strings.Contains(string(response.body), "hunter2") || strings.Contains(string(response.body), "refused") {
		t.Fatalf("body leaks the error: %s", response.body)
	}

	if !strings.Contains(f.logs.String(), "readiness check failed") {
		t.Fatal("the failure must be logged for operators")
	}
}

func TestAHungDependencyIsReportedDownWithinTheTimeout(t *testing.T) {
	hanging := hangingCheck{release: make(chan struct{})}
	t.Cleanup(func() { close(hanging.release) })

	f := newFixtureWithOptions(t, fixtureOptions{
		extraReadiness:   []port.HealthCheck{hanging},
		readinessTimeout: 200 * time.Millisecond,
	})

	started := time.Now()
	response := f.call(t, http.MethodGet, "/health/ready", "", nil, "")

	if response.status != http.StatusServiceUnavailable || time.Since(started) > 3*time.Second {
		t.Fatalf("ready = %d after %v", response.status, time.Since(started))
	}

	if !strings.Contains(string(response.body), `"slow":"DOWN"`) ||
		!strings.Contains(string(response.body), `"postgres":"UP"`) {
		t.Fatalf("body = %s", response.body)
	}
}

func TestHealthRoutesOnlyAnswerGET(t *testing.T) {
	f := newFixture(t)

	for _, path := range []string{"/health/live", "/health/ready"} {
		response := f.call(t, http.MethodPost, path, "", nil, "")

		assertProblem(t, response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
	}
}
