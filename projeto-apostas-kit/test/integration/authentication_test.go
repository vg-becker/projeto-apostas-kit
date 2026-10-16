//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"apostas-go/internal/testsupport/keycloak"
)

func assertProblem(t *testing.T, response reply, status int, code string) {
	t.Helper()

	if response.status != status || response.code(t) != code {
		t.Fatalf("got %d %s (%s), want %d %s", response.status, response.code(t), response.body, status, code)
	}
}

// TestTheRealmIssuesTheTokensTheServiceExpects trava o contrato entre o realm provisionado
// e o autenticador: se o realm mudar, este teste quebra antes de qualquer outro.
func TestTheRealmIssuesTheTokensTheServiceExpects(t *testing.T) {
	provider := claimsOf(t, bearer(t, "provider-a"))
	internal := claimsOf(t, bearer(t, "internal-service"))

	for name, claims := range map[string]map[string]any{"provider-a": provider, "internal": internal} {
		if claims["iss"] != keycloakServer.Issuer(keycloak.ProjectRealm) {
			t.Fatalf("%s: iss = %v, want %s", name, claims["iss"], keycloakServer.Issuer(keycloak.ProjectRealm))
		}

		if !strings.Contains(toText(claims["aud"]), audience) {
			t.Fatalf("%s: aud = %v, want it to contain %s", name, claims["aud"], audience)
		}
	}

	if provider["provider_id"] != "provider-a" || strings.Contains(toText(provider["scope"]), "wallet:internal") {
		t.Fatalf("provider claims = %v", provider)
	}

	if internal["provider_id"] != nil || !strings.Contains(toText(internal["scope"]), "wallet:internal") {
		t.Fatalf("internal claims = %v", internal)
	}
}

func toText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}

	items, _ := value.([]any)
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			parts = append(parts, text)
		}
	}

	return strings.Join(parts, " ")
}

func TestEachRealIdentityDoesOnlyWhatItMay(t *testing.T) {
	s := newStack(t, realmConfig())
	wallet := s.openWallet(t, "1000.00")
	providerA := bearer(t, "provider-a")
	providerB := bearer(t, "provider-b")
	internal := bearer(t, "internal-service")

	bet := s.submit(t, providerA, "provider-a:tx-1", operationBody(wallet, "provider-a", "tx-1", "25.00"))
	betID, _ := bet.object(t)["transactionId"].(string)

	if bet.status != http.StatusOK || bet.object(t)["status"] != "PROCESSED" {
		t.Fatalf("provider-a bet = %d %s", bet.status, bet.body)
	}

	assertProblem(t, s.call(t, http.MethodGet, "/wallets/"+wallet.id, providerA, nil, ""), 403, "FORBIDDEN")
	assertProblem(t, s.call(t, http.MethodGet, "/wallets/"+wallet.id+"/ledger", providerB, nil, ""), 403, "FORBIDDEN")
	assertProblem(t, s.submit(t, internal, "k-internal", operationBody(wallet, "provider-a", "tx-2", "1.00")), 403, "FORBIDDEN")

	own := s.call(t, http.MethodGet, "/wagering/transactions/"+betID, providerA, nil, "")
	foreign := s.call(t, http.MethodGet, "/wagering/transactions/"+betID, providerB, nil, "")
	foreignPath := s.call(t, http.MethodGet, "/providers/provider-a/wagering/transactions/tx-1", providerB, nil, "")
	everything := s.call(t, http.MethodGet, "/wagering/transactions/"+betID, internal, nil, "")

	if own.status != 200 || everything.status != 200 {
		t.Fatalf("own = %d, internal = %d", own.status, everything.status)
	}

	assertProblem(t, foreign, http.StatusNotFound, "TRANSACTION_NOT_FOUND")
	assertProblem(t, foreignPath, http.StatusForbidden, "FORBIDDEN")

	reconciliation := s.call(t, http.MethodPost, "/wallets/"+wallet.id+"/reconciliation", internal, nil, "")
	if reconciliation.status != 200 || reconciliation.object(t)["consistent"] != true {
		t.Fatalf("reconciliation = %d %s", reconciliation.status, reconciliation.body)
	}
}

func TestMissingInvalidAndTamperedCredentialsAreRefusedWithoutAnyEffect(t *testing.T) {
	s := newStack(t, realmConfig())
	wallet := s.openWallet(t, "1000.00")
	genuine := bearer(t, "provider-a")
	body := operationBody(wallet, "provider-a", "tx-1", "25.00")

	attempts := map[string]string{
		"no credential":           "",
		"garbage":                 "Bearer not-a-token",
		"wrong scheme":            "Basic " + strings.TrimPrefix(genuine, "Bearer "),
		"broken signature":        withBrokenSignature(t, genuine),
		"payload swapped to b":    withClaim(t, genuine, "provider_id", "provider-b"),
		"internal scope injected": withClaim(t, genuine, "scope", "wallet:internal"),
		"audience rewritten":      withClaim(t, genuine, "aud", "someone-else"),
	}

	for name, authorization := range attempts {
		t.Run(name, func(t *testing.T) {
			response := s.submit(t, authorization, "k-"+strings.ReplaceAll(name, " ", "-"), body)

			assertProblem(t, response, http.StatusUnauthorized, "UNAUTHENTICATED")

			if response.header.Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("a 401 must carry WWW-Authenticate")
			}
		})
	}

	if s.count(t, `SELECT count(*) FROM wager_transactions WHERE kind <> 'OPENING'`) != 0 ||
		s.count(t, `SELECT count(*) FROM wallet_ledger_entries WHERE direction = 'DEBIT'`) != 0 {
		t.Fatal("a refused credential had financial effects")
	}
}

