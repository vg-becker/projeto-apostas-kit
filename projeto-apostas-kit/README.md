# Processamento distribuído de apostas

Serviço em Go (Uber Fx) que processa as operações financeiras dos provedores de jogos
(`BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`) por **API HTTP** e por **fila SQS FIFO**, com as
mesmas garantias nos dois caminhos: idempotência persistente, saldo sem atualização perdida,
ledger imutável, inbox e outbox transacionais.

- As decisões técnicas, o contrato HTTP e as limitações estão em
  [`ARCHITECTURE.md`](ARCHITECTURE.md). Leia a seção 14 (limitações) antes de colocar o serviço
  em qualquer ambiente.
- O enunciado está em `docs/requisitos/desafio.md`.

## Pré-requisitos

| Para | Precisa de |
|---|---|
| subir o ambiente | Docker e Docker Compose v2 |
| rodar os testes e o serviço fora do container | Go 1.27.2 (a mesma versão do `go.mod` e do `Dockerfile`) |
| os exemplos deste README | `curl` e `jq` |

## Início rápido

```bash
cp .env.example .env            # valores locais de exemplo, sem segredos reais
docker compose up --build       # Postgres, LocalStack (SQS), Keycloak e o serviço
```

Quando o serviço estiver no ar:

```bash
curl -s localhost:8081/health/ready
# {"status":"UP","checks":{"postgres":"UP","sqs":"UP"}}
```

O Compose sobe quatro containers: `postgres` (17), `localstack` (3.8, só SQS), `keycloak` (26,
com o realm `apostas` importado) e `app`. O `app` só inicia depois que os outros três ficam
saudáveis, e **aplica as migrations ao iniciar** (`APP_MIGRATE_ON_START=true` no Compose).

Se uma porta já estiver em uso na sua máquina, mude-a sem editar arquivos, por exemplo
`POSTGRES_PORT=5433 docker compose up --build` (o serviço fala com o Postgres pela rede interna do
Compose, então a porta publicada só importa para quem acessa de fora). O Keycloak usa
`KEYCLOAK_PORT` (padrão 8080) e o serviço publica a 8081.

Para parar: `docker compose down`. Para **apagar também os dados locais** (volume do Postgres):
`docker compose down -v`.

## Variáveis de ambiente

Lidas e validadas na partida. Se algo estiver errado, o serviço lista **todos** os problemas de
uma vez (citando só o nome da variável, nunca o valor) e não sobe. O `.env.example` traz os
valores locais.

| Variável | Obrigatória | Padrão | Uso |
|---|---|---|---|
| `APP_HTTP_ADDR` | não | `:8081` | endereço do servidor HTTP |
| `APP_SHUTDOWN_TIMEOUT` | não | `30s` | prazo do desligamento (deve ser maior que 10 s, a espera longa do consumidor) |
| `APP_LOG_LEVEL` | não | `info` | `debug`, `info`, `warn`, `error` |
| `APP_MIGRATE_ON_START` | não | `false` | aplica as migrations pendentes ao iniciar |
| `APP_MIGRATIONS_DIR` | não | `migrations` | diretório das migrations (`/migrations` na imagem) |
| `DATABASE_URL` | sim | | `postgres://usuario:senha@host:5432/banco?sslmode=disable` |
| `AWS_REGION` | sim | | região do SQS |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | não | cadeia padrão da AWS | credenciais estáticas (só LocalStack) |
| `SQS_ENDPOINT` | não | | endpoint do SQS; só para LocalStack |
| `SQS_QUEUE_URL` | sim | | fila de operações `wager-transactions.fifo` |
| `SQS_DLQ_URL` | sim | | fila morta `wager-transactions-dlq.fifo` |
| `SQS_EVENTS_QUEUE_URL` | sim | | fila de eventos de saída `wager-events.fifo` |
| `OIDC_ISSUER_URL` | sim | | valor exigido no claim `iss` dos tokens |
| `OIDC_JWKS_URL` | sim | | de onde vêm as chaves públicas (pode ser um endereço interno) |
| `OIDC_AUDIENCE` | sim | | valor exigido no claim `aud` (`apostas-api`) |
| `OIDC_INTERNAL_SCOPE` | não | `wallet:internal` | escopo que identifica o serviço interno |

As credenciais do `.env.example` e do realm (`*-secret-local`, `apostas_local`, `test`) são
**fictícias e valem só para o ambiente local**. Nunca as use fora dele.

## Filas SQS

Quem cria as filas é o script [`deploy/localstack/init/ready.d/01-sqs.sh`](deploy/localstack/init/ready.d/01-sqs.sh),
que o LocalStack executa sozinho quando fica pronto (o Compose o monta no container). Os testes de
integração usam este mesmo script.

| Fila | Papel |
|---|---|
| `wager-transactions.fifo` | operações dos provedores; redrive para a DLQ depois de 5 recebimentos; visibilidade de 60 s |
| `wager-transactions-dlq.fifo` | mensagens que não podem ser processadas, com o atributo `failureReason` |
| `wager-events.fifo` | eventos de saída (`WagerTransactionProcessed`, `WagerTransactionRejected`, `WalletBalanceChanged`, `WagerTransactionPendingReference`) |

