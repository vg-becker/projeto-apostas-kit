# ARCHITECTURE

Decisões técnicas do serviço de processamento distribuído de apostas. Este documento é
atualizado a cada etapa. Cada seção informa o **estado**:

- **Implementado**: existe no código e tem testes que passam.
- **Decidido**: a decisão está tomada, mas o código ainda não existe (etapa indicada).

Estado atual do projeto: **Etapas 0 a 7 concluídas** (esqueleto, Model, Repository com
migrations, Service, View, Controller HTTP, autenticação com o Keycloak, mensageria SQS com
workers, e a composição com Fx, configuração, logs e métricas). Os testes com três processos
e o README ainda **não foram feitos**; ver "Limitações e trabalho não concluído".

## 1. Mapeamento MVC + Service

| Camada | Pasta | Estado |
|---|---|---|
| Model | `internal/model/{money,identity,wallet,ledger,wager,event,port}` | Implementado |
| Repository | `internal/repository/postgres` | Implementado |
| Service | `internal/service` | Implementado (Etapa 3) |
| Controller | `internal/controller/{http,sqs,worker,wire}` | Implementado (Etapas 4 e 6) |
| View | `internal/view/{response,presenter,problem}` | Implementado (Etapa 4) |
| Platform | `internal/platform/{clock,idgen,auth,sqsclient,config,logging,metrics,fxmodules}` | Implementado (Etapas 5 a 7) |

Regra de dependência: o Model importa **apenas a stdlib e a si mesmo**. Verificado com
`go list -deps ./internal/model/...` (nenhuma dependência externa). O Service depende só
das interfaces de `internal/model/port`, nunca do Repository concreto (só os testes de
integração o compõem com os repositórios reais). HTTP e SQS compartilharão o mesmo caso de
uso, `OperationService.Process` (seção 15).

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

**Referência indisponível (Implementado: Service e worker periódico).**

Vale para `REFUND`, `ROLLBACK` e para `WIN` com referência (esta deve ser uma BET processada
da mesma rodada, provedor, jogador, carteira e moeda; o valor do WIN é livre; vários WINs
podem referenciar a mesma BET).

- Referência ausente: a operação é persistida como `PENDING_REFERENCE` e o evento
  `WagerTransactionPendingReference` é emitido (uma única vez). O `ReferenceResolver`
  reivindica as pendências vencidas com `SKIP LOCKED` e lease, e tenta de novo com backoff
  exponencial. O estado sobrevive a reinício (`reference_attempts`, `next_attempt_at`,
  `lease_until` são persistidos) e pode ser assumido por qualquer instância.
- **Política padrão** (`ReferencePolicy`, configurável): 10 tentativas, TTL de 10 minutos,
  atraso de 2 s dobrando a cada tentativa até 1 min, lease de reivindicação de 1 min.
  Esgotado o limite de tentativas **ou** o TTL, a operação termina `REJECTED` e o evento de
  rejeição é emitido.
- Qual código: `REFERENCE_NOT_FOUND` se a referência nunca apareceu; `REFERENCE_NOT_PROCESSED`
  se ela existe mas nunca terminou.
- Referência existente, mas ainda `PENDING` ou `PENDING_REFERENCE`: a validação devolve
  `ErrReferencePending` e a operação espera, no mesmo ciclo de tentativas.
- Referência existente que terminou `REJECTED` ou `FAILED`: rejeição imediata com
  `REFERENCE_NOT_PROCESSED`.
- Referência de outro provedor é invisível (a busca é por `(provedor, referência)`), então
  não resolve; referência em outra carteira do mesmo provedor é `REFERENCE_MISMATCH`.
- Só a carteira da própria operação é travada, nunca duas, então não há deadlock entre
  carteiras. Uma reversão que debitaria mais que o saldo termina
  `REVERSAL_INSUFFICIENT_FUNDS`, distinto de `INSUFFICIENT_FUNDS`.
- O `correlationId` e o `causationId` da requisição original ficam gravados na transação
  (migration `0006`), então os eventos emitidos depois pelo resolver os preservam.

## 6. Idempotência

**Persistência (Implementado):** `UNIQUE (provider_id, idempotency_key)` e
`UNIQUE (provider_id, external_transaction_id)`. Assim, uma operação identificada por
`(providerId, externalTransactionId)` não pode ser reaplicada com outra chave, e a
idempotência sobrevive a reinício porque está no banco, não na memória. O repositório
distingue as duas violações pelo nome da constraint.

**Hash do payload (Implementado; `service.CanonicalHash`).** SHA-256, em hex minúsculo, de
um JSON com chaves ordenadas (sem escape de HTML) contendo `providerId`,
`externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money`
(`amount` normalizado e `currency`) e, quando houver, `referenceExternalTransactionId`.
Ficam de fora a chave de idempotência e os metadados de transporte (`messageId`,
`occurredAt`, correlação e causação). O mesmo cálculo serve a HTTP e SQS. O teste usa
vetores calculados por outro programa (Python), porque mudar o algoritmo invalidaria hashes
já persistidos. O banco exige 64 hex minúsculos.

**Replay (Implementado).** O saldo observado no processamento original é persistido em
`result_balance_minor_units`, então o replay o devolve mesmo depois de outras movimentações,
com `idempotentReplay: true`. Uma rejeição também é um resultado persistido e é reproduzida.
Chave igual com conteúdo diferente, ou o mesmo ID externo sob outra chave, retorna
`ErrIdempotencyConflict`.

**Ordem das verificações e a corrida corrigida.** O `Process` consulta primeiro só a chave,
sem lock (replays não disputam a carteira). Depois trava a carteira e repete a consulta,
agora também por ID externo. A consulta por ID externo precisa rodar **sob o lock**: sem ele,
uma requisição que lia "chave inexistente" podia, entre as duas leituras, ver a operação
recém-confirmada por outra e acusar conflito indevido. O erro apareceu de forma intermitente
no teste de 50 envios idênticos e foi corrigido; há um teste de regressão com 12 rodadas
simultâneas. Duas operações iguais sobre carteiras diferentes não ocorrem, pois a carteira
faz parte do hash; um insert concorrente que viole a unicidade vira `ErrIdempotencyConflict`.

