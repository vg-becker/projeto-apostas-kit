#!/usr/bin/env python3
"""Baixa as documentações listadas em sources.json e salva como texto em docs/rag/raw/.

Uso:
  python3 scripts/rag/fetch_docs.py            # baixa tudo
  python3 scripts/rag/fetch_docs.py fx-guia    # baixa só uma fonte

Somente biblioteca padrão do Python (sem pip).
"""
import json
import re
import sys
import time
import urllib.parse
import urllib.request
from html.parser import HTMLParser
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SOURCES = ROOT / "scripts" / "rag" / "sources.json"
OUT = ROOT / "docs" / "rag" / "raw"
UA = "Mozilla/5.0 (rag-fetch; +local)"


class TextExtractor(HTMLParser):
    SKIP = {"script", "style", "nav", "footer", "header", "noscript", "svg", "form"}
    BLOCK = {"p", "div", "br", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "pre", "section", "article", "table"}

    def __init__(self):
        super().__init__()
        self.parts, self.links, self._skip = [], [], 0

    def handle_starttag(self, tag, attrs):
        if tag in self.SKIP:
            self._skip += 1
        if tag in self.BLOCK:
            self.parts.append("\n")
        if tag in {"h1", "h2", "h3", "h4"}:
            self.parts.append("\n" + "#" * int(tag[1]) + " ")
        if tag == "a":
            href = dict(attrs).get("href")
            if href:
                self.links.append(href)

    def handle_endtag(self, tag):
        if tag in self.SKIP and self._skip:
            self._skip -= 1
        if tag in self.BLOCK:
            self.parts.append("\n")

    def handle_data(self, data):
        if not self._skip:
            self.parts.append(data)

    def text(self):
        t = "".join(self.parts)
        t = re.sub(r"[ \t]+", " ", t)
        t = re.sub(r"\n\s*\n\s*\n+", "\n\n", t)
        return t.strip()


def get(url):
    req = urllib.request.Request(url, headers={"User-Agent": UA})
    with urllib.request.urlopen(req, timeout=40) as r:
        ctype = r.headers.get("Content-Type", "")
        body = r.read().decode("utf-8", errors="replace")
    return body, ctype


def norm(url):
    return url.split("#")[0]


def crawl(src):
    start, prefix = src["start"], src["prefix"]
    limit = src.get("max_pages", 1)
    include = re.compile(src["include"]) if src.get("include") else None
    seen, queue, pages = set(), [start], []
    while queue and len(pages) < limit:
        url = norm(queue.pop(0))
        if url in seen:
            continue
        seen.add(url)
        try:
            body, ctype = get(url)
        except Exception as e:  # noqa: BLE001
            print(f"   ! falha {url}: {e}")
            continue
        if "html" in ctype:
            p = TextExtractor()
            p.feed(body)
            text = p.text()
            for href in p.links:
                nxt = norm(urllib.parse.urljoin(url, href))
                if nxt.startswith(prefix) and nxt not in seen and (not include or include.search(nxt)):
                    queue.append(nxt)
        else:
            text = body  # markdown / texto puro
        if len(text) > 300:
            pages.append((url, text))
        time.sleep(0.4)
    return pages


def main():
    only = set(sys.argv[1:])
    sources = json.loads(SOURCES.read_text(encoding="utf-8"))
    OUT.mkdir(parents=True, exist_ok=True)
    total = 0
    for src in sources:
        if only and src["name"] not in only:
            continue
        print(f"-> {src['name']}")
        pages = crawl(src)
        if not pages:
            print("   (nada baixado)")
            continue
        out = OUT / f"{src['name']}.txt"
        with out.open("w", encoding="utf-8") as f:
            for url, text in pages:
                f.write(f"\n\n=====SOURCE {url}=====\n\n{text}\n")
        total += len(pages)
        print(f"   {len(pages)} página(s) -> {out.relative_to(ROOT)}")
    print(f"\nOK: {total} páginas. Agora rode: python3 scripts/rag/build_index.py")


if __name__ == "__main__":
    main()