func TestAnExpiredRealTokenIsRefused(t *testing.T) {
	s := newStack(t, realmConfig())
	ctx := context.Background()

	if err := keycloakServer.SetAccessTokenLifespan(ctx, keycloak.ProjectRealm, 2); err != nil {
		t.Fatalf("shorten lifespan: %v", err)
	}

	t.Cleanup(func() {
		if err := keycloakServer.SetAccessTokenLifespan(ctx, keycloak.ProjectRealm, 300); err != nil {
			t.Errorf("restore lifespan: %v", err)
		}
	})

	short := bearer(t, "provider-a")
	probe := "/providers/provider-a/wagering/transactions/ghost"

	fresh := s.call(t, http.MethodGet, probe, short, nil, "")
	assertProblem(t, fresh, http.StatusNotFound, "TRANSACTION_NOT_FOUND")

	eventually(t, 10*time.Second, func() bool {
		return s.call(t, http.MethodGet, probe, short, nil, "").status == http.StatusUnauthorized
	})

	assertProblem(t, s.call(t, http.MethodGet, probe, short, nil, ""), http.StatusUnauthorized, "UNAUTHENTICATED")
}

func TestTokensFromAnotherRealmOrWithoutTheAudienceAreRefused(t *testing.T) {
	s := newStack(t, realmConfig())
	ctx := context.Background()

	if err := keycloakServer.CreateRealm(ctx, "rogue"); err != nil {
		t.Fatalf("create realm: %v", err)
	}

	rogueClient := keycloak.ClientOptions{Audience: audience, ProviderID: "provider-a"}
	if err := keycloakServer.CreateServiceClient(ctx, "rogue", "mimic", "mimic-secret", rogueClient); err != nil {
		t.Fatalf("create rogue client: %v", err)
	}

	if err := keycloakServer.CreateServiceClient(
		ctx, keycloak.ProjectRealm, "no-audience", "no-audience-secret",
		keycloak.ClientOptions{ProviderID: "provider-a"},
	); err != nil {
		t.Fatalf("create client without audience: %v", err)
	}

	foreign, err := keycloakServer.Token(ctx, "rogue", "mimic", "mimic-secret")
	if err != nil {
		t.Fatalf("rogue token: %v", err)
	}

	missingAudience, err := keycloakServer.Token(ctx, keycloak.ProjectRealm, "no-audience", "no-audience-secret")
	if err != nil {
		t.Fatalf("no-audience token: %v", err)
	}

	probe := "/providers/provider-a/wagering/transactions/ghost"

	assertProblem(t, s.call(t, http.MethodGet, probe, "Bearer "+foreign, nil, ""), 401, "UNAUTHENTICATED")
	assertProblem(t, s.call(t, http.MethodGet, probe, "Bearer "+missingAudience, nil, ""), 401, "UNAUTHENTICATED")
}

func TestTheProviderComesFromTheTokenNeverFromTheBody(t *testing.T) {
	s := newStack(t, realmConfig())
	wallet := s.openWallet(t, "1000.00")
	providerA := bearer(t, "provider-a")

	impersonation := s.submit(t, providerA, "k-1", operationBody(wallet, "provider-b", "tx-1", "10.00"))

	assertProblem(t, impersonation, http.StatusForbidden, "PROVIDER_MISMATCH")

	if s.count(t, `SELECT count(*) FROM wager_transactions WHERE kind <> 'OPENING'`) != 0 {
		t.Fatal("an impersonation attempt created a transaction")
	}

	first := s.submit(t, providerA, "shared-key", operationBody(wallet, "provider-a", "tx-1", "10.00"))
	second := s.submit(t, bearer(t, "provider-b"), "shared-key", operationBody(wallet, "provider-b", "tx-1", "10.00"))

	if first.status != 200 || second.status != 200 || second.object(t)["idempotentReplay"] != false ||
		first.object(t)["transactionId"] == second.object(t)["transactionId"] {
		t.Fatalf("first = %d %s, second = %d %s: a replay must never cross providers",
			first.status, first.body, second.status, second.body)
	}
}

func TestAnUnreachableIdentityProviderIsA503NotA401(t *testing.T) {
	config := realmConfig()
	config.JWKSURL = "http://127.0.0.1:1/protocol/openid-connect/certs"
	config.RequestTimeout = 2 * time.Second
	s := newStack(t, config)

	response := s.call(t, http.MethodGet, "/providers/provider-a/wagering/transactions/ghost",
		bearer(t, "provider-a"), nil, "")

	assertProblem(t, response, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")

	if response.header.Get("Retry-After") == "" {
		t.Fatal("a 503 must carry Retry-After")
	}
}

func TestKeysAreCachedSoTokensKeepWorkingWhileTheyAreNotRefetched(t *testing.T) {
	s := newStack(t, realmConfig())
	token := bearer(t, "provider-a")
	probe := "/providers/provider-a/wagering/transactions/ghost"

	for range 5 {
		assertProblem(t, s.call(t, http.MethodGet, probe, token, nil, ""), http.StatusNotFound, "TRANSACTION_NOT_FOUND")
	}
}
