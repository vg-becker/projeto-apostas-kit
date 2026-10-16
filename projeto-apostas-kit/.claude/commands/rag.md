---
description: Busca no RAG local e responde com base nos trechos encontrados
argument-hint: <pergunta ou termos>
---
Use o RAG local para responder: $ARGUMENTS

1. Rode `python3 scripts/rag/search.py "<termos em inglês e/ou português>" -k 8` (pode rodar mais de uma busca com termos diferentes e usar `-d <doc>` para filtrar).
2. Responda **somente** com o que os trechos sustentam, citando o documento e a fonte (URL) de cada afirmação.
3. Se os trechos não bastarem, diga o que faltou e sugira qual documentação adicionar em `scripts/rag/sources.json`.
