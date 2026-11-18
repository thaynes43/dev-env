#!/usr/bin/env python3
"""Qualify pinned native exports and flags offline in the built agent image."""
import os
from pathlib import Path
import subprocess
import tempfile

Path(os.environ["CODEX_HOME"]).mkdir(parents=True, exist_ok=True)
for args, required in [
    (["--help"], ["--remote", "--no-alt-screen", "--dangerously-bypass-approvals-and-sandbox"]),
    (["exec", "--help"], ["--json", "--model", "--dangerously-bypass-approvals-and-sandbox"]),
]:
    result = subprocess.run(["codex", *args], stdin=subprocess.DEVNULL,
                            capture_output=True, text=True, timeout=10, check=True)
    for flag in required:
        if flag not in result.stdout:
            raise SystemExit("Missing native flag: " + flag)
with tempfile.TemporaryDirectory(prefix="codex-schema-") as tmp:
    subprocess.run(["codex", "app-server", "generate-json-schema", "--experimental", "--out", tmp],
                   stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                   timeout=30, check=True)
    count = 0
    for folder in ["thread", "rc", "discovery"]:
        for fixture in sorted(Path("/fixtures", folder).glob("*.json")):
            if fixture.name == "provenance.json":
                continue
            generated = Path(tmp, "v2", fixture.name)
            if generated.read_bytes() != fixture.read_bytes():
                raise SystemExit("Native schema differs: " + fixture.name)
            count += 1
    if count != 16:
        raise SystemExit("Incomplete schema qualification")
    print("Native flags and 16 pinned schema exports match (offline)")