**Erros de entrada não são persistidos** (validação, carteira inexistente, jogador ou moeda
divergentes): persisti-los travaria a correção, pois o hash mudaria e a mesma chave daria
conflito. O código `CURRENCY_MISMATCH` fica reservado e não é produzido. Rejeições de
negócio, ao contrário, são resultados persistidos e terminais.

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
- Migrations versionadas em `migrations/` (`0001` a `0006`, `.up.sql` e `.down.sql`).
  Migrations já criadas nunca são editadas; mudanças entram em uma nova migration. A `0006` acrescentou `correlation_id` e `causation_id` à `wager_transactions`.

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

## 9. Inbox e outbox (Implementado)

**Inbox.** `Begin` insere com `ON CONFLICT DO NOTHING`; `NEW` na primeira vez, `DUPLICATE` se
já concluída com o mesmo hash, `HASH_CONFLICT` se o `messageId` voltou com outro conteúdo.
Duas transações concorrentes com a mesma mensagem são serializadas pela unicidade
(10 entregas simultâneas resultam em exatamente um `NEW`). Uma linha existente, com hash
igual e **não concluída**, devolve `NEW`: o tratamento é refeito em vez de a mensagem ser
descartada. `Complete` é idempotente. O registro da inbox deve compartilhar a transação das
alterações de domínio, e a mensagem SQS só é apagada após o commit (seção 12).

**Outbox.** `Add` grava o snapshot do evento (`jsonb`) na transação do domínio, então o
evento só existe se a transação confirmar; testes provam que um publisher não o enxerga antes
do commit nem após rollback. `Claim` usa `FOR UPDATE SKIP LOCKED` com lease: vários
publishers disputam sem sobreposição, e um lease vencido (publisher que morreu entre publicar
e confirmar) permite que outra instância assuma o mesmo evento. O `eventId` é estável e é
preservado em republicações. `MarkFailed` incrementa `attempts` e agenda o backoff. O
snapshot é imutável também no banco (trigger). `Claim` devolve os eventos **em ordem de
ocorrência** `(occurred_at, event_id)`: o `UPDATE ... RETURNING` não garante a ordem das
linhas, então o repositório as ordena (defeito encontrado na Etapa 6, quando os eventos de uma
carteira chegaram à fila com as versões 1, 3, 2, 4).

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

**Publisher (Implementado; `service.OutboxPublisher`).** `PublishPending` reivindica um lote
(`SKIP LOCKED` com lease), publica em ordem de ocorrência por uma porta
`port.EventPublisher` e confirma com `MarkPublished`. Se o destino recusa, agenda a nova
tentativa com atraso dobrando de 2 s até 1 min (`PublishPolicy`: lotes de 50, lease de
30 s). **Um evento nunca é descartado:** não há limite de tentativas, só repetição. Se o
processo morre entre publicar e confirmar, o lease vence e outra instância republica o
**mesmo `eventId`**; o destino deduplica por ele (`MessageDeduplicationId` = `eventId` e
`MessageGroupId` = `aggregateId`, seção 12). O cancelamento do contexto interrompe o lote;
o que já foi entregue é confirmado com um contexto próprio e o restante volta depois do lease.
O destino real é o SQS (`sqsclient.EventPublisher`); o worker que roda o publisher é descrito
na seção 12.

**Limitações:** o `jsonb` normaliza ordem de chaves e espaços, então o payload publicado é a
renderização do banco (equivalente ao original e idêntica em toda republicação). Com vários
publishers, a **ordem entre eventos do mesmo agregado não é garantida**; a entrega é
at-least-once e o consumidor deve deduplicar por `eventId`.

## 10. Autenticação e autorização (Implementado na borda HTTP; mensageria na Etapa 6)

### Escolhas e justificativa

- **IdP externo: Keycloak** (OAuth 2.0 / OIDC), com `client_credentials` entre serviços. É o
  recomendado pelo enunciado, roda em container, provisiona o realm por arquivo e emite tokens
  JWT padrão. Cadastro de senhas e emissão própria de tokens estão fora do escopo: o serviço
  só **valida** tokens.
- **Biblioteca:** `go-oidc` (validação) com `go-jose` (assinatura). Não há código próprio de
  verificação criptográfica.

### Modelo de permissões

| Identidade | Como é reconhecida | Pode |
|---|---|---|
| Provedor (`provider-a`, `provider-b`) | claim `provider_id`, fixado no realm por client | enviar operações e ler as **próprias** transações |
| Serviço interno (`internal-service`) | escopo `wallet:internal` | abrir e ler carteiras, ledger, reconciliação, ler qualquer transação |

- O `providerId` vem **do token**, nunca do corpo; um corpo com outro `providerId` é 403. O
  claim `provider_id` é um mapper do tipo "hardcoded claim" do realm: o cliente não o
  controla, só o administrador do realm.
- Um token **válido sem permissão** (nem `provider_id` nem o escopo), um `provider_id` fora
  do formato `[A-Za-z0-9._-]{1,128}` ou **os dois ao mesmo tempo** (ambíguo) não têm
  permissão alguma: o Controller responde 403. Falha-se sempre fechando.
- O escopo é comparado por **igualdade exata** entre os separados por espaço (`wallet:internal-x`
  não vale).
- Provedores acessam apenas as próprias transações, inclusive em replays (seção 15).

### Validação do token (`internal/platform/auth`)

- Assinatura **RS256** (só esse algoritmo: `alg: none` e a confusão RS256↔HS256 são
  recusados), emissor (`iss`) esperado, audiência `apostas-api` e expiração.
- **Sem descoberta:** o emissor esperado e a URL do JWKS são configurados
  (`OIDC_ISSUER_URL`, `OIDC_JWKS_URL`), então o serviço sobe sem depender do IdP. As chaves
  são buscadas na primeira verificação, ficam em cache e são buscadas de novo quando aparece
  um `kid` desconhecido (rotação). Com a chave em cache, tokens válidos continuam sendo aceitos
  mesmo com o Keycloak fora do ar.
