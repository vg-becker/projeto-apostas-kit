package http

import (
	"net/http"
	"strconv"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/port"
	"apostas-go/internal/view/presenter"
)

const (
	minLedgerLimit = 1
	maxLedgerLimit = 200
)

const correlationHeader = "X-Correlation-Id"

func (handler *controller) openWallet(writer http.ResponseWriter, request *http.Request) {
	if !handler.requireInternal(writer, request) {
		return
	}

	var body openWalletRequest
	if failure := decodeJSON(writer, request, &body); failure != nil {
		writeProblem(writer, *failure)

		return
	}

	command, err := body.command(request.Header.Get(correlationHeader))
	if err != nil {
		handler.fail(writer, request, err)

		return
	}

	opened, err := handler.Wallets.OpenWallet(request.Context(), command)
	if err != nil {
		handler.fail(writer, request, err)

		return
	}

	writer.Header().Set("Location", "/wallets/"+opened.ID().String())
	writeJSON(writer, http.StatusCreated, presenter.Wallet(opened))
}

func (handler *controller) getWallet(writer http.ResponseWriter, request *http.Request) {
	walletID, ok := handler.internalWalletID(writer, request)
	if !ok {
		return
	}

	found, err := handler.Wallets.GetWallet(request.Context(), walletID)
	if err != nil {
		handler.fail(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, presenter.Wallet(found))
}

func (handler *controller) listLedger(writer http.ResponseWriter, request *http.Request) {
	walletID, ok := handler.internalWalletID(writer, request)
	if !ok {
		return
	}

	limit, err := ledgerLimit(request)
	if err != nil {
		handler.fail(writer, request, err)

		return
	}

	page, err := handler.Queries.ListLedger(request.Context(), port.LedgerQuery{
		WalletID: walletID,
		Cursor:   request.URL.Query().Get("cursor"),
		Limit:    limit,
	})
	if err != nil {
		handler.fail(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, presenter.LedgerPage(page))
}

func (handler *controller) reconcileWallet(writer http.ResponseWriter, request *http.Request) {
	walletID, ok := handler.internalWalletID(writer, request)
	if !ok {
		return
	}

	result, err := handler.Wallets.ReconcileWallet(request.Context(), walletID)
	if err != nil {
		handler.fail(writer, request, err)

		return
	}

	writeJSON(writer, http.StatusOK, presenter.Reconciliation(result))
}

// internalWalletID exige o serviço interno e lê o walletId do caminho; em qualquer falha já
// respondeu.
func (handler *controller) internalWalletID(
	writer http.ResponseWriter,
	request *http.Request,
) (identity.WalletID, bool) {
	if !handler.requireInternal(writer, request) {
		return "", false
	}

	walletID, err := identity.ParseWalletID(request.PathValue("walletId"))
	if err != nil {
		handler.fail(writer, request, err)

		return "", false
	}

	return walletID, true
}

// ledgerLimit lê o parâmetro limit; ausente vale o padrão do repositório, e fora de 1 a 200
// é erro explícito, nunca ajuste silencioso.
func ledgerLimit(request *http.Request) (int, error) {
	text := request.URL.Query().Get("limit")
	if text == "" {
		return 0, nil
	}

	limit, err := strconv.Atoi(text)
	if err != nil || limit < minLedgerLimit || limit > maxLedgerLimit {
		return 0, badRequest("limit must be an integer between 1 and 200.")
	}

	return limit, nil
}
