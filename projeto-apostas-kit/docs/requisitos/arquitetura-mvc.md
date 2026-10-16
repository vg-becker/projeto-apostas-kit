# Arquitetura MVC do projeto

MVC aplicado a um serviço sem tela: a **View** é a camada que transforma o resultado em resposta (JSON, códigos HTTP, corpo de erro). Como HTTP e SQS precisam compartilhar o mesmo caso de uso e as mesmas garantias de idempotência (enunciado §10), o MVC leva uma camada de **Service** entre Controller e Model. É o desenho conhecido como "MVC + Service layer".

## Responsabilidades

| Camada | Pasta | Faz | Não faz |
|---|---|---|---|
| **Model** | `internal/model/` | Entidades, value objects (Money), máquina de estados, regras financeiras, erros de domínio, interfaces dos repositórios | Importar Fx, net/http, SQS, pgx, JSON de transporte |
| **Repository** | `internal/repository/postgres/` | Implementa as interfaces do Model com SQL explícito (pgx), locks e transações | Regra de negócio |
| **Service** | `internal/service/` | Orquestra casos de uso: idempotência, transação, lock da carteira, outbox, inbox | Conhecer HTTP ou SQS |
| **Controller** | `internal/controller/http`, `.../sqs`, `.../worker` | Recebe a entrada, valida formato, autentica/autoriza, chama o Service | Regra financeira, SQL |
| **View** | `internal/view/` | Monta o corpo da resposta, traduz erro de domínio em HTTP status + `failureCode` | Decidir regra |
| **Platform** | `internal/platform/` | Config, módulos Fx, logging, métricas, middleware JWT, clientes SQS/Keycloak | Regra de negócio |

## Regra de dependência (nunca violar)

```
Controller ──► Service ──► Model ◄── Repository
    │                        ▲
    └──► View ───────────────┘   (View lê o Model, nunca o contrário)
```

- `model` não importa nenhuma outra pasta do projeto.
- `service` depende só de `model` (interfaces) — nunca de `repository` concreto.
- `repository` depende de `model` e de pgx.
- `controller` depende de `service` e `view`.
- Fx liga tudo em `internal/platform/fxmodules`; é o único lugar que conhece as implementações concretas.

## Árvore

```
cmd/
  api/main.go                       # só fx.New(...).Run()
internal/
  model/
    money/                          # Money (int64 centavos + moeda)
    wallet/                         # Wallet (agregado), versão, débito/crédito
    wager/                          # WagerTransaction, estados, kinds, failure codes
    ledger/                         # WalletLedgerEntry imutável
    event/                          # envelope + 4 eventos tipados
    port/                           # interfaces: WalletRepository, TransactionRepository,
                                    #   LedgerRepository, InboxRepository, OutboxRepository,
                                    #   TxManager, Clock, IDGenerator
  repository/
    postgres/                       # implementações pgx + TxManager
  service/
    wallet_service.go               # OpenWallet, GetWallet, Reconcile
    operation_service.go            # ProcessOperation (BET/WIN/LOSS/REFUND/ROLLBACK)
    reference_resolver_service.go   # retomada de PENDING_REFERENCE
    outbox_publisher_service.go     # claim + publish + ack
  controller/
    http/                           # handlers + middleware de auth + router
    sqs/                            # consumer (inbox, delete pós-commit, backoff, DLQ)
    worker/                         # outbox publisher loop, reference loop (lifecycle)
  view/
    response/                       # structs JSON de saída
    presenter/                      # model -> response
    problem/                        # erro de domínio -> HTTP status + failureCode
  platform/
    config/  fxmodules/  logging/  metrics/  auth/  sqsclient/
migrations/                         # NNNN_nome.up.sql / .down.sql
deploy/                             # keycloak/realm-export.json, localstack/init
test/integration/                   # testcontainers (Postgres, LocalStack, Keycloak reais)
```

## Fluxo de uma requisição (POST /wagering/transactions)

1. `controller/http` autentica o JWT, extrai `providerId` do token, lê `Idempotency-Key` e o corpo.
2. Converte para um comando (`service.ProcessOperationCommand`) e chama o Service.
3. `service` calcula o hash canônico, abre a transação via `TxManager`, bloqueia a carteira, aplica a regra do `model`, grava ledger e outbox, confirma.
4. `view/presenter` transforma o resultado em `response`; `view/problem` traduz erros.
5. `controller/http` escreve status + corpo.

O consumidor SQS faz os passos 1–5 trocando o JWT pela inbox e a resposta HTTP pelo delete da mensagem — o `service` é o mesmo.

## Onde cada exigência do enunciado mora

- Money, estados, invariantes, reidratação sem efeito → `model`.
- Locks, UNIQUE, trigger append-only, outbox `SKIP LOCKED` → `repository/postgres` + `migrations`.
- Idempotência, hash canônico, reversões, referências pendentes → `service`.
- Auth, SIGTERM, delete pós-commit, workers → `controller` + `platform` (Fx lifecycle).
- Códigos HTTP e `failureCode` estáveis → `view/problem`.
