//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"apostas-go/internal/testsupport/keycloak"
	"apostas-go/internal/testsupport/localstack"
)

// keycloakServer e broker são compartilhados por todos os testes do pacote: subir cada um
// leva dezenas de segundos, então sobem uma vez só, em paralelo.
var (
	keycloakServer *keycloak.Server
	broker         *localstack.Server
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	var (
		group       sync.WaitGroup
		keycloakErr error
		brokerErr   error
		stopIdP     func()
		stopBroker  func()
	)

	group.Add(2)

	go func() {
		defer group.Done()

		keycloakServer, stopIdP, keycloakErr = keycloak.Start(context.Background())
	}()

	go func() {
		defer group.Done()

		broker, stopBroker, brokerErr = localstack.Start(context.Background())
	}()

	group.Wait()

	defer stopAll(stopIdP, stopBroker)

	if keycloakErr != nil || brokerErr != nil {
		fmt.Fprintln(os.Stderr, "integration: cannot start the environment:", keycloakErr, brokerErr)

		return 1
	}

	return m.Run()
}

func stopAll(stops ...func()) {
	for _, stop := range stops {
		if stop != nil {
			stop()
		}
	}
}