- Cabeçalho: só `Authorization: Bearer <token>`, um único token, sem espaços, até 8 KiB.

| Situação | Resposta |
|---|---|
| Sem credencial, malformada, assinatura inválida, expirada, emissor ou audiência errados | **401**, com o mesmo detalhe fixo (o motivo só vai para o log, e o token nunca) |
| Token válido sem permissão, ou ambíguo | **403** |
| IdP inacessível e a chave necessária não está em cache | **503** com `Retry-After`: não prova que o token seja inválido |

A distinção 401×503 não depende de texto de erro: o cliente HTTP do JWKS registra o instante da
última busca que falhou (o `go-oidc` busca em segundo plano, fora do contexto de quem
verifica), e uma verificação que falha depois disso é "indisponível". O IdP **não** entra no
`/health/ready`: derrubar a instância por uma queda do IdP seria pior do que seguir atendendo
com as chaves em cache.

### Provisionamento e execução local

- O realm `apostas` é importado de `deploy/keycloak/realm-export.json`, com os clients
  `provider-a`, `provider-b` e `internal-service`, todos com audiência `apostas-api` (mapper) e
  conta de serviço. Os **secrets do arquivo são fictícios e só valem em desenvolvimento**; um
  ambiente real deve substituí-los e não versioná-los.
- O Compose fixa `KC_HOSTNAME`, de modo que o `iss` seja sempre
  `http://localhost:8080/realms/apostas`, e o app alcança o JWKS pela rede interna
  (`OIDC_JWKS_URL=http://keycloak:8080/...`).
- Obter um token e chamar a API:

```bash
TOKEN=$(curl -s http://localhost:8080/realms/apostas/protocol/openid-connect/token \
  -d grant_type=client_credentials -d client_id=provider-a \
  -d client_secret=provider-a-secret-local | jq -r .access_token)

curl -s http://localhost:8081/providers/provider-a/wagering/transactions/tx-1 \
  -H "Authorization: Bearer $TOKEN"
```

  Use `client_id=internal-service` e `client_secret=internal-service-secret-local` para as
  rotas de carteira.

### Evidência

- Testes com um IdP falso (sem Docker) cobrem cada classe de rejeição de forma
  determinística: expirado, emissor e audiência errados, assinatura de outra chave, `kid`
  desconhecido, `alg: none`, ataque de confusão HS256, payload adulterado, cabeçalhos
  malformados, rotação de chaves, IdP fora do ar com e sem cache, e que nem o token nem seus
  claims aparecem no log.
- Testes de integração em `test/integration` usam o **Keycloak real**, o Postgres real e a
  borda HTTP completa: o contrato dos claims do realm; o que cada identidade pode; credencial
  ausente, lixo, esquema errado, assinatura quebrada, `provider_id` ou escopo trocados no
  payload e audiência reescrita (todos 401, sem efeito financeiro); **token expirado de
  verdade** (tempo de vida reduzido pela API de administração); token de **outro realm** e de
  um client **sem audiência**; `providerId` do corpo diferente do token; e um JWKS inalcançável
  dando 503, não 401. Um defeito do próprio teste foi corrigido no caminho: trocar o último
  caractere da assinatura nem sempre a altera (em base64url ele carrega bits de
  preenchimento); o teste agora altera um caractere do meio e confere que os bytes mudaram.

### Limitações

- **Revogação não é consultada** (não há introspecção): a proteção contra um token vazado é o
  tempo de vida curto (300 s no realm). Um token revogado vale até expirar.
- O `auth.Authenticator` importa os tipos do Controller (`Principal` e os erros); o Controller
  não importa a plataforma, então não há ciclo.
- Sem limite de requisições por provedor.
- O acesso à mensageria é controlado por credenciais e políticas do broker, preservando as
  validações de domínio no consumidor (seção 12). O `providerId` das mensagens vem do corpo, não
  de um token: quem pode publicar na fila é uma decisão do broker.

## 11. Composição com Uber Fx, configuração e observabilidade (Implementado)

### Composição

`cmd/api/main.go` contém só `fx.New(fxmodules.Application()).Run()`. `Application()` lê o
ambiente; uma configuração inválida vira um erro do Fx, que o imprime e encerra o processo com
status 1 **antes de subir qualquer coisa**. Os testes usam `ApplicationWith(config, logOutput)`
com uma configuração própria.

Um `fx.Module` por camada, em `internal/platform/fxmodules` (o único lugar que conhece as
implementações concretas, ligando-as às portas do Model): `platform` (relógio, IDs, pool do
banco, cliente SQS, autenticador, métricas), `repository` (repositórios), `service` (casos de
uso) e `controller` (roteador, servidor HTTP, consumidor, workers e o `Runtime`). O grafo é
validado por `fx.ValidateApp` num teste unitário, e o serviço inteiro sobe e para num teste de
integração com `fxtest`. Os eventos do próprio Fx saem pelo mesmo logger JSON.

### Início e parada (o `Runtime`)

A ordem é parte do contrato e fica num único hook do Fx, não na ordem em que os hooks são
registrados:

- **Início (não bloqueia):** workers, consumidor SQS e, por último, o servidor HTTP, que só
  aceita tráfego com tudo no ar. O `Listen` acontece no início, então um erro de porta aparece
  ali. Se uma etapa falha, o que já subiu é parado no próprio hook (o Fx só executa o `OnStop`
  de hooks cujo `OnStart` deu certo).
- **Parada:** servidor HTTP e consumidor (as entradas) em paralelo, esperando o que está em
  andamento; depois os workers; por último o pool do banco, num hook anterior do Fx (o Fx
  executa os `OnStop` na ordem inversa da construção, e tudo que usa o pool nasce depois dele).
  O prazo é `APP_SHUTDOWN_TIMEOUT` (padrão 30 s); vencido, o servidor fecha as conexões que
  restam, e o consumidor e os workers abandonam o trabalho (a transação SQL faz rollback e as
  mensagens voltam à fila). O `StopTimeout` do Fx é esse prazo mais 5 s, para o fechamento do
  pool caber.
