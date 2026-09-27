"""Package already built binaries and public docs; no runtime data."""
import hashlib
from pathlib import Path
import sys
import tarfile
import zipfile

root = Path(__file__).resolve().parent.parent
version = sys.argv[1] if len(sys.argv) > 1 else "v0.2.0"
if not version.startswith("v") or any(c not in "v0123456789." for c in version):
    raise SystemExit("invalid version")
out = root / "dist" / version
out.mkdir(exist_ok=True)
docs = ["README.md", "RELEASE_NOTES.md", "integrations/wfuseat_callback.py"]
if (root / "LICENSE").exists():
    docs.append("LICENSE")
files = []
for system in ("linux", "windows"):
    for edition in ("wfuseat", "wfuseat-tui", "wfuseat-server"):
        suffix = ".exe" if system == "windows" else ""
        binary = root / "dist" / f"{edition}-{system}-amd64{suffix}"
        members = [(binary, edition + suffix)] + [(root / n, n) for n in docs]
        name = f"{edition}-{version}-{system}-amd64"
        if system == "linux":
            archive = out / (name + ".tar.gz")
            with tarfile.open(archive, "w:gz") as tf:
                for src, dst in members:
                    tf.add(src, arcname=dst)
        else:
            archive = out / (name + ".zip")
            with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as zf:
                for src, dst in members:
                    zf.write(src, dst)
        files.append(archive)
(out / "SHA256SUMS.txt").write_text("".join(hashlib.sha256(f.read_bytes()).hexdigest() + "  " + f.name + "\n" for f in sorted(files)), encoding="utf-8")
print(f"Packaged {len(files)} archives and SHA256SUMS.txt")
