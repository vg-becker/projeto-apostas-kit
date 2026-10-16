# Projeto: Processamento Distribuído de Apostas em Go

Serviço Go (Uber Fx) que processa operações financeiras de provedores de jogos (BET, WIN, LOSS, REFUND, ROLLBACK) em ambiente distribuído, com API HTTP + consumidor SQS. O enunciado completo está em `docs/requisitos/desafio.md` — é a fonte da verdade.

## Protocolo de segurança (PRIORIDADE MÁXIMA — acima de qualquer outra regra)

Detalhes em `docs/requisitos/protocolo-seguranca.md`. Resumo inegociável:

1. **Pergunte antes de qualquer alteração.** Criar/editar/mover/apagar arquivo, `go mod tidy`, `gofmt -w`, instalar pacote, subir containers, rodar migration, `git add/commit` — tudo exige a PROPOSTA DE ALTERAÇÃO (arquivos, o que muda, por quê, risco, como desfazer) e um "sim" explícito. Um lote por vez.
2. **Nunca desfaça nem reverta nada sem autorização**, nem quando teste ou lint falhar. Pare, mostre o erro, proponha a correção e pergunte.
3. **Proibido** (o hook bloqueia): `rm -rf`, `git reset`, `git clean`, `git checkout/restore` para descartar mudanças, `git stash drop`, `git push --force`, `git rebase`, `docker compose down -v`, `docker system prune`, `DROP/TRUNCATE`, `DELETE` sem `WHERE`, `sudo`.
4. **Aditivo antes de destrutivo**: edite o trecho mínimo; migrations já criadas nunca são editadas (crie uma nova); não renomeie/mova pacotes sem aprovar a lista completa.
5. **Arquivos protegidos** (só o usuário edita): `docs/requisitos/desafio.md`, `.claude/settings.json`, `scripts/safety/`, `.git/`, `.env`. Nunca leia nem imprima `.env`.
6. Backups automáticos ficam em `.claude/backups/`; para desfazer algo use `/desfazer` (um arquivo por vez, com confirmação).
7. Se uma ação for bloqueada pelo hook, não tente contornar: explique ao usuário e peça que ele execute ou autorize.

## Leia antes de codar (4 documentos)

1. `docs/requisitos/desafio.md` — requisitos e critérios eliminatórios.
2. `docs/requisitos/arquitetura-mvc.md` — estrutura de pastas, responsabilidades e regra de dependência.
3. `docs/requisitos/estilo-codigo.md` — estilo de código vertical, com exemplos.
4. `docs/requisitos/protocolo-seguranca.md` — o que pode e o que exige pergunta.

## Arquitetura: MVC + Service layer (obrigatório)

```
Controller ──► Service ──► Model ◄── Repository
    └──► View
```

- **Model** (`internal/model/`): entidades, Money, estados, regras, erros de domínio e interfaces (`port/`). Importa **só a stdlib**.
- **Repository** (`internal/repository/postgres/`): implementa as interfaces do Model com pgx e SQL explícito.
- **Service** (`internal/service/`): casos de uso compartilhados por HTTP e SQS (idempotência, transação, lock, outbox, inbox).
- **Controller** (`internal/controller/{http,sqs,worker}/`): entrada, auth, validação de formato; só chama Service.
- **View** (`internal/view/`): responses JSON, presenters e tradução de erro de domínio em HTTP + `failureCode`.
- **Platform** (`internal/platform/`): config, módulos Fx, logging, métricas, JWT, clientes.
- `cmd/api/main.go` contém somente `fx.New(...).Run()`.

Nunca coloque regra financeira em Controller ou Repository. Nunca importe `repository` a partir de `service` (dependa da interface em `model/port`).

## Estilo: código vertical (obrigatório)

Detalhes e exemplos em `docs/requisitos/estilo-codigo.md`. Resumo:

- Linha ≤ 100 colunas; 3+ argumentos, campos ou parâmetros → **um por linha**, com vírgula final.
- Guard clauses e retorno antecipado; caminho feliz à esquerda; máximo 2 níveis de indentação.
- Funções ~20 linhas, um nível de abstração; **stepdown**: quem chama fica acima de quem é chamado.
- Erro tratado logo após a chamada; nomes sem abreviação; comentário só explica o porquê.
- SQL em `const` raw string, uma cláusula por linha; encadeamentos um elo por linha.
- Testes table-driven.
- `gofmt` e `golangci-lint run` (config em `.golangci.yml`) sem avisos antes de concluir qualquer arquivo.

## Como consultar o conhecimento (RAG local) — OBRIGATÓRIO

Antes de escrever código que use Fx, pgx, SQS, Keycloak/OIDC, migrations, Postgres (locks/constraints/triggers), testcontainers ou a spec do Go, **consulte o RAG** em vez de confiar na memória:

```bash
python3 scripts/rag/search.py "fx.Lifecycle OnStop"            # busca geral
python3 scripts/rag/search.py "FOR UPDATE SKIP LOCKED" -d pg-locking
python3 scripts/rag/search.py "visibility timeout redrive" -k 8
python3 scripts/rag/search.py "<requisito>" -d desafio          # reler o enunciado
python3 scripts/rag/search.py "guard clauses" -d estilo-codigo  # reler o estilo
```

