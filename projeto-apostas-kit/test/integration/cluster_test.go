//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"apostas-go/internal/platform/sqsclient"
)

// lockedBuffer recebe a saída de um processo filho, escrita por uma goroutine do os/exec e
// lida pelo teste.
type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (locked *lockedBuffer) Write(data []byte) (int, error) {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()

	return locked.buffer.Write(data)
}

func (locked *lockedBuffer) String() string {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()

	return locked.buffer.String()
}

var (
	binaryOnce      sync.Once
	binaryDirectory string
	binaryPath      string
	binaryBuildErr  error
)

// serviceBinary compila o cmd/api uma vez por execução dos testes.
func serviceBinary(t *testing.T) string {
	t.Helper()

	binaryOnce.Do(func() {
		binaryDirectory, binaryBuildErr = os.MkdirTemp("", "apostas-api-*")
		if binaryBuildErr != nil {
			return
		}

		binaryPath = binaryDirectory + "/api"

		build := exec.Command("go", "build", "-race", "-o", binaryPath, "./cmd/api")
		build.Dir = repositoryPath(t, ".")

		if output, err := build.CombinedOutput(); err != nil {
			binaryBuildErr = &buildError{err: err, output: string(output)}
		}
	})

	if binaryBuildErr != nil {
		t.Fatalf("build the service binary: %v", binaryBuildErr)
	}

	return binaryPath
}

type buildError struct {
	err    error
	output string
}

func (failure *buildError) Error() string {
	return failure.err.Error() + "\n" + failure.output
}

func removeBinary() {
	if binaryDirectory != "" {
		_ = os.RemoveAll(binaryDirectory)
	}
}

// instance é um processo do serviço: memória, conexões e consumidor próprios.
type instance struct {
	address string
	command *exec.Cmd
	logs    *lockedBuffer
	exited  chan error
	// killed marca a instância derrubada com SIGKILL: não há desligamento a conferir.
	killed bool
}

// cluster são várias instâncias do serviço sobre o mesmo banco, as mesmas filas e o mesmo
// IdP: a situação que o enunciado pede para demonstrar (o enunciado pede pelo menos três).
type cluster struct {
	environment appEnvironment
	instances   []*instance
}

func newCluster(t *testing.T, size int) *cluster {
	t.Helper()

	binary := serviceBinary(t)
	running := &cluster{environment: newAppEnvironment(t), instances: make([]*instance, size)}

	var group sync.WaitGroup

	for index := range running.instances {
		running.instances[index] = running.start(t, binary)
	}

	for _, started := range running.instances {
		group.Add(1)

		go func() {
			defer group.Done()

			waitUntilReady(t, "http://"+started.address, started.exited, &bytes.Buffer{})
		}()
	}

	group.Wait()
	t.Cleanup(func() { running.shutDown(t) })

	return running
}

func (running *cluster) start(t *testing.T, binary string) *instance {
	t.Helper()

	address := freeAddress(t)
	values := running.environment.variables(
		running.environment.database.Config().ConnString(),
		address,
	)

	logs := &lockedBuffer{}
	command := exec.Command(binary)
	command.Env = environmentOf(values)
	command.Stdout, command.Stderr = logs, logs

	if err := command.Start(); err != nil {
		t.Fatalf("start instance: %v", err)
	}

	started := &instance{address: address, command: command, logs: logs, exited: make(chan error, 1)}

	go func() { started.exited <- command.Wait() }()

	return started
}

// shutDown encerra todas as instâncias com SIGTERM, ao mesmo tempo, e confere que cada uma
// saiu com status 0 e sem nenhum registro de erro nos logs: um erro registrado durante um
// teste é um defeito.
func (running *cluster) shutDown(t *testing.T) {
	for _, started := range running.instances {
		if !started.killed {
			_ = started.command.Process.Signal(syscall.SIGTERM)
		}
	}

	for index, started := range running.instances {
		if !started.killed {
			waitForCleanExit(t, index, started)
		}
	}
}

func waitForCleanExit(t *testing.T, index int, started *instance) {
	select {
	case err := <-started.exited:
		if err != nil {
			t.Errorf("instance %d did not exit cleanly: %v\n%s", index, err, started.logs.String())
		}
	case <-time.After(40 * time.Second):
		_ = started.command.Process.Kill()

		t.Errorf("instance %d did not stop after SIGTERM\n%s", index, started.logs.String())
	}

	if lines := errorLines(started.logs.String()); len(lines) > 0 {
		t.Errorf("instance %d logged errors:\n%s", index, strings.Join(lines, "\n"))
	}
}