Para listar as filas e enviar uma mensagem (exemplo completo mais abaixo):

```bash
docker compose exec localstack awslocal sqs list-queues
```

Como operar a DLQ (apagar cada mensagem ao recolhê-la, o significado de cada `failureReason`) está
na seção 12 do `ARCHITECTURE.md`.

## Migrations

As migrations ficam em [`migrations/`](migrations) (`NNNN_nome.up.sql` e `.down.sql`) e **nunca são
editadas depois de criadas**: uma mudança vira uma migration nova.

**Aplicação automática.** Com `APP_MIGRATE_ON_START=true` o serviço aplica as pendentes antes de
aceitar tráfego. Há um lock consultivo no Postgres, então várias instâncias subindo juntas
aplicam cada migration uma só vez. Fora do ambiente local deixe a variável desligada e aplique as
migrations como parte do deploy.

**Aplicação e reversão manuais**, com a CLI oficial `migrate` (sem instalar nada além do Docker;
o Compose cria a rede `projeto-apostas-kit_default`, e o nome do projeto é o do diretório):

```bash
DB='postgres://apostas:apostas_local@postgres:5432/apostas?sslmode=disable'
MIGRATE="docker run --rm --network projeto-apostas-kit_default -v $PWD/migrations:/migrations:ro \
  migrate/migrate -path=/migrations -database $DB"

$MIGRATE version        # versão atual (6 com todas aplicadas)
$MIGRATE up             # aplica todas as pendentes
$MIGRATE down 1         # reverte a última migration
$MIGRATE goto 3         # vai para uma versão específica
```

Reverter uma migration **apaga os dados** das estruturas que ela criou: faça backup antes. Um
banco marcado como `dirty` (migration que falhou no meio) recusa novas migrations até alguém
conferir o estado real e usar `$MIGRATE force <versão>`.

## Executar o serviço fora do container

Útil para depurar. Suba só a infraestrutura e rode o binário com as variáveis do `.env.example`:

```bash
docker compose up -d postgres localstack keycloak
set -a; . ./.env.example; set +a
go run ./cmd/api
```

O `SIGTERM` (ou `Ctrl+C`) desliga o serviço com ordem: servidor HTTP e consumidor SQS param de
aceitar trabalho e terminam o que está em andamento, depois os workers, e por último o pool do
banco.

## Exemplos de chamadas

Todas as rotas de negócio exigem um token do Keycloak (`client_credentials`). Os clientes do realm
local:

| Cliente | Segredo local | Pode |
|---|---|---|
| `internal-service` | `internal-service-secret-local` | abrir e consultar carteiras, ledger e reconciliação; ler qualquer transação |
| `provider-a` | `provider-a-secret-local` | enviar operações e consultar **só as suas** transações |
| `provider-b` | `provider-b-secret-local` | idem, para o outro provedor |

O `providerId` vem do **token**, nunca do corpo: um provedor que mande o `providerId` de outro
recebe 403.

```bash
token() {   # token <cliente> <segredo>
  curl -s -X POST http://localhost:8080/realms/apostas/protocol/openid-connect/token \
    -d grant_type=client_credentials -d client_id="$1" -d client_secret="$2" | jq -r .access_token
}
INTERNAL=$(token internal-service internal-service-secret-local)
PROVIDER=$(token provider-a provider-a-secret-local)
```

**1. Abrir uma carteira** (serviço interno):

```bash
PLAYER=$(uuidgen | tr A-Z a-z)
WALLET=$(curl -s -X POST localhost:8081/wallets \
  -H "Authorization: Bearer $INTERNAL" -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$PLAYER\",\"initialBalance\":{\"amount\":\"1000.00\",\"currency\":\"BRL\"}}" | jq -r .id)
```

**2. Enviar uma aposta** (provedor). O cabeçalho `Idempotency-Key` é obrigatório; o dinheiro é
sempre uma **string** decimal, nunca um número. O identificador externo e a chave são únicos por
provedor, para sempre: por isso o exemplo gera um novo a cada execução.

```bash
TX="tx-$(date +%s)"
bet() {
  curl -s -X POST localhost:8081/wagering/transactions \
    -H "Authorization: Bearer $PROVIDER" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: provider-a:$TX" -H 'X-Correlation-Id: demo-1' \
    -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$TX\",\"playerId\":\"$PLAYER\",
         \"walletId\":\"$WALLET\",\"roundId\":\"round-1\",\"gameId\":\"game-1\",\"kind\":\"BET\",
         \"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}"
}
bet   # {"transactionId":"...","status":"PROCESSED","balance":{"amount":"975.00",...},"idempotentReplay":false}
bet   # o mesmo pedido de novo: a mesma resposta, com "idempotentReplay":true e nenhum novo débito
```

Reutilizar a mesma `Idempotency-Key` com **outro conteúdo** devolve `409 IDEMPOTENCY_CONFLICT`.
Uma aposta sem saldo devolve `422` com o código de falha `INSUFFICIENT_FUNDS`. Um `REFUND` ou
`ROLLBACK` cuja aposta de referência ainda não chegou devolve `202` e fica `PENDING_REFERENCE`
até um worker resolvê-lo. Os status, códigos e o formato `problem+json` estão na seção 16 do
`ARCHITECTURE.md`.

