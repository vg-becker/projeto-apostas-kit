package http

import (
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"apostas-go/internal/model/port"
	"apostas-go/internal/service"
	"apostas-go/internal/view/problem"
)

// Dependencies reúne o que o Controller HTTP precisa.
type Dependencies struct {
	Authenticator Authenticator
	Wallets       *service.WalletService
	Operations    *service.OperationService
	Queries       *service.QueryService
	// Readiness são as dependências que o /health/ready verifica; vazio significa pronto.
	Readiness []port.HealthCheck
	// ReadinessTimeout é o prazo de cada verificação; zero usa 2 segundos.
	ReadinessTimeout time.Duration
	// Logger é opcional; sem ele, nada é registrado.
	Logger *slog.Logger
}

type controller struct {
	Dependencies
	logger *slog.Logger
}

// methods despacha por método dentro de um caminho; um método ausente vira 405 em
// problem+json, com o cabeçalho Allow.
type methods map[string]http.HandlerFunc

func (routes methods) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler, found := routes[request.Method]; found {
		handler(writer, request)

		return
	}

	allowed := make([]string, 0, len(routes))
	for method := range routes {
		allowed = append(allowed, method)
	}

	sort.Strings(allowed)
	writer.Header().Set("Allow", strings.Join(allowed, ", "))
	writeProblem(writer, problem.MethodNotAllowed())
}

// NewRouter monta as rotas de negócio. Rotas inexistentes e métodos errados respondem
// problem+json, como todos os erros.
func NewRouter(dependencies Dependencies) http.Handler {
	handler := &controller{Dependencies: dependencies, logger: dependencies.Logger}
	if handler.logger == nil {
		handler.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	mux := http.NewServeMux()
	mux.Handle("/wallets", methods{http.MethodPost: handler.openWallet})
	mux.Handle("/wallets/{walletId}", methods{http.MethodGet: handler.getWallet})
	mux.Handle("/wallets/{walletId}/ledger", methods{http.MethodGet: handler.listLedger})
	mux.Handle(
		"/wallets/{walletId}/reconciliation",
		methods{http.MethodPost: handler.reconcileWallet},
	)
	mux.Handle("/wagering/transactions", methods{http.MethodPost: handler.processOperation})
	mux.Handle(
		"/wagering/transactions/{transactionId}",
		methods{http.MethodGet: handler.getTransaction},
	)
	mux.Handle(
		"/providers/{providerId}/wagering/transactions/{externalTransactionId}",
		methods{http.MethodGet: handler.getProviderTransaction},
	)
	mux.Handle("/health/live", methods{http.MethodGet: handler.live})
	mux.Handle("/health/ready", methods{http.MethodGet: handler.ready})
	mux.HandleFunc("/", func(writer http.ResponseWriter, _ *http.Request) {
		writeProblem(writer, problem.RouteNotFound())
	})

	return withMiddleware(mux, handler.logger)
}

// authenticate resolve a identidade; sem ela, responde 401 e devolve false.
func (handler *controller) authenticate(
	writer http.ResponseWriter,
	request *http.Request,
) (Principal, bool) {
	principal, err := handler.Authenticator.Authenticate(request)
	if err != nil {
		writeProblem(writer, problem.Unauthenticated())

		return Principal{}, false
	}

	return principal, true
}

// requireInternal exige o serviço interno; qualquer outra identidade recebe 403.
func (handler *controller) requireInternal(
	writer http.ResponseWriter,
	request *http.Request,
) bool {
	principal, authenticated := handler.authenticate(writer, request)
	if !authenticated {
		return false
	}

	if !principal.Internal {
		writeProblem(writer, problem.From(service.ErrAccessDenied))

		return false
	}

	return true
}

// requireProvider exige um provedor autenticado e devolve o seu ID.
func (handler *controller) requireProvider(
	writer http.ResponseWriter,
	request *http.Request,
) (string, bool) {
	principal, authenticated := handler.authenticate(writer, request)
	if !authenticated {
		return "", false
	}

	if principal.ProviderID == "" || principal.Internal {
		writeProblem(writer, problem.From(service.ErrAccessDenied))

		return "", false
	}

	return principal.ProviderID, true
}
