//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

	environment appEnvironment
	logs        *bytes.Buffer
}

// appEnvironment é o que sobrevive a um reinício do serviço: o banco e as filas.
type appEnvironment struct {
	database postgresPool
	client   *sqsclient.Client
	queues   sqsclient.Queues
}

func newAppEnvironment(t *testing.T) appEnvironment {
	t.Helper()

	client, err := sqsclient.New(context.Background(), broker.Config())
	if err != nil {
		t.Fatalf("sqsclient.New() = %v", err)
	}

	return appEnvironment{
		database: postgresdb.Start(t),
		client:   client,
		queues:   broker.Isolated(t, client),
	}
}

// variables são as variáveis de ambiente do serviço sobre este ambiente.
func (environment appEnvironment) variables(databaseURL string, httpAddr string) map[string]string {
	sqsConfig := broker.Config()

	return map[string]string{
		"APP_HTTP_ADDR":         httpAddr,
		"APP_SHUTDOWN_TIMEOUT":  "10s",
		"DATABASE_URL":          databaseURL,
		"AWS_REGION":            sqsConfig.Region,
		"AWS_ACCESS_KEY_ID":     sqsConfig.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY": sqsConfig.SecretAccessKey,
		"SQS_ENDPOINT":          sqsConfig.Endpoint,
		"SQS_QUEUE_URL":         environment.queues.Requests,
		"SQS_DLQ_URL":           environment.queues.DeadLetter,
		"SQS_EVENTS_QUEUE_URL":  environment.queues.Events,
		"OIDC_ISSUER_URL":       realmConfig().IssuerURL,
		"OIDC_JWKS_URL":         realmConfig().JWKSURL,
		"OIDC_AUDIENCE":         realmConfig().Audience,
	}
}

func (environment appEnvironment) settings(t *testing.T, httpAddr string) config.Config {
	t.Helper()

	values := environment.variables(environment.database.Config().ConnString(), httpAddr)

	settings, err := config.Load(func(name string) (string, bool) {
		value, found := values[name]

		return value, found
	})
	if err != nil {
		t.Fatalf("config.Load() = %v", err)
	}

	return settings
}

func newApp(t *testing.T, environment appEnvironment, httpAddr string) *runningApp {
	t.Helper()

	running := &runningApp{environment: environment, logs: &bytes.Buffer{}}
	running.app = fxtest.New(
		t,
		fxmodules.ApplicationWith(environment.settings(t, httpAddr), running.logs),
		fx.Populate(&running.runtime, &running.workers, &running.pool),
	)

	return running
}

