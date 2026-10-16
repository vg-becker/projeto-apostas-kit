//go:build integration

package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	httpcontroller "apostas-go/internal/controller/http"
	"apostas-go/internal/model/port"
	"apostas-go/internal/platform/idgen"
	"apostas-go/internal/repository/postgres"
	"apostas-go/internal/service"
	"apostas-go/internal/testsupport/postgresdb"
)

var baseTime = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

// steppingClock só anda quando o teste manda.
type steppingClock struct {
	mutex   sync.Mutex
	instant time.Time
}

func (clock *steppingClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()

	return clock.instant
}

func (clock *steppingClock) Advance(duration time.Duration) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()

	clock.instant = clock.instant.Add(duration)
}

// tokenAuthenticator troca o JWT da Etapa 5 por tokens fixos, só para testar as regras do
// Controller: quem pode o quê.
type tokenAuthenticator struct{}

func (tokenAuthenticator) Authenticate(request *http.Request) (httpcontroller.Principal, error) {
	switch request.Header.Get("Authorization") {
	case "Bearer internal":
		return httpcontroller.Principal{Internal: true}, nil
	case "Bearer provider-a":
		return httpcontroller.Principal{ProviderID: "provider-a"}, nil
	case "Bearer provider-b":
		return httpcontroller.Principal{ProviderID: "provider-b"}, nil
	case "Bearer panic":
		panic("boom-secret-token")
	default:
		return httpcontroller.Principal{}, httpcontroller.ErrUnauthenticated
	}
}

// unavailableTransactions simula o Postgres fora do ar.
type unavailableTransactions struct{}

func (unavailableTransactions) WithinTransaction(
	_ context.Context,
	_ func(ctx context.Context) error,
) error {
	return fmt.Errorf("%w: connection refused password=hunter2", port.ErrTransient)
}

type fixture struct {
	server   *httptest.Server
	pool     *pgxpool.Pool
	clock    *steppingClock
	resolver *service.ReferenceResolver
	ids      idgen.Generator
	logs     *lockedBuffer
	sqs      *standInCheck
}

// lockedBuffer recebe o log do servidor, que escreve de várias goroutines.
type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedBuffer) Write(payload []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()

	return buffer.buffer.Write(payload)
}

func (buffer *lockedBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()

	return buffer.buffer.String()
}

// standInCheck substitui só o SQS no readiness, que só existirá na Etapa 6.
type standInCheck struct {
	name string
	down atomic.Bool
}

func (check *standInCheck) Name() string { return check.name }

func (check *standInCheck) Check(context.Context) error {
	if check.down.Load() {
		return errors.New("connection refused password=hunter2")
	}

	return nil
}

// hangingCheck ignora o contexto e nunca responde, até o teste liberá-lo.
type hangingCheck struct {
	release chan struct{}
}

func (hangingCheck) Name() string { return "slow" }

func (check hangingCheck) Check(context.Context) error {
	<-check.release

	return nil
}

type fixtureOptions struct {
	// operationTransactions, se não for nulo, é usado só pelo serviço de operações.
	operationTransactions port.TxManager
	extraReadiness        []port.HealthCheck
	readinessTimeout      time.Duration
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	return newFixtureWithOptions(t, fixtureOptions{})
}

func newFixtureWith(t *testing.T, operationTransactions port.TxManager) *fixture {
	t.Helper()

	return newFixtureWithOptions(t, fixtureOptions{operationTransactions: operationTransactions})
}

