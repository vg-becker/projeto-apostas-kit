//go:build integration

package http_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func assertProblem(t *testing.T, response reply, status int, code string) {
	t.Helper()

	if response.status != status || response.code(t) != code {
		t.Fatalf("got %d %s (%s), want %d %s", response.status, response.code(t), response.body, status, code)
	}

	if contentType := response.header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/problem+json") {
		t.Fatalf("content type = %q, errors must be problem+json", contentType)
	}
}

func TestWalletLifecycleOverHTTP(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "1000.00")

	created := f.call(t, http.MethodGet, "/wallets/"+wallet.id, "internal", nil, "")
	body := created.object(t)

	if created.status != http.StatusOK || body["id"] != wallet.id || body["playerId"] != wallet.playerID ||
		body["version"] != float64(1) {
		t.Fatalf("wallet = %d %s", created.status, created.body)
	}

	balance := body["balance"].(map[string]any)
	if balance["amount"] != "1000.00" || balance["currency"] != "BRL" {
		t.Fatalf("balance = %v", balance)
	}

	f.assertLedgerAndReconciliation(t, wallet)
}

func (f *fixture) assertLedgerAndReconciliation(t *testing.T, wallet walletRef) {
	t.Helper()

	ledger := f.call(t, http.MethodGet, "/wallets/"+wallet.id+"/ledger", "internal", nil, "")
	entries := ledger.object(t)["entries"].([]any)

	if ledger.status != http.StatusOK || len(entries) != 1 ||
		entries[0].(map[string]any)["direction"] != "CREDIT" {
		t.Fatalf("ledger = %d %s", ledger.status, ledger.body)
	}

	reconciliation := f.call(t, http.MethodPost, "/wallets/"+wallet.id+"/reconciliation", "internal", nil, "")
	result := reconciliation.object(t)

	if reconciliation.status != http.StatusOK || result["consistent"] != true ||
		result["checkedEntries"] != float64(1) {
		t.Fatalf("reconciliation = %d %s", reconciliation.status, reconciliation.body)
	}

	difference := result["difference"].(map[string]any)
	if difference["amount"] != "0.00" {
		t.Fatalf("difference = %v", difference)
	}
}

func TestCreatingAWalletAnswersWithTheContractHeadersAndBody(t *testing.T) {
	f := newFixture(t)
	playerID := f.newID(t)
	body := fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":"1000.00","currency":"BRL"}}`, playerID)

	response := f.call(t, http.MethodPost, "/wallets", "internal", nil, body)
	created := response.object(t)

	if response.status != http.StatusCreated ||
		response.header.Get("Location") != "/wallets/"+created["id"].(string) {
		t.Fatalf("response = %d, location = %q", response.status, response.header.Get("Location"))
	}

	if response.header.Get("Cache-Control") != "no-store" ||
		!strings.HasPrefix(response.header.Get("Content-Type"), "application/json") {
		t.Fatalf("headers = %v", response.header)
	}

	if len(created) != 4 || created["version"] != float64(1) {
		t.Fatalf("body = %s, want exactly id, playerId, balance and version", response.body)
	}
}

func TestADuplicateWalletConflicts(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "10.00")
	body := fmt.Sprintf(
		`{"playerId":%q,"initialBalance":{"amount":"5.00","currency":"BRL"}}`,
		wallet.playerID,
	)

	response := f.call(t, http.MethodPost, "/wallets", "internal", nil, body)

	assertProblem(t, response, http.StatusConflict, "WALLET_ALREADY_EXISTS")
}

