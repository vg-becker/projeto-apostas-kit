package problem

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
	"apostas-go/internal/service"
)

func TestEveryKnownErrorMapsToItsStatusAndStableCode(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"idempotency conflict", service.ErrIdempotencyConflict, 409, "IDEMPOTENCY_CONFLICT"},
		{"wallet exists", service.ErrWalletExists, 409, "WALLET_ALREADY_EXISTS"},
		{"wallet not found", service.ErrWalletNotFound, 404, "WALLET_NOT_FOUND"},
		{"transaction not found", service.ErrTransactionNotFound, 404, "TRANSACTION_NOT_FOUND"},
		{"access denied", service.ErrAccessDenied, 403, "FORBIDDEN"},
		{"player mismatch", service.ErrWalletPlayerMismatch, 400, "WALLET_PLAYER_MISMATCH"},
		{"service currency mismatch", service.ErrCurrencyMismatch, 400, "CURRENCY_MISMATCH"},
		{"money currency mismatch", money.ErrCurrencyMismatch, 400, "CURRENCY_MISMATCH"},
		{"wallet currency mismatch", wallet.ErrCurrencyMismatch, 400, "CURRENCY_MISMATCH"},
		{"opening", wager.ErrOpeningNotAllowed, 400, "KIND_NOT_ALLOWED"},
		{"invalid kind", wager.ErrInvalidKind, 400, "INVALID_KIND"},
		{"unknown currency", money.ErrUnknownCurrency, 400, "UNKNOWN_CURRENCY"},
		{"amount policy", wager.ErrAmountPolicy, 400, "INVALID_AMOUNT"},
		{"invalid format", money.ErrInvalidFormat, 400, "INVALID_AMOUNT"},
		{"scale exceeded", money.ErrScaleExceeded, 400, "INVALID_AMOUNT"},
		{"negative", money.ErrNegativeNotAllowed, 400, "INVALID_AMOUNT"},
		{"overflow", money.ErrOverflow, 400, "INVALID_AMOUNT"},
		{"uninitialized money", money.ErrUninitialized, 400, "INVALID_AMOUNT"},
		{"invalid cursor", port.ErrInvalidCursor, 400, "INVALID_CURSOR"},
		{"invalid transaction", wager.ErrInvalidTransaction, 400, "INVALID_REQUEST"},
		{"invalid wallet state", wallet.ErrInvalidState, 400, "INVALID_REQUEST"},
		{"invalid id", identity.ErrInvalidID, 400, "INVALID_REQUEST"},
		{"transient", port.ErrTransient, 503, "SERVICE_UNAVAILABLE"},
		{"deadline", context.DeadlineExceeded, 503, "SERVICE_UNAVAILABLE"},
		{"unknown", errors.New("boom"), 500, "INTERNAL_ERROR"},
		{"nil", nil, 500, "INTERNAL_ERROR"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := From(test.err)

			if got.Status != test.wantStatus || got.Code != test.wantCode {
				t.Fatalf("got %d %s, want %d %s", got.Status, got.Code, test.wantStatus, test.wantCode)
			}
		})
	}
}

func TestWrappedErrorsAreRecognized(t *testing.T) {
	wrapped := fmt.Errorf("process: %w", fmt.Errorf("lookup: %w", service.ErrWalletNotFound))

	got := From(wrapped)

	if got.Status != http.StatusNotFound || got.Code != "WALLET_NOT_FOUND" {
		t.Fatalf("got %d %s", got.Status, got.Code)
	}
}

func TestATransientFailureWinsOverAnyOtherMeaning(t *testing.T) {
	err := fmt.Errorf("%w: while reading: %w", service.ErrWalletNotFound, port.ErrTransient)

	got := From(err)

	if got.Status != http.StatusServiceUnavailable || got.RetryAfterSeconds <= 0 {
		t.Fatalf("got %d with retry-after %d, a transient failure must be a retryable 503",
			got.Status, got.RetryAfterSeconds)
	}
}

