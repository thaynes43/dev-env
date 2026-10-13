"""Hosted offline provenance only; never opens credentials or executes the client."""

import hashlib
import json
from pathlib import Path
import subprocess
import sys

source = Path(__file__).resolve().parent
root = source.parents[1]
artifact = Path(sys.argv[1]).resolve()
checksum = lambda path: hashlib.sha256(path.read_bytes()).hexdigest()
binary = artifact / "retain-darwin-arm64"
manifest = {
    "schemaVersion": 1,
    "sourceCommit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip(),
    "toolchain": "go1.27.1",
    "sourceFiles": {str(path.relative_to(root)): checksum(path) for path in sorted(source.rglob("*")) if path.is_file()},
    "workflowSha256": checksum(root / ".github/workflows/h7-retain-action.yml"),
    "binaries": {binary.name: {"sha256": checksum(binary), "bytes": binary.stat().st_size}},
    "target": "/api/v1/persistentvolumes/pvc-ec80a337-740f-49d3-8b27-171bb6eed5af",
    "patchSha256": "70d829eb6049d81a3c52be451c3b4b9502d8e34bbb9e43239f47772f19e9fd47",
    "ownerDecisionRequiredAfterIndependentExecutableReview": True,
    "approvalFlagIsProceduralProvenanceNotCryptographicAuthorization": True,
    "requestBudgetIsPerProcessAndDoesNotAuthorizeReexecution": True,
    "realClientExecuted": False,
    "maxRequests": 3,
    "maxPatchAttempts": 1,
    "processWallSeconds": 30,
    "requestSeconds": 5,
    "noRetryOrRestartAfterFailureOrAmbiguity": True,
    "missingOrTruncatedReportAfterDispatchIsUncertain": True,
}
(artifact / "manifest.json").write_text(json.dumps(manifest, sort_keys=True, indent=2) + "\n")