**3. Consultar** (serviço interno):

```bash
curl -s localhost:8081/wallets/$WALLET                  -H "Authorization: Bearer $INTERNAL"
curl -s "localhost:8081/wallets/$WALLET/ledger"         -H "Authorization: Bearer $INTERNAL"
curl -s -X POST localhost:8081/wallets/$WALLET/reconciliation -H "Authorization: Bearer $INTERNAL"
# o provedor consulta a sua transação pelo ID externo:
curl -s localhost:8081/providers/provider-a/wagering/transactions/$TX -H "Authorization: Bearer $PROVIDER"
```

A reconciliação recalcula o saldo a partir do ledger e o compara com o saldo armazenado
(`"consistent": true` quando batem).

**4. Enviar a mesma operação pela fila.** O `MessageGroupId` é o `walletId` (serializa as
operações da carteira) e o `idempotencyKey` vai dentro da mensagem:

```bash
docker compose exec -T localstack awslocal sqs send-message \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id "$WALLET" --message-deduplication-id "demo-$TX" \
  --message-body "{\"messageId\":\"msg-$TX-sqs\",\"type\":\"WagerTransactionRequested\",
    \"occurredAt\":\"2026-09-08T12:00:00.000Z\",
    \"data\":{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$TX-sqs\",
      \"idempotencyKey\":\"provider-a:$TX-sqs\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",
      \"roundId\":\"round-1\",\"gameId\":\"game-1\",\"kind\":\"BET\",
      \"money\":{\"amount\":\"10.00\",\"currency\":\"BRL\"}}}"
```

A mesma operação enviada por HTTP e por SQS é aplicada **uma só vez**.

**5. Saúde e métricas** (sem autenticação):

```bash
curl -s localhost:8081/health/live
curl -s localhost:8081/health/ready      # verifica o Postgres e as filas do SQS
curl -s localhost:8081/metrics           # formato de texto do Prometheus
```

As métricas e o formato dos logs estão descritos na seção 11 do `ARCHITECTURE.md`. Os logs saem em
JSON, uma linha por registro, com `correlationId`, `messageId`, `transactionId`, `walletId` e
`providerId`, e nunca levam valores, saldos, tokens nem a chave de idempotência.

## Testes

```bash
gofmt -l .                  # deve sair vazio
go vet ./...
golangci-lint run           # configuração em .golangci.yml
go test -race ./...         # unitários
```

Os **testes de integração** ficam atrás da build tag `integration` e exigem Docker: sobem
PostgreSQL 17, Keycloak 26 e LocalStack 3.8 reais com testcontainers (nada de infraestrutura
simulada). Na primeira execução baixam as imagens (cerca de 1 GB do LocalStack e 500 MB do
Keycloak).

```bash
go vet -tags=integration ./...
go test -tags=integration -race ./...
```

O pacote `test/integration` compila o `cmd/api` com `-race` e sobe **três instâncias
independentes** sobre o mesmo banco e as mesmas filas. Os cenários (50 requisições idênticas,
duas apostas de 80,00 sobre 100,00, carteiras em paralelo, `SIGKILL`, estorno antes da aposta,
publishers mortos, troca de todas as instâncias, reinício no meio do tráfego) estão descritos na
seção 17 do `ARCHITECTURE.md`. Alguns cenários esperam o tempo de visibilidade da fila e levam
mais de um minuto; a execução completa do pacote leva alguns minutos.

Para rodar um teste só:

```bash
go test -tags=integration -race -count=1 -v -run TestFiftyIdenticalRequestsAcrossThree ./test/integration
```

## Estrutura

```
cmd/api/                 main: só fx.New(...).Run()
internal/model/          domínio: Money, carteira, ledger, estados, eventos, portas (só stdlib)
internal/repository/     Postgres (pgx), migrations, inbox e outbox
internal/service/        casos de uso, compartilhados por HTTP e SQS
internal/controller/     http, sqs (consumidor), worker, wire (validação compartilhada)
internal/view/           respostas JSON e tradução de erro em HTTP + failureCode
internal/platform/       config, logging, métricas, autenticação, cliente SQS, módulos Fx
migrations/              SQL versionado (up e down)
deploy/                  realm do Keycloak e script das filas
test/integration/        testes com containers reais e três instâncias
```

A regra de dependência (Controller → Service → Model ← Repository) e o estilo de código estão em
`docs/requisitos/arquitetura-mvc.md` e `docs/requisitos/estilo-codigo.md`.

## O que não está coberto

Está registrado, com o motivo, na seção 14 do `ARCHITECTURE.md`. Em resumo: não há teste de carga;
a expiração de uma referência por TTL é provada nos testes de Service, mas não com instâncias
reais; `/metrics` é público no servidor de negócio; não há limite de requisições por provedor; e
o controle de acesso à fila SQS depende do IAM da AWS (o LocalStack guarda a política, mas não a
aplica).