func TestOnlyUnavailabilityCarriesRetryAfter(t *testing.T) {
	for _, err := range []error{service.ErrWalletExists, errors.New("boom"), wager.ErrInvalidKind} {
		if got := From(err); got.RetryAfterSeconds != 0 {
			t.Fatalf("%v: retry-after = %d, want none", err, got.RetryAfterSeconds)
		}
	}
}

func TestServerErrorsNeverLeakTheErrorText(t *testing.T) {
	secret := "password=hunter2 host=10.0.0.5"

	for _, err := range []error{errors.New(secret), fmt.Errorf("%w: %s", port.ErrTransient, secret)} {
		got := From(err)

		if strings.Contains(got.Detail, "hunter2") || strings.Contains(got.Body().Detail, "10.0.0.5") {
			t.Fatalf("detail leaked internals: %q", got.Detail)
		}
	}
}

func TestClientErrorsExplainThemselves(t *testing.T) {
	err := fmt.Errorf("%w: round-1 does not match", service.ErrWalletPlayerMismatch)

	got := From(err)

	if !strings.Contains(got.Detail, "round-1 does not match") {
		t.Fatalf("detail = %q, want the domain message", got.Detail)
	}
}

func TestBodyFollowsTheProblemContract(t *testing.T) {
	body := From(service.ErrIdempotencyConflict).Body()

	if body.Type != "urn:apostas:problem:IDEMPOTENCY_CONFLICT" || body.Status != 409 ||
		body.Code != "IDEMPOTENCY_CONFLICT" || body.Title == "" {
		t.Fatalf("body = %+v", body)
	}
}

func TestCodesAreUniquePerMeaning(t *testing.T) {
	statusByCode := map[string]int{}

	for _, candidate := range rules {
		if previous, seen := statusByCode[candidate.code]; seen && previous != candidate.status {
			t.Fatalf("code %s is used with two statuses", candidate.code)
		}

		statusByCode[candidate.code] = candidate.status
	}
}

func TestBorderProblemsHaveDistinctStableCodes(t *testing.T) {
	problems := []Problem{
		InvalidRequest("x"),
		InvalidJSON("x"),
		MissingIdempotencyKey(),
		Unauthenticated(),
		ProviderMismatch(),
		PayloadTooLarge(),
		UnsupportedMediaType(),
		RouteNotFound(),
		MethodNotAllowed(),
	}

	wantStatus := map[string]int{
		"INVALID_REQUEST":         http.StatusBadRequest,
		"INVALID_JSON":            http.StatusBadRequest,
		"MISSING_IDEMPOTENCY_KEY": http.StatusBadRequest,
		"UNAUTHENTICATED":         http.StatusUnauthorized,
		"PROVIDER_MISMATCH":       http.StatusForbidden,
		"PAYLOAD_TOO_LARGE":       http.StatusRequestEntityTooLarge,
		"UNSUPPORTED_MEDIA_TYPE":  http.StatusUnsupportedMediaType,
		"ROUTE_NOT_FOUND":         http.StatusNotFound,
		"METHOD_NOT_ALLOWED":      http.StatusMethodNotAllowed,
	}

	seen := map[string]bool{}
	for _, problem := range problems {
		if seen[problem.Code] || wantStatus[problem.Code] != problem.Status || problem.Title == "" {
			t.Fatalf("problem %+v is duplicated or has the wrong status", problem)
		}

		seen[problem.Code] = true
	}
}

func TestUnauthenticatedNeverExplainsWhyTheCredentialWasRefused(t *testing.T) {
	detail := Unauthenticated().Detail

	for _, leak := range []string{"expired", "signature", "issuer", "audience"} {
		if strings.Contains(strings.ToLower(detail), leak) {
			t.Fatalf("detail %q reveals %s", detail, leak)
		}
	}
}
