#!/usr/bin/env python3
"""Package only launch materials. Never include secrets or local state."""
import argparse
from pathlib import Path
import re
import tarfile

parser = argparse.ArgumentParser()
parser.add_argument("--surname", required=True)
parser.add_argument("--name", required=True)
args = parser.parse_args()
for value in (args.surname, args.name):
    if not re.fullmatch(r"[^\W\d_][\w-]{0,79}", value, re.UNICODE):
        parser.error("Name and surname must contain letters, digits or hyphens, without spaces or paths")
root = Path(__file__).resolve().parent.parent
stem = f"{args.surname}_{args.name}_DevOps_Тестовое"
output = root / "dist" / f"{stem}.tar.gz"
output.parent.mkdir(exist_ok=True)
files = ["README.md", "AI_REVIEW.md", "dagger.json", "go.mod", ".env.example", ".gitignore", ".dockerignore", "compose.app.yaml", "compose.monitoring.yaml"]
trees = [".dagger", "cmd", "internal", "config", "scripts", "docs"]
paths = [root / file for file in files]
for directory in trees:
    paths.extend(p for p in (root / directory).rglob("*") if p.is_file() and "__pycache__" not in p.parts)
with tarfile.open(output, "w:gz") as archive:
    for path in sorted(set(paths)):
        if path.is_symlink():
            raise SystemExit(f"Refusing symlink: {path}")
        archive.add(path, arcname=f"{stem}/{path.relative_to(root)}", recursive=False)
print(output)
