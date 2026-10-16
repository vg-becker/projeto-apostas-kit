//go:build integration

package integration

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"apostas-go/internal/platform/config"
	"apostas-go/internal/platform/fxmodules"
	"apostas-go/internal/platform/sqsclient"
	"apostas-go/internal/testsupport/postgresdb"
)

// runningApp é o serviço montado pelo Fx, de verdade, sobre containers reais.
type runningApp struct {
	app     *fxtest.App
	runtime *fxmodules.Runtime
	workers fxmodules.Workers
	pool    postgresPool
}

func appSettings(t *testing.T, httpAddr string) config.Config {
	t.Helper()

	database := postgresdb.Start(t)

	client, err := sqsclient.New(context.Background(), broker.Config())
	if err != nil {
		t.Fatalf("sqsclient.New() = %v", err)
	}

	queues := broker.Isolated(t, client)
	sqsConfig := broker.Config()
	environment := map[string]string{
		"APP_HTTP_ADDR":         httpAddr,
		"APP_SHUTDOWN_TIMEOUT":  "10s",
		"DATABASE_URL":          database.Config().ConnString(),
		"AWS_REGION":            sqsConfig.Region,
		"AWS_ACCESS_KEY_ID":     sqsConfig.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY": sqsConfig.SecretAccessKey,
		"SQS_ENDPOINT":          sqsConfig.Endpoint,
		"SQS_QUEUE_URL":         queues.Requests,
		"SQS_DLQ_URL":           queues.DeadLetter,
		"SQS_EVENTS_QUEUE_URL":  queues.Events,
		"OIDC_ISSUER_URL":       realmConfig().IssuerURL,
		"OIDC_JWKS_URL":         realmConfig().JWKSURL,
		"OIDC_AUDIENCE":         realmConfig().Audience,
	}

	settings, err := config.Load(func(name string) (string, bool) {
		value, found := environment[name]

		return value, found
	})
	if err != nil {
		t.Fatalf("config.Load() = %v", err)
	}

	return settings
}

func newApp(t *testing.T, httpAddr string) *runningApp {
	t.Helper()

	running := &runningApp{}
	running.app = fxtest.New(
		t,
		fxmodules.ApplicationWith(appSettings(t, httpAddr), io.Discard),
		fx.Populate(&running.runtime, &running.workers, &running.pool),
	)

	return running
}

func get(t *testing.T, url string) (int, error) {
	t.Helper()

	client := &http.Client{Timeout: 5 * time.Second}

	response, err := client.Get(url)
	if err != nil {
		return 0, err
	}

	defer func() { _ = response.Body.Close() }()

	return response.StatusCode, nil
}

func TestTheApplicationServesAndShutsDownCleanly(t *testing.T) {
	running := newApp(t, "127.0.0.1:0")
	running.app.RequireStart()

	base := "http://" + running.runtime.Addr()

	for _, path := range []string{"/health/live", "/health/ready"} {
		if status, err := get(t, base+path); err != nil || status != http.StatusOK {
			t.Fatalf("GET %s = %d, %v", path, status, err)
		}
	}

	running.app.RequireStop()

	if _, err := get(t, base+"/health/live"); err == nil {
		t.Fatal("the server must refuse connections after the stop")
	}

	for _, stopped := range running.workers {
		select {
		case <-stopped.Done():
		default:
			t.Fatal("every worker must have finished after the stop")
		}
	}

	if err := running.pool.Ping(context.Background()); err == nil {
		t.Fatal("the database pool must be closed after the stop")
	}
}

func TestAFailedStartLeavesNothingRunning(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}

	defer func() { _ = occupied.Close() }()

	running := newApp(t, occupied.Addr().String())

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := running.app.Start(ctx); err == nil {
		t.Fatal("Start() must fail when the port is taken")
	}

	for _, started := range running.workers {
		select {
		case <-started.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the workers started before the failure must have been stopped")
		}
	}
}

func metricsOf(t *testing.T, base string) string {
	t.Helper()

	response, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics = %v", err)
	}

	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics = %d, %v", response.StatusCode, err)
	}

	return string(body)
}

func TestTheApplicationExposesItsMetricsOnTheSameServer(t *testing.T) {
	running := newApp(t, "127.0.0.1:0")
	running.app.RequireStart()

	defer running.app.RequireStop()

	base := "http://" + running.runtime.Addr()
	if status, err := get(t, base+"/health/live"); err != nil || status != http.StatusOK {
		t.Fatalf("GET /health/live = %d, %v", status, err)
	}

	// as leituras de coleta (banco e broker reais) e os workers já rodando
	deadline := time.Now().Add(15 * time.Second)

	var output string

	for {
		output = metricsOf(t, base)
		if strings.Contains(output, `worker_iterations_total{outcome="ok",worker="outbox-publisher"}`) {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("the workers never showed up in the metrics:\n%s", output)
		}

		time.Sleep(200 * time.Millisecond)
	}

	for _, want := range []string{
		`http_requests_total{method="GET",route="/health/live",status="200"} 1`,
		"outbox_pending_events 0",
		"outbox_oldest_pending_age_seconds 0",
		"sqs_dead_letter_queue_messages 0",
		"go_goroutines",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in:\n%s", want, output)
		}
	}
}
