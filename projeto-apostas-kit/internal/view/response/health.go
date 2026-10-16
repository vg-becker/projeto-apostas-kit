package response

// Health é o corpo dos health checks. Checks só existe no readiness e traz apenas UP ou
// DOWN por dependência, nunca o texto do erro.
type Health struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}
