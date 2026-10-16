---
description: Propõe como desfazer algo, sem executar nada sem confirmação
argument-hint: <o que desfazer>
---
O usuário quer desfazer: $ARGUMENTS

Siga `docs/requisitos/protocolo-seguranca.md`. **Não execute nenhuma reversão agora.**

1. Rode `git status` e `git diff` (leitura) e liste `.claude/backups/` para encontrar a versão anterior dos arquivos envolvidos.
2. Apresente, arquivo por arquivo: o que existe hoje, qual backup/estado anterior seria restaurado, e o efeito esperado (incluindo imports e migrations afetados).
3. Proponha a forma mais **branda** de desfazer (ajuste incremental > restaurar um arquivo > reverter em lote).
4. Pergunte "Posso restaurar <arquivo>?" **um arquivo por vez** e só execute após o "sim" explícito.
5. Nunca use `git reset`, `git checkout/restore` em lote, `git clean` nem `rm -rf`.
