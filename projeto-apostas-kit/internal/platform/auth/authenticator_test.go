package auth

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpcontroller "apostas-go/internal/controller/http"
)

func newTestAuthenticator(t *testing.T, idp *fakeIdP) *Authenticator {
	t.Helper()

	authenticator, err := New(idp.config(), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New error = %v", err)
	}

	return authenticator
}

func requestWith(header string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if header != "" {
		request.Header.Set("Authorization", header)
	}

	return request
}

func TestTheAuthenticatorMapsTokensToPrincipals(t *testing.T) {
	idp := newFakeIdP(t, "k1")
	authenticator := newTestAuthenticator(t, idp)

	tests := []struct {
		name   string
		header string
		want   httpcontroller.Principal
	}{
		{
			name:   "provider",
			header: "Bearer " + idp.mint(t, "k1", providerClaims()),
			want:   httpcontroller.Principal{ProviderID: "provider-a"},
		},
		{
			name:   "internal service",
			header: "Bearer " + idp.mint(t, "k1", internalClaims()),
			want:   httpcontroller.Principal{Internal: true},
		},
		{
			name:   "scheme is case insensitive",
			header: "bearer " + idp.mint(t, "k1", providerClaims()),
			want:   httpcontroller.Principal{ProviderID: "provider-a"},
		},
		{
			name:   "valid token without permission is an empty principal",
			header: "Bearer " + idp.mint(t, "k1", withoutProvider(providerClaims())),
			want:   httpcontroller.Principal{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := authenticator.Authenticate(requestWith(test.header))

			if err != nil || got != test.want {
				t.Fatalf("principal = %+v (%v), want %+v", got, err, test.want)
			}
		})
	}
}

func withoutProvider(claims map[string]any) map[string]any {
	delete(claims, "provider_id")

	return claims
}

func TestTheAuthenticatorRefusesBadHeaders(t *testing.T) {
	idp := newFakeIdP(t, "k1")
	authenticator := newTestAuthenticator(t, idp)
	valid := idp.mint(t, "k1", providerClaims())

	headers := map[string]string{
		"missing":             "",
		"basic scheme":        "Basic " + valid,
		"no scheme":           valid,
		"empty token":         "Bearer ",
		"token with a space":  "Bearer " + valid + " extra",
		"two schemes":         "Bearer Bearer " + valid,
		"oversized token":     "Bearer " + strings.Repeat("a", maxTokenLength+1),
		"forged token":        "Bearer " + foreignToken(t, "k1", providerClaims()),
		"unsigned token":      "Bearer " + unsignedToken(t, providerClaims()),
		"hmac confusion":      "Bearer " + idp.hmacConfusedToken(t, "k1", providerClaims()),
		"tampered token":      "Bearer " + tamper(valid),
		"expired token":       "Bearer " + idp.mint(t, "k1", expiredClaims()),
		"wrong audience":      "Bearer " + idp.mint(t, "k1", withAudience(providerClaims(), "other")),
		"token in the scheme": "Bearer",
	}

	for name, header := range headers {
		t.Run(name, func(t *testing.T) {
			_, err := authenticator.Authenticate(requestWith(header))

			if !errors.Is(err, httpcontroller.ErrUnauthenticated) {
				t.Fatalf("error = %v, want %v", err, httpcontroller.ErrUnauthenticated)
			}
		})
	}
}

func expiredClaims() map[string]any {
	claims := providerClaims()
	claims["exp"] = int64(1)

	return claims
}

func TestAnUnreachableIdPSurfacesAsUnavailableAndNeverAsUnauthenticated(t *testing.T) {
	idp := newFakeIdP(t, "k1")
	idp.down.Store(true)
	authenticator := newTestAuthenticator(t, idp)

	_, err := authenticator.Authenticate(requestWith("Bearer " + idp.mint(t, "k1", providerClaims())))

	if !errors.Is(err, httpcontroller.ErrIdentityProviderUnavailable) ||
		errors.Is(err, httpcontroller.ErrUnauthenticated) {
		t.Fatalf("error = %v, want %v", err, httpcontroller.ErrIdentityProviderUnavailable)
	}
}

func TestBearerTokenExtraction(t *testing.T) {
	tests := []struct {
		header string
		token  string
		ok     bool
	}{
		{header: "Bearer abc", token: "abc", ok: true},
		{header: "BEARER abc", token: "abc", ok: true},
		{header: "Bearer", ok: false},
		{header: "Bearer  abc", ok: false},
		{header: "Bearer a b", ok: false},
		{header: "Token abc", ok: false},
		{header: "", ok: false},
	}

	for _, test := range tests {
		token, ok := bearerToken(requestWith(test.header))

		if token != test.token || ok != test.ok {
			t.Fatalf("header %q = (%q, %v), want (%q, %v)", test.header, token, ok, test.token, test.ok)
		}
	}
}
