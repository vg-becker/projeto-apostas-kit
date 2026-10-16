# Guia de estilo: código vertical (Go)

"Vertical" aqui significa dois princípios que programadores experientes aplicam juntos:

1. **Leitura de cima para baixo** (vertical formatting do Clean Code): o arquivo se lê como uma notícia — o que é mais importante e quem chama vem primeiro, os detalhes vêm depois, na ordem em que são chamados.
2. **Um elemento por linha**: argumentos, campos, cláusulas SQL e encadeamentos quebrados na vertical, em vez de uma linha longa horizontal.

`gofmt` é obrigatório e vence qualquer preferência. Estas regras valem dentro do que o gofmt permite.

## Regras

1. **Linha com no máximo 100 caracteres.** Passou disso, quebre na vertical.
2. **Chamada com 3 ou mais argumentos, ou que passe de 80 colunas: um argumento por linha**, com vírgula final e `)` sozinho na última linha.
3. **Struct literal com mais de 2 campos: um campo por linha**, sempre com nomes de campo.
4. **Assinatura com mais de 2 parâmetros: um parâmetro por linha.**
5. **Encadeamento (builder, query, fluent): um elo por linha**, o ponto no início da linha.
6. **Guard clauses e retorno antecipado.** O caminho feliz fica alinhado à esquerda. Máximo de 2 níveis de indentação dentro de uma função. Evite `else` depois de `return`.
7. **Tratou o erro logo depois da chamada**, sem linha em branco entre a chamada e o `if err != nil`.
8. **Funções curtas, ~20 linhas, um nível de abstração.** Se precisa de comentário para explicar um bloco, extraia uma função com nome que diga o que ele faz.
9. **Regra do stepdown:** função chamadora acima, funções auxiliares abaixo, na ordem em que aparecem.
10. **Ordem no arquivo:** pacote, imports, constantes, tipos, construtor `New...`, métodos exportados, métodos não exportados.
11. **Linha em branco separa conceitos; não há linha em branco dentro de um bloco coeso.** Variável declarada o mais perto possível do primeiro uso.
12. **Nomes que revelam intenção, sem abreviação** (`transaction`, não `txn`). Exceções convencionais: `ctx`, `err`, `tx`, `id`, `ok`, `i` em laço curto.
13. **Comentário explica o porquê, nunca o quê.** Sem comentário que repete o código. Documentação em tipos e funções exportadas, em uma frase.
14. **Constantes e erros sentinela agrupados** em blocos `const (...)` e `var (...)`, um por linha.
15. **SQL em `const` com raw string, uma cláusula por linha**, indentada, com parâmetros posicionais (`$1`, `$2`).
16. **Sem `panic` para regra de negócio, sem variável global mutável, sem `init()`.**
17. **Testes:** table-driven, um caso por linha de tabela com `name` descritivo; padrão given/when/then separado por linhas em branco.

## Exemplos

### Assinatura e chamada

```go
// ruim: horizontal
func NewWagerTransaction(id ID, providerID string, externalID string, walletID ID, kind Kind, money money.Money) (*WagerTransaction, error) {

// bom: vertical
func NewWagerTransaction(
	id ID,
	providerID string,
	externalID string,
	walletID ID,
	kind Kind,
	amount money.Money,
) (*WagerTransaction, error) {
```

### Guard clauses

```go
// ruim: aninhado
func (wallet *Wallet) Debit(amount money.Money) error {
	if amount.Currency() == wallet.balance.Currency() {
		if amount.IsPositive() {
			if wallet.balance.GreaterOrEqual(amount) {
				// ...
			}
		}
	}
}

// bom: caminho feliz à esquerda
func (wallet *Wallet) Debit(amount money.Money) error {
	if amount.Currency() != wallet.balance.Currency() {
		return ErrCurrencyMismatch
	}

	if !amount.IsPositive() {
		return ErrAmountNotPositive
	}

	if wallet.balance.LessThan(amount) {
		return ErrInsufficientFunds
	}

	return wallet.applyDebit(amount)
}
```

### Stepdown no Service

```go
func (service *OperationService) Process(
	ctx context.Context,
	command ProcessOperationCommand,
) (Result, error) {
	hash, err := canonicalHash(command)
	if err != nil {
		return Result{}, err
	}

	return service.transactions.WithinTransaction(ctx, func(ctx context.Context) (Result, error) {
		return service.processInsideTransaction(ctx, command, hash)
	})
}

func (service *OperationService) processInsideTransaction(
	ctx context.Context,
	command ProcessOperationCommand,
	hash string,
) (Result, error) {
	existing, found, err := service.findReplay(ctx, command, hash)
	if err != nil {
		return Result{}, err
	}

	if found {
		return existing, nil
	}

	return service.applyNewOperation(ctx, command, hash)
}

func (service *OperationService) findReplay(/* ... */) { /* ... */ }

func (service *OperationService) applyNewOperation(/* ... */) { /* ... */ }
```

### Struct literal e SQL

```go
const lockWalletForUpdate = `
	SELECT id,
	       player_id,
	       currency,
	       balance_minor_units,
	       version
	  FROM wallets
	 WHERE id = $1
	   FOR UPDATE
`

response := response.Transaction{
	TransactionID:    result.TransactionID.String(),
	Status:           string(result.Status),
	Balance:          presenter.Money(result.Balance),
	IdempotentReplay: result.IdempotentReplay,
}
```

### Teste table-driven

```go
func TestParseMoney(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr error
	}{
		{name: "two decimals", input: "25.00", want: 2500},
		{name: "zero", input: "0.00", want: 0},
		{name: "too many decimals", input: "1.001", wantErr: ErrScaleExceeded},
		{name: "scientific notation", input: "1e2", wantErr: ErrInvalidFormat},
		{name: "empty", input: "", wantErr: ErrInvalidFormat},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.input, BRL)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}

			if err == nil && got.MinorUnits() != test.want {
				t.Fatalf("minor units = %d, want %d", got.MinorUnits(), test.want)
			}
		})
	}
}
```

## Checklist antes de concluir um arquivo

- Cabe em 100 colunas e foi passado pelo `gofmt`.
- Nenhuma função passa de ~20 linhas nem de 2 níveis de indentação.
- Quem chama está acima de quem é chamado.
- Nenhum comentário repete o código.
- `golangci-lint run` sem avisos (config em `.golangci.yml`).
