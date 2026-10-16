package response

import (
	"encoding/json"
	"testing"
)

func marshal(t *testing.T, value any) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal error = %v", err)
	}

	return string(encoded)
}

func TestWalletJSONMatchesTheContract(t *testing.T) {
	got := marshal(t, Wallet{
		ID:       "0192f291-27dd-7d3f-8071-5f8685deef37",
		PlayerID: "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
		Balance:  Money{Amount: "1000.00", Currency: "BRL"},
		Version:  1,
	})

	want := `{"id":"0192f291-27dd-7d3f-8071-5f8685deef37",` +
		`"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",` +
		`"balance":{"amount":"1000.00","currency":"BRL"},"version":1}`

	if got != want {
		t.Fatalf("json = %s\nwant   %s", got, want)
	}
}

func TestOperationJSONForEachOutcome(t *testing.T) {
	balance := Money{Amount: "975.00", Currency: "BRL"}
	correctable := false

	tests := []struct {
		name  string
		value Operation
		want  string
	}{
		{
			name: "processed matches the example of the challenge",
			value: Operation{
				TransactionID: "0192f298-345e-7e38-af88-e43f851a819d",
				Status:        "PROCESSED",
				Balance:       &balance,
			},
			want: `{"transactionId":"0192f298-345e-7e38-af88-e43f851a819d","status":"PROCESSED",` +
				`"balance":{"amount":"975.00","currency":"BRL"},"idempotentReplay":false}`,
		},
		{
			name: "pending has no balance",
			value: Operation{
				TransactionID:    "t-1",
				Status:           "PENDING_REFERENCE",
				IdempotentReplay: true,
			},
			want: `{"transactionId":"t-1","status":"PENDING_REFERENCE","idempotentReplay":true}`,
		},
		{
			name: "rejected carries the failure code",
			value: Operation{
				TransactionID: "t-2",
				Status:        "REJECTED",
				FailureCode:   "INSUFFICIENT_FUNDS",
				Correctable:   &correctable,
			},
			want: `{"transactionId":"t-2","status":"REJECTED","failureCode":"INSUFFICIENT_FUNDS",` +
				`"correctable":false,"idempotentReplay":false}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := marshal(t, test.value); got != test.want {
				t.Fatalf("json = %s\nwant   %s", got, test.want)
			}
		})
	}
}

func TestLedgerPageAlwaysSerializesEntriesAsAnArray(t *testing.T) {
	got := marshal(t, LedgerPage{Entries: []LedgerEntry{}})

	if got != `{"entries":[]}` {
		t.Fatalf("json = %s, an empty page must be [] and not null", got)
	}
}

func TestProblemJSON(t *testing.T) {
	got := marshal(t, Problem{
		Type:   "urn:apostas:problem:IDEMPOTENCY_CONFLICT",
		Title:  "Idempotency key reused with different content",
		Status: 409,
		Code:   "IDEMPOTENCY_CONFLICT",
	})

	want := `{"type":"urn:apostas:problem:IDEMPOTENCY_CONFLICT",` +
		`"title":"Idempotency key reused with different content","status":409,` +
		`"code":"IDEMPOTENCY_CONFLICT"}`

	if got != want {
		t.Fatalf("json = %s\nwant   %s", got, want)
	}
}

func TestReconciliationJSON(t *testing.T) {
	zero := Money{Amount: "0.00", Currency: "BRL"}
	balance := Money{Amount: "975.00", Currency: "BRL"}

	got := marshal(t, Reconciliation{
		WalletID:          "w-1",
		StoredBalance:     balance,
		CalculatedBalance: balance,
		Difference:        zero,
		Consistent:        true,
		CheckedEntries:    2,
	})

	want := `{"walletId":"w-1","storedBalance":{"amount":"975.00","currency":"BRL"},` +
		`"calculatedBalance":{"amount":"975.00","currency":"BRL"},` +
		`"difference":{"amount":"0.00","currency":"BRL"},"consistent":true,"checkedEntries":2}`

	if got != want {
		t.Fatalf("json = %s\nwant   %s", got, want)
	}
}

func TestHealthJSON(t *testing.T) {
	live := marshal(t, Health{Status: "UP"})
	if live != `{"status":"UP"}` {
		t.Fatalf("live = %s", live)
	}

	ready := marshal(t, Health{
		Status: "DOWN",
		Checks: map[string]string{"sqs": "DOWN", "postgres": "UP"},
	})

	if ready != `{"status":"DOWN","checks":{"postgres":"UP","sqs":"DOWN"}}` {
		t.Fatalf("ready = %s", ready)
	}
}
