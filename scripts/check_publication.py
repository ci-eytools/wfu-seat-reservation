"""Inspect publishable Git content without printing secret values.

Default: tracked and non-ignored untracked files. --staged: exact index blobs.
This is a conservative local guard, not a complete secret-detection product.
"""
import argparse
import json
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
RULES = {
    "private-key": re.compile(rb"-----BEGIN (?:OPENSSH |RSA |EC |DSA )?PRIVATE KEY-----"),
    "github-token": re.compile(rb"(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})"),
    "jwt": re.compile(rb"eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}"),
    "personal-home-path": re.compile(rb"/home/[A-Za-z0-9_.-]+/|[CD]:[\\/]+Users[\\/]+[A-Za-z0-9_.-]+[\\/]"),
    "embedded-seat-token": re.compile(rb"wfw_token=[A-Za-z0-9_-]{16,}"),
}
BANNED_PARTS = {"work", "dist", ".ssh", ".venv", ".ipynb_checkpoints", "accounts", "remotes", "logs"}
BANNED_NAMES = {"session.json", "cookies.json", "client.json", "id_rsa", "id_ed25519"}
BANNED_SUFFIXES = {".db", ".sqlite", ".sqlite3", ".jsonl", ".log", ".pem", ".key", ".har", ".exe", ".pending"}

def inspect(name, data):
    """Return finding names and line numbers, never matched secret content."""
    path = PurePosixPath(name)
    findings = []
    if (set(path.parts) & BANNED_PARTS or path.name in BANNED_NAMES
            or path.suffix in BANNED_SUFFIXES
            or (path.name.startswith(".env") and path.name != ".env.example")
            or path.name.endswith((".db-wal", ".db-shm", ".sqlite3-wal", ".sqlite3-shm"))):
        findings.append(("runtime-or-credential-file", 1))
    if data.startswith((b"\x7fELF", b"MZ")):
        findings.append(("compiled-binary", 1))
    for label, pattern in RULES.items():
        for match in pattern.finditer(data):
            findings.append((label, data.count(b"\n", 0, match.start()) + 1))
    if path.suffix == ".ipynb":
        try:
            notebook = json.loads(data)
            if notebook.get("metadata", {}).get("widgets"):
                findings.append(("notebook-widget-state", 1))
            for number, cell in enumerate(notebook["cells"], 1):
                if cell.get("outputs") or cell.get("attachments") or cell.get("execution_count") is not None:
                    findings.append((f"notebook-executed-cell-{number}", 1))
        except (ValueError, KeyError, TypeError):
            findings.append(("invalid-notebook", 1))
    return findings

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--staged", action="store_true", help="scan the exact Git index")
    args = parser.parse_args()
    command = ["git", "ls-files", "-z", "--cached"]
    if not args.staged:
        command += ["--others", "--exclude-standard"]
    files = sorted(set(subprocess.check_output(command, cwd=ROOT).decode().split("\0")) - {""})
    failures = 0
    for name in files:
        if args.staged:
            data = subprocess.check_output(["git", "show", ":" + name], cwd=ROOT)
        else:
            path = ROOT / name
            if not path.exists():
                continue
            data = path.read_bytes()
        for kind, line in inspect(name, data):
            print(f"{name}:{line}: {kind}")
            failures += 1
    print(f"Checked {len(files)} files; {failures} findings. Matched values are never printed.")
    return 1 if failures else 0

if __name__ == "__main__":
    sys.exit(main())
