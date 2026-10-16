package auth

import (
	"net/http"
	"sync/atomic"
	"time"
)

// jwksTransport observa as buscas do JWKS. O go-oidc faz essas buscas em segundo plano,
// fora do contexto de quem verifica, então a única forma segura de saber se uma falha de
// verificação veio da infraestrutura é registrar quando a última busca falhou.
type jwksTransport struct {
	base           http.RoundTripper
	lastFailureAt  atomic.Int64
	failureCounter atomic.Int64
}

func (transport *jwksTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil || response.StatusCode != http.StatusOK {
		transport.lastFailureAt.Store(time.Now().UnixNano())
		transport.failureCounter.Add(1)
	}

	return response, err
}

// failedSince informa se alguma busca do JWKS falhou depois de instant.
func (transport *jwksTransport) failedSince(instant time.Time) bool {
	return transport.lastFailureAt.Load() >= instant.UnixNano()
}
