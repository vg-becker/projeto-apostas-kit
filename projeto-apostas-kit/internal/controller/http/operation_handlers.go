package http

import (
	"net/http"

	"apostas-go/internal/service"
	"apostas-go/internal/view/presenter"
	"apostas-go/internal/view/problem"
)

const idempotencyKeyHeader = "Idempotency-Key"

// processOperation recebe uma operação de provedor. O provedor vem da identidade; o corpo
// não pode trazer outro. A chave de idempotência é obrigatória e usada como recebida.
func (handler *controller) processOperation(
	writer http.ResponseWriter,
	request *http.Request,
) {
	providerID, ok := handler.requireProvider(writer, request)
	if !ok {
		return
	}

	command, ok := handler.readOperation(writer, request, providerID)
	if !ok {
		return
	}

	result, err := handler.Operations.Process(request.Context(), command)
	if err != nil {
		handler.fail(writer, request, err)

		return
	}

	respondOperation(writer, result)
}

// readOperation valida o formato da requisição e monta o pedido; em qualquer falha já
// respondeu.
func (handler *controller) readOperation(
	writer http.ResponseWriter,
	request *http.Request,
	providerID string,
) (service.ProcessOperationCommand, bool) {
	idempotencyKey := request.Header.Get(idempotencyKeyHeader)
	if idempotencyKey == "" {
		writeProblem(writer, problem.MissingIdempotencyKey())

		return service.ProcessOperationCommand{}, false
	}

	var body operationRequest
	if failure := decodeJSON(writer, request, &body); failure != nil {
		writeProblem(writer, *failure)

		return service.ProcessOperationCommand{}, false
	}

	if !handler.providerMatches(writer, body.ProviderID, providerID) {
		return service.ProcessOperationCommand{}, false
	}

	command, err := body.command(providerID, idempotencyKey, request.Header.Get(correlationHeader))
	if err != nil {
		handler.fail(writer, request, err)

		return service.ProcessOperationCommand{}, false
	}

	return command, true
}

func respondOperation(writer http.ResponseWriter, result service.OperationResult) {
	status := presenter.OperationStatus(result)
	if status == http.StatusAccepted {
		writer.Header().Set("Location", "/wagering/transactions/"+result.TransactionID.String())
	}

	writeJSON(writer, status, presenter.Operation(result))
}

// providerMatches confere o providerId do corpo com o da identidade: ausente é entrada
// inválida, diferente é 403.
func (handler *controller) providerMatches(
	writer http.ResponseWriter,
	bodyProviderID string,
	authenticatedProviderID string,
) bool {
	if bodyProviderID == "" {
		writeProblem(writer, problem.InvalidRequest("providerId is required."))

		return false
	}

	if bodyProviderID != authenticatedProviderID {
		writeProblem(writer, problem.ProviderMismatch())

		return false
	}

	return true
}