- Se o servidor HTTP cair por outro motivo, o serviço inteiro é parado (`fx.Shutdowner`): sem a
  borda HTTP ele não pode ficar de pé.
- **SIGTERM:** o `Run()` do Fx o trata; o binário real sai com status 0 (um teste compila
  `cmd/api`, o executa e lhe envia o sinal).

### Configuração (variáveis de ambiente)

Lidas e validadas na partida; **todos** os erros saem juntos, citando só o nome da variável,
nunca o valor. A senha do banco é um tipo `Secret`, que se imprime como `[redacted]` em `%v`,
`%#v`, JSON e `slog`; a chave da AWS vem de um campo de texto do cliente SQS, então
`Config.String()` e `LogValue()` entregam só um resumo sem segredos.

| Variável | Obrigatória | Padrão | Uso |
|---|---|---|---|
| `APP_HTTP_ADDR` | não | `:8081` | endereço do servidor |
| `APP_SHUTDOWN_TIMEOUT` | não | `30s` | prazo do desligamento |
| `APP_LOG_LEVEL` | não | `info` | `debug`, `info`, `warn`, `error` |
| `APP_MIGRATE_ON_START` | não | `false` | aplica as migrations na partida |
| `APP_MIGRATIONS_DIR` | não | `migrations` | diretório das migrations |
| `DATABASE_URL` | sim | | `postgres://...` |
| `AWS_REGION` | sim | | região do SQS |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | não | cadeia padrão da AWS | credenciais estáticas (LocalStack) |
| `SQS_ENDPOINT` | não | | só para LocalStack |
| `SQS_QUEUE_URL`, `SQS_DLQ_URL`, `SQS_EVENTS_QUEUE_URL` | sim | | as três filas |
| `OIDC_ISSUER_URL`, `OIDC_JWKS_URL`, `OIDC_AUDIENCE` | sim | | validação dos tokens |
| `OIDC_INTERNAL_SCOPE` | não | `wallet:internal` | escopo do serviço interno |

Timeouts do `http.Server` (fixos): cabeçalho 5 s, leitura 15 s, escrita 30 s, ocioso 60 s.

### Migrations na partida

`postgres.Migrate` aplica as migrations pendentes com `golang-migrate`. O driver toma um **lock
consultivo** do Postgres, então várias instâncias subindo juntas aplicam cada migration uma só
vez e nenhuma falha (há teste com quatro em paralelo). Sem nada a aplicar, não faz nada; um
banco "dirty" recusa novas migrations até alguém corrigi-lo. Fica **desligado por padrão**: o
Docker Compose e o `.env.example` o ligam, mas em produção aplicar o schema é uma escolha de
quem opera. Corre dentro da montagem do grafo, antes de qualquer hook de início, então nada
aceita tráfego antes do schema existir. As migrations já criadas nunca são editadas.

### Logs

`log/slog` em JSON, uma linha por registro, em stdout. Nunca levam corpo de requisição, valores
monetários, tokens, chave de idempotência nem a senha do banco; os campos de negócio são
identificadores de correlação (`correlationId`, `messageId`, `groupId`).

### Métricas (`GET /metrics`)

No **mesmo servidor** das rotas de negócio, **sem autenticação** (o formato de texto do
Prometheus não carrega dado de negócio). Registro próprio, não o global, com as métricas de Go
e do processo:

| Métrica | Rótulos | O que mede |
|---|---|---|
| `http_requests_total` | `method`, `route`, `status` | requisições concluídas |
| `http_request_duration_seconds` | `method`, `route` | latência |
| `sqs_consumer_messages_total` | `outcome` | desfecho de cada mensagem |
| `sqs_consumer_dead_letters_total` | `reason` | envios à DLQ, por motivo |
| `worker_iterations_total` | `worker`, `outcome` (`ok`, `error`, `panic`) | iterações dos workers |
| `worker_iteration_duration_seconds` | `worker` | duração das iterações |
| `outbox_pending_events` | | eventos ainda não publicados |
| `outbox_oldest_pending_age_seconds` | | idade do evento mais antigo |
| `sqs_dead_letter_queue_messages` | | mensagens na DLQ (aproximado) |

Os três últimos são lidos **a cada coleta**, com prazo de 2 s (uma consulta ao banco e uma ao
broker); se a leitura falha, só a série daquela métrica some e o resto continua saindo. Os
rótulos têm valores de um conjunto pequeno e fixo: a rota é o **padrão** (`/wallets/{walletId}`,
nunca o caminho com IDs), métodos HTTP desconhecidos viram `OTHER`, e nunca entram `walletId`,
`providerId`, `messageId` nem chave de idempotência (há testes). Alertas úteis: idade da
outbox crescendo (destino quebrado), DLQ acima de zero, `worker_iterations_total{outcome!="ok"}`.

## 12. Mensageria SQS (Implementado)

### Filas e provisionamento

`deploy/localstack/init/ready.d/01-sqs.sh` provisiona as três filas (o Docker Compose o executa
no LocalStack, e os testes de integração montam **este mesmo script**, então os valores abaixo
são verificados):

| Fila | Papel | Configuração |
|---|---|---|
| `wager-transactions.fifo` | operações dos provedores | FIFO, sem deduplicação por conteúdo, `VisibilityTimeout` 60 s, redrive para a DLQ depois de **5 recebimentos** (`maxReceiveCount`), política de acesso declarada |
| `wager-transactions-dlq.fifo` | mensagens que não podem ser processadas | FIFO |
| `wager-events.fifo` | eventos de saída | FIFO |

Para reprovisionar manualmente: `docker compose up localstack` (ou rode o script com
`awslocal`).

### Mensagem de entrada

Envelope `WagerTransactionRequested`, como no enunciado:

```json
{
  "messageId": "msg-123",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-08T12:00:00.000Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "transaction-123",
    "idempotencyKey": "provider-a:transaction-123",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" }
  }
}
```

