package http

import (
	"errors"
	"net/http"

	"apostas-go/internal/service"
)

// ErrUnauthenticated indica credencial ausente, inválida ou expirada.
var ErrUnauthenticated = errors.New("http: unauthenticated")

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
