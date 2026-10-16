#!/usr/bin/env python3
"""Hook PreToolUse de segurança do Claude Code.

- Bash: bloqueia comandos destrutivos (exit 2) e pede confirmação nos arriscados ("ask").
- Edit/Write/NotebookEdit: bloqueia arquivos protegidos e salva backup do arquivo
  existente em .claude/backups/<UTC>/<caminho> antes de qualquer alteração.

Falha fechada: erro inesperado bloqueia a ação (nunca libera por engano).
"""
import json
import os
import re
import shutil
import sys
from datetime import datetime, timezone
from pathlib import Path

HARD_BLOCKS = [
    (r"\brm\s+(-\w*[rRf]\w*|--recursive|--force)\b", "rm recursivo/forçado"),
    (r"\bfind\b.*\s-delete\b", "find -delete"),
    (r"\bgit\s+reset\b", "git reset"),
    (r"\bgit\s+clean\b", "git clean"),
    (r"\bgit\s+push\b.*(--force|--force-with-lease|\s-f\b)", "git push forçado"),
    (r"\bgit\s+branch\b.*\s-D\b", "git branch -D"),
    (r"\bgit\s+stash\s+(drop|clear)\b", "git stash drop/clear"),
    (r"\bgit\s+commit\b.*--amend", "git commit --amend"),
    (r"\bgit\s+rebase\b", "git rebase"),
    (r"\bgit\s+revert\b", "git revert"),
    (r"\bgit\s+update-ref\b", "git update-ref"),
    (r"\bdocker\s+compose\s+down\b.*(-v\b|--volumes)", "docker compose down -v"),
    (r"\bdocker\s+(system|volume|container|image|network)\s+(prune|rm)\b", "docker prune/rm"),
    (r"\bdocker\s+rm\b", "docker rm"),
    (r"\bdrop\s+(database|table|schema)\b", "DROP no banco"),
    (r"\btruncate\b", "TRUNCATE"),
    (r"\bdelete\s+from\b(?!.*\bwhere\b)", "DELETE sem WHERE"),
    (r"\bsudo\b", "sudo"),
    (r"\bchmod\s+-R\b|\bchown\s+-R\b", "chmod/chown recursivo"),
    (r"\b(curl|wget)\b.*\|\s*(ba)?sh\b", "curl | sh"),
    (r"\bmkfs\b|\bdd\s+if=", "operação de disco"),
    (r">\s*/dev/sd", "escrita em disco"),
]

ASK_FIRST = [
    (r"\bgit\s+(checkout|restore|switch|stash|merge|cherry-pick|pull)\b", "git que altera árvore"),
    (r"\bgit\s+(add|commit|push|tag)\b", "git add/commit/push"),
    (r"\bmigrate\b.*\b(down|drop|force|goto)\b", "migration reversa"),
    (r"\bgo\s+mod\s+(tidy|edit|vendor)\b", "go mod altera go.mod/go.sum"),
    (r"\bgofmt\b.*\s-w\b|\bgoimports\b.*\s-w\b", "formatação in-place"),
    (r"\b(mv|cp|install|ln)\b", "mover/copiar arquivos"),
    (r"\bdocker\s+compose\s+(up|build|restart|stop|kill)\b", "docker compose altera ambiente"),
    (r"\b(sed|perl)\b.*\s-i\b", "edição in-place"),
    (r"\bgo\s+(get|install|generate)\b", "go get/install/generate"),
    (r"\b(pip|pip3|npm|brew|apt|apt-get)\s+install\b", "instalação de pacote"),
]

PROTECTED_FILES = [
    "docs/requisitos/desafio.md",
    ".claude/settings.json",
    ".claude/settings.local.json",
    "scripts/safety/guard.py",
]
PROTECTED_PREFIXES = [".git/", "scripts/safety/"]
PROTECTED_NAMES = {".env"}  # .env.example é permitido


def block(message):
    print(f"BLOQUEADO pelo guard de segurança: {message}", file=sys.stderr)
    print("Explique ao usuário e peça que ele execute/autorize manualmente.", file=sys.stderr)
    sys.exit(2)


def ask(reason):
    decision = {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "ask",
            "permissionDecisionReason": f"Segurança: {reason}. Confirme antes de executar.",
        }
    }
    print(json.dumps(decision))
    sys.exit(0)


def check_bash(command):
    lowered = command.lower()
    for pattern, label in HARD_BLOCKS:
        if re.search(pattern, lowered):
            block(f"{label}. Comando: {command[:160]}")
    for pattern, label in ASK_FIRST:
        if re.search(pattern, lowered):
            ask(label)


def relative(path, project):
    try:
        return str(Path(path).resolve().relative_to(project.resolve()))
    except ValueError:
        return None


def check_file(path, project):
    rel = relative(path, project)
    if rel is None:
        block(f"caminho fora do projeto: {path}")
    if rel in PROTECTED_FILES or Path(rel).name in PROTECTED_NAMES:
        block(f"arquivo protegido: {rel}")
    if any(rel.startswith(prefix) for prefix in PROTECTED_PREFIXES):
        block(f"pasta protegida: {rel}")
    return rel


def backup(path, rel, project):
    source = Path(path)
    if not source.exists() or rel.startswith(".claude/backups/"):
        return
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
    target = project / ".claude" / "backups" / stamp / rel
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, target)


def main():
    data = json.load(sys.stdin)
    tool = data.get("tool_name", "")
    tool_input = data.get("tool_input", {}) or {}
    project = Path(os.environ.get("CLAUDE_PROJECT_DIR") or data.get("cwd") or ".")

    if tool == "Bash":
        check_bash(tool_input.get("command", ""))
        return

    if tool in {"Edit", "Write", "NotebookEdit"}:
        path = tool_input.get("file_path") or tool_input.get("notebook_path")
        if not path:
            return
        rel = check_file(path, project)
        backup(path, rel, project)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as error:  # noqa: BLE001 — falha fechada
        print(f"BLOQUEADO: guard de segurança falhou ({error}).", file=sys.stderr)
        sys.exit(2)
