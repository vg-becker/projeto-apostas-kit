//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
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

// shutDown encerra cada instância com SIGTERM e confere que saiu com status 0 e sem nenhum
// registro de erro nos logs: um erro registrado durante um teste é um defeito.
func (running *cluster) shutDown(t *testing.T) {
	for index, started := range running.instances {
		_ = started.command.Process.Signal(syscall.SIGTERM)

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
