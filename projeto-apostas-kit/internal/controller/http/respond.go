package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"apostas-go/internal/view/problem"
)

const (
	jsonContentType    = "application/json; charset=utf-8"
	problemContentType = "application/problem+json; charset=utf-8"
)

// requestError carrega um problema que nasce na validação de formato do Controller.
type requestError struct {
	problem problem.Problem
}

func (failure requestError) Error() string {
	return failure.problem.Detail
}

func badRequest(detail string) error {
	return requestError{problem: problem.InvalidRequest(detail)}
}

func writeJSON(writer http.ResponseWriter, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		writeProblem(writer, problem.From(err))

		return
	}

	setCommonHeaders(writer)
	writer.Header().Set("Content-Type", jsonContentType)
	writer.WriteHeader(status)
	_, _ = writer.Write(encoded)
}

func writeProblem(writer http.ResponseWriter, failure problem.Problem) {
	encoded, err := json.Marshal(failure.Body())
	if err != nil {
		http.Error(writer, "internal error", http.StatusInternalServerError)

		return
	}

	setCommonHeaders(writer)
	writer.Header().Set("Content-Type", problemContentType)

	if failure.RetryAfterSeconds > 0 {
		writer.Header().Set("Retry-After", strconv.Itoa(failure.RetryAfterSeconds))
	}

	if failure.Status == http.StatusUnauthorized {
		writer.Header().Set("WWW-Authenticate", "Bearer")
	}

	writer.WriteHeader(failure.Status)
	_, _ = writer.Write(encoded)
}

// setCommonHeaders: dados financeiros nunca vão para cache e o tipo não é farejado.
func setCommonHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
}

// fail traduz o erro e responde. Erros 5xx são registrados sem payload financeiro: só
// método, caminho, status e o erro.
func (handler *controller) fail(
	writer http.ResponseWriter,
	request *http.Request,
	err error,
) {
	var invalid requestError
	if errors.As(err, &invalid) {
		writeProblem(writer, invalid.problem)

		return
	}

	failure := problem.From(err)
	if failure.Status >= http.StatusInternalServerError {
		handler.logger.Error(
			"request failed",
			"method", request.Method,
			"path", request.URL.Path,
			"status", failure.Status,
			"error", err,
		)
	}

	writeProblem(writer, failure)
}
