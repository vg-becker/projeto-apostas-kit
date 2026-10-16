# ARCHITECTURE

Decisões técnicas do serviço de processamento distribuído de apostas. Este documento é
atualizado a cada etapa. Cada seção informa o **estado**:

- **Implementado**: existe no código e tem testes que passam.
- **Decidido**: a decisão está tomada, mas o código ainda não existe (etapa indicada).

Estado atual do projeto: **Etapas 0, 1 e 2 concluídas** (esqueleto, Model, Repository e
migrations). Service, HTTP, autenticação, SQS, Fx, observabilidade e a documentação de
uso (README) ainda **não foram feitos**; ver "Limitações e trabalho não concluído".

## 1. Mapeamento MVC + Service

| Camada | Pasta | Estado |
|---|---|---|
| Model | `internal/model/{money,identity,wallet,ledger,wager,event,port}` | Implementado |
| Repository | `internal/repository/postgres` | Implementado |
| Service | `internal/service` | Decidido (Etapa 3) |
| Controller | `internal/controller/{http,sqs,worker}` | Decidido (Etapas 4 e 6) |
| View | `internal/view/{response,presenter,problem}` | Decidido (Etapa 4) |
| Platform | `internal/platform/{clock,idgen,...}` | Parcial: `clock` e `idgen` implementados |

Regra de dependência: o Model importa **apenas a stdlib e a si mesmo**. Verificado com
`go list -deps ./internal/model/...` (nenhuma dependência externa). O Service dependerá só
das interfaces de `internal/model/port`, nunca do Repository concreto. HTTP e SQS
compartilharão o mesmo caso de uso.

**Pacote `identity`.** `wallet` devolve um `ledger.Entry` e `ledger` referencia o ID da
carteira; isso formaria um ciclo. Os identificadores tipados (`WalletID`, `PlayerID`,
`TransactionID`, `EntryID`, `EventID`, todos UUID validados) ficam em
`internal/model/identity`, que só importa a stdlib. O zero value é inválido.

## 2. Dinheiro (Implementado)

- `Money` é um value object imutável: `int64` em **centavos** mais a moeda ISO 4217. Nunca
  `float32` ou `float64`, nem no parsing, no cálculo, na serialização ou na persistência.
- Moedas aceitas: BRL, USD, EUR (todas com 2 casas). Moeda desconhecida é rejeitada.
- Faixa: de -92.233.720.368.547.758,08 a 92.233.720.368.547.758,07. Overflow é detectado
  no parsing (dígito a dígito), na soma, na subtração e na negação (`MinInt64`), e retorna
  `ErrOverflow`.
- Parsing de entrada externa (`Parse`): decimal com até 2 casas, sem sinal. Rejeita vazio,
  `NaN`, `Infinity`, notação científica, `+`, espaços, ponto sem dígitos, zeros à esquerda
  (`007.00`), escala excedente (`ErrScaleExceeded`) e negativos (`ErrNegativeNotAllowed`).
  Nada é arredondado. `ParseSigned` aceita sinal, só para valores internos.
- **Normalização (interpretação adotada):** `"25"`, `"25.5"` e `"25.00"` são formas
  equivalentes e normalizam para `25.00`. O hash de idempotência usa a forma normalizada
  (`Amount()`). A alternativa mais rígida (exigir sempre 2 casas) foi descartada para
  aceitar provedores que enviam `"25"`.
- Aritmética e comparação exigem moedas iguais (`ErrCurrencyMismatch`); o zero value é
  inválido (`ErrUninitialized`).
- Persistência: `BIGINT` em unidades menores, mais `currency char(3)`. O contrato externo
  é `{"amount":"25.00","currency":"BRL"}`.
- Política de zero por tipo: `LOSS` exige exatamente `0.00`; `BET`, `WIN`, `REFUND`,
  `ROLLBACK` e `OPENING` exigem valor > 0. Saldo inicial zero é aceito e não cria `OPENING`.

## 3. Carteira e controle de concorrência

**Domínio (Implementado).** `Wallet` é a raiz do agregado, com estado privado. Criação
(`Open`) e reidratação (`Rehydrate`) são separadas; a reidratação valida invariantes e não
reaplica movimentações nem emite eventos. `Debit` e `Credit` devolvem o `ledger.Entry`
correspondente, criado **antes** de mudar o estado: se o lançamento for inválido, nada muda.
A versão começa em 1 e só incrementa quando o saldo muda. A abertura com saldo positivo
produz o lançamento de abertura sem incrementar a versão.

