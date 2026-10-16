//go:build integration

package localstack

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"apostas-go/internal/platform/sqsclient"
)

const (
	image   = "localstack/localstack:3.8"
	account = "000000000000"
)

// Região e credenciais fictícias do LocalStack.
const (
	Region          = "us-east-1"
	AccessKeyID     = "test"
	SecretAccessKey = "test"
)

// Valores do contrato de provisionamento (deploy/localstack/init/ready.d/01-sqs.sh).
const (
	MaxReceiveCount   = 5
	VisibilityTimeout = 60
)

// Nomes das filas provisionadas pelo script.
const (
	RequestsQueue   = "wager-transactions.fifo"
	DeadLetterQueue = "wager-transactions-dlq.fifo"
	EventsQueue     = "wager-events.fifo"
)

// Server é um LocalStack em execução, com as filas do script provisionadas.
type Server struct {
	Endpoint string
}

// Start sobe o LocalStack, espera o script de provisionamento terminar e devolve a função
// que remove o container.
func Start(ctx context.Context) (*Server, func(), error) {
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: containerRequest(),
		Started:          true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start localstack: %w", err)
	}

	stop := func() { _ = testcontainers.TerminateContainer(container) }

	server, err := serverOf(ctx, container)
	if err != nil {
		stop()

		return nil, nil, err
	}

	if err := server.waitForProvisioning(ctx); err != nil {
		stop()

		return nil, nil, err
	}

	return server, stop, nil
}

func containerRequest() testcontainers.ContainerRequest {
	return testcontainers.ContainerRequest{
		Image:        image,
		ExposedPorts: []string{"4566/tcp"},
		Env:          map[string]string{"SERVICES": "sqs"},
		Files: []testcontainers.ContainerFile{{
			HostFilePath:      initScript(),
			ContainerFilePath: "/etc/localstack/init/ready.d/01-sqs.sh",
			FileMode:          0o755,
		}},
		WaitingFor: wait.ForHTTP("/_localstack/health").
			WithPort("4566/tcp").
			WithStartupTimeout(3 * time.Minute),
	}
}

func serverOf(ctx context.Context, container testcontainers.Container) (*Server, error) {
	host, err := container.Host(ctx)
	if err != nil {
		return nil, fmt.Errorf("localstack host: %w", err)
	}

	port, err := container.MappedPort(ctx, "4566/tcp")
	if err != nil {
		return nil, fmt.Errorf("localstack port: %w", err)
	}

	return &Server{Endpoint: fmt.Sprintf("http://%s:%s", host, port.Port())}, nil
}

func initScript() string {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")

	return filepath.Join(root, "deploy", "localstack", "init", "ready.d", "01-sqs.sh")
}

// Config devolve a configuração do cliente SQS para este servidor.
func (server *Server) Config() sqsclient.Config {
	return sqsclient.Config{
		Region:          Region,
		Endpoint:        server.Endpoint,
		AccessKeyID:     AccessKeyID,
		SecretAccessKey: SecretAccessKey,
		RequestTimeout:  10 * time.Second,
	}
}

// QueueURL monta a URL de uma fila neste servidor (estilo caminho).
func (server *Server) QueueURL(name string) string {
	return fmt.Sprintf("%s/%s/%s", server.Endpoint, account, name)
}

// Provisioned devolve as filas criadas pelo script do projeto.
func (server *Server) Provisioned() sqsclient.Queues {
	return sqsclient.Queues{
		Requests:   server.QueueURL(RequestsQueue),
		DeadLetter: server.QueueURL(DeadLetterQueue),
		Events:     server.QueueURL(EventsQueue),
	}
}

// waitForProvisioning espera as três filas do script existirem.
func (server *Server) waitForProvisioning(ctx context.Context) error {
	client, err := sqsclient.New(ctx, server.Config())
	if err != nil {
		return err
	}

	deadline := time.Now().Add(2 * time.Minute)
	queues := server.Provisioned()

	for time.Now().Before(deadline) {
		if server.allExist(ctx, client, queues) {
			return nil
		}

		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("localstack: the provisioning script did not create the queues in time")
}

func (server *Server) allExist(
	ctx context.Context,
	client *sqsclient.Client,
	queues sqsclient.Queues,
) bool {
	for _, url := range []string{queues.Requests, queues.DeadLetter, queues.Events} {
		if _, err := client.QueueAttributes(ctx, url); err != nil {
			return false
		}
	}

	return true
}

// Isolated cria um conjunto de filas só deste teste, com os mesmos atributos do script
// (FIFO, visibilidade de 60 s, redrive depois de 5 recebimentos), para que os testes não
// interfiram entre si. As URLs são montadas a partir do endpoint mapeado: as que o LocalStack
// devolve apontam para a porta interna do container.
func (server *Server) Isolated(t testing.TB, client *sqsclient.Client) sqsclient.Queues {
	t.Helper()

	var random [4]byte

	_, _ = rand.Read(random[:])
	prefix := "t" + hex.EncodeToString(random[:]) + "-"

	deadLetter := server.createQueue(t, client, prefix+DeadLetterQueue, nil)

	attributes, err := client.QueueAttributes(t.Context(), deadLetter)
	if err != nil {
		t.Fatalf("dlq attributes: %v", err)
	}

	requests := server.createQueue(t, client, prefix+RequestsQueue, map[string]string{
		"VisibilityTimeout": fmt.Sprint(VisibilityTimeout),
		"RedrivePolicy": fmt.Sprintf(
			`{"deadLetterTargetArn":%q,"maxReceiveCount":"%d"}`,
			attributes["QueueArn"],
			MaxReceiveCount,
		),
	})

	events := server.createQueue(t, client, prefix+EventsQueue, nil)

	return sqsclient.Queues{Requests: requests, DeadLetter: deadLetter, Events: events}
}

func (server *Server) createQueue(
	t testing.TB,
	client *sqsclient.Client,
	name string,
	extra map[string]string,
) string {
	t.Helper()

	if _, err := client.CreateFIFOQueue(t.Context(), name, extra); err != nil {
		t.Fatalf("create queue %s: %v", name, err)
	}

	return server.QueueURL(name)
}