A validação de formato é a **mesma do HTTP** (pacote `controller/wire`, compartilhado): JSON
estrito (sem campo desconhecido nem dados depois do JSON), até 64 KiB, `type` igual a
`WagerTransactionRequested`, `messageId` obrigatório (até 128 caracteres, sem espaços nas
pontas), `occurredAt` em RFC 3339, dinheiro em **texto** com `money.Parse` (número JSON é
recusado), `OPENING` recusado. O mesmo conteúdo lógico gera **o mesmo comando e o mesmo hash**
por HTTP e por SQS (há teste), inclusive `"25"` contra `"25.00"`. A chave de idempotência é
`data.idempotencyKey`; o `messageId` vira correlação e causação dos eventos.

### MessageGroupId e MessageDeduplicationId

- **`MessageGroupId` = `walletId`:** o broker FIFO serializa as operações de uma carteira e
  deixa as de carteiras diferentes andarem em paralelo, em linha com o lock por carteira do
  banco. O consumidor não depende disso para a correção.
- **`MessageDeduplicationId` = `messageId`:** a deduplicação do broker só vale **5 minutos**.
  A idempotência durável é a **inbox** `UNIQUE (consumerName, messageId)` mais a unicidade
  `(providerId, idempotencyKey)` do banco, que sobrevivem a reinício e valem para sempre.
- A concorrência entre HTTP e SQS é validada: a mesma operação enviada pelas duas vias, em
  qualquer ordem, é aplicada **uma só vez** (a segunda é um replay); a mesma chave com outro
  conteúdo é conflito.

### Consumidor (`controller/sqs`)

Recebe lotes de até 10 mensagens (espera longa), trata as do mesmo grupo **em sequência** e
grupos diferentes **em paralelo**, com no máximo 20 mensagens em voo. O tratamento de cada
mensagem é uma única transação SQL: inbox, operação, ledger, saldo e eventos da outbox.

| Resultado | Destino da mensagem |
|---|---|
| Aplicada, duplicata, **rejeição de negócio** ou **referência pendente** | apagada, **só depois do commit** (rejeição é resultado terminal persistido; a pendência já está persistida e o worker de referências assume) |
| Falha **transitória** ou **desconhecida** | não apagada; a visibilidade é adiada com backoff (`RetryBase` 5 s dobrando pelo número de recebimentos até 5 min). Esgotados os **5 recebimentos**, o **broker** a move para a DLQ. |
| **Permanente** (mensagem malformada, erro de entrada) | enviada **à DLQ pelo consumidor** na primeira tentativa, depois apagada |
| Falha ao enviar à DLQ | tratada como transitória |
| Falha no `Delete` depois do commit | só registrada: a mensagem volta, a inbox a reconhece como duplicata e ela é apagada |

- **Limites:** 5 tentativas; `VisibilityTimeout` 60 s na fila; prazo de 30 s por mensagem
  (abaixo da visibilidade); backoff de 5 s a 5 min. Se o tratamento passar da visibilidade e a
  mensagem for reentregue durante o processamento, a unicidade da inbox serializa as duas e a
  segunda vê duplicata.
- **Mensagens inválidas:** vão direto à DLQ, com o corpo original, o grupo original e os
  atributos `failureReason`, `originalMessageId` e `receiveCount` (só códigos e
  identificadores, nunca texto livre de erro nem dados do payload). Motivos: `invalid_json`,
  `too_large`, `unsupported_type`, `invalid_field` (formato ou valor), `invalid_message`
  (`messageId` inválido), `hash_conflict` (`messageId` reutilizado com outro conteúdo),
  `idempotency_conflict`, `wallet_not_found` e `invalid_input` (demais erros de entrada).
- **Classificação:** um erro é permanente se o HTTP o responderia como 400, 404 ou 409, e um
  teste de consistência garante que as duas listas não divergem. Transitório tem precedência.
  O que não é reconhecido conta como transitório (a opção conservadora: a mensagem espera e
  chega à DLQ pelo broker, em vez de ser descartada).
- **SIGTERM / `Stop(ctx)`:** para de receber na hora; as mensagens recebidas e **ainda não
  iniciadas** são devolvidas à fila (`ChangeMessageVisibility` 0); as em andamento terminam
  dentro do prazo; vencido o prazo, o trabalho é **abandonado** (o contexto é cancelado, a
  transação SQL faz rollback e não sobra efeito parcial) e as mensagens voltam à fila. Nada é
  recebido depois do `Stop`.
- **Observabilidade:** um `Observer` recebe cada desfecho (`applied`, `duplicate`, `retried`,
  `dead_lettered`, `released`, `delete_failed`) para as métricas (seção 11). Os logs levam
  `messageId`, `groupId`, `receiveCount` e o desfecho, nunca o corpo.

### Eventos de saída: roteamento e consumo

- **Destino:** `wager-events.fifo`. **Roteamento:** `MessageGroupId` = `aggregateId` (ordem por
  agregado), `MessageDeduplicationId` = `eventId`. **Corpo:** o snapshot imutável da outbox (o
  envelope da seção 9). **Atributos:** `eventType` e `eventId`.
- **Consumo:** a entrega é **at-least-once**, e o consumidor deve **deduplicar por `eventId`**.
  Para ordenar os eventos de uma carteira, use o `walletVersion` do `WalletBalanceChanged`.
- **Recuperação:** se o publisher cai entre publicar e confirmar na outbox, o evento é
  republicado depois do lease com o **mesmo `eventId`**, e o broker descarta a cópia: o
  destino continua com uma só mensagem (há teste de ponta a ponta). Com o destino
  indisponível, nenhum evento se perde: o publisher repete com backoff até a fila voltar.
- **Ordem:** com um publisher e sem falhas, os eventos de um agregado chegam em ordem de
  ocorrência. Com falhas ou vários publishers, a ordem entre eventos do mesmo agregado **não é
  garantida** (um evento adiado por falha pode ser ultrapassado); o `walletVersion` é a fonte
  de ordem.

### Workers (`controller/worker`)

| Worker | Tarefa | Espera padrão | Repete na hora enquanto |
|---|---|---|---|
| `outbox-publisher` | publica a outbox no SQS | 500 ms | houver eventos reivindicados |
| `reference-resolver` | conclui `PENDING_REFERENCE` vencidas | 2 s | o lote vier cheio |
| `pending-resumer` | retoma `PENDING` abandonadas | 5 s | o lote vier cheio |

