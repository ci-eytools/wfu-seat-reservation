"""Build three editions for Linux/Windows amd64, without starting any services."""
import os
from pathlib import Path
import shutil
import subprocess

root = Path(__file__).resolve().parent.parent
go = shutil.which("go") or "/usr/local/go/bin/go"
dist = root / "dist"
dist.mkdir(exist_ok=True)
editions = [
    ("wfuseat", ".", []),
    ("wfuseat-tui", "./cmd/wfuseat-tui", ["-tags", "wfuseat_tui"]),
    ("wfuseat-server", "./cmd/wfuseat-server", []),
]
for system in ("linux", "windows"):
    for name, entry, tags in editions:
        suffix = ".exe" if system == "windows" else ""
        output = dist / f"{name}-{system}-amd64{suffix}"
        env = dict(os.environ, GOOS=system, GOARCH="amd64", CGO_ENABLED="0")
        subprocess.run([go, "build", "-trimpath", *tags, "-o", str(output), entry],
                       cwd=root, env=env, check=True)
        print(output.name, flush=True)
subprocess.run([go, "build", "-o", str(root / "wfuseat"), "."], cwd=root, check=True)
