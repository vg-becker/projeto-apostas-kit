package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	httpcontroller "apostas-go/internal/controller/http"
)

// maxTokenLength recusa de imediato cabeçalhos absurdos antes de qualquer parsing.
const maxTokenLength = 8 << 10

var _ httpcontroller.Authenticator = (*Authenticator)(nil)

// Authenticator implementa a interface do Controller HTTP com JWT do IdP. Ele depende dos
// tipos do Controller; o Controller não conhece este pacote.
type Authenticator struct {
	verifier *Verifier
	logger   *slog.Logger
}

// New cria o autenticador a partir da configuração.
func New(config Config, logger *slog.Logger) (*Authenticator, error) {
	verifier, err := NewVerifier(config, logger)
	if err != nil {
		return nil, err
	}

	return &Authenticator{verifier: verifier, logger: logger}, nil
}

// Authenticate resolve a identidade da requisição. Sem token ou com token que não vale,
// devolve ErrUnauthenticated; com o IdP inacessível, ErrIdentityProviderUnavailable; com um
// token válido sem permissão, um Principal vazio, que o Controller recusa com 403.
func (authenticator *Authenticator) Authenticate(
	request *http.Request,
) (httpcontroller.Principal, error) {
	token, ok := bearerToken(request)
	if !ok {
		return httpcontroller.Principal{}, httpcontroller.ErrUnauthenticated
	}

	identity, err := authenticator.verifier.Verify(request.Context(), token)

	switch {
	case errors.Is(err, ErrIdentityProviderUnavailable):
		authenticator.logger.Warn("cannot verify token: identity provider unavailable")

		return httpcontroller.Principal{}, httpcontroller.ErrIdentityProviderUnavailable
	case err != nil:
		return httpcontroller.Principal{}, httpcontroller.ErrUnauthenticated
	default:
		return httpcontroller.Principal{
			ProviderID: identity.ProviderID,
			Internal:   identity.Internal,
		}, nil
	}
}

// bearerToken extrai o token do cabeçalho Authorization: o esquema Bearer (sem diferenciar
// maiúsculas), um único token, sem espaços e dentro do limite de tamanho.
func bearerToken(request *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(request.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}

	if token == "" || len(token) > maxTokenLength || strings.ContainsAny(token, " \t") {
		return "", false
	}

	return token, true
}