Cada espera leva um jitter de até 10%, e erros seguidos dobram a espera até 30 s (voltando ao
normal no primeiro sucesso). Um panic na tarefa é recuperado e registrado (tipo e pilha, nunca
o valor) e o laço continua. Várias instâncias de cada worker rodam ao mesmo tempo: o trabalho é
reivindicado com `SKIP LOCKED` e lease.

### Operação: a DLQ

- Numa fila FIFO, as mensagens de um grupo em voo travam as demais do mesmo grupo. Na DLQ as
  mensagens **mantêm o `MessageGroupId` original**, então quem a drena deve **apagar cada
  mensagem ao recolhê-la**; senão só avança um lote por visibilidade.
- Uma mensagem na DLQ por `invalid_json`, `unsupported_type`, `invalid_field` ou
  `invalid_message` precisa ser corrigida na origem. `wallet_not_found` pode ser reenviada
  depois de abrir a carteira. `hash_conflict` e `idempotency_conflict` indicam um produtor
  reutilizando identificadores. Uma mensagem que chegou à DLQ **pelo broker** (sem
  `failureReason`) esgotou as tentativas por falha transitória e pode ser reenviada à fila
  principal depois que a causa passar: a inbox e a idempotência tornam o reenvio seguro.

### Controle de acesso ao broker

O enunciado manda controlar o acesso por credenciais e políticas do broker, preservando as
validações de domínio no consumidor. O serviço usa credenciais próprias do SQS, a fila principal
declara uma política restrita à conta confiável, e todas as validações de domínio (formato,
valor, carteira, jogador, moeda, hash, idempotência) continuam no consumidor. **O LocalStack
Community guarda a política, mas não a aplica:** localmente isso é só configuração verificada;
a aplicação real depende do IAM da AWS. Como o SQS não carrega identidade do chamador, o
`providerId` vem do corpo da mensagem, e quem pode publicar em nome de cada provedor é uma
decisão da política do broker, não do serviço.

### Evidência

Testes de integração com **LocalStack, Postgres e (no ponta a ponta) Keycloak reais**:
o script entregue cria as filas com os atributos do contrato; o cliente respeita ordem e
bloqueio por grupo FIFO, a deduplicação do broker e o redrive para a DLQ; o consumidor só apaga
depois do commit, trata a queda entre commit e remoção como duplicata, faz backoff e leva à DLQ
as tentativas esgotadas, envia cada erro permanente à DLQ na primeira tentativa com o motivo
certo, mantém a ordem por carteira e o paralelismo entre carteiras, e desliga limpo ou por
prazo sem deixar efeito parcial; o publisher entrega todos os eventos uma vez, recupera-se de
confirmação perdida, de dois publishers e de destino indisponível; o resolver e o resumer
funcionam como workers; e um teste de ponta a ponta recebe a operação por SQS, a consulta pela
API HTTP autenticada, confere ledger, reconciliação, isolamento entre provedores e o fluxo de
eventos, e cruza HTTP e SQS. O `/health/ready` reflete o broker real (`UP`, fila inexistente e
broker inalcançável).

## 13. Decisões de ambiente

- Go 1.27.2, igual em `go.mod` e no `Dockerfile`; `go.sum` versionado.
- Postgres 17, LocalStack 3.8 e Keycloak 26 no Docker Compose.
- UUIDv7 (`google/uuid`) para os identificadores, gerados fora do Model por `port.IDGenerator`.
- `port.Clock` injeta o tempo; timestamps sempre em UTC.
- LocalStack 3.8 (SQS) nos testes de mensageria: a imagem tem cerca de 1 GB e o container sobe
  uma vez por pacote de teste. Divergências do simulador observadas: aceita um corpo de
  mensagem vazio (a AWS recusa); devolve URLs de fila com a porta interna do container (os
  testes montam as URLs a partir do endpoint mapeado); não aplica políticas IAM.
- Dependências adicionadas na Etapa 7: `go.uber.org/fx` e `github.com/prometheus/client_golang`
  (e suas transitivas), com `go mod tidy` aplicado.
- Testes de integração usam Postgres real via testcontainers e aplicam as migrations reais
  com `golang-migrate`, incluindo `up`, `down` e `up`. Ficam atrás da build tag
  `integration`.

## 14. Limitações e trabalho não concluído

Não concluído (previsto em `docs/PLANO.md`):

- **Etapa 8:** testes com três processos, falhas entre commit e publicação, reinício.
- **Etapa 9:** `README.md` (pré-requisitos, variáveis, filas, aplicação e reversão das
  migrations, exemplos de chamada).

O serviço é executável (`cmd/api`) e está testado de ponta a ponta: HTTP autenticado,
consumidor SQS, workers, publicação de eventos, migrations na partida e SIGTERM no binário
real. Os itens eliminatórios que dependem das etapas acima, como os testes com três instâncias
independentes, ainda não foram atendidos. Os testes de concorrência
atuais rodam em um único processo; o teste exigido, com três processos, é da Etapa 8.

Limitações conhecidas já registradas:

- Sem trigger de verificação saldo × ledger no commit (seção 7).
- `ErrVersionConflict` cobre também carteira inexistente (seção 3).
- Cursor de paginação não assinado (seção 8).
- Ordem entre eventos do mesmo agregado não garantida com falhas ou vários publishers
  (seções 9 e 12); o `walletVersion` é a fonte de ordem.
- O consumidor não aplica contrapressão além do limite de 20 mensagens em voo; um lote lento
  atrasa só os grupos dele, mas não há limite de taxa por provedor.
- Dentro de um lote, um grupo lento mantém ocupados os slots em voo dele; as mensagens do mesmo
  grupo já recebidas esperam, e as de outros grupos seguem.
- O publisher não tem limite de tentativas nem fila morta para eventos: nenhum evento se
  perde, mas um destino permanentemente quebrado faz a outbox crescer (a métrica
  `outbox_oldest_pending_age_seconds` deve alertar).