// newFixtureWithOptions monta o Controller sobre o Service e o Postgres reais.
func newFixtureWithOptions(t *testing.T, options fixtureOptions) *fixture {
	t.Helper()

	pool := postgresdb.Start(t)
	connection := postgres.NewConnection(pool)
	stepping := &steppingClock{instant: baseTime}
	manager := postgres.NewTransactionManager(pool)
	outbox := postgres.NewOutboxRepository(connection)

	operationDependencies := service.OperationServiceDependencies{
		Transactions: manager,
		Wallets:      postgres.NewWalletRepository(connection),
		Operations:   postgres.NewTransactionRepository(connection),
		Ledger:       postgres.NewLedgerRepository(connection),
		Outbox:       outbox,
		Clock:        stepping,
		IDs:          idgen.Generator{},
	}
	resolver := service.NewReferenceResolver(operationDependencies)

	if options.operationTransactions != nil {
		operationDependencies.Transactions = options.operationTransactions
	}

	logs := &lockedBuffer{}
	sqs := &standInCheck{name: "sqs"}

	router := httpcontroller.NewRouter(httpcontroller.Dependencies{
		Authenticator: tokenAuthenticator{},
		Wallets: service.NewWalletService(service.WalletServiceDependencies{
			Transactions: manager,
			Wallets:      operationDependencies.Wallets,
			Operations:   operationDependencies.Operations,
			Ledger:       operationDependencies.Ledger,
			Outbox:       outbox,
			Clock:        stepping,
			IDs:          idgen.Generator{},
		}),
		Operations: service.NewOperationService(operationDependencies),
		Queries: service.NewQueryService(service.QueryServiceDependencies{
			Wallets:    operationDependencies.Wallets,
			Operations: operationDependencies.Operations,
			Ledger:     operationDependencies.Ledger,
		}),
		Readiness:        append([]port.HealthCheck{postgres.NewHealthCheck(pool), sqs}, options.extraReadiness...),
		ReadinessTimeout: options.readinessTimeout,
		Logger:           slog.New(slog.NewJSONHandler(logs, nil)),
	})

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &fixture{
		server:   server,
		pool:     pool,
		clock:    stepping,
		resolver: resolver,
		logs:     logs,
		sqs:      sqs,
	}
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (r reply) object(t *testing.T) map[string]any {
	t.Helper()

	var decoded map[string]any
	if err := json.Unmarshal(r.body, &decoded); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", r.body, err)
	}

	return decoded
}

func (r reply) code(t *testing.T) string {
	t.Helper()

	code, _ := r.object(t)["code"].(string)

	return code
}

// call faz uma requisição real contra o servidor de teste.
func (f *fixture) call(
	t *testing.T,
	method string,
	path string,
	token string,
	headers map[string]string,
	body string,
) reply {
	t.Helper()

	request, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}

	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()

	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	return reply{status: response.StatusCode, header: response.Header, body: payload}
}

func (f *fixture) count(t *testing.T, query string, arguments ...any) int {
	t.Helper()

	var total int
	if err := f.pool.QueryRow(context.Background(), query, arguments...).Scan(&total); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}

	return total
}

func (f *fixture) newID(t *testing.T) string {
	t.Helper()

	id, err := f.ids.NewWalletID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}

	return id.String()
}

type walletRef struct {
	id       string
	playerID string
}

// createWallet abre uma carteira em BRL pela API, como o serviço interno.
func (f *fixture) createWallet(t *testing.T, amount string) walletRef {
	t.Helper()

	playerID := f.newID(t)
	body := fmt.Sprintf(
		`{"playerId":%q,"initialBalance":{"amount":%q,"currency":"BRL"}}`,
		playerID,
		amount,
	)

	response := f.call(t, http.MethodPost, "/wallets", "internal", nil, body)
	if response.status != http.StatusCreated {
		t.Fatalf("create wallet = %d %s", response.status, response.body)
	}

	return walletRef{id: response.object(t)["id"].(string), playerID: playerID}
}

func operationBody(wallet walletRef, external string, kind string, amount string) string {
	return fmt.Sprintf(
		`{"providerId":"provider-a","externalTransactionId":%q,"playerId":%q,`+
			`"walletId":%q,"roundId":"round-1","gameId":"game-1","kind":%q,`+
			`"money":{"amount":%q,"currency":"BRL"}}`,
		external, wallet.playerID, wallet.id, kind, amount,
	)
}

func referenceBody(wallet walletRef, external string, kind string, amount string, ref string) string {
	body := operationBody(wallet, external, kind, amount)

	return strings.TrimSuffix(body, "}") + fmt.Sprintf(`,"referenceExternalTransactionId":%q}`, ref)
}

// submit envia uma operação como provider-a, com a chave padrão {providerId}:{externalId}.
func (f *fixture) submit(t *testing.T, key string, body string) reply {
	t.Helper()

	return f.call(t, http.MethodPost, "/wagering/transactions", "provider-a",
		map[string]string{"Idempotency-Key": key}, body)
}

func (f *fixture) bet(t *testing.T, wallet walletRef, external string, amount string) reply {
	t.Helper()

	return f.submit(t, "provider-a:"+external, operationBody(wallet, external, "BET", amount))
}

func (f *fixture) debits(t *testing.T, walletID string) int {
	t.Helper()

	return f.count(
		t,
		`SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`,
		walletID,
	)
}