**Estratégia (Implementado no Repository; orquestração na Etapa 3):** locking pessimista
por carteira.

1. A transação lê a carteira com `SELECT … FOR UPDATE` (`GetForUpdate`).
2. O domínio aplica a regra; o repositório grava o ledger e atualiza a carteira com
   `UPDATE … WHERE id = $1 AND version = $expected` (defesa em profundidade).
3. O banco impõe `CHECK (balance_minor_units >= 0)` e `CHECK (version >= 1)`.

Não há lock global: carteiras diferentes avançam em paralelo. Transações usam
`READ COMMITTED`; a consistência vem do lock por linha e das constraints, não de isolamento
mais alto. `Update` devolve `ErrVersionConflict` quando a versão diverge **ou** quando a
carteira não existe; os dois casos não são distinguidos.

**Evidência (testes de integração com Postgres real e `-race`):**
20 apostas simultâneas de 5,00 sobre 100,00 resultam em saldo 0 e 20 débitos; 30 apostas
resultam em 20 processadas e 10 rejeitadas, sem saldo negativo; duas apostas de 80,00 sobre
100,00 resultam em uma processada, uma rejeitada, saldo 20,00 e um único débito. Em todos os
casos o saldo armazenado bate com o ledger reconstruído. Esses são ensaios no nível do
repositório, em um único processo; o teste exigido com **três processos independentes** é
trabalho da Etapa 8.

## 4. Transações, estados e falhas (Implementado no Model)

`WagerTransaction` (`wager.Transaction`) nasce em `PENDING` e muda por métodos explícitos:

```
PENDING           -> PENDING_REFERENCE | PROCESSED | REJECTED | FAILED
PENDING_REFERENCE -> PROCESSED | REJECTED | FAILED
PROCESSED, REJECTED, FAILED são terminais
```

Transição a partir de estado terminal retorna `ErrTerminalState`, e o banco também a recusa
(trigger em `wager_transactions`). O tempo não anda para trás.

**Falha transitória × permanente.** Indisponibilidade de Postgres ou SQS **nunca** vira
`FAILED`: o repositório marca o erro como `port.ErrTransient` (conexão, `40001`, `40P01`,
classes `08`, `53` e `57P`, rede, EOF, deadline) e o chamador repete. `FAILED` registra
somente falha permanente de infraestrutura, com o código `INFRASTRUCTURE_FAILURE`, após
esgotar tentativas ou diante de mensagem impossível de processar. Violações de constraint
são permanentes. O `TxManager` **não** faz retry interno; o Service decide, pois só ele sabe
se repetir é seguro.

**Códigos de falha estáveis** (`wager.FailureCode`):

| Código | Quando | Classe |
|---|---|---|
| `INSUFFICIENT_FUNDS` | BET sem saldo | definitivo |
| `REVERSAL_INSUFFICIENT_FUNDS` | reversão que debitaria mais que o saldo | definitivo |
| `REFERENCE_NOT_FOUND` | referência não chegou até o limite de tentativas ou TTL | definitivo |
| `REFERENCE_NOT_PROCESSED` | referência existe, mas terminou `REJECTED` ou `FAILED` | definitivo |
| `REFERENCE_MISMATCH` | provedor, jogador, carteira, moeda ou rodada divergem | definitivo |
| `REFERENCE_KIND_INVALID` | tipo incompatível com a reversão | definitivo |
| `AMOUNT_MISMATCH` | valor diferente do referenciado | definitivo |
| `ALREADY_REVERSED` | a referência já recebeu reversão bem-sucedida | definitivo |
| `CURRENCY_MISMATCH` | moeda diferente da carteira | corrigível |
| `INFRASTRUCTURE_FAILURE` | apenas em `FAILED` | definitivo |

O código da reversão sem saldo difere do da aposta sem saldo, como o enunciado exige.
Erros de entrada corrigíveis (formato, escala, tipo desconhecido) são devolvidos antes de
persistir; o mapeamento para HTTP e o contrato de erro serão definidos na Etapa 4.

## 5. Reversões e referências pendentes

**Política (Implementado em `wager.ValidateReversal`, função pura).**

- `REFUND` só referencia `BET`. `ROLLBACK` referencia `BET`, `WIN` ou `REFUND`.
- A operação e a referência devem concordar em provedor, ID da referência, jogador,
  carteira, rodada e moeda; o valor da reversão deve ser igual (sem reversão parcial).
