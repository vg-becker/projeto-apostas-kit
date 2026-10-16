//go:build integration

package sqs_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"apostas-go/internal/testsupport/localstack"
)

// broker é compartilhado por todos os testes do pacote: subir o LocalStack leva dezenas de
// segundos, então ele sobe uma vez só; cada teste usa filas próprias.
var broker *localstack.Server

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	server, stop, err := localstack.Start(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "sqs: cannot start localstack:", err)

		return 1
	}
	defer stop()

	broker = server

	return m.Run()
}
