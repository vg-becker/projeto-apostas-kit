package response

// Money é o valor monetário do contrato externo: sempre texto decimal com duas casas.
type Money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}
