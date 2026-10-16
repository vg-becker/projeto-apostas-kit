//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	httpcontroller "apostas-go/internal/controller/http"
	"apostas-go/internal/platform/auth"
	"apostas-go/internal/platform/clock"
	"apostas-go/internal/platform/idgen"
	"apostas-go/internal/repository/postgres"
	"apostas-go/internal/service"
	"apostas-go/internal/testsupport/keycloak"
	"apostas-go/internal/testsupport/postgresdb"
)

const audience = "apostas-api"

// realmConfig é a configuração de autenticação que o serviço usaria contra o Keycloak do
// teste.
func realmConfig() auth.Config {
	return auth.Config{
		IssuerURL: keycloakServer.Issuer(keycloak.ProjectRealm),
		JWKSURL:   keycloakServer.JWKSURL(keycloak.ProjectRealm),
		Audience:  audience,
	}
}

// stack é o serviço completo: borda HTTP com autenticação real, Service e Postgres reais.
type stack struct {
	server *httptest.Server
	pool   *pgxpool.Pool
	ids    idgen.Generator
}

func newStack(t *testing.T, config auth.Config) *stack {
	t.Helper()

	pool := postgresdb.Start(t)
	connection := postgres.NewConnection(pool)
	outbox := postgres.NewOutboxRepository(connection)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	authenticator, err := auth.New(config, logger)
	if err != nil {
		t.Fatalf("auth.New error = %v", err)
	}

	operations := service.OperationServiceDependencies{
		Transactions: postgres.NewTransactionManager(pool),
		Wallets:      postgres.NewWalletRepository(connection),
		Operations:   postgres.NewTransactionRepository(connection),
		Ledger:       postgres.NewLedgerRepository(connection),
		Outbox:       outbox,
		Clock:        clock.System{},
		IDs:          idgen.Generator{},
	}

	server := httptest.NewServer(httpcontroller.NewRouter(httpcontroller.Dependencies{
		Authenticator: authenticator,
		Wallets: service.NewWalletService(service.WalletServiceDependencies{
			Transactions: operations.Transactions,
			Wallets:      operations.Wallets,
			Operations:   operations.Operations,
			Ledger:       operations.Ledger,
			Outbox:       outbox,
			Clock:        operations.Clock,
			IDs:          operations.IDs,
		}),
		Operations: service.NewOperationService(operations),
		Queries: service.NewQueryService(service.QueryServiceDependencies{
			Wallets:    operations.Wallets,
			Operations: operations.Operations,
			Ledger:     operations.Ledger,
		}),
		Logger: logger,
	}))
	t.Cleanup(server.Close)

	return &stack{server: server, pool: pool}
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

func (s *stack) call(
	t *testing.T,
	method string,
	path string,
	authorization string,
	headers map[string]string,
	body string,
) reply {
	t.Helper()

	request, err := http.NewRequest(method, s.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	if authorization != "" {
		request.Header.Set("Authorization", authorization)
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

func (s *stack) count(t *testing.T, query string) int {
	t.Helper()

	var total int
	if err := s.pool.QueryRow(context.Background(), query).Scan(&total); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}

	return total
}

func (s *stack) newID(t *testing.T) string {
	t.Helper()

	id, err := s.ids.NewWalletID()
	if err != nil {
		t.Fatalf("new id: %v", err)
	}

	return id.String()
}

// credentials são os segredos locais do realm de desenvolvimento; não valem fora dele.
var credentials = map[string]string{
	"provider-a":       "provider-a-secret-local",
	"provider-b":       "provider-b-secret-local",
	"internal-service": "internal-service-secret-local",
}

// bearer obtém um token real do Keycloak para o client e o devolve como cabeçalho.
func bearer(t *testing.T, clientID string) string {
	t.Helper()

	token, err := keycloakServer.Token(
		context.Background(),
		keycloak.ProjectRealm,
		clientID,
		credentials[clientID],
	)
	if err != nil {
		t.Fatalf("token for %s: %v", clientID, err)
	}

	return "Bearer " + token
}

// claimsOf lê o payload de um JWT sem verificá-lo, só para os testes inspecionarem o contrato.
func claimsOf(t *testing.T, authorization string) map[string]any {
	t.Helper()

	segments := strings.Split(strings.TrimPrefix(authorization, "Bearer "), ".")
	if len(segments) != 3 {
		t.Fatalf("not a jwt: %q", authorization)
	}

	payload, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("parse payload: %v", err)
	}

	return claims
}

// withClaim troca um claim do payload e mantém o cabeçalho e a assinatura originais.
func withClaim(t *testing.T, authorization string, name string, value any) string {
	t.Helper()

	segments := strings.Split(strings.TrimPrefix(authorization, "Bearer "), ".")
	claims := claimsOf(t, authorization)
	claims[name] = value

	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}

	segments[1] = base64.RawURLEncoding.EncodeToString(payload)

	return "Bearer " + strings.Join(segments, ".")
}

// withBrokenSignature altera um caractere do meio da assinatura e confere que os bytes
// decodificados mudaram. Não serve o último caractere: em base64url ele leva bits de
// preenchimento, e trocá-lo por um vizinho pode decodificar para a mesma assinatura.
func withBrokenSignature(t *testing.T, authorization string) string {
	t.Helper()

	segments := strings.Split(strings.TrimPrefix(authorization, "Bearer "), ".")
	signature := []byte(segments[2])

	middle := len(signature) / 2
	replacement := byte('A')
	if signature[middle] == 'A' {
		replacement = 'B'
	}

	original := decodeSegment(t, segments[2])
	signature[middle] = replacement
	segments[2] = string(signature)

	if bytes.Equal(original, decodeSegment(t, segments[2])) {
		t.Fatal("the tampered signature decodes to the same bytes: the test would prove nothing")
	}

	return "Bearer " + strings.Join(segments, ".")
}

func decodeSegment(t *testing.T, segment string) []byte {
	t.Helper()

	decoded, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("decode segment: %v", err)
	}

	return decoded
}

type walletRef struct {
	id       string
	playerID string
}

// openWallet abre uma carteira como o serviço interno.
func (s *stack) openWallet(t *testing.T, amount string) walletRef {
	t.Helper()

	playerID := s.newID(t)
	body := fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":%q,"currency":"BRL"}}`, playerID, amount)

	response := s.call(t, http.MethodPost, "/wallets", bearer(t, "internal-service"), nil, body)
	if response.status != http.StatusCreated {
		t.Fatalf("open wallet = %d %s", response.status, response.body)
	}

	return walletRef{id: response.object(t)["id"].(string), playerID: playerID}
}

func operationBody(wallet walletRef, providerID string, external string, amount string) string {
	return fmt.Sprintf(
		`{"providerId":%q,"externalTransactionId":%q,"playerId":%q,"walletId":%q,`+
			`"roundId":"round-1","gameId":"game-1","kind":"BET","money":{"amount":%q,"currency":"BRL"}}`,
		providerID, external, wallet.playerID, wallet.id, amount,
	)
}

// submit envia uma operação com o cabeçalho de autorização dado.
func (s *stack) submit(t *testing.T, authorization string, key string, body string) reply {
	t.Helper()

	return s.call(t, http.MethodPost, "/wagering/transactions", authorization,
		map[string]string{"Idempotency-Key": key}, body)
}

func eventually(t *testing.T, within time.Duration, check func() bool) {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if check() {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatal("condition not met in time")
}
