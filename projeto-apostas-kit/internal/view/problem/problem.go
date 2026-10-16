package problem

import (
	"context"
	"errors"
	"net/http"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
	"apostas-go/internal/model/port"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/model/wallet"
	"apostas-go/internal/service"
	"apostas-go/internal/view/response"
)

const (
	typePrefix          = "urn:apostas:problem:"
	retryAfterSeconds   = 5
	genericInternalText = "An unexpected error occurred."
	genericUnavailable  = "The service is temporarily unavailable. Retry later."
)

// Problem é um erro já traduzido para HTTP.
type Problem struct {
	Status int
	Code   string
	Title  string
	Detail string
	// RetryAfterSeconds é maior que zero só nas indisponibilidades transitórias.
	RetryAfterSeconds int
}

// Body devolve o corpo application/problem+json.
func (problem Problem) Body() response.Problem {
	return response.Problem{
		Type:   typePrefix + problem.Code,
		Title:  problem.Title,
		Status: problem.Status,
		Code:   problem.Code,
		Detail: problem.Detail,
	}
}

type rule struct {
	sentinels []error
	status    int
	code      string
	title     string
}

// rules é percorrida em ordem; a primeira que casar vence.
var rules = []rule{
	{
		sentinels: []error{service.ErrIdempotencyConflict},
		status:    http.StatusConflict,
		code:      "IDEMPOTENCY_CONFLICT",
		title:     "Idempotency key reused with different content",
	},
	{
		sentinels: []error{service.ErrWalletExists},
		status:    http.StatusConflict,
		code:      "WALLET_ALREADY_EXISTS",
		title:     "A wallet already exists for this player and currency",
	},
	{
		sentinels: []error{service.ErrWalletNotFound},
		status:    http.StatusNotFound,
		code:      "WALLET_NOT_FOUND",
		title:     "Wallet not found",
	},
	{
		sentinels: []error{service.ErrTransactionNotFound},
		status:    http.StatusNotFound,
		code:      "TRANSACTION_NOT_FOUND",
		title:     "Transaction not found",
	},
	{
		sentinels: []error{service.ErrAccessDenied},
		status:    http.StatusForbidden,
		code:      "FORBIDDEN",
		title:     "Access denied",
	},
	{
		sentinels: []error{service.ErrWalletPlayerMismatch},
		status:    http.StatusBadRequest,
		code:      "WALLET_PLAYER_MISMATCH",
		title:     "The wallet belongs to another player",
	},
	{
		sentinels: []error{service.ErrCurrencyMismatch, money.ErrCurrencyMismatch},
		status:    http.StatusBadRequest,
		code:      "CURRENCY_MISMATCH",
		title:     "The currency differs from the wallet currency",
	},
	{
		sentinels: []error{wager.ErrOpeningNotAllowed},
		status:    http.StatusBadRequest,
		code:      "KIND_NOT_ALLOWED",
		title:     "This operation kind cannot be sent by providers",
	},
	{
		sentinels: []error{wager.ErrInvalidKind},
		status:    http.StatusBadRequest,
		code:      "INVALID_KIND",
		title:     "Unknown operation kind",
	},
	{
		sentinels: []error{money.ErrUnknownCurrency},
		status:    http.StatusBadRequest,
		code:      "UNKNOWN_CURRENCY",
		title:     "Unknown currency",
	},
	{
		sentinels: []error{
			wager.ErrAmountPolicy,
			money.ErrInvalidFormat,
			money.ErrScaleExceeded,
			money.ErrNegativeNotAllowed,
			money.ErrOverflow,
			money.ErrUninitialized,
		},
		status: http.StatusBadRequest,
		code:   "INVALID_AMOUNT",
		title:  "Invalid amount",
	},
	{
		sentinels: []error{port.ErrInvalidCursor},
		status:    http.StatusBadRequest,
		code:      "INVALID_CURSOR",
		title:     "Invalid pagination cursor",
	},
	{
		sentinels: []error{
			wager.ErrInvalidTransaction,
			wallet.ErrInvalidState,
			identity.ErrInvalidID,
		},
		status: http.StatusBadRequest,
		code:   "INVALID_REQUEST",
		title:  "Invalid request",
	},
}