- **Cada referência recebe no máximo uma reversão bem-sucedida, de qualquer tipo.** Uma BET
  reembolsada não pode sofrer `ROLLBACK`, nem o contrário: isso impede devolver duas vezes o
  mesmo débito. Reverter uma reversão (`ROLLBACK` de `REFUND`) é permitido uma vez; depois
  disso, a BET original continua marcada como já revertida (`ALREADY_REVERSED`). É a
  interpretação mais conservadora financeiramente.
- Direção do lançamento: `REFUND` e `ROLLBACK` de BET creditam; `ROLLBACK` de WIN ou REFUND
  debita.
- **Imposição no banco (Implementado):** índice único parcial
  `wager_transactions_one_reversal_per_reference` sobre `resolved_reference_id` para
  reversões `PROCESSED`. Sob concorrência, a segunda reversão falha com `ErrAlreadyExists`.

**Referência indisponível (Decidido; worker na Etapa 6).**

- Referência ausente: a operação é persistida como `PENDING_REFERENCE` e o evento
  `WagerTransactionPendingReference` é emitido. Um worker reivindica as pendências com
  `SKIP LOCKED`, com backoff exponencial, sobrevivendo a reinício (`reference_attempts`,
  `next_attempt_at` e `lease_until` são persistidos). Ao esgotar o limite de tentativas ou o
  TTL, a operação termina `REJECTED` com `REFERENCE_NOT_FOUND` e o evento de rejeição.
  Os valores de limite e TTL serão definidos e documentados na Etapa 6.
- Referência existente, mas ainda `PENDING` ou `PENDING_REFERENCE`: a validação devolve
  `ErrReferencePending` e a operação espera, no mesmo ciclo de tentativas.
- Referência existente que terminou `REJECTED` ou `FAILED`: rejeição imediata com
  `REFERENCE_NOT_PROCESSED`.

## 6. Idempotência

**Persistência (Implementado):** `UNIQUE (provider_id, idempotency_key)` e
`UNIQUE (provider_id, external_transaction_id)`. Assim, uma operação identificada por
`(providerId, externalTransactionId)` não pode ser reaplicada com outra chave, e a
idempotência sobrevive a reinício porque está no banco, não na memória. O repositório
distingue as duas violações pelo nome da constraint.

**Hash do payload (Decidido; Etapa 3):** SHA-256 de JSON canônico (chaves ordenadas) dos
campos de negócio, excluindo a chave de idempotência e metadados de transporte, com o valor
monetário na forma normalizada (seção 2). O mesmo cálculo vale para HTTP e SQS. O banco
exige que o hash seja 64 hex minúsculos.

**Replay (Decidido; Etapa 3):** o saldo observado no processamento original é persistido em
`result_balance_minor_units` justamente para o replay devolvê-lo mesmo depois de outras
movimentações. Chave igual com conteúdo igual retorna o resultado persistido com
`idempotentReplay: true`; chave reutilizada com conteúdo diferente retorna conflito.

## 7. Ledger e integridade no banco (Implementado)

O schema impõe as invariantes independentemente do código e de locks locais:

- `wallets`: saldo `>= 0`, versão `>= 1`, `UNIQUE (player_id, currency)`.
- `wallet_ledger_entries`: valor `> 0`, saldos `>= 0`, `balance_after = balance_before ± amount`
  conforme a direção, `UNIQUE (wallet_id, transaction_id)`. **Append-only:** triggers
  `BEFORE UPDATE OR DELETE` e `BEFORE TRUNCATE` lançam exceção. Correções exigem novos
  lançamentos. `LOSS` e rejeições não geram lançamento.
- `wager_transactions`: um `CHECK` distingue origem interna (`OPENING`, sem metadados
  externos) de externa (todos os metadados presentes); coerência entre estado, código de
  falha e saldo de resultado; política de valor por tipo; política de referência por tipo;
  índice único parcial que impede **crédito inicial duplicado** (uma `OPENING` por carteira);
  trigger que impede alterar ou apagar uma transação terminal.
- Migrations versionadas em `migrations/` (`0001` a `0005`, `.up.sql` e `.down.sql`).
  Migrations já criadas nunca são editadas; mudanças entram em uma nova migration.

**Reconciliação (Repositório implementado; endpoint na Etapa 4).** `CalculateBalance` lê o
saldo armazenado e o reconstruído pelo ledger **na mesma instrução SQL**, garantindo um único
snapshot sem alterar nada. `difference = armazenado - reconstruído`.

