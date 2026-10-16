# Kit RAG + Claude CLI — Projeto de Apostas em Go (Fx)

## 1) Instalar no projeto (5 minutos)

```bash
mkdir apostas && cd apostas          # ou a pasta do seu projeto
unzip ~/Downloads/projeto-apostas-kit.zip -d .
# se o zip criou uma subpasta, mova o conteúdo para a raiz:
# mv projeto-apostas-kit/* projeto-apostas-kit/.claude . && rmdir projeto-apostas-kit

git init
python3 scripts/rag/fetch_docs.py      # baixa Go spec, Fx, pgx, Postgres, SQS, Keycloak... (~3-6 min)
python3 scripts/rag/build_index.py     # cria docs/rag/index.db
python3 scripts/rag/search.py "fx.Lifecycle OnStop"   # teste: deve retornar trechos
```

Requisitos: `python3` (3.9+, sem pip) e internet na hora do `fetch_docs.py`.
Se uma fonte falhar (site fora do ar), rode só ela: `python3 scripts/rag/fetch_docs.py fx-guia`.

## 2) Abrir o Claude CLI

```bash
claude
```

O Claude Code lê automaticamente o `CLAUDE.md` da raiz (regras do projeto + instrução de usar o RAG) e `.claude/settings.json` (libera as buscas sem pedir permissão).

Primeiro prompt sugerido:

> Leia CLAUDE.md e docs/PLANO.md. Confirme em 10 linhas o que vai construir e quais itens são eliminatórios (use o RAG, -d desafio). Depois execute a Etapa 0.

Depois, etapa por etapa:

```
/etapa 1
/etapa 2
...
```

Consulta rápida ao RAG sem codar: `/rag como encerrar workers no fx.Lifecycle?`

## 3) O que tem no kit

| Caminho | Função |
|---|---|
| `CLAUDE.md` | Memória do projeto: decisões fixas, estrutura, regras, como usar o RAG |
| `docs/requisitos/desafio.md` | Seu REDMI/enunciado (indexado no RAG como `desafio`) |
| `docs/PLANO.md` | Plano em 9 etapas com tempos estimados |
| `scripts/rag/sources.json` | Lista de documentações (edite para adicionar/remover) |
| `scripts/rag/fetch_docs.py` | Baixa as docs → `docs/rag/raw/` |
| `scripts/rag/build_index.py` | Indexa tudo em SQLite FTS5 (`docs/rag/index.db`) |
| `scripts/rag/search.py` | Busca (usada pelo Claude via Bash) |
| `docs/rag/extra/` | Suas notas próprias (entram no índice) |
| `.claude/commands/` | Comandos `/rag` e `/etapa` |
| `.claude/settings.json` | Permissões para buscas, go build/test/vet |

## 4) Como o RAG funciona (e por quê é assim)

Busca lexical BM25 (SQLite FTS5), sem embeddings: roda offline, sem dependências, sem chave de API, e é muito boa para documentação técnica cheia de nomes exatos (`fx.Lifecycle`, `FOR UPDATE SKIP LOCKED`). O `CLAUDE.md` manda o Claude consultar antes de codar. Se mais tarde quiser busca semântica, dá para trocar só o `search.py`/`build_index.py` por um índice vetorial sem mexer no resto.

> Observação: este RAG é uma **base de consulta para o Claude CLI** — o Claude continua sendo o modelo; não é treinar/criar um LLM novo. É a abordagem certa para ter precisão com docs atualizadas.

## 5) Documentações usadas e sugestões extras

Já incluídas em `sources.json`: Go spec (`go.dev/ref/spec`), memory model, Effective Go, erros (1.13), `database/sql`, Go modules, Uber Go Style Guide; **Uber Fx** (guia completo em uber-go.github.io/fx + API/`fxtest` + `dig`); pgx v5/pgxpool/pgconn, golang-migrate; PostgreSQL (locking, isolation, INSERT ON CONFLICT, constraints, triggers, NUMERIC); SQS FIFO/DLQ/visibility/redrive, AWS SDK Go v2 SQS, LocalStack SQS; Keycloak (import de realm, containers, admin), go-oidc, oauth2 client_credentials; testcontainers-go; Prometheus client_golang, `log/slog`, `net/http`, `testing`; padrões transactional outbox e idempotent consumer.

Sugestões para somar (adicione em `sources.json` no mesmo formato):
- `https://pkg.go.dev/github.com/go-chi/chi/v5` — se escolher chi como roteador
- `https://pkg.go.dev/github.com/google/uuid` — UUID v7 (`uuid.NewV7`) usado nos exemplos do enunciado
- `https://pkg.go.dev/github.com/stretchr/testify` — se usar testify
- `https://pkg.go.dev/go.uber.org/goleak` — detectar goroutines vazadas nos testes de shutdown
- `https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/` — referência da API SQS
- `https://www.postgresql.org/docs/current/sql-createfunction.html` e `.../plpgsql-errors-and-messages.html` — funções/erros dos triggers
- `https://docs.docker.com/compose/how-tos/startup-order/` — healthchecks e `depends_on` no Compose
- Seu próprio material: salve em `docs/rag/extra/` (ex.: exemplos de código aprovados) e reindexe.

Atualização: rode `fetch_docs.py` + `build_index.py` de novo sempre que quiser docs frescas (as fontes apontam para as versões "current/latest").


## 6) Segurança: como o kit protege seu projeto

Três camadas, todas ativas ao abrir o `claude` na raiz do projeto:

| Camada | Onde | O que faz |
|---|---|---|
| Regras | `CLAUDE.md` + `docs/requisitos/protocolo-seguranca.md` | O Claude propõe cada lote de alteração (arquivos, o que muda, risco, como desfazer) e espera seu "sim". Nunca reverte por conta própria. |
| Permissões | `.claude/settings.json` | Leitura/testes liberados; **todo Edit/Write e comandos que alteram coisas pedem sua confirmação**; comandos destrutivos negados. |
| Hook | `scripts/safety/guard.py` | Bloqueia `rm -rf`, `git reset/clean/rebase`, `push --force`, `compose down -v`, `DROP/TRUNCATE`, `DELETE` sem WHERE, `sudo`...; pede confirmação em `git checkout/commit`, `migrate down`, `go mod tidy`...; **faz backup automático** do arquivo em `.claude/backups/` antes de cada edição. |

Arquivos que só você edita (o hook bloqueia o Claude): `docs/requisitos/desafio.md`, `.claude/settings.json`, `scripts/safety/`, `.git/`, `.env`.

Comandos úteis dentro do `claude`:
- `/etapa N` — executa uma etapa em lotes, pedindo aprovação a cada um.
- `/desfazer <o quê>` — só propõe a reversão (com o backup correspondente) e pede confirmação arquivo por arquivo.

Recomendações:
- Antes de cada etapa grande, faça você mesmo um checkpoint: `git add -A && git commit -m "checkpoint antes da etapa N"`.
- Teste o hook uma vez: peça ao Claude "rode `git reset --hard`" — deve ser bloqueado.
- Para o hook funcionar é preciso `python3` no PATH. Se ele falhar por qualquer motivo, a ação é bloqueada (falha fechada), nunca liberada.
- Limite honesto: o hook analisa o texto do comando; ele reduz muito o risco, mas não substitui o checkpoint no git nem a sua revisão antes de aprovar.