func TestWalletRoutesRequireTheInternalIdentity(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "100.00")
	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/wallets", fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":"1.00","currency":"BRL"}}`, f.newID(t))},
		{http.MethodGet, "/wallets/" + wallet.id, ""},
		{http.MethodGet, "/wallets/" + wallet.id + "/ledger", ""},
		{http.MethodPost, "/wallets/" + wallet.id + "/reconciliation", ""},
	}

	for _, route := range routes {
		noToken := f.call(t, route.method, route.path, "", nil, route.body)
		badToken := f.call(t, route.method, route.path, "forged", nil, route.body)
		provider := f.call(t, route.method, route.path, "provider-a", nil, route.body)

		assertProblem(t, noToken, http.StatusUnauthorized, "UNAUTHENTICATED")
		assertProblem(t, badToken, http.StatusUnauthorized, "UNAUTHENTICATED")
		assertProblem(t, provider, http.StatusForbidden, "FORBIDDEN")

		if noToken.header.Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("%s %s: missing WWW-Authenticate", route.method, route.path)
		}
	}

	if f.count(t, `SELECT count(*) FROM wallets`) != 1 {
		t.Fatal("an unauthorized request created a wallet")
	}
}

func TestOpeningAWalletRejectsHostileInput(t *testing.T) {
	f := newFixture(t)
	player := f.newID(t)
	valid := func(amount string) string {
		return fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":%s,"currency":"BRL"}}`, player, amount)
	}

	tests := []struct {
		name    string
		body    string
		headers map[string]string
		status  int
		code    string
	}{
		{name: "malformed json", body: `{`, status: 400, code: "INVALID_JSON"},
		{name: "unknown field", body: `{"playerId":"x","extra":1}`, status: 400, code: "INVALID_JSON"},
		{name: "data after the json", body: valid(`"1.00"`) + ` {}`, status: 400, code: "INVALID_JSON"},
		{name: "amount as a json number", body: valid(`10.5`), status: 400, code: "INVALID_JSON"},
		{name: "negative amount", body: valid(`"-1.00"`), status: 400, code: "INVALID_AMOUNT"},
		{name: "scientific notation", body: valid(`"1e2"`), status: 400, code: "INVALID_AMOUNT"},
		{name: "three decimals", body: valid(`"1.001"`), status: 400, code: "INVALID_AMOUNT"},
		{name: "empty amount", body: valid(`""`), status: 400, code: "INVALID_AMOUNT"},
		{
			name:   "unknown currency",
			body:   fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":"1.00","currency":"XXX"}}`, player),
			status: 400, code: "UNKNOWN_CURRENCY",
		},
		{
			name:   "invalid player id",
			body:   `{"playerId":"nope","initialBalance":{"amount":"1.00","currency":"BRL"}}`,
			status: 400, code: "INVALID_REQUEST",
		},
		{
			name:   "missing initial balance",
			body:   fmt.Sprintf(`{"playerId":%q}`, player),
			status: 400, code: "INVALID_REQUEST",
		},
		{
			name:    "wrong content type",
			body:    valid(`"1.00"`),
			headers: map[string]string{"Content-Type": "text/plain"},
			status:  415, code: "UNSUPPORTED_MEDIA_TYPE",
		},
		{
			name:   "oversized body",
			body:   fmt.Sprintf(`{"playerId":%q}`, strings.Repeat("a", 70<<10)),
			status: 413, code: "PAYLOAD_TOO_LARGE",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := f.call(t, http.MethodPost, "/wallets", "internal", test.headers, test.body)

			assertProblem(t, response, test.status, test.code)
		})
	}

	if f.count(t, `SELECT count(*) FROM wallets`) != 0 {
		t.Fatal("hostile input created a wallet")
	}
}

func TestReadingWalletsWithBadIdentifiers(t *testing.T) {
	f := newFixture(t)

	unknown := f.call(t, http.MethodGet, "/wallets/"+f.newID(t), "internal", nil, "")
	malformed := f.call(t, http.MethodGet, "/wallets/not-a-uuid", "internal", nil, "")
	ledgerUnknown := f.call(t, http.MethodGet, "/wallets/"+f.newID(t)+"/ledger", "internal", nil, "")

	assertProblem(t, unknown, http.StatusNotFound, "WALLET_NOT_FOUND")
	assertProblem(t, malformed, http.StatusBadRequest, "INVALID_REQUEST")
	assertProblem(t, ledgerUnknown, http.StatusNotFound, "WALLET_NOT_FOUND")
}

func TestLedgerPaginationOverHTTP(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "1000.00")
	for index := range 3 {
		if response := f.bet(t, wallet, fmt.Sprintf("bet-%d", index), "1.00"); response.status != 200 {
			t.Fatalf("bet = %d %s", response.status, response.body)
		}
	}

	first := f.call(t, http.MethodGet, "/wallets/"+wallet.id+"/ledger?limit=3", "internal", nil, "")
	cursor, _ := first.object(t)["nextCursor"].(string)

	if len(first.object(t)["entries"].([]any)) != 3 || cursor == "" {
		t.Fatalf("first page = %s", first.body)
	}

	second := f.call(t, http.MethodGet, "/wallets/"+wallet.id+"/ledger?limit=3&cursor="+cursor, "internal", nil, "")

	if len(second.object(t)["entries"].([]any)) != 1 || second.object(t)["nextCursor"] != nil {
		t.Fatalf("second page = %s", second.body)
	}
}

func TestLedgerQueryParametersAreValidated(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "10.00")
	base := "/wallets/" + wallet.id + "/ledger"

	for _, query := range []string{"?limit=0", "?limit=abc", "?limit=201", "?limit=-1"} {
		assertProblem(t, f.call(t, http.MethodGet, base+query, "internal", nil, ""), 400, "INVALID_REQUEST")
	}

	assertProblem(t, f.call(t, http.MethodGet, base+"?cursor=garbage", "internal", nil, ""), 400, "INVALID_CURSOR")
}