**Limitação deliberada:** não há trigger que cruze o saldo da carteira com a soma do ledger
no commit (seria uma constraint `DEFERRABLE` com uma soma por commit). A atomicidade vem do
código transacional e a divergência é detectada pela reconciliação. Fica registrado como
limitação.

## 8. Acesso ao banco e transações (Implementado)

- **pgx v5 (`pgxpool`) com SQL explícito**, em `const` com raw string. Sem ORM.
- **Mapeamento de Money:** `BIGINT` em unidades menores e `char(3)` de moeda; as linhas
  são convertidas em `Snapshot`s do domínio, que revalida as invariantes na reidratação.
- **Delimitação da transação:** o `TxManager` abre a transação, a guarda no `context`, e
  todos os repositórios a recuperam por `Connection.Querier(ctx)`. Commit se o trabalho
  retorna `nil`; erro ou panic fazem rollback (com `context.WithoutCancel`, para que um
  cancelamento não deixe a transação aberta); panic é repropagado. Chamada aninhada
  reaproveita a transação externa. O Service decidirá o retry a partir de `ErrTransient`.
- Mapeamento de erros para as portas: `ErrNotFound`, `ErrAlreadyExists` (unicidade),
  `ErrVersionConflict`, `ErrInvalidCursor`, `ErrTransient`.
- **Paginação do ledger:** ordenação estável por `(created_at, id)`, cursor opaco
  (`base64url` de um JSON com instante, ID e carteira), limite padrão 50 e máximo 200.
  O cursor é validado em formato e carteira, mas **não é assinado**.

## 9. Inbox e outbox (Repositórios implementados; consumidor e publisher nas Etapas 3 e 6)

**Inbox.** `Begin` insere com `ON CONFLICT DO NOTHING`; `NEW` na primeira vez, `DUPLICATE` se
já concluída com o mesmo hash, `HASH_CONFLICT` se o `messageId` voltou com outro conteúdo.
Duas transações concorrentes com a mesma mensagem são serializadas pela unicidade
(10 entregas simultâneas resultam em exatamente um `NEW`). Uma linha existente, com hash
igual e **não concluída**, devolve `NEW`: o tratamento é refeito em vez de a mensagem ser
descartada. `Complete` é idempotente. O registro da inbox deve compartilhar a transação das
alterações de domínio, e a mensagem SQS só é apagada após o commit (Etapa 6).

**Outbox.** `Add` grava o snapshot do evento (`jsonb`) na transação do domínio, então o
evento só existe se a transação confirmar; testes provam que um publisher não o enxerga antes
do commit nem após rollback. `Claim` usa `FOR UPDATE SKIP LOCKED` com lease: vários
publishers disputam sem sobreposição, e um lease vencido (publisher que morreu entre publicar
e confirmar) permite que outra instância assuma o mesmo evento. O `eventId` é estável e é
preservado em republicações. `MarkFailed` incrementa `attempts` e agenda o backoff. O
snapshot é imutável também no banco (trigger).

**Contrato dos eventos (Implementado no Model):** envelope com `eventId`, `eventType`,
`aggregateId`, `correlationId`, `causationId` opcional, `occurredAt` (UTC, RFC 3339),
`version` (1) e `data` tipado; dinheiro sempre como `{"amount","currency"}` em texto.

| Evento | Gatilho | Agregado |
|---|---|---|
| `WagerTransactionProcessed` | conclusão com sucesso, incluindo `LOSS` e a abertura | transactionId |
| `WagerTransactionRejected` | rejeição definitiva por regra de negócio | transactionId |
| `WalletBalanceChanged` | alteração efetiva do saldo | walletId |
| `WagerTransactionPendingReference` | registro de espera pela referência | transactionId |

`WalletBalanceChanged` inclui `walletId`, `transactionId`, `direction`, `money`,
`balanceBefore`, `balanceAfter` e `walletVersion`. Eventos da abertura interna omitem os
metadados externos.

**Limitações:** o `jsonb` normaliza ordem de chaves e espaços, então o payload publicado é a
renderização do banco (equivalente ao original e idêntica em toda republicação). Com vários
publishers, a **ordem entre eventos do mesmo agregado não é garantida**; a entrega é
at-least-once e o consumidor deve deduplicar por `eventId`.

## 10. Autenticação e autorização (Decidido; Etapa 5)

