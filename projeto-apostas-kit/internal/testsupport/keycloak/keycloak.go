//go:build integration

package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	image         = "quay.io/keycloak/keycloak:26.0"
	adminUser     = "admin"
	adminPassword = "admin_test"
	// ProjectRealm é o realm importado de deploy/keycloak/realm-export.json.
	ProjectRealm = "apostas"
)

// Server é um Keycloak em execução.
type Server struct {
	baseURL string
	client  *http.Client
}

// Start sobe o Keycloak com o realm do projeto importado e devolve a função que o remove.
func Start(ctx context.Context) (*Server, func(), error) {
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: containerRequest(),
		Started:          true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start keycloak: %w", err)
	}

	stop := func() { _ = testcontainers.TerminateContainer(container) }

	server, err := serverOf(ctx, container)
	if err != nil {
		stop()

		return nil, nil, err
	}

	return server, stop, nil
}

func containerRequest() testcontainers.ContainerRequest {
	return testcontainers.ContainerRequest{
		Image:        image,
		Cmd:          []string{"start-dev", "--import-realm"},
		ExposedPorts: []string{"8080/tcp", "9000/tcp"},
		Env: map[string]string{
			"KC_BOOTSTRAP_ADMIN_USERNAME": adminUser,
			"KC_BOOTSTRAP_ADMIN_PASSWORD": adminPassword,
			"KC_HEALTH_ENABLED":           "true",
			"KC_HOSTNAME_STRICT":          "false",
		},
		Files: []testcontainers.ContainerFile{{
			HostFilePath:      realmFile(),
			ContainerFilePath: "/opt/keycloak/data/import/realm-export.json",
			FileMode:          0o644,
		}},
		WaitingFor: wait.ForHTTP("/health/ready").
			WithPort("9000/tcp").
			WithStartupTimeout(4 * time.Minute),
	}
}

