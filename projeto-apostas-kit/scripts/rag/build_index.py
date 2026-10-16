#!/usr/bin/env python3
"""Indexa docs/ (requisitos + docs baixadas) em um banco SQLite FTS5 (docs/rag/index.db).

Uso: python3 scripts/rag/build_index.py
Indexa: docs/requisitos/*.md, docs/rag/raw/*.txt, docs/rag/extra/*.md|txt (suas notas).
"""
import re
import sqlite3
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
DB = ROOT / "docs" / "rag" / "index.db"
CHUNK_CHARS = 1800
OVERLAP = 250

INPUTS = [
    (ROOT / "docs" / "requisitos", ("*.md", "*.txt")),
    (ROOT / "docs" / "rag" / "raw", ("*.txt", "*.md")),
    (ROOT / "docs" / "rag" / "extra", ("*.md", "*.txt")),
]


def chunk(text):
    paras = re.split(r"\n\s*\n", text)
    buf, out = "", []
    for p in paras:
        if len(buf) + len(p) > CHUNK_CHARS and buf:
            out.append(buf.strip())
            buf = buf[-OVERLAP:] + "\n\n"
        buf += p + "\n\n"
        while len(buf) > CHUNK_CHARS * 2:  # parágrafo gigante (ex.: pre/código)
            out.append(buf[:CHUNK_CHARS].strip())
            buf = buf[CHUNK_CHARS - OVERLAP:]
    if buf.strip():
        out.append(buf.strip())
    return out


def main():
    DB.parent.mkdir(parents=True, exist_ok=True)
    if DB.exists():
        DB.unlink()
    con = sqlite3.connect(DB)
    con.execute(
        "CREATE VIRTUAL TABLE chunks USING fts5("
        "doc UNINDEXED, source UNINDEXED, body, tokenize='unicode61 remove_diacritics 2')"
    )
    n = 0
    for folder, patterns in INPUTS:
        if not folder.exists():
            continue
        for pat in patterns:
            for f in sorted(folder.glob(pat)):
                raw = f.read_text(encoding="utf-8", errors="replace")
                parts = re.split(r"=====SOURCE (\S+)=====", raw)
                if len(parts) > 1:  # arquivo vindo do fetch_docs
                    pairs = [(parts[i], parts[i + 1]) for i in range(1, len(parts), 2)]
                else:
                    pairs = [(str(f.relative_to(ROOT)), raw)]
                for source, text in pairs:
                    for c in chunk(text):
                        con.execute(
                            "INSERT INTO chunks(doc, source, body) VALUES (?,?,?)",
                            (f.stem, source, c),
                        )
                        n += 1
    con.commit()
    con.close()
    print(f"Índice criado: {n} trechos em {DB.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