- IdP externo OAuth 2.0/OIDC: **Keycloak** com `client_credentials` entre serviços. Cadastro
  de senhas e emissão própria de tokens estão fora do escopo.
- Realm provisionado automaticamente em `deploy/keycloak/realm-export.json`, com os clients
  `provider-a`, `provider-b` e `internal-service`. Os clients de provedor carregam um claim
  fixo `provider_id`; todos recebem o audience `apostas-api`; o escopo `wallet:internal` é
  exclusivo do `internal-service`. Os secrets do arquivo são fictícios e locais.
- Validação do JWT (issuer, audience, expiração e assinatura via JWKS) com `go-oidc`.
- O `providerId` vem **do token**, nunca do corpo. Provedores acessam apenas as próprias
  transações, inclusive em replays. Operações de carteira exigem o escopo interno.
- O acesso à mensageria é controlado por credenciais e políticas do broker, preservando as
  validações de domínio no consumidor.

O realm e o Compose existem; o middleware, a validação e os testes 401/403 **ainda não**.

## 11. Composição com Uber Fx e shutdown (Decidido; Etapa 7)

Um `fx.Module` por camada (o Model não participa), `fx.Provide` com construtores e
`fx.Invoke` só para registrar rotas e workers. `fx.Lifecycle`: `OnStart` não bloqueia, as
goroutines usam `context` cancelável e `OnStop` espera o término do worker com prazo.
Ordem de shutdown: parar as entradas (HTTP e SQS), esperar o trabalho em andamento e fechar
o pool do banco por último. A composição será verificada com `fxtest`. `cmd/api/main.go`
hoje é um placeholder vazio.

## 12. Mensageria SQS (Decidido; Etapa 6)

O provisionamento das filas existe em `deploy/localstack/init/ready.d/01-sqs.sh`:
`wager-transactions.fifo`, `wager-transactions-dlq.fifo` (redrive com `maxReceiveCount` 5,
visibility timeout de 60 s) e `wager-events.fifo`, o destino dos eventos de saída. O
consumidor, `MessageGroupId`, `MessageDeduplicationId`, limites de tentativas, backoff e
tratamento de mensagem inválida serão definidos e documentados na Etapa 6.

## 13. Decisões de ambiente

- Go 1.27.2, igual em `go.mod` e no `Dockerfile`; `go.sum` versionado.
- Postgres 17, LocalStack 3.8 e Keycloak 26 no Docker Compose.
- UUIDv7 (`google/uuid`) para os identificadores, gerados fora do Model por `port.IDGenerator`.
- `port.Clock` injeta o tempo; timestamps sempre em UTC.
- Testes de integração usam Postgres real via testcontainers e aplicam as migrations reais
  com `golang-migrate`, incluindo `up`, `down` e `up`. Ficam atrás da build tag
  `integration`.

## 14. Limitações e trabalho não concluído

Não concluído (previsto em `docs/PLANO.md`):

- **Etapa 3:** Service (`OperationService`, hash canônico, reconciliação, resolver de
  referências, publisher da outbox).
- **Etapa 4:** View, Controller HTTP, contratos de erro, health checks, endpoint de
  reconciliação.
- **Etapa 5:** middleware JWT e testes de autenticação e isolamento entre provedores.
- **Etapa 6:** consumidor SQS, workers e provisionamento via aplicação.
- **Etapa 7:** módulos Fx, `main.go`, logs JSON, métricas Prometheus.
- **Etapa 8:** testes com três processos, falhas entre commit e publicação, reinício.
- **Etapa 9:** `README.md` (pré-requisitos, variáveis, filas, aplicação e reversão das
  migrations, exemplos de chamada).

Hoje **não existe endpoint de negócio, autenticação efetiva nem consumidor funcionando**;
vários itens eliminatórios do enunciado só serão atendidos quando as etapas acima forem
concluídas.

Limitações conhecidas já registradas:

- Sem trigger de verificação saldo × ledger no commit (seção 7).
- `ErrVersionConflict` cobre também carteira inexistente (seção 3).
- Cursor de paginação não assinado (seção 8).
- Ordem entre eventos do mesmo agregado não garantida com vários publishers (seção 9).
- O claim de `PENDING` não espera idade mínima: se a instância original ainda processa, a
  duplicidade é inofensiva, pois o Service relê o estado sob o lock da carteira e o trigger
  impede nova transição de registro terminal.
- Moedas limitadas a BRL, USD e EUR.
