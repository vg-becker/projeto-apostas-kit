package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func newTestVerifier(t *testing.T, idp *fakeIdP) (*Verifier, *bytes.Buffer) {
	t.Helper()

	var logs bytes.Buffer

	verifier, err := NewVerifier(idp.config(), slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("NewVerifier error = %v", err)
	}

	return verifier, &logs
}

func TestValidTokensYieldTheirIdentity(t *testing.T) {
	idp := newFakeIdP(t, "k1")
	verifier, _ := newTestVerifier(t, idp)

	tests := []struct {
		name   string
		claims map[string]any
		want   Identity
	}{
		{name: "provider", claims: providerClaims(), want: Identity{ProviderID: "provider-a"}},
		{name: "internal service", claims: internalClaims(), want: Identity{Internal: true}},
		{
			name:   "internal scope among others",
			claims: withScope(internalClaims(), "email wallet:internal profile"),
			want:   Identity{Internal: true},
		},
		{
			name:   "audience among several",
			claims: withAudience(providerClaims(), "other-api", testAudience),
			want:   Identity{ProviderID: "provider-a"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := verifier.Verify(context.Background(), idp.mint(t, "k1", test.claims))

			if err != nil || got != test.want {
				t.Fatalf("identity = %+v (%v), want %+v", got, err, test.want)
			}
		})
	}
}

func withScope(claims map[string]any, scope string) map[string]any {
	claims["scope"] = scope

	return claims
}

func withAudience(claims map[string]any, audience ...string) map[string]any {
	claims["aud"] = audience

	return claims
}

func TestInvalidTokensAreRejected(t *testing.T) {
	idp := newFakeIdP(t, "k1")
	verifier, _ := newTestVerifier(t, idp)

	expired := providerClaims()
	expired["exp"] = time.Now().Add(-time.Minute).Unix()
	otherIssuer := providerClaims()
	otherIssuer["iss"] = "https://evil.example.test/realms/apostas"
	otherAudience := withAudience(providerClaims(), "another-api")
	noAudience := providerClaims()
	delete(noAudience, "aud")
	noExpiry := providerClaims()
	delete(noExpiry, "exp")

	tests := []struct {
		name  string
		token string
	}{
		{name: "expired", token: idp.mint(t, "k1", expired)},
		{name: "without expiry", token: idp.mint(t, "k1", noExpiry)},
		{name: "wrong issuer", token: idp.mint(t, "k1", otherIssuer)},
		{name: "wrong audience", token: idp.mint(t, "k1", otherAudience)},
		{name: "no audience", token: idp.mint(t, "k1", noAudience)},
		{name: "signed by a foreign key", token: foreignToken(t, "k1", providerClaims())},
		{name: "unsigned (alg none)", token: unsignedToken(t, providerClaims())},
		{name: "hmac confusion attack", token: idp.hmacConfusedToken(t, "k1", providerClaims())},
		{name: "empty", token: ""},
		{name: "garbage", token: "not-a-jwt"},
		{name: "two segments", token: "aaa.bbb"},
		{name: "tampered payload", token: tamper(idp.mint(t, "k1", providerClaims()))},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := verifier.Verify(context.Background(), test.token)

			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidToken)
			}
		})
	}
}

// tamper troca o payload por outro, mantendo a assinatura original.
func tamper(token string) string {
	segments := strings.Split(token, ".")
	segments[1] = "eyJwcm92aWRlcl9pZCI6InByb3ZpZGVyLWIifQ"

	return strings.Join(segments, ".")
}

func TestValidTokensWithoutPermissionYieldAnEmptyIdentity(t *testing.T) {
	idp := newFakeIdP(t, "k1")
	verifier, logs := newTestVerifier(t, idp)

	noRole := providerClaims()
	delete(noRole, "provider_id")
	both := withScope(providerClaims(), "wallet:internal")
	badProvider := providerClaims()
	badProvider["provider_id"] = "provider a; DROP"
	numericProvider := providerClaims()
	numericProvider["provider_id"] = 42
	lookalike := withScope(internalClaims(), "wallet:internal-admin walletXinternal")
	emptyProvider := providerClaims()
	emptyProvider["provider_id"] = ""

	tests := []struct {
		name   string
		claims map[string]any
	}{
		{name: "no provider and no scope", claims: noRole},
		{name: "provider and internal scope together", claims: both},
		{name: "provider id outside the allowed format", claims: badProvider},
		{name: "provider id that is not text", claims: numericProvider},
		{name: "empty provider id", claims: emptyProvider},
		{name: "look-alike scopes", claims: lookalike},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := verifier.Verify(context.Background(), idp.mint(t, "k1", test.claims))

			if err != nil || got != (Identity{}) {
				t.Fatalf("identity = %+v (%v), want an empty identity", got, err)
			}
		})
	}

	if !strings.Contains(logs.String(), "no permission") {
		t.Fatalf("the ambiguous token must be logged:\n%s", logs.String())
	}
}