// caller devolve um cliente HTTP do serviço, com os mesmos auxiliares dos demais testes: um
// proxy local, porque os auxiliares falam com um httptest.Server.
func (running *runningApp) caller(t *testing.T) *stack {
	t.Helper()

	target, err := url.Parse("http://" + running.runtime.Addr())
	if err != nil {
		t.Fatalf("parse address: %v", err)
	}

	proxy := httptest.NewServer(httputil.NewSingleHostReverseProxy(target))
	t.Cleanup(proxy.Close)

	return &stack{server: proxy, pool: running.environment.database}
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
	running := newApp(t, newAppEnvironment(t), "127.0.0.1:0")
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

	running := newApp(t, newAppEnvironment(t), occupied.Addr().String())

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
	running := newApp(t, newAppEnvironment(t), "127.0.0.1:0")
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

// Um reinício no meio do tráfego não pode perder nem duplicar nada: o que o primeiro processo
// deixou pela metade, o segundo termina, sobre o mesmo banco e as mesmas filas.
func TestARestartInTheMiddleOfTheTrafficLosesAndDuplicatesNothing(t *testing.T) {
	const (
		walletCount   = 8
		perWallet     = 25
		totalMessages = walletCount * perWallet
	)

	environment := newAppEnvironment(t)

	first := newApp(t, environment, "127.0.0.1:0")
	first.app.RequireStart()

	wallets := make([]walletRef, walletCount)
	for index := range wallets {
		wallets[index] = first.caller(t).openWallet(t, "1000.00")
	}

	sendAll(t, environment, wallets, perWallet)

	// o gatilho olha o banco a cada poucos milissegundos para parar com trabalho em andamento
	started := waitEvery(5*time.Millisecond, 40*time.Second, func() bool {
		return environment.completedMessages(t) >= 5
	})
	if !started {
		t.Fatal("the first process never handled a message")
	}

	first.app.RequireStop()

	stoppedAt := time.Now()
	done := environment.completedMessages(t)

	if done >= totalMessages {
		t.Fatalf("all %d messages were done before the stop: raise the volume so the restart is mid-flight", done)
	}

	second := newApp(t, environment, "127.0.0.1:0")
	second.app.RequireStart()

	defer second.app.RequireStop()

	if !waitFor(120*time.Second, func() bool { return environment.completedMessages(t) == totalMessages }) {
		t.Fatalf("not every message completed: %s\nfirst app logs:\n%s\nsecond app logs:\n%s",
			environment.describe(t), first.logs.String(), second.logs.String())
	}

	t.Logf("restart: %d of %d messages were done at the stop; the rest took %v after it",
		done, totalMessages, time.Since(stoppedAt).Round(time.Second))

	second.assertEveryWalletSettled(t, wallets, perWallet)
	environment.assertEventsDeliveredExactlyOnce(t)
}

func sendAll(t *testing.T, environment appEnvironment, wallets []walletRef, perWallet int) {
	t.Helper()

	for index := range perWallet {
		for position, wallet := range wallets {
			external := fmt.Sprintf("tx-w%d-%d", position, index)

			_, err := environment.client.Send(context.Background(), sqsclient.SendParams{
				QueueURL:        environment.queues.Requests,
				Body:            sqsBody("msg-"+external, wallet, external, "10.00"),
				GroupID:         wallet.id,
				DeduplicationID: randomID(),
			})
			if err != nil {
				t.Fatalf("send: %v", err)
			}
		}
	}
}

func (environment appEnvironment) completedMessages(t *testing.T) int {
	t.Helper()

	var total int

	err := environment.database.QueryRow(
		context.Background(),
		`SELECT count(*) FROM inbox_messages WHERE completed_at IS NOT NULL`,
	).Scan(&total)
	if err != nil {
		t.Fatalf("count inbox: %v", err)
	}

	return total
}

func (running *runningApp) assertEveryWalletSettled(
	t *testing.T,
	wallets []walletRef,
	perWallet int,
) {
	t.Helper()

	internal := bearer(t, "internal-service")
	caller := running.caller(t)

	for _, wallet := range wallets {
		var debits, balance int

		err := running.environment.database.QueryRow(
			context.Background(),
			`SELECT (SELECT count(*) FROM wallet_ledger_entries
			          WHERE wallet_id = $1 AND direction = 'DEBIT'),
			        (SELECT balance_minor_units FROM wallets WHERE id = $1)`,
			wallet.id,
		).Scan(&debits, &balance)
		if err != nil {
			t.Fatalf("read wallet %s: %v", wallet.id, err)
		}

		if debits != perWallet || balance != 100000-perWallet*1000 {
			t.Fatalf("wallet %s: %d debits, balance %d minor units", wallet.id, debits, balance)
		}

		reconciliation := caller.call(
			t, http.MethodPost, "/wallets/"+wallet.id+"/reconciliation", internal, nil, "",
		).object(t)
		if reconciliation["consistent"] != true {
			t.Fatalf("wallet %s is not consistent: %v", wallet.id, reconciliation)
		}
	}
}

// assertEventsDeliveredExactlyOnce confere que cada evento da outbox chegou à fila de saída
// uma vez só, mesmo com um reinício no meio da publicação.
func (environment appEnvironment) assertEventsDeliveredExactlyOnce(t *testing.T) {
	t.Helper()

	var total int

	eventually(t, 60*time.Second, func() bool {
		err := environment.database.QueryRow(
			context.Background(),
			`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`,
		).Scan(&total)

		return err == nil && total == 0
	})

	if err := environment.database.QueryRow(
		context.Background(),
		`SELECT count(*) FROM outbox_events`,
	).Scan(&total); err != nil {
		t.Fatalf("count outbox: %v", err)
	}

	delivered := (&messagingStack{client: environment.client}).drain(t, environment.queues.Events, total, 30*time.Second)
	unique := map[string]bool{}

	for _, message := range delivered {
		unique[message.Attributes["eventId"]] = true
	}

	if len(delivered) != total || len(unique) != total {
		t.Fatalf("outbox has %d events, the stream delivered %d (%d unique)", total, len(delivered), len(unique))
	}
}

// O binário de verdade: aplica as migrations de um banco vazio, atende uma chamada autenticada
// e sai com status 0 ao receber SIGTERM.
func TestTheBinaryMigratesServesAndExitsCleanlyOnSIGTERM(t *testing.T) {
	environment := appEnvironment{
		client: nil,
	}

	client, err := sqsclient.New(context.Background(), broker.Config())
	if err != nil {
		t.Fatalf("sqsclient.New() = %v", err)
	}

	environment.client = client
	environment.queues = broker.Isolated(t, client)

	address := freeAddress(t)
	values := environment.variables(postgresdb.StartEmpty(t), address)
	values["APP_MIGRATE_ON_START"] = "true"
	values["APP_MIGRATIONS_DIR"] = repositoryPath(t, "migrations")

	binary := filepath.Join(t.TempDir(), "api")
	build := exec.Command("go", "build", "-o", binary, "./cmd/api")
	build.Dir = repositoryPath(t, ".")

	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}

	var logs bytes.Buffer

	command := exec.Command(binary)
	command.Env = environmentOf(values)
	command.Stdout, command.Stderr = &logs, &logs

	if err := command.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}

	exited := make(chan error, 1)

	go func() { exited <- command.Wait() }()

	t.Cleanup(func() { _ = command.Process.Kill() })

	waitUntilReady(t, "http://"+address, exited, &logs)

	opened := (&stack{server: &httptest.Server{URL: "http://" + address}}).call(
		t, http.MethodPost, "/wallets", bearer(t, "internal-service"), nil,
		fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":"10.00","currency":"BRL"}}`, newUUID(t)),
	)
	if opened.status != http.StatusCreated {
		t.Fatalf("open wallet = %d %s\nlogs:\n%s", opened.status, opened.body, logs.String())
	}

	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}

	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("the binary must exit with status 0 on SIGTERM: %v\nlogs:\n%s", err, logs.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("the binary did not exit after SIGTERM\nlogs:\n%s", logs.String())
	}

	assertLogsHave(t, logs.String(), "migrations applied", "started", "received signal")
}