func serverOf(ctx context.Context, container testcontainers.Container) (*Server, error) {
	host, err := container.Host(ctx)
	if err != nil {
		return nil, fmt.Errorf("keycloak host: %w", err)
	}

	port, err := container.MappedPort(ctx, "8080/tcp")
	if err != nil {
		return nil, fmt.Errorf("keycloak port: %w", err)
	}

	return &Server{
		baseURL: fmt.Sprintf("http://%s:%s", host, port.Port()),
		client:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func realmFile() string {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")

	return filepath.Join(root, "deploy", "keycloak", "realm-export.json")
}

// Issuer devolve o valor que o claim iss dos tokens do realm terá.
func (server *Server) Issuer(realm string) string {
	return server.baseURL + "/realms/" + realm
}

// JWKSURL devolve onde as chaves públicas do realm são publicadas.
func (server *Server) JWKSURL(realm string) string {
	return server.Issuer(realm) + "/protocol/openid-connect/certs"
}

// Token obtém um access token pelo fluxo client_credentials.
func (server *Server) Token(
	ctx context.Context,
	realm string,
	clientID string,
	secret string,
) (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {secret},
	}

	body, err := server.send(ctx, http.MethodPost,
		server.Issuer(realm)+"/protocol/openid-connect/token",
		"application/x-www-form-urlencoded", "", []byte(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("token for %s/%s: %w", realm, clientID, err)
	}

	var decoded struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.AccessToken == "" {
		return "", fmt.Errorf("token for %s/%s: unexpected response", realm, clientID)
	}

	return decoded.AccessToken, nil
}

// SetAccessTokenLifespan altera o tempo de vida dos access tokens do realm, em segundos.
func (server *Server) SetAccessTokenLifespan(ctx context.Context, realm string, seconds int) error {
	return server.admin(ctx, http.MethodPut, "/admin/realms/"+realm, map[string]any{
		"realm":               realm,
		"accessTokenLifespan": seconds,
	})
}

// CreateRealm cria um realm vazio, para provar que tokens de outro emissor são recusados.
func (server *Server) CreateRealm(ctx context.Context, realm string) error {
	return server.admin(ctx, http.MethodPost, "/admin/realms", map[string]any{
		"realm":       realm,
		"enabled":     true,
		"sslRequired": "none",
	})
}

// ClientOptions escolhe os mappers do client de teste: sem nenhum, o token não leva a
// audiência nem o provider_id.
type ClientOptions struct {
	Audience   string
	ProviderID string
}

// CreateServiceClient cria um client com segredo e conta de serviço (client_credentials).
func (server *Server) CreateServiceClient(
	ctx context.Context,
	realm string,
	clientID string,
	secret string,
	options ClientOptions,
) error {
	return server.admin(ctx, http.MethodPost, "/admin/realms/"+realm+"/clients", map[string]any{
		"clientId":                  clientID,
		"enabled":                   true,
		"protocol":                  "openid-connect",
		"publicClient":              false,
		"serviceAccountsEnabled":    true,
		"standardFlowEnabled":       false,
		"directAccessGrantsEnabled": false,
		"clientAuthenticatorType":   "client-secret",
		"secret":                    secret,
		"protocolMappers":           mappers(options),
	})
}

func mappers(options ClientOptions) []map[string]any {
	var result []map[string]any

	if options.Audience != "" {
		result = append(result, audienceMapper(options.Audience))
	}

	if options.ProviderID != "" {
		result = append(result, providerMapper(options.ProviderID))
	}

	return result
}

func audienceMapper(audience string) map[string]any {
	return map[string]any{
		"name":           "audience",
		"protocol":       "openid-connect",
		"protocolMapper": "oidc-audience-mapper",
		"config": map[string]string{
			"included.custom.audience": audience,
			"access.token.claim":       "true",
			"id.token.claim":           "false",
		},
	}
}

func providerMapper(providerID string) map[string]any {
	return map[string]any{
		"name":           "provider-id",
		"protocol":       "openid-connect",
		"protocolMapper": "oidc-hardcoded-claim-mapper",
		"config": map[string]string{
			"claim.name":         "provider_id",
			"claim.value":        providerID,
			"jsonType.label":     "String",
			"access.token.claim": "true",
			"id.token.claim":     "false",
		},
	}
}

func (server *Server) admin(ctx context.Context, method string, path string, payload any) error {
	token, err := server.adminToken(ctx)
	if err != nil {
		return err
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode admin payload: %w", err)
	}

	_, err = server.send(ctx, method, server.baseURL+path, "application/json", token, encoded)
	if err != nil {
		return fmt.Errorf("admin %s %s: %w", method, path, err)
	}

	return nil
}

func (server *Server) adminToken(ctx context.Context) (string, error) {
	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {"admin-cli"},
		"username":   {adminUser},
		"password":   {adminPassword},
	}

	body, err := server.send(ctx, http.MethodPost,
		server.baseURL+"/realms/master/protocol/openid-connect/token",
		"application/x-www-form-urlencoded", "", []byte(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("admin token: %w", err)
	}

	var decoded struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.AccessToken == "" {
		return "", fmt.Errorf("admin token: unexpected response")
	}

	return decoded.AccessToken, nil
}

// send faz a chamada e devolve o corpo; qualquer status fora de 2xx é erro, com o corpo
// recortado para diagnóstico.
func (server *Server) send(
	ctx context.Context,
	method string,
	target string,
	contentType string,
	bearer string,
	payload []byte,
) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	request.Header.Set("Content-Type", contentType)
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}

	response, err := server.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()

	return readSuccess(response)
}

func readSuccess(response *http.Response) ([]byte, error) {
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("status %d: %s", response.StatusCode, summary(body))
	}

	return body, nil
}

func summary(body []byte) string {
	const limit = 300

	text := strings.TrimSpace(string(body))
	if len(text) > limit {
		return text[:limit]
	}

	return text
}