// From traduz um erro em status HTTP e código estável. Indisponibilidade transitória (ou
// prazo expirado) tem precedência e vira 503 com Retry-After. Em 5xx o detalhe é fixo e
// genérico: o texto do erro nunca vaza.
func From(err error) Problem {
	if isUnavailable(err) {
		return Problem{
			Status:            http.StatusServiceUnavailable,
			Code:              "SERVICE_UNAVAILABLE",
			Title:             "Service temporarily unavailable",
			Detail:            genericUnavailable,
			RetryAfterSeconds: retryAfterSeconds,
		}
	}

	for _, candidate := range rules {
		if matches(err, candidate.sentinels) {
			return Problem{
				Status: candidate.status,
				Code:   candidate.code,
				Title:  candidate.title,
				Detail: err.Error(),
			}
		}
	}

	return Problem{
		Status: http.StatusInternalServerError,
		Code:   "INTERNAL_ERROR",
		Title:  "Internal error",
		Detail: genericInternalText,
	}
}

func isUnavailable(err error) bool {
	return errors.Is(err, port.ErrTransient) || errors.Is(err, context.DeadlineExceeded)
}

func matches(err error, sentinels []error) bool {
	for _, sentinel := range sentinels {
		if errors.Is(err, sentinel) {
			return true
		}
	}

	return false
}

// New cria um problema com código estável próprio; serve aos erros que nascem na borda HTTP.
func New(status int, code string, title string, detail string) Problem {
	return Problem{Status: status, Code: code, Title: title, Detail: detail}
}

// InvalidRequest é uma entrada malformada ou incompleta (400).
func InvalidRequest(detail string) Problem {
	return New(http.StatusBadRequest, "INVALID_REQUEST", "Invalid request", detail)
}

// InvalidJSON é um corpo que não é um JSON aceito pelo contrato (400).
func InvalidJSON(detail string) Problem {
	return New(http.StatusBadRequest, "INVALID_JSON", "The request body is not valid", detail)
}

// MissingIdempotencyKey é a ausência do cabeçalho Idempotency-Key (400).
func MissingIdempotencyKey() Problem {
	return New(
		http.StatusBadRequest,
		"MISSING_IDEMPOTENCY_KEY",
		"The Idempotency-Key header is required",
		"Send the Idempotency-Key header with a unique key for this operation.",
	)
}

// Unauthenticated é a ausência ou invalidez da credencial (401). O detalhe é fixo para não
// ensinar a um atacante por que a credencial foi recusada.
func Unauthenticated() Problem {
	return New(
		http.StatusUnauthorized,
		"UNAUTHENTICATED",
		"Authentication is required",
		"Provide a valid access token.",
	)
}

// ProviderMismatch é um providerId no corpo diferente do da identidade autenticada (403).
func ProviderMismatch() Problem {
	return New(
		http.StatusForbidden,
		"PROVIDER_MISMATCH",
		"The providerId does not match the authenticated identity",
		"The providerId is taken from the access token and must match the request body.",
	)
}

// PayloadTooLarge é um corpo acima do limite (413).
func PayloadTooLarge() Problem {
	return New(
		http.StatusRequestEntityTooLarge,
		"PAYLOAD_TOO_LARGE",
		"The request body is too large",
		"",
	)
}

// UnsupportedMediaType é um Content-Type diferente de application/json (415).
func UnsupportedMediaType() Problem {
	return New(
		http.StatusUnsupportedMediaType,
		"UNSUPPORTED_MEDIA_TYPE",
		"Content-Type must be application/json",
		"",
	)
}

// RouteNotFound é um caminho que não existe (404).
func RouteNotFound() Problem {
	return New(http.StatusNotFound, "ROUTE_NOT_FOUND", "Route not found", "")
}

// MethodNotAllowed é um método que o caminho não aceita (405).
func MethodNotAllowed() Problem {
	return New(
		http.StatusMethodNotAllowed,
		"METHOD_NOT_ALLOWED",
		"Method not allowed for this route",
		"",
	)
}