func errorLines(logs string) []string {
	var found []string

	for _, text := range strings.Split(logs, "\n") {
		var line struct {
			Level string `json:"level"`
		}

		if json.Unmarshal([]byte(text), &line) == nil && line.Level == "ERROR" {
			found = append(found, text)
		}
	}

	return found
}

// caller devolve o cliente HTTP da instância dada (índice cíclico).
func (running *cluster) caller(index int) *stack {
	target := running.instances[index%len(running.instances)]

	return &stack{
		server: &httptest.Server{URL: "http://" + target.address},
		pool:   running.environment.database,
	}
}

// spread dispara count chamadas ao mesmo tempo, a i-ésima na instância i mod tamanho.
func spread[T any](running *cluster, count int, call func(index int, target *stack) T) []T {
	results := make([]T, count)
	start := make(chan struct{})

	var group sync.WaitGroup

	for index := range count {
		group.Add(1)

		go func() {
			defer group.Done()
			<-start

			results[index] = call(index, running.caller(index))
		}()
	}

	close(start)
	group.Wait()

	return results
}

func (running *cluster) number(t *testing.T, query string, arguments ...any) int {
	t.Helper()

	var total int

	err := running.environment.database.QueryRow(context.Background(), query, arguments...).Scan(&total)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}

	return total
}

func (running *cluster) debits(t *testing.T, walletID string) int {
	t.Helper()

	return running.number(
		t,
		`SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`,
		walletID,
	)
}

func (running *cluster) balance(t *testing.T, walletID string) int {
	t.Helper()

	return running.number(t, `SELECT balance_minor_units FROM wallets WHERE id = $1`, walletID)
}

// assertLedgerMatchesEveryBalance confere, para todas as carteiras, o saldo armazenado contra
// a soma de créditos menos débitos do ledger.
func (running *cluster) assertLedgerMatchesEveryBalance(t *testing.T) {
	t.Helper()

	wallets := running.number(t, `SELECT count(*) FROM wallets`)

	divergent := running.number(t, `
		SELECT count(*)
		  FROM wallets AS wallet
		 WHERE wallet.balance_minor_units <> COALESCE((
		       SELECT SUM(CASE entry.direction
		                    WHEN 'CREDIT' THEN entry.amount_minor_units
		                    ELSE -entry.amount_minor_units
		                  END)
		         FROM wallet_ledger_entries AS entry
		        WHERE entry.wallet_id = wallet.id), 0)
	`)

	if wallets == 0 || divergent != 0 {
		t.Fatalf("%d of %d wallets have a stored balance that differs from their ledger", divergent, wallets)
	}
}

// assertSound é a conferência final de todo cenário: saldo contra ledger, nenhuma transação
// presa e cada evento da outbox entregue exatamente uma vez.
func (running *cluster) assertSound(t *testing.T) {
	t.Helper()

	running.assertLedgerMatchesEveryBalance(t)

	stuck := running.number(t, `SELECT count(*) FROM wager_transactions WHERE status IN ('PENDING', 'PENDING_REFERENCE')`)
	if stuck != 0 {
		t.Fatalf("%d transactions are still pending", stuck)
	}

	running.environment.assertEventsDeliveredExactlyOnce(t)
}

func replyOf(response reply, t *testing.T) (status int, object map[string]any) {
	t.Helper()

	return response.status, response.object(t)
}

func (running *cluster) metrics(t *testing.T, index int) string {
	t.Helper()

	return metricsOf(t, "http://"+running.instances[index].address)
}

// sendBet publica uma aposta do provider-a na fila de operações, no grupo da carteira.
func (environment appEnvironment) sendBet(
	t *testing.T,
	wallet walletRef,
	messageID string,
	external string,
	amount string,
) {
	t.Helper()

	_, err := environment.client.Send(context.Background(), sqsclient.SendParams{
		QueueURL:        environment.queues.Requests,
		Body:            sqsBody(messageID, wallet, external, amount),
		GroupID:         wallet.id,
		DeduplicationID: randomID(),
	})
	if err != nil {
		t.Errorf("send %s: %v", messageID, err)
	}
}

