package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

var (
	// ErrInvalidToken indica um token que não vale: malformado, assinatura inválida,
	// expirado, emissor ou audiência errados.
	ErrInvalidToken = errors.New("auth: invalid token")
	// ErrIdentityProviderUnavailable indica que o token não pôde ser verificado porque as
	// chaves do IdP estão inacessíveis e a necessária não está em cache. Não significa que o
	// token seja inválido.
	ErrIdentityProviderUnavailable = errors.New("auth: identity provider unavailable")
)

// providerIDPattern restringe o providerId que vem do token, pois ele vira chave de dados.
var providerIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Identity é o que um token válido autoriza. A identidade vazia (nem provedor nem
// interno) é um token válido sem permissão alguma.
type Identity struct {
	ProviderID string
	Internal   bool
}

// Verifier valida tokens JWT do IdP: assinatura (RS256, chaves do JWKS), emissor,
// audiência e expiração.
type Verifier struct {
	config    Config
	verifier  *oidc.IDTokenVerifier
	transport *jwksTransport
	logger    *slog.Logger
	now       func() time.Time
}

// NewVerifier cria o verificador. Nenhuma chamada ao IdP é feita aqui: as chaves são
// buscadas na primeira verificação e ficam em cache, com nova busca quando aparece um kid
// desconhecido.
func NewVerifier(config Config, logger *slog.Logger) (*Verifier, error) {
	config = config.withDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	transport := &jwksTransport{base: http.DefaultTransport}
	client := &http.Client{Timeout: config.RequestTimeout, Transport: transport}
	keys := oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), client), config.JWKSURL)

	return &Verifier{
		config:    config,
		transport: transport,
		logger:    logger,
		now:       time.Now,
		verifier: oidc.NewVerifier(config.IssuerURL, keys, &oidc.Config{
			ClientID:             config.Audience,
			SupportedSigningAlgs: []string{oidc.RS256},
		}),
	}, nil
}

// Verify valida o token e devolve o que ele autoriza. Erros: ErrInvalidToken (401) ou
// ErrIdentityProviderUnavailable (503). O motivo detalhado de uma recusa só vai para o
// log, e o token nunca é registrado.
func (verifier *Verifier) Verify(ctx context.Context, token string) (Identity, error) {
	started := verifier.now()

	verified, err := verifier.verifier.Verify(ctx, token)
	if err != nil {
		return Identity{}, verifier.classify(err, started)
	}

	var claims map[string]any
	if err := verified.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("%w: unreadable claims", ErrInvalidToken)
	}

	return verifier.identityOf(claims), nil
}

// classify separa um token inválido (401) de uma falha de infraestrutura (503): se uma
// busca do JWKS falhou durante a verificação, o token não pôde ser julgado.
func (verifier *Verifier) classify(err error, started time.Time) error {
	if verifier.transport.failedSince(started) {
		return fmt.Errorf("%w: %w", ErrIdentityProviderUnavailable, err)
	}

	verifier.logger.Info("token refused", "reason", err.Error())

	return fmt.Errorf("%w: %w", ErrInvalidToken, err)
}

// identityOf converte os claims na identidade. Um token com provider_id e escopo interno ao
// mesmo tempo é ambíguo, e um provider_id fora do formato não é confiável: ambos falham
// fechados, sem nenhuma permissão.
func (verifier *Verifier) identityOf(claims map[string]any) Identity {
	providerID, hasProvider := verifier.providerOf(claims)
	internal := hasScope(claims["scope"], verifier.config.InternalScope)

	switch {
	case hasProvider && internal:
		verifier.logger.Warn("token carries both a provider and the internal scope: no permission")

		return Identity{}
	case hasProvider:
		return Identity{ProviderID: providerID}
	case internal:
		return Identity{Internal: true}
	default:
		return Identity{}
	}
}

func (verifier *Verifier) providerOf(claims map[string]any) (string, bool) {
	value, present := claims[verifier.config.ProviderClaim]
	if !present {
		return "", false
	}

	providerID, isText := value.(string)
	if !isText || !providerIDPattern.MatchString(providerID) {
		verifier.logger.Warn("token carries a malformed provider claim: no permission")

		return "", false
	}

	return providerID, true
}

// hasScope procura o escopo exato entre os separados por espaço do claim scope.
func hasScope(claim any, wanted string) bool {
	text, isText := claim.(string)
	if !isText {
		return false
	}

	for _, scope := range strings.Fields(text) {
		if scope == wanted {
			return true
		}
	}

	return false
}