func TestKeyRotationIsPickedUpAndOldKeysKeepWorkingWhilePublished(t *testing.T) {
	idp := newFakeIdP(t, "k1")
	verifier, _ := newTestVerifier(t, idp)
	ctx := context.Background()

	if _, err := verifier.Verify(ctx, idp.mint(t, "k1", providerClaims())); err != nil {
		t.Fatalf("k1 before rotation: %v", err)
	}

	idp.addKey(t, "k2")

	if _, err := verifier.Verify(ctx, idp.mint(t, "k2", providerClaims())); err != nil {
		t.Fatalf("k2 after rotation must be fetched on demand: %v", err)
	}

	if _, err := verifier.Verify(ctx, idp.mint(t, "k1", providerClaims())); err != nil {
		t.Fatalf("k1 is still published and must keep working: %v", err)
	}
}

func TestAnUnknownKeyIDIsRejectedAfterAFreshFetch(t *testing.T) {
	idp := newFakeIdP(t, "k1")
	verifier, _ := newTestVerifier(t, idp)

	_, err := verifier.Verify(context.Background(), foreignToken(t, "ghost", providerClaims()))

	if !errors.Is(err, ErrInvalidToken) || idp.fetches.Load() == 0 {
		t.Fatalf("error = %v, fetches = %d, want a rejection after trying the JWKS", err, idp.fetches.Load())
	}
}

func TestWithoutCachedKeysAnUnreachableIdPIsUnavailableNotInvalid(t *testing.T) {
	idp := newFakeIdP(t, "k1")
	idp.down.Store(true)
	verifier, _ := newTestVerifier(t, idp)

	_, err := verifier.Verify(context.Background(), idp.mint(t, "k1", providerClaims()))

	if !errors.Is(err, ErrIdentityProviderUnavailable) || errors.Is(err, ErrInvalidToken) {
		t.Fatalf("error = %v, want %v", err, ErrIdentityProviderUnavailable)
	}
}

func TestAJWKSEndpointThatAnswersWithAnErrorIsUnavailable(t *testing.T) {
	idp := newFakeIdP(t, "k1")
	idp.status.Store(404)
	verifier, _ := newTestVerifier(t, idp)

	_, err := verifier.Verify(context.Background(), idp.mint(t, "k1", providerClaims()))

	if !errors.Is(err, ErrIdentityProviderUnavailable) {
		t.Fatalf("error = %v, want %v", err, ErrIdentityProviderUnavailable)
	}
}

func TestCachedKeysKeepValidatingWhileTheIdPIsDown(t *testing.T) {
	idp := newFakeIdP(t, "k1")
	verifier, _ := newTestVerifier(t, idp)
	ctx := context.Background()

	if _, err := verifier.Verify(ctx, idp.mint(t, "k1", providerClaims())); err != nil {
		t.Fatalf("warm-up: %v", err)
	}

	idp.down.Store(true)

	if _, err := verifier.Verify(ctx, idp.mint(t, "k1", providerClaims())); err != nil {
		t.Fatalf("a cached key must keep working during an outage: %v", err)
	}

	idp.addKey(t, "k2")
	if _, err := verifier.Verify(ctx, idp.mint(t, "k2", providerClaims())); !errors.Is(err, ErrIdentityProviderUnavailable) {
		t.Fatalf("an unknown key during an outage = %v, want %v", err, ErrIdentityProviderUnavailable)
	}
}

func TestAMalformedTokenIsInvalidEvenWhileTheIdPIsDown(t *testing.T) {
	idp := newFakeIdP(t, "k1")
	idp.down.Store(true)
	verifier, _ := newTestVerifier(t, idp)

	_, err := verifier.Verify(context.Background(), "garbage")

	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("error = %v, a token that cannot even be parsed is a plain 401", err)
	}
}

func TestNeitherTheTokenNorItsClaimsAreLogged(t *testing.T) {
	idp := newFakeIdP(t, "k1")
	verifier, logs := newTestVerifier(t, idp)

	expired := providerClaims()
	expired["exp"] = time.Now().Add(-time.Hour).Unix()
	expired["provider_id"] = "provider-secret-name"
	token := idp.mint(t, "k1", expired)

	if _, err := verifier.Verify(context.Background(), token); err == nil {
		t.Fatal("an expired token must be refused")
	}

	if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "provider-secret-name") {
		t.Fatalf("the log must not contain the token or its claims:\n%s", logs.String())
	}
}
