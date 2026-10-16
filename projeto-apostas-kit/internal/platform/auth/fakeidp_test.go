package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	testIssuer   = "https://idp.example.test/realms/apostas"
	testAudience = "apostas-api"
)

// fakeIdP serve um JWKS por HTTP e assina tokens, para exercitar cada classe de rejeição de
// forma determinística. Os testes com o Keycloak real ficam no Lote 5B.
type fakeIdP struct {
	server  *httptest.Server
	mutex   sync.Mutex
	keys    map[string]*rsa.PrivateKey
	down    atomic.Bool
	status  atomic.Int32
	fetches atomic.Int32
}

func newFakeIdP(t *testing.T, kids ...string) *fakeIdP {
	t.Helper()

	idp := &fakeIdP{keys: map[string]*rsa.PrivateKey{}}
	for _, kid := range kids {
		idp.addKey(t, kid)
	}

	idp.server = httptest.NewServer(http.HandlerFunc(idp.serveJWKS))
	t.Cleanup(idp.server.Close)

	return idp
}

func (idp *fakeIdP) addKey(t *testing.T, kid string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	idp.mutex.Lock()
	defer idp.mutex.Unlock()

	idp.keys[kid] = key
}

func (idp *fakeIdP) serveJWKS(writer http.ResponseWriter, _ *http.Request) {
	idp.fetches.Add(1)

	if status := int(idp.status.Load()); status != 0 {
		writer.WriteHeader(status)

		return
	}

	if idp.down.Load() {
		writer.WriteHeader(http.StatusServiceUnavailable)

		return
	}

	idp.mutex.Lock()
	defer idp.mutex.Unlock()

	var set jose.JSONWebKeySet
	for kid, key := range idp.keys {
		set.Keys = append(set.Keys, jose.JSONWebKey{
			Key:       &key.PublicKey,
			KeyID:     kid,
			Algorithm: string(jose.RS256),
			Use:       "sig",
		})
	}

	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(set)
}

func (idp *fakeIdP) config() Config {
	return Config{
		IssuerURL:      testIssuer,
		JWKSURL:        idp.server.URL + "/jwks",
		Audience:       testAudience,
		RequestTimeout: 2 * time.Second,
	}
}

// claims devolve os claims de um token válido de provedor; cada teste altera o que precisa.
func providerClaims() map[string]any {
	now := time.Now()

	return map[string]any{
		"iss":         testIssuer,
		"aud":         []string{testAudience},
		"sub":         "service-account-provider-a",
		"iat":         now.Unix(),
		"exp":         now.Add(5 * time.Minute).Unix(),
		"scope":       "profile",
		"provider_id": "provider-a",
	}
}

func internalClaims() map[string]any {
	claims := providerClaims()
	delete(claims, "provider_id")
	claims["scope"] = "profile wallet:internal"

	return claims
}

func (idp *fakeIdP) mint(t *testing.T, kid string, claims map[string]any) string {
	t.Helper()

	idp.mutex.Lock()
	key := idp.keys[kid]
	idp.mutex.Unlock()

	return signWith(t, key, kid, claims)
}

func signWith(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: kid}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	return token
}

// foreignKey assina com uma chave que o IdP nunca publicou.
func foreignToken(t *testing.T, kid string, claims map[string]any) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	return signWith(t, key, kid, claims)
}

// unsignedToken é um JWT com alg none, que nunca pode ser aceito.
func unsignedToken(t *testing.T, claims map[string]any) string {
	t.Helper()

	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}

	encode := base64.RawURLEncoding.EncodeToString

	return encode(header) + "." + encode(payload) + "."
}

// hmacConfusedToken assina com HS256 usando a chave pública do IdP como segredo, o ataque
// clássico de confusão de algoritmo.
func (idp *fakeIdP) hmacConfusedToken(t *testing.T, kid string, claims map[string]any) string {
	t.Helper()

	idp.mutex.Lock()
	key := idp.keys[kid]
	idp.mutex.Unlock()

	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}

	secret := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: jose.JSONWebKey{Key: secret, KeyID: kid}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("new hmac signer: %v", err)
	}

	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("hmac sign: %v", err)
	}

	return token
}