func freeAddress(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() = %v", err)
	}

	defer func() { _ = listener.Close() }()

	return listener.Addr().String()
}

func repositoryPath(t *testing.T, relative string) string {
	t.Helper()

	absolute, err := filepath.Abs(filepath.Join("..", "..", relative))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	return absolute
}

// environmentOf monta um ambiente só com as variáveis do serviço: o processo filho não herda
// nada do teste, nem credenciais.
func environmentOf(values map[string]string) []string {
	environment := []string{"PATH=" + os.Getenv("PATH")}
	for name, value := range values {
		environment = append(environment, name+"="+value)
	}

	return environment
}

func waitUntilReady(t *testing.T, base string, exited <-chan error, logs *bytes.Buffer) {
	t.Helper()

	deadline := time.Now().Add(60 * time.Second)

	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			t.Fatalf("the binary exited before becoming ready: %v\nlogs:\n%s", err, logs.String())
		default:
		}

		if status, err := get(t, base+"/health/ready"); err == nil && status == http.StatusOK {
			return
		}

		time.Sleep(300 * time.Millisecond)
	}

	t.Fatalf("the binary never became ready\nlogs:\n%s", logs.String())
}

func assertLogsHave(t *testing.T, logs string, messages ...string) {
	t.Helper()

	seen := map[string]bool{}

	for _, line := range strings.Split(logs, "\n") {
		var entry struct {
			Message string `json:"msg"`
		}

		if json.Unmarshal([]byte(line), &entry) == nil {
			seen[entry.Message] = true
		}
	}

	for _, message := range messages {
		if !seen[message] {
			t.Fatalf("the logs have no %q entry (every line must be JSON):\n%s", message, logs)
		}
	}
}

func newUUID(t *testing.T) string {
	t.Helper()

	return (&stack{}).newID(t)
}

func waitFor(within time.Duration, check func() bool) bool {
	return waitEvery(200*time.Millisecond, within, check)
}

func waitEvery(interval time.Duration, within time.Duration, check func() bool) bool {
	deadline := time.Now().Add(within)

	for time.Now().Before(deadline) {
		if check() {
			return true
		}

		time.Sleep(interval)
	}

	return false
}

