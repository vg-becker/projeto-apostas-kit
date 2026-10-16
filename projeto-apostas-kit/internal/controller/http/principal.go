package http

import (
	"errors"
	"net/http"

	"apostas-go/internal/service"
)

var (
	// ErrUnauthenticated indica credencial ausente, inválida ou expirada (401).
	ErrUnauthenticated = errors.New("http: unauthenticated")
	// ErrIdentityProviderUnavailable indica que a credencial não pôde ser verificada porque
	// o IdP está inacessível (503), o que não prova que ela seja inválida.
	ErrIdentityProviderUnavailable = errors.New("http: identity provider unavailable")
)

// Principal é a identidade autenticada: um provedor ou o serviço interno.
type Principal struct {
	ProviderID string
	Internal   bool
}

// Authenticator resolve a identidade de uma requisição. A implementação com JWT (Keycloak,
// JWKS) é da Etapa 5; devolve ErrUnauthenticated quando a credencial não vale.
type Authenticator interface {
	Authenticate(request *http.Request) (Principal, error)
}

func (principal Principal) scope() service.AccessScope {
	return service.AccessScope{ProviderID: principal.ProviderID, Internal: principal.Internal}
}
