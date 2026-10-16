---
description: Executa uma etapa de docs/PLANO.md com aprovação a cada lote de alterações
argument-hint: <número da etapa>
---
Prepare e execute a **Etapa $ARGUMENTS** de `docs/PLANO.md`, seguindo `docs/requisitos/protocolo-seguranca.md`.

Procedimento:
1. Releia os requisitos relevantes: `python3 scripts/rag/search.py "<tema da etapa>" -d desafio`, e confirme camada MVC (`-d arquitetura-mvc`) e estilo (`-d estilo-codigo`).
2. **Antes de alterar qualquer coisa**, apresente o plano da etapa dividido em lotes (3-6 arquivos relacionados cada) e, para o primeiro lote, a PROPOSTA DE ALTERAÇÃO no formato fixo. Aguarde o "sim".
3. Após o "sim", aplique **somente** aquele lote, com testes junto.
4. Rode `gofmt -l .`, `go vet ./...`, `golangci-lint run`, `go test ./...` (e `-race` se envolver concorrência). Se falhar: pare, mostre o erro, proponha a correção e pergunte. **Não reverta por conta própria.**
5. Relate em 3 linhas (feito / pendente / próximo lote) e peça aprovação para o próximo lote.
6. Atualize `ARCHITECTURE.md` só com autorização. Não faça commit.
