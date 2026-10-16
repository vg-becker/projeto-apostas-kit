# Protocolo de segurança (obrigatório em toda sessão)

Objetivo: não quebrar nada, não desfazer nada sem autorização, e **nunca alterar sem perguntar**.

## 1. Leitura é livre; alteração é sempre perguntada

- Livre: ler arquivos, buscar no RAG, `git status/diff/log`, `go build/vet/test`, `gofmt -l`, `golangci-lint run`.
- **Pergunte antes de qualquer alteração**: criar, editar, mover, copiar, apagar arquivo; `go mod tidy`; `gofmt -w`; instalar pacote; `docker compose up/build`; rodar migration; `git add/commit/push`; baixar docs (`fetch_docs.py`) ou reindexar (`build_index.py`).

## 2. Como perguntar (formato fixo)

Antes de cada lote de alteração, escreva e **espere o "sim" explícito do usuário**:

```
PROPOSTA DE ALTERAÇÃO
Arquivos: <lista de arquivos novos / modificados>
O que muda: <1-3 linhas>
Por quê: <1 linha>
Risco: <baixo | médio | alto> — <o que pode quebrar>
Como desfazer: <passo manual, sem executar>
Posso prosseguir?
```

- Um lote por vez: poucos arquivos relacionados, nunca "a etapa inteira de uma vez" sem aprovação.
- "Talvez", silêncio ou aprovação de outro lote **não** é autorização.
- Se a alteração mudar de escopo no meio, pare e pergunte de novo.

## 3. Nunca desfazer sem autorização

- Não reverter, apagar, sobrescrever ou "limpar" nada que já foi feito, mesmo que o teste falhe, o lint reclame ou você ache que ficou ruim.
- Se algo falhar depois de uma alteração aprovada: **pare, mostre o erro, proponha a correção** (de preferência um novo ajuste incremental) e espere autorização. Não reverta por conta própria.
- Proibido executar (o hook bloqueia): `rm -rf`, `git reset`, `git clean`, `git checkout/restore` para descartar mudanças, `git stash drop`, `git push --force`, `git rebase`, `git commit --amend`, `docker compose down -v`, `docker system prune`, `DROP/TRUNCATE`, `DELETE` sem `WHERE`, `sudo`.
- Se o usuário pedir para desfazer algo, use `/desfazer`: liste o que será revertido, mostre o backup correspondente e peça confirmação antes de cada arquivo.

## 4. Preferir mudanças aditivas e reversíveis

- Prefira **adicionar** a **substituir**. Edite o trecho mínimo (Edit) em vez de reescrever o arquivo inteiro (Write).
- **Migrations**: nunca editar migration já criada/aplicada; crie uma nova (`NNNN+1_*.up.sql` e `.down.sql`). Rodar `migrate down` exige autorização explícita.
- Não renomear nem mover pacotes/arquivos sem aprovar a lista completa do que muda (imports incluídos).
- Não alterar `go.mod`/`go.sum` à mão; dependências só via `go get`/`go mod tidy` com autorização.
- Não atualizar versão do Go nem de dependências sem pedir.

## 5. Rede de proteção automática (já configurada)

- **Backup**: antes de qualquer Edit/Write em arquivo existente, o hook copia o original para `.claude/backups/<data-hora>/<caminho>`. Restaurar é uma ação manual/autorizada.
- **Bloqueio**: comandos destrutivos são recusados pelo hook e pela lista `deny` do `settings.json`.
- **Arquivos protegidos** (só o usuário edita): `docs/requisitos/desafio.md`, `.claude/settings.json`, `scripts/safety/`, `.git/`, `.env`.
- **Segredos**: nunca ler nem imprimir `.env`; usar `.env.example` com valores locais fictícios.
- **Checkpoint no git**: antes de uma etapa grande, sugira ao usuário `git add -A && git commit -m "checkpoint antes da etapa N"`. Só ele decide e executa (ou autoriza você).

## 6. Verificação depois de cada alteração aprovada

Rode `gofmt -l .`, `go vet ./...`, `go test ./...` (e `-race` quando houver concorrência) e relate o resultado em 3 linhas. Falhou? Aplique a regra 3: pare e pergunte.