- Os lançamentos usam o relógio da aplicação, lido depois do lock da carteira. A cadeia de
  saldos (`balance_before` e `balance_after`) é sempre correta, mas a ordem por `created_at`
  pode divergir dela se os relógios de instâncias diferentes estiverem defasados.
- Um WIN que estoure o `int64` do saldo (acima de 92 quatrilhões) devolve o erro de `money`,
  sem código de falha.
- O claim de `PENDING` não espera idade mínima: se a instância original ainda processa, a
  duplicidade é inofensiva, pois o Service relê o estado sob o lock da carteira e o trigger
  impede nova transição de registro terminal.
- Uma `PENDING` confirmada que nunca possa ser aplicada (jogador ou moeda divergentes da
  carteira) vira `FAILED` com `INFRASTRUCTURE_FAILURE`, para auditoria, sem evento: não há
  evento definido para `FAILED`.
- Moedas limitadas a BRL, USD e EUR.
- O readiness de SQS verifica as filas de operações e de eventos (`GetQueueAttributes`); o
  atraso da outbox e o tamanho da DLQ vão só para as métricas, não para o readiness.
- Sem limite de requisições por provedor (rate limiting). `/metrics` é público no servidor de
  negócio; quem precisar restringi-lo deve fazê-lo na borda (rede ou proxy).
- **Reinício sob carga:** o `Stop` cancela o `ReceiveMessage` em andamento. Se o broker já
  tinha entregue uma mensagem naquela chamada, ela fica invisível até o `VisibilityTimeout` de
  60 s e só então volta (nada se perde nem duplica, mas essa mensagem atrasa até 1 minuto). Foi
  observado no teste de reinício: em cinco execuções, 199 de 200 mensagens já estavam
  concluídas no `Stop`, e a última chegou na hora ou 61 s depois. Esperar a recepção terminar
  eliminaria o atraso ao custo de até `WaitSeconds` (10 s) a mais no desligamento.
- As migrations aplicadas na partida não têm "down" automático: a reversão é manual, com a
  ferramenta `migrate` (ver README, Etapa 9).
- Os testes com o Keycloak real baixam a imagem (cerca de 500 MB) e sobem o IdP uma vez por
  execução (dezenas de segundos); ficam atrás da tag `integration`.

## 15. Casos de uso do Service (Implementado)

Todos usam só portas do Model e leem o relógio e os IDs por `port.Clock` e `port.IDGenerator`.

| Caso de uso | Onde | O que garante |
|---|---|---|
| Abertura de carteira | `WalletService.OpenWallet` | Carteira, `OPENING`, lançamento e dois eventos no mesmo commit; saldo zero cria só a carteira; jogador e moeda repetidos dão `ErrWalletExists`, inclusive em disputa (10 aberturas simultâneas criam uma) |
| Processar operação | `OperationService.Process` | Idempotência, lock só da carteira, BET, WIN, LOSS, REFUND, ROLLBACK, rejeição persistida, espera por referência, eventos na outbox, tudo atômico |
| Reconciliação | `WalletService.ReconcileWallet` | Saldo armazenado e reconstruído lidos na mesma instrução SQL; diferença = armazenado menos reconstruído; não altera nada |
| Resolver referências | `ReferenceResolver.ResolveDue` | Conclui `PENDING_REFERENCE` vencidas; várias instâncias não resolvem a mesma duas vezes |
| Retomar `PENDING` | `PendingResumer.ResumeAbandoned` | Uma transação aceita e nunca executada é assumida por outra instância quando o lease vence |
| Publicar eventos | `OutboxPublisher.PublishPending` | Ver seção 9 |
| Consultas | `QueryService` | Isolamento por provedor |

**Estrutura.** `OperationService`, `ReferenceResolver` e `PendingResumer` compartilham um
motor interno (`operationEngine`) cujo núcleo é `settle`: aplica a regra do tipo com a
carteira já bloqueada, seja a operação nova ou já persistida. Antes de abrir a transação
SQL, a entrada é validada e a `wager.Transaction` é montada em memória, então entrada
inválida não deixa rastro. A rejeição de negócio volta como resultado (`Status` `REJECTED`
com `FailureCode`) e não como erro; falhas transitórias voltam como erro reconhecível por
`service.IsTransient`, e repetir com a mesma chave é seguro.

**Consultas e isolamento (`QueryService`, `AccessScope`).** O escopo vem do token já validado
pelo Controller: um provedor ou o serviço interno; o escopo vazio é negado. Um provedor lendo
por ID a transação de outro provedor, ou a abertura interna, recebe
`ErrTransactionNotFound`, de modo que a existência não vaza. Um provedor que peça o caminho
`/providers/{outro}/...` recebe `ErrAccessDenied` (403). Replays também são isolados: a
chave é por provedor, então outro provedor reenviando a mesma chave cria a própria operação e
nunca recebe o resultado alheio. A listagem do ledger devolve páginas por cursor opaco; a
restrição dela ao serviço interno é do Controller.

**Retomada de `PENDING`.** Hoje o `Process` conclui tudo em um único commit, então uma
transação `PENDING` confirmada só existe com aceite assíncrono. O enunciado exige retomada
durável nesse caso, e o `PendingResumer` a garante: reivindica as `PENDING` sem lease ativo,
trava a carteira, recarrega e conclui com a mesma regra. Se outra instância já a levou
adiante, não faz nada; se ela nunca puder ser aplicada, vira `FAILED` (ver limitações).

## 16. Contrato HTTP e borda (Implementado)

Pacotes: `internal/controller/http` (roteamento, autorização, validação de formato) e
`internal/view/{response,presenter,problem}` (corpos e tradução de erros). O Controller só
chama o Service; nenhuma regra financeira nem SQL fica nele.

### Rotas e autorização