// describe resume o estado do banco e das filas, para diagnosticar uma espera que estourou.
func (environment appEnvironment) describe(t *testing.T) string {
	t.Helper()

	var started, completed, debits int

	_ = environment.database.QueryRow(context.Background(),
		`SELECT count(*), count(completed_at) FROM inbox_messages`).Scan(&started, &completed)
	_ = environment.database.QueryRow(context.Background(),
		`SELECT count(*) FROM wallet_ledger_entries WHERE direction = 'DEBIT'`).Scan(&debits)

	requests, _ := environment.client.QueueAttributes(context.Background(), environment.queues.Requests)
	dead, _ := environment.client.QueueAttributes(context.Background(), environment.queues.DeadLetter)

	return fmt.Sprintf(
		"inbox rows=%d completed=%d, debits=%d, requests visible=%s in flight=%s delayed=%s, "+
			"dlq visible=%s reasons=%v",
		started, completed, debits,
		requests["ApproximateNumberOfMessages"], requests["ApproximateNumberOfMessagesNotVisible"],
		requests["ApproximateNumberOfMessagesDelayed"], dead["ApproximateNumberOfMessages"],
		environment.deadLetterReasons(),
	)
}

// deadLetterReasons lê (e apaga) as mensagens da DLQ e devolve os motivos registrados.
func (environment appEnvironment) deadLetterReasons() map[string]int {
	reasons := map[string]int{}

	for range 5 {
		messages, err := environment.client.Receive(context.Background(), sqsclient.ReceiveParams{
			QueueURL: environment.queues.DeadLetter, MaxMessages: 10, WaitSeconds: 1,
		})
		if err != nil || len(messages) == 0 {
			break
		}

		for _, message := range messages {
			reasons[message.Attributes["failureReason"]+"/"+message.Attributes["receiveCount"]]++

			_ = environment.client.Delete(context.Background(), environment.queues.DeadLetter, message.ReceiptHandle)
		}
	}

	return reasons
}

// Cada operação deixa uma linha de log com os identificadores de rastreio e uma contagem nas
// métricas, sem vazar o valor, o saldo nem a chave de idempotência.
func TestTheApplicationLogsAndCountsEachOperationWithoutLeakingValues(t *testing.T) {
	running := newApp(t, newAppEnvironment(t), "127.0.0.1:0")
	running.app.RequireStart()

	defer running.app.RequireStop()

	caller := running.caller(t)
	wallet := caller.openWallet(t, "1000.00")

	response := caller.call(
		t, http.MethodPost, "/wagering/transactions", bearer(t, "provider-a"),
		map[string]string{"Idempotency-Key": "provider-a:tx-secret-key", "X-Correlation-Id": "corr-e2e-1"},
		operationBody(wallet, "provider-a", "tx-e2e-1", "25.00"),
	)
	if response.status != http.StatusOK {
		t.Fatalf("operation = %d %s", response.status, response.body)
	}

	transactionID := response.object(t)["transactionId"].(string)
	line := operationLogLine(t, running.logs.String(), transactionID)

	for field, want := range map[string]string{
		"source": "request", "kind": "BET", "result": "applied", "status": "PROCESSED",
		"providerId": "provider-a", "walletId": wallet.id, "correlationId": "corr-e2e-1",
	} {
		if line[field] != want {
			t.Fatalf("log %s = %v, want %s (line %v)", field, line[field], want, line)
		}
	}

	for _, forbidden := range []string{"25.00", "975.00", "tx-secret-key"} {
		if strings.Contains(running.logs.String(), forbidden) {
			t.Fatalf("the logs leak %q", forbidden)
		}
	}

	output := metricsOf(t, "http://"+running.runtime.Addr())

	want := `operations_total{failure_code="",kind="BET",result="applied",source="request",status="PROCESSED"} 1`
	if !strings.Contains(output, want) {
		t.Fatalf("missing %q in the metrics:\n%s", want, output)
	}
}

func operationLogLine(t *testing.T, logs string, transactionID string) map[string]any {
	t.Helper()

	for _, text := range strings.Split(logs, "\n") {
		var line map[string]any
		if json.Unmarshal([]byte(text), &line) == nil && line["msg"] == "operation" &&
			line["transactionId"] == transactionID {
			return line
		}
	}

	t.Fatalf("no operation log line for %s in:\n%s", transactionID, logs)

	return nil
}
