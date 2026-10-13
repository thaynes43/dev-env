"""Hosted build provenance only. Never reads live configuration or credentials."""

import hashlib
import json
from pathlib import Path
import subprocess
import sys

root = Path(__file__).resolve().parents[2]
source = Path(__file__).resolve().parent
artifact = Path(sys.argv[1]).resolve()
checksum = lambda path: hashlib.sha256(path.read_bytes()).hexdigest()
manifest = {
    "schemaVersion": 1,
    "sourceCommit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip(),
    "toolchain": "go1.27.1",
    "celProfile": "cel-go0.30.0",
    "originalExpectedBundleSha256": "97beed7e1b37f009ed21f3ceace439b1e8498f0ad7b91b5ac606d787ad8ebed1",
    "sourceFiles": {str(path.relative_to(root)): checksum(path) for path in sorted(source.rglob("*")) if path.is_file()},
    "binaries": {name: {"sha256": checksum(artifact / name), "bytes": (artifact / name).stat().st_size}
                 for name in ("cel-linux-amd64", "cel-darwin-arm64", "get-darwin-arm64")},
    "realCapturePerformed": False,
}
(artifact / "manifest.json").write_text(json.dumps(manifest, sort_keys=True, indent=2) + "\n")