// kill derruba a instância com SIGKILL, como uma queda de máquina: sem desligamento, sem
// liberar mensagens, sem devolver locks de aplicação.
func (running *cluster) kill(t *testing.T, index int) {
	t.Helper()

	target := running.instances[index]
	target.killed = true

	if err := target.command.Process.Kill(); err != nil {
		t.Fatalf("kill instance %d: %v", index, err)
	}

	select {
	case <-target.exited:
	case <-time.After(10 * time.Second):
		t.Fatalf("instance %d did not die", index)
	}
}

// restart desliga todas as instâncias (cada uma deve sair com status 0) e sobe outras tantas,
// novas, sobre o mesmo banco e as mesmas filas.
func (running *cluster) restart(t *testing.T) {
	t.Helper()

	running.shutDown(t)

	binary := serviceBinary(t)
	fresh := make([]*instance, len(running.instances))

	for index := range fresh {
		fresh[index] = running.start(t, binary)
	}

	for _, started := range fresh {
		waitUntilReady(t, "http://"+started.address, started.exited, &bytes.Buffer{})
	}

	running.instances = fresh
}

// trySubmit envia uma operação a uma instância sem encerrar o teste se a conexão falhar: um
// cliente de verdade tenta de novo, em outra instância, com a mesma chave.
func (running *cluster) trySubmit(
	instance int,
	token string,
	key string,
	body string,
) (reply, error) {
	target := running.instances[instance%len(running.instances)]

	request, err := http.NewRequest(http.MethodPost, "http://"+target.address+"/wagering/transactions", strings.NewReader(body))
	if err != nil {
		return reply{}, err
	}

	request.Header.Set("Authorization", token)
	request.Header.Set("Idempotency-Key", key)
	request.Header.Set("Content-Type", "application/json")

	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return reply{}, err
	}

	defer func() { _ = response.Body.Close() }()

	payload, err := io.ReadAll(response.Body)

	return reply{status: response.StatusCode, header: response.Header, body: payload}, err
}

// submitUntilAnswered repete a mesma operação, com a mesma chave, passando por todas as
// instâncias, até uma resposta que não seja falha de conexão nem 5xx.
func (running *cluster) submitUntilAnswered(
	t *testing.T,
	first int,
	token string,
	key string,
	body string,
) reply {
	t.Helper()

	for attempt := range 30 {
		response, err := running.trySubmit(first+attempt, token, key, body)
		if err == nil && response.status < http.StatusInternalServerError {
			return response
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Errorf("no instance answered %s", key)

	return reply{}
}

// referencingBody monta um estorno (REFUND ou ROLLBACK) que referencia uma aposta do provider-a.
func referencingBody(wallet walletRef, external string, kind string, amount string, reference string) string {
	return fmt.Sprintf(
		`{"providerId":"provider-a","externalTransactionId":%q,"playerId":%q,"walletId":%q,`+
			`"roundId":"round-1","gameId":"game-1","kind":%q,"money":{"amount":%q,"currency":"BRL"},`+
			`"referenceExternalTransactionId":%q}`,
		external, wallet.playerID, wallet.id, kind, amount, reference,
	)
}

// sendReversal publica um estorno do provider-a na fila de operações.
func (environment appEnvironment) sendReversal(
	t *testing.T,
	wallet walletRef,
	messageID string,
	external string,
	kind string,
	reference string,
) {
	t.Helper()

	body := fmt.Sprintf(
		`{"messageId":%q,"type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00.000Z",`+
			`"data":{"providerId":"provider-a","externalTransactionId":%q,"idempotencyKey":%q,`+
			`"playerId":%q,"walletId":%q,"roundId":"round-1","gameId":"game-1","kind":%q,`+
			`"money":{"amount":"5.00","currency":"BRL"},"referenceExternalTransactionId":%q}}`,
		messageID, external, "provider-a:"+external, wallet.playerID, wallet.id, kind, reference,
	)

	_, err := environment.client.Send(context.Background(), sqsclient.SendParams{
		QueueURL:        environment.queues.Requests,
		Body:            body,
		GroupID:         wallet.id,
		DeduplicationID: randomID(),
	})
	if err != nil {
		t.Errorf("send %s: %v", messageID, err)
	}
}