Documentos disponíveis (use em `-d`): `desafio`, `arquitetura-mvc`, `estilo-codigo`, `protocolo-seguranca`, `go-spec`, `go-effective`, `go-erros`, `go-database`, `go-modules`, `go-code-review-comments`, `google-go-style`, `uber-go-style`, `fx-guia`, `fx-api`, `fx-fxtest-api`, `dig-api`, `pgx-api`, `pgxpool-api`, `pgconn-api`, `golang-migrate`, `pg-locking`, `pg-isolation`, `pg-insert-onconflict`, `pg-constraints`, `pg-triggers`, `pg-createtrigger`, `pg-numeric-types`, `sqs-fifo`, `aws-sdk-go-v2-sqs`, `localstack-sqs`, `keycloak-*`, `go-oidc`, `oauth2-client-credentials`, `testcontainers-go`, `prometheus-client-go`, `go-slog`, `go-net-http`, `go-testing`, `padrao-transactional-outbox`, `padrao-idempotent-consumer`.

Buscas de docs de bibliotecas funcionam melhor em **inglês**. Se o índice estiver vazio/ausente: `python3 scripts/rag/fetch_docs.py && python3 scripts/rag/build_index.py`.

## Decisões fixas (não renegociar sem pedir)

- Go (versão em `go.mod` e `Dockerfile` iguais), módulos com `go.sum` versionado.
- **Uber Fx** para toda a composição: um `fx.Module` por camada (model não participa), `fx.Provide` com construtores, `fx.Invoke` só para registrar rotas/workers, `fx.Lifecycle` (OnStart não bloqueia; goroutines com context cancelável; OnStop espera o término do worker com prazo). Ordem de shutdown: parar entradas (HTTP/SQS) → esperar em andamento → fechar pool do banco por último.
- Acesso a banco: **pgx v5 (pgxpool) com SQL explícito**. `TxManager` injeta a tx no `context`; repositórios a recuperam de lá.
- **Money**: `int64` em centavos + moeda ISO 4217; parsing por string (nunca float); trata overflow; persistido em `BIGINT`. Contrato externo: `{"amount":"25.00","currency":"BRL"}`.
- Concorrência por carteira: `SELECT ... FOR UPDATE` na linha da wallet + `CHECK (balance >= 0)` e versão. Sem lock global.
- Idempotência persistente: `UNIQUE(provider_id, idempotency_key)` e `UNIQUE(provider_id, external_transaction_id)`; hash SHA-256 de JSON canônico (chaves ordenadas), idêntico em HTTP e SQS.
- Ledger append-only: trigger `BEFORE UPDATE OR DELETE` que lança exceção; `UNIQUE(wallet_id, transaction_id)`.
- Outbox transacional + publisher separado com `FOR UPDATE SKIP LOCKED`, backoff, recuperação de lease abandonado; `eventId` estável.
- Inbox `UNIQUE(consumer_name, message_id)` na mesma tx do domínio; mensagem SQS só é apagada após commit.
- Auth: Keycloak + `client_credentials`; validar JWT (issuer, audience, exp, assinatura via JWKS) com `go-oidc`; `providerId` vem do token, nunca do corpo; operações de carteira só para o serviço interno (role/scope).
- Criação ≠ reidratação. Erros de domínio tipados (`errors.Is/As`), sem `panic`.
- Logs JSON (`log/slog`) sem payload financeiro completo; métricas Prometheus; `/health/live` e `/health/ready`.

## Regras de trabalho

1. Trabalhe em etapas pequenas, na ordem de `docs/PLANO.md`; ao fim de cada etapa rode `gofmt -l .`, `go vet ./...`, `golangci-lint run` e `go test ./...`.
2. Escreva o teste junto do código (unitário no Model; integração com containers reais para banco/SQS/IdP — nada de mock de infra inteira).
3. Rode `go test -race ./...` antes de concluir qualquer etapa de concorrência.
4. Nunca use `float32/float64` em nada monetário. Nunca publique evento antes do commit.
5. Ao tomar uma decisão ou adotar uma interpretação do enunciado, anote em `ARCHITECTURE.md` (incluindo limitações e o que não foi concluído).
6. Se o enunciado for ambíguo, consulte `-d desafio`, escolha a interpretação mais conservadora financeiramente e documente.
7. Não invente APIs: confirme assinatura de Fx/pgx/SDK no RAG (`fx-api`, `pgx-api`, `aws-sdk-go-v2-sqs`) antes de usar.
8. Antes de criar um arquivo novo, confirme em qual camada MVC ele mora (`arquitetura-mvc`). Em dúvida, pergunte.
9. Não faça commit sem eu pedir. Nada de alteração sem perguntar (ver Protocolo de segurança).

## Comandos de verificação

```bash
gofmt -l .            # deve sair vazio
go vet ./...
golangci-lint run
go test ./...
go test -race ./...
docker compose up --build
```
