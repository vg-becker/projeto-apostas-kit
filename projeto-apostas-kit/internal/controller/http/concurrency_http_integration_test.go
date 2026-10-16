//go:build integration

package http_test

import (
	"net/http"
	"sync"
	"testing"
)

// submitAll dispara todas as requisições ao mesmo tempo pela borda HTTP.
func (f *fixture) submitAll(t *testing.T, requests []operationCall) []reply {
	t.Helper()

	replies := make([]reply, len(requests))
	start := make(chan struct{})

	var group sync.WaitGroup
	for index, request := range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start

			replies[index] = f.submit(t, request.key, request.body)
		}()
	}

	close(start)
	group.Wait()

	return replies
}

type operationCall struct {
	key  string
	body string
}

func TestFiftyIdenticalRequestsDebitOnce(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "1000.00")
	call := operationCall{key: "provider-a:tx-1", body: operationBody(wallet, "tx-1", "BET", "25.00")}
	requests := make([]operationCall, 50)
	for index := range requests {
		requests[index] = call
	}

	originals, replays := 0, 0
	for _, response := range f.submitAll(t, requests) {
		if response.status != http.StatusOK {
			t.Fatalf("response = %d %s", response.status, response.body)
		}

		if response.object(t)["idempotentReplay"] == true {
			replays++
		} else {
			originals++
		}
	}

	if originals != 1 || replays != 49 || f.debits(t, wallet.id) != 1 {
		t.Fatalf("originals = %d, replays = %d, debits = %d", originals, replays, f.debits(t, wallet.id))
	}
}

func TestTwoBetsOf80OverABalanceOf100ThroughHTTP(t *testing.T) {
	f := newFixture(t)
	wallet := f.createWallet(t, "100.00")
	requests := []operationCall{
		{key: "provider-a:tx-1", body: operationBody(wallet, "tx-1", "BET", "80.00")},
		{key: "provider-a:tx-2", body: operationBody(wallet, "tx-2", "BET", "80.00")},
	}

	statuses := map[int]int{}
	for _, response := range f.submitAll(t, requests) {
		statuses[response.status]++
	}

	if statuses[http.StatusOK] != 1 || statuses[http.StatusUnprocessableEntity] != 1 {
		t.Fatalf("statuses = %v, want one 200 and one 422", statuses)
	}

	current := f.call(t, http.MethodGet, "/wallets/"+wallet.id, "internal", nil, "").object(t)
	balance := current["balance"].(map[string]any)

	if balance["amount"] != "20.00" || f.debits(t, wallet.id) != 1 {
		t.Fatalf("balance = %v, debits = %d", balance, f.debits(t, wallet.id))
	}
}
