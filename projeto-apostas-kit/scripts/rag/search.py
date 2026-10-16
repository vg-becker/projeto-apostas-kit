#!/usr/bin/env python3
"""Consulta o RAG local.

Uso:
  python3 scripts/rag/search.py "fx.Lifecycle OnStop shutdown"
  python3 scripts/rag/search.py "SELECT FOR UPDATE SKIP LOCKED" -k 5 -d pg-locking
  python3 scripts/rag/search.py "visibility timeout" --full

-k N       quantidade de trechos (padrão 6)
-d DOC     filtra por documento (nome do arquivo sem extensão, ex.: fx-guia, go-spec, desafio)
--full     não corta o texto dos trechos
"""
import argparse
import re
import sqlite3
import sys
from pathlib import Path

DB = Path(__file__).resolve().parents[2] / "docs" / "rag" / "index.db"


def fts_query(q):
    # termos entre aspas (evita erro de sintaxe com . : ( ) etc.), unidos por OR
    terms = re.findall(r"[\w]+", q, flags=re.UNICODE)
    return " OR ".join(f'"{t}"' for t in terms) if terms else '""'


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("query")
    ap.add_argument("-k", type=int, default=6)
    ap.add_argument("-d", "--doc")
    ap.add_argument("--full", action="store_true")
    a = ap.parse_args()

    if not DB.exists():
        sys.exit("Índice não existe. Rode: python3 scripts/rag/build_index.py")
    con = sqlite3.connect(DB)
    sql = "SELECT doc, source, body, bm25(chunks) AS r FROM chunks WHERE chunks MATCH ?"
    params = [fts_query(a.query)]
    if a.doc:
        sql += " AND doc = ?"
        params.append(a.doc)
    sql += " ORDER BY r LIMIT ?"
    params.append(a.k)
    rows = con.execute(sql, params).fetchall()
    if not rows:
        print("Nenhum resultado. Tente outros termos (em inglês para docs de libs).")
        return
    for i, (doc, source, body, _) in enumerate(rows, 1):
        text = body if a.full else (body[:1400] + ("…" if len(body) > 1400 else ""))
        print(f"\n### [{i}] {doc} — {source}\n{text}")


if __name__ == "__main__":
    main()
