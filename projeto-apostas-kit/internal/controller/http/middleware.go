package http

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"apostas-go/internal/view/problem"
)

const (
	maxCorrelationLength = 128
	unmatchedRoute       = "unmatched"
)

var errPanic = errors.New("http: handler panicked")

// RequestObserver recebe cada requisição concluída: a rota é o padrão (sem IDs), então o
// conjunto de valores é pequeno e fixo.
type RequestObserver interface {
	ObserveRequest(method string, route string, status int, duration time.Duration)
}

// withMiddleware envolve o roteador. Ordem, de fora para dentro: correlação (todos os
// registros a usam), log de acesso e métricas (enxergam o status final) e recuperação de
// panic (o 500 que ela escreve já passa pelo log de acesso e pelas métricas).
func withMiddleware(
	next http.Handler,
	logger *slog.Logger,
	observer RequestObserver,
) http.Handler {
	return correlation(accessLog(measure(recoverer(next, logger), observer), logger))
}

// measure informa a duração e o status de cada requisição ao observer, se houver.
func measure(next http.Handler, observer RequestObserver) http.Handler {
	if observer == nil {
		return next
	}

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		wrapped := &statusWriter{ResponseWriter: writer, status: http.StatusOK}

		next.ServeHTTP(wrapped, request)

		observer.ObserveRequest(
			request.Method,
			routeOf(request),
			wrapped.status,
			time.Since(started),
		)
	})
}

// correlation garante um X-Correlation-Id válido na requisição e na resposta; o valor
// recebido só é aceito se for ASCII imprimível, sem espaços e com até 128 caracteres.
func correlation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		id := request.Header.Get(correlationHeader)
		if !validCorrelationID(id) {
			id = newCorrelationID()
		}

		request.Header.Set(correlationHeader, id)
		writer.Header().Set(correlationHeader, id)
		next.ServeHTTP(writer, request)
	})
}

func validCorrelationID(id string) bool {
	if id == "" || len(id) > maxCorrelationLength {
		return false
	}

	for index := range len(id) {
		if id[index] <= ' ' || id[index] > '~' {
			return false
		}
	}

	return true
}

func newCorrelationID() string {
	var random [16]byte

	_, _ = rand.Read(random[:])

	return hex.EncodeToString(random[:])
}

// statusWriter registra o status escrito, para o log de acesso.
type statusWriter struct {
	http.ResponseWriter
	status  int
	started bool
}

func (writer *statusWriter) WriteHeader(status int) {
	if !writer.started {
		writer.status = status
		writer.started = true
	}

	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusWriter) Write(payload []byte) (int, error) {
	if !writer.started {
		writer.WriteHeader(http.StatusOK)
	}

	return writer.ResponseWriter.Write(payload)
}

// Unwrap expõe o escritor original ao http.ResponseController.
func (writer *statusWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

// accessLog registra uma linha por requisição: método, rota (o padrão, sem IDs), status,
// duração e correlação. Nunca registra corpo, valores, tokens nem a chave de idempotência.
func accessLog(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		wrapped := &statusWriter{ResponseWriter: writer, status: http.StatusOK}

		next.ServeHTTP(wrapped, request)

		logger.Info(
			"request",
			"method", request.Method,
			"route", routeOf(request),
			"status", wrapped.status,
			"durationMs", time.Since(started).Milliseconds(),
			"correlationId", request.Header.Get(correlationHeader),
		)
	})
}

// isAbort reconhece o panic que o net/http usa de propósito para interromper uma resposta.
func isAbort(recovered any) bool {
	err, isError := recovered.(error)

	return isError && errors.Is(err, http.ErrAbortHandler)
}

func routeOf(request *http.Request) string {
	if request.Pattern == "" {
		return unmatchedRoute
	}

	return request.Pattern
}

// recoverer transforma um panic em 500 genérico e mantém o servidor de pé. Registra o tipo
// e a pilha, nunca o valor do panic, que poderia carregar dados.
func recoverer(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			if isAbort(recovered) {
				panic(recovered)
			}

			logger.Error(
				"panic recovered",
				"type", fmt.Sprintf("%T", recovered),
				"stack", string(debug.Stack()),
				"correlationId", request.Header.Get(correlationHeader),
			)
			writeProblem(writer, problem.From(errPanic))
		}()

		next.ServeHTTP(writer, request)
	})
}
