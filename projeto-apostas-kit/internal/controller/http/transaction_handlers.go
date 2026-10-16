package http

import (
	"net/http"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/view/presenter"
)

// getTransaction lê uma transação pelo ID interno, dentro do escopo da identidade.
func (handler *controller) getTransaction(writer http.ResponseWriter, request *http.Request) {
	principal, ok := handler.authenticate(writer, request)
	if !ok {
		return
	}

	id, err := identity.ParseTransactionID(request.PathValue("transactionId"))
	if err != nil {
		handler.fail(writer, request, err)

		return
	}

	found, err := handler.Queries.GetTransaction(request.Context(), principal.scope(), id)
	if err != nil {
		handler.fail(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, presenter.Transaction(found))
}

// getProviderTransaction lê uma transação por (provedor, ID externo); o provedor do
// caminho precisa ser o da identidade, salvo para o serviço interno.
func (handler *controller) getProviderTransaction(
	writer http.ResponseWriter,
	request *http.Request,
) {
	principal, ok := handler.authenticate(writer, request)
	if !ok {
		return
	}

	found, err := handler.Queries.GetProviderTransaction(
		request.Context(),
		principal.scope(),
		request.PathValue("providerId"),
		request.PathValue("externalTransactionId"),
	)
	if err != nil {
		handler.fail(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, presenter.Transaction(found))
}
