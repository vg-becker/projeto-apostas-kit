//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"

	"apostas-go/internal/testsupport/keycloak"
)

// keycloakServer é compartilhado por todos os testes do pacote: subir o Keycloak leva
// dezenas de segundos, então ele sobe uma vez só.
var keycloakServer *keycloak.Server

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	server, stop, err := keycloak.Start(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration: cannot start keycloak:", err)

		return 1
	}
	defer stop()

	keycloakServer = server

	return m.Run()
}
