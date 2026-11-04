#!/usr/bin/env python3
"""Generate local secrets once; existing files are preserved."""
import os
from pathlib import Path
import secrets

root = Path(__file__).resolve().parent.parent / ".secrets"
root.mkdir(mode=0o700, exist_ok=True)
root.chmod(0o700)
for name in ("root", "kuma", "exporter", "grafana"):
    path = root / f"{name}-password"
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        print(f"Preserved {path.name}")
        continue
    with os.fdopen(fd, "w") as file:
        file.write(secrets.token_hex(32))
    print(f"Created {path.name}")
print("Secrets ready; values were not printed.")
