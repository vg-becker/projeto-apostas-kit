package sqsclient

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"

	"apostas-go/internal/model/port"
)

var (
	// ErrQueueNotFound indica que a fila não existe: erro permanente de configuração.
	ErrQueueNotFound = errors.New("sqsclient: queue does not exist")
	// ErrRejected indica que o broker recusou a requisição (parâmetro inválido, acesso
	// negado, mensagem grande demais): repetir não adianta.
	ErrRejected = errors.New("sqsclient: request rejected by the broker")
)

var throttlingCodes = map[string]bool{
	"ThrottlingException":                    true,
	"RequestThrottled":                       true,
	"Throttling":                             true,
	"RequestLimitExceeded":                   true,
	"ServiceUnavailable":                     true,
	"AWS.SimpleQueueService.TooManyRequests": true,
}

var missingQueueCodes = map[string]bool{
	"AWS.SimpleQueueService.NonExistentQueue": true,
	"QueueDoesNotExist":                       true,
}

// translateError classifica um erro do SDK: transitório (rede, 5xx, throttling, prazo) vira
// port.ErrTransient; fila inexistente e recusas do broker são permanentes.
func translateError(err error) error {
	if err == nil {
		return nil
	}

	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		return translateAPIError(apiError, err)
	}

	if isNetworkFailure(err) {
		return fmt.Errorf("%w: %w", port.ErrTransient, err)
	}

	return err
}

func translateAPIError(apiError smithy.APIError, err error) error {
	code := apiError.ErrorCode()

	switch {
	case missingQueueCodes[code]:
		return fmt.Errorf("%w: %w", ErrQueueNotFound, err)
	case throttlingCodes[code] || isServerError(err):
		return fmt.Errorf("%w: %w", port.ErrTransient, err)
	default:
		return fmt.Errorf("%w: %w", ErrRejected, err)
	}
}

func isServerError(err error) bool {
	var response *awshttp.ResponseError

	return errors.As(err, &response) && response.HTTPStatusCode() >= 500
}

func isNetworkFailure(err error) bool {
	var networkError net.Error

	return errors.As(err, &networkError) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		strings.Contains(err.Error(), "connection refused") ||
		strings.Contains(err.Error(), "context deadline exceeded")
}