| Rota | Quem pode | Sucesso |
|---|---|---|
| `POST /wallets` | só serviço interno | **201** + `Location`, corpo da carteira |
| `GET /wallets/{walletId}` | só serviço interno | 200 |
| `GET /wallets/{walletId}/ledger?cursor=&limit=` | só serviço interno | 200, página com `nextCursor` |
| `POST /wallets/{walletId}/reconciliation` | só serviço interno | 200 |
| `POST /wagering/transactions` | só provedor autenticado | 200, 202 ou 422 |
| `GET /wagering/transactions/{transactionId}` | provedor (as próprias) ou interno | 200 |
| `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}` | provedor (só o próprio caminho) ou interno | 200 |
| `GET /health/live`, `GET /health/ready` | público | 200 / 503 |

Sem credencial ou com credencial inválida, **401** com `WWW-Authenticate: Bearer` e o mesmo
detalhe fixo (não revela por que foi recusada). Identidade sem permissão: **403**. O
`providerId` das operações vem **da identidade**; um `providerId` de corpo diferente é 403
`PROVIDER_MISMATCH`, e um ausente é 400. Em nenhum desses casos há efeito financeiro.

### Resposta de `POST /wagering/transactions`

| Situação | Status | Corpo |
|---|---|---|
| `PROCESSED` (inclusive replay) | **200** | `{transactionId, status, balance, idempotentReplay}` |
| `PENDING` ou `PENDING_REFERENCE` | **202** + `Location` | `{transactionId, status, idempotentReplay}`, sem `balance` |
| `REJECTED` (rejeição de negócio persistida) | **422** | `{transactionId, status, failureCode, correctable, idempotentReplay}` |
| `FAILED` (falha permanente de infraestrutura) | **500** | o resultado, com `INFRASTRUCTURE_FAILURE` |

O 422 é exclusivo de resultado persistido. Tudo que o cliente pode corrigir e reenviar com a
mesma chave é 400.

### Erros (`application/problem+json`)

Corpo: `{type, title, status, code, detail}`, com `type` = `urn:apostas:problem:{code}`. O
`detail` de 4xx traz a mensagem do erro de domínio; em 5xx e 503 é fixo, e o texto do erro
nunca vaza.

| Status | `code` |
|---|---|
| 400 | `INVALID_REQUEST`, `INVALID_JSON`, `MISSING_IDEMPOTENCY_KEY`, `INVALID_AMOUNT`, `UNKNOWN_CURRENCY`, `INVALID_KIND`, `KIND_NOT_ALLOWED`, `WALLET_PLAYER_MISMATCH`, `CURRENCY_MISMATCH`, `INVALID_CURSOR` |
| 401 | `UNAUTHENTICATED` (a identidade vem do JWT validado, seção 10) |
| 403 | `FORBIDDEN`, `PROVIDER_MISMATCH` |
| 404 | `WALLET_NOT_FOUND`, `TRANSACTION_NOT_FOUND`, `ROUTE_NOT_FOUND` |
| 405 | `METHOD_NOT_ALLOWED` (com `Allow`) |
| 409 | `IDEMPOTENCY_CONFLICT`, `WALLET_ALREADY_EXISTS` |
| 413 | `PAYLOAD_TOO_LARGE` |
| 415 | `UNSUPPORTED_MEDIA_TYPE` |
| 500 | `INTERNAL_ERROR` |
| 503 | `SERVICE_UNAVAILABLE` (com `Retry-After`) |

Uma indisponibilidade transitória (`port.ErrTransient` ou prazo expirado) tem precedência
sobre qualquer outro significado do erro e vira 503.

### Entrada

- `Idempotency-Key` é obrigatório e usado **exatamente como recebido**; o servidor nunca o
  substitui por outro calculado.
- JSON estrito: tipo de mídia `application/json`, no máximo 64 KiB, sem campos desconhecidos
  e sem dados depois do JSON. Valor monetário como **número** JSON é recusado (dinheiro é
  sempre texto). `amount` passa por `money.Parse`.
- `limit` do ledger deve estar entre 1 e 200 (senão 400, nunca ajuste silencioso).
- `X-Correlation-Id` é aceito se for ASCII imprimível, sem espaços e com até 128 caracteres;
  caso contrário é substituído por um valor gerado (32 hex). O valor final é devolvido no
  cabeçalho da resposta e chega ao `correlationId` dos eventos da outbox.

### Health checks

- `/health/live`: 200 `{"status":"UP"}` enquanto o processo responde; nunca consulta
  dependências.
- `/health/ready`: consulta cada dependência em paralelo, com prazo próprio (2 s por padrão).
  200 se todas estão `UP`; 503 se alguma está `DOWN`, e o corpo indica qual
  (`{"status":"DOWN","checks":{"postgres":"UP","sqs":"DOWN"}}`). Só `UP` ou `DOWN` aparecem:
  o texto do erro vai para o log. Uma dependência que trava, mesmo ignorando o contexto,
  conta como `DOWN` dentro do prazo.

### Middlewares e logs

Ordem, de fora para dentro: correlação, log de acesso, recuperação de panic.

- **Log de acesso:** uma linha por requisição, com `method`, `route` (o padrão da rota, como
  `/wagering/transactions`, nunca o caminho com IDs), `status`, `durationMs` e
  `correlationId`. Nunca registra corpo, valores monetários, tokens, a chave de idempotência
  nem IDs de carteira ou jogador.
- **Panic:** vira 500 genérico em `problem+json`, e o servidor continua atendendo. O log
  registra o tipo e a pilha, nunca o valor do panic. Cada resposta traz o `Cache-Control:
  no-store` e `X-Content-Type-Options: nosniff`.
- Erros 5xx e 503 são registrados com método, caminho, status e erro, sem payload financeiro.

### Evidência

Os testes de integração sobem a borda HTTP real sobre o Service e o Postgres reais. Cobrem:
o fluxo completo de um provedor (BET, WIN, REFUND, rejeição de um segundo REFUND,
consultas, ledger e reconciliação); entrada hostil (JSON malformado, números no lugar de
texto, negativos, notação científica, escala, corpo grande, tipo de mídia errado); a matriz
de autorização; o isolamento entre provedores; a indisponibilidade de banco como 503; 50
envios idênticos simultâneos e a disputa de duas apostas de 80,00 sobre 100,00 pela borda.
O readiness dos testes de `controller/http` usa um stand-in para o SQS; o readiness com o
broker real é verificado em `test/integration`.
