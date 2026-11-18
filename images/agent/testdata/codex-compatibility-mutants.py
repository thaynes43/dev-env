#!/usr/bin/env python3
"""Finite image-CI witnesses: each native guard test must fail without its guard."""
import os
from pathlib import Path
import subprocess

if not Path(os.environ["DEV_ENV_PINNED_NATIVE_BINARY"]).is_file():
    raise SystemExit("native guard witnesses require the image's complete package")

cases = [
    (
        "previous receipt admission",
        Path("internal/agentd/codex_host.go"),
        "(saved.CLI != managedCodexVersion && saved.CLI != previousPinnedCodexVersion)",
        "saved.CLI != managedCodexVersion",
        "^TestPinnedCodexPreviousHostStarts$/^receipt-0.160.1$/^package-0.160.1$",
        "previous host: starts=0 updates=0",
    ),
    (
        "native HOME classification",
        Path("internal/agentd/historyexport_layout.go"),
        '{"auth.lock", protocol.HistoryClassNotHistory, kindAny},',
        "",
        "^TestPinnedCodexHomeLayout$",
        "unclassified CODEX_HOME names: [auth.lock]",
    ),
]
for label, path, old, new, test, witness in cases:
    original = path.read_bytes()
    source = original.decode()
    if source.count(old) != 1:
        raise SystemExit("guard witness source no longer matches: " + label)
    try:
        path.write_text(source.replace(old, new, 1))
        result = subprocess.run(
            ["nice", "-n", "19", "go", "test", "-p", "2", "-timeout", "3m",
             "./internal/agentd", "-run", test, "-count=1"],
            stdin=subprocess.DEVNULL, capture_output=True, text=True,
            timeout=210, check=False,
        )
        if result.returncode == 0 or witness not in result.stdout:
            raise SystemExit("native test did not witness its removed guard: " + label)
    finally:
        path.write_bytes(original)
    print("Native guard witness: " + label + " failed as expected; restored")
