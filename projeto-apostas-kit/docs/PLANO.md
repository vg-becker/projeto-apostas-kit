# Plano de execução (ordem sugerida para terminar hoje)

Cada etapa = um pedido ao Claude CLI. Peça `/etapa N` e valide com os comandos do CLAUDE.md. Toda etapa segue o MVC de `docs/requisitos/arquitetura-mvc.md` e o estilo de `docs/requisitos/estilo-codigo.md`.

## Etapa 0 — Esqueleto (≈20 min)
`go mod init`, árvore de pastas MVC vazia (com `doc.go` por pacote), `Dockerfile` (multi-stage, mesma versão do go.mod), `docker-compose.yml` com Postgres, LocalStack, Keycloak (realm importado), `.env.example`, `Makefile`.

## Etapa 1 — Model (≈60 min) · `internal/model/`
`money`, `wallet`, `ledger`, `wager` (máquina de estados e failure codes), `event` (envelope + 4 tipos), `port` (interfaces). Testes unitários completos: parsing, escala, overflow, moedas incompatíveis, política de zero por tipo, transições, reidratação sem efeitos. Só stdlib.

## Etapa 2 — Repository + migrations (≈45 min) · `internal/repository/postgres/`, `migrations/`
Migrations versionadas (up/down): wallets, wager_transactions (interna vs externa via CHECK), wallet_ledger_entries (trigger append-only + UNIQUE), inbox, outbox, índices de pendências. Repositórios pgx + `TxManager`.

## Etapa 3 — Service (≈75 min) · `internal/service/`
`OpenWallet`, `OperationService.Process` (idempotência, hash canônico, lock por carteira, BET/WIN/LOSS/REFUND/ROLLBACK, códigos de falha), reconciliação, resolver de referências, publisher da outbox.

## Etapa 4 — View + Controller HTTP (≈60 min) · `internal/view/`, `internal/controller/http/`
Responses, presenters, `problem` (400/409/422/202/503 distinguíveis), handlers, ledger paginado por cursor, health.

## Etapa 5 — Autenticação (≈40 min) · `internal/platform/auth/`, `deploy/keycloak/`
Realm Keycloak (clients `provider-a`, `provider-b`, `internal-service`), middleware JWT via JWKS, `providerId` do token, escopo interno para carteiras, testes 401/403 e isolamento entre provedores.

## Etapa 6 — Controllers SQS e workers (≈70 min) · `internal/controller/sqs/`, `.../worker/`
Consumidor SQS FIFO (inbox na mesma tx, delete pós-commit, backoff, DLQ, shutdown gracioso), loop do publisher da outbox, loop de referências pendentes (backoff exponencial + TTL), provisionamento das filas e do destino de eventos.

## Etapa 7 — Fx + observabilidade (≈30 min) · `internal/platform/`
Módulos Fx por camada, lifecycle dos workers, `fxtest` verificando start/stop, slog JSON, métricas Prometheus.

## Etapa 8 — Testes de integração e concorrência (≈90 min) · `test/integration/`
testcontainers: 50 envios paralelos → 1 débito; duas apostas de 80 sobre 100; carteiras distintas em paralelo; 3 instâncias; crash pós-commit/pré-delete; dois publishers; reversão antes da referência; reinício; reconciliação final. `go test -race`.

## Etapa 9 — Documentação (≈30 min)
`README.md` (pré-requisitos, env, filas, migrations up/down, exemplos curl autenticados, testes) e `ARCHITECTURE.md` (todas as decisões do §15 do enunciado, o mapeamento MVC + Service e limitações).

> Prioridade de pontos se o tempo apertar: integridade financeira (20) = concorrência (20) > idempotência (15) = mensageria (15). Itens eliminatórios: `search.py -d desafio "eliminatórios"`.
