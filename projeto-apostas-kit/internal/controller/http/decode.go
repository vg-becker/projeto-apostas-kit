package http

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"apostas-go/internal/view/problem"
)

const maxBodyBytes = 64 << 10

// decodeJSON lê o corpo como um único JSON estrito: tipo de mídia application/json, no
// máximo 64 KiB, sem campos desconhecidos e sem dados depois do JSON. Devolve o problema a
// responder, ou nil se o corpo foi lido.
func decodeJSON(
	writer http.ResponseWriter,
	request *http.Request,
	target any,
) *problem.Problem {
	if !isJSON(request) {
		failure := problem.UnsupportedMediaType()

		return &failure
	}

	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(target); err != nil {
		return classifyDecodeError(err)
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return classifyTrailing(err)
	}

	return nil
}

func isJSON(request *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))

	return err == nil && mediaType == "application/json"
}

func classifyDecodeError(err error) *problem.Problem {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		failure := problem.PayloadTooLarge()

		return &failure
	}

	if errors.Is(err, io.EOF) {
		failure := problem.InvalidJSON("The request body is empty.")

		return &failure
	}

	failure := problem.InvalidJSON(err.Error())

	return &failure
}

func classifyTrailing(err error) *problem.Problem {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		failure := problem.PayloadTooLarge()

		return &failure
	}

	failure := problem.InvalidJSON("Unexpected data after the JSON body.")

	return &failure
}
