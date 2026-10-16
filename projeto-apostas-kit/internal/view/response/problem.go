package response

// Problem é o corpo de erro (application/problem+json). Code é estável e serve para o
// cliente decidir o que fazer; Detail é legível e nunca expõe detalhes internos em 5xx.
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}
