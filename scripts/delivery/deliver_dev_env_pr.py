#!/usr/bin/env python3
"""Deliver one dev-env pull request through the pinned haynes-ops guard.

The wrapper checks the shared checkout, then invokes
``scripts/upgrade-agent/deliver_reviewed_pr.py`` once. That guard runs this
file's ``preflight`` subcommand and, only after it exits 0, performs the
source-only squash. This file does not merge, reconcile, release, or deploy.

Evidence is a coordinator attestation. It does not grant authority. Native
evidence provenance stays Unknown: the receipt hash is preserved, not treated
as a signature. Action-specific holds stay in force.

Subprocess limits match the pinned guard (120s for one gh call, a 1..600s
preflight window). They are not a story deadline. A timed-out or unknown
guard result is not replayed.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
from pathlib import Path

REPO = "thaynes43/dev-env"
PIN_SCHEMA = "dev-env.shared-guard-pin/v1"
EVIDENCE_SCHEMA = "dev-env.delivery-evidence/v1"
AUDIT_SCHEMA = "dev-env.delivery-audit/v1"
PINNED_REPOSITORY = "thaynes43/haynes-ops"
PINNED_COMMIT = "63f6b2a5fc54cde0f3df4b0a8456320f7ae12285"
PINNED_PATH = "scripts/upgrade-agent/deliver_reviewed_pr.py"
PINNED_SHA256 = "1e451ca00c80d176d8f08a142f40cb491b94e8936610389e7c61137db90a21e0"
PINNED_SOURCE_PR = 4284
PINNED_REVIEWED_HEAD = "61fc2ebb71302442eab762f0b9a243fdbd7eab6b"
PIN_PATH = Path(__file__).with_name("shared-guard-pin.json")

# Pinned guard bounds. GH_TIMEOUT_S is 120. Preflight performs two gh reads.
# The guard then reads, merges, and reads again. Not a story deadline.
GUARD_GH_TIMEOUT_S = 120
PREFLIGHT_TIMEOUT_S = GUARD_GH_TIMEOUT_S * 2
GUARD_INVOCATION_TIMEOUT_S = PREFLIGHT_TIMEOUT_S + GUARD_GH_TIMEOUT_S * 3
EVIDENCE_LIMIT = 128 * 1024
AUDIT_LIMIT = 128 * 1024
MAX_CHECKS = 80
MAX_FINDINGS = 40

SHA_RE = re.compile(r"[0-9a-f]{40}")
SHA256_RE = re.compile(r"[0-9a-f]{64}")
REQUIRED_CHECK = "CI - Success"
ADVISORY_CHECKS = frozenset(
    {
        "Claude advisory review",
        "Claude assistant (@claude)",
    }
)
# Skipped only when the workflow's own condition does not apply on a pull request.
CONDITIONAL_SKIP_CHECKS = frozenset(
    {
        "Image (agent)",
        "Image (Git wait fixture)",
        "Claude advisory review",
        "Claude assistant (@claude)",
        "H7 offline checker and Mac binaries",
        "Portal offline presentation fixtures",
        "H7 offline Retain action and Mac binary",
        "Exact native continuation diagnostic",
    }
)
RUNNING_STATUS = {"QUEUED", "IN_PROGRESS", "PENDING", "WAITING", "REQUESTED"}
NONZERO_CONCLUSIONS = {
    "FAILURE",
    "CANCELLED",
    "TIMED_OUT",
    "ACTION_REQUIRED",
    "STALE",
    "STARTUP_FAILURE",
}
VAGUE_REASONS = {
    "unknown",
    "unresolved",
    "missing",
    "n/a",
    "na",
    "none",
    "not applicable",
    "not-applicable",
}
SECRET_MARKERS = ("token", "bearer", "authorization", "password", "secret")
EVIDENCE_KEYS = {
    "schema",
    "repository",
    "pullRequest",
    "reviewedCommit",
    "nativeVerdict",
    "findings",
    "scopeAuthorization",
    "pendingHolds",
    "writableImplementer",
    "receipt",
}
FINDING_KEYS = {"id", "disposition", "reason"}
SCOPE_KEYS = {"reference", "scope"}
RECEIPT_KEYS = {"hash", "source"}
ALLOWED_VERDICTS = {"PASS", "PASS-with-minors"}
ALLOWED_DISPOSITIONS = {"resolved", "not-applicable"}
PIN_RECORD = {
    "schema": PIN_SCHEMA,
    "repository": PINNED_REPOSITORY,
    "commit": PINNED_COMMIT,
    "path": PINNED_PATH,
    "sha256": PINNED_SHA256,
    "sourcePR": PINNED_SOURCE_PR,
    "reviewedHead": PINNED_REVIEWED_HEAD,
    "provenance": "Unknown",
}


class DeliveryError(Exception):
    def __init__(self, message: str, code: int) -> None:
        super().__init__(message)
        self.code = code


def python_command() -> list[str]:
    # This wrapper, the pinned helper, and its preflight must not import a
    # sibling module or a module supplied through PYTHONPATH.  ``-I`` also
    # makes the script directory unavailable to imports.  Callers must use
    # this same isolated invocation for the top-level wrapper: a wrapper that
    # has already been imported cannot retroactively isolate those imports.
    command = [sys.executable, "-I", "-B"]
    if sys.flags.optimize:
        command.append("-" + ("O" * sys.flags.optimize))
    return command


def tainted(value: str) -> bool:
    lowered = value.lower()
    return any(marker in lowered for marker in SECRET_MARKERS)


def reject_duplicate_keys(pairs: list[tuple[str, object]]) -> dict[str, object]:
    document: dict[str, object] = {}
    for key, value in pairs:
        if key in document:
            raise ValueError("duplicate key")
        document[key] = value
    return document


def parse_json_document(text: str) -> dict[str, object]:
    decoder = json.JSONDecoder(object_pairs_hook=reject_duplicate_keys)
    try:
        value, end = decoder.raw_decode(text)
    except (json.JSONDecodeError, ValueError) as exc:
        raise DeliveryError("evidence is unparseable", 2) from exc
    if text[end:].strip():
        raise DeliveryError("evidence has trailing data", 2)
    if not isinstance(value, dict):
        raise DeliveryError("evidence is not an object", 2)
    return value


def write_audit(path: Path, document: dict[str, object]) -> None:
    payload = (json.dumps(document, indent=2, sort_keys=True) + "\n").encode("utf-8")
    if len(payload) > AUDIT_LIMIT:
        raise OSError("audit document is too large")
    parent = path.parent
    if not parent.is_dir():
        raise OSError("audit directory is missing")
    temporary = path.with_name(path.name + ".tmp")
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o644)
    try:
        os.write(fd, payload)
        os.fsync(fd)
    except OSError:
        os.close(fd)
        try:
            os.unlink(temporary)
        except OSError:
            pass
        raise
    os.close(fd)
    try:
        os.replace(temporary, path)
    except OSError:
        try:
            os.unlink(temporary)
        except OSError:
            pass
        raise


def git_env() -> dict[str, str]:
    env = os.environ.copy()
    env["GIT_PAGER"] = "cat"
    env["GIT_TERMINAL_PROMPT"] = "0"
    env["GCM_INTERACTIVE"] = "never"
    return env


def run_git(checkout: Path, args: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["git", "-C", str(checkout), *args],
        check=False,
        capture_output=True,
        text=True,
        timeout=GUARD_GH_TIMEOUT_S,
        stdin=subprocess.DEVNULL,
        env=git_env(),
    )


def run_gh(args: list[str]) -> tuple[str | None, str | None]:
    """Return (stdout, refusal). Refusal is set when the read cannot be used."""
    try:
        proc = subprocess.run(
            ["gh", *args],
            check=False,
            capture_output=True,
            text=True,
            errors="replace",
            timeout=GUARD_GH_TIMEOUT_S,
            stdin=subprocess.DEVNULL,
        )
    except subprocess.TimeoutExpired:
        return None, "timed out"
    except OSError:
        return None, "could not be started"
    if proc.returncode != 0:
        return None, "failed"
    return proc.stdout, None


def helper_path(checkout: Path) -> Path | None:
    root = checkout.resolve()
    candidate = (root / PINNED_PATH).resolve()
    if candidate != root and root not in candidate.parents:
        return None
    if not candidate.is_file():
        return None
    return candidate


def load_pin_matches() -> bool:
    try:
        text = PIN_PATH.read_text(encoding="utf-8")
        document = json.loads(text)
    except (OSError, json.JSONDecodeError, UnicodeError):
        return False
    return document == PIN_RECORD


def observe_pin(checkout_text: str) -> dict[str, object]:
    observed: dict[str, object] = {
        "expectedCommit": PINNED_COMMIT,
        "observedHead": None,
        "expectedSha256": PINNED_SHA256,
        "observedSha256": None,
        "dirty": None,
        "recordMismatch": not load_pin_matches(),
        "error": None,
    }
    if observed["recordMismatch"]:
        return observed
    checkout = Path(checkout_text)
    try:
        root = checkout.resolve()
    except OSError:
        observed["error"] = "unusable"
        return observed
    if not root.is_dir():
        observed["error"] = "unusable"
        return observed
    try:
        head = run_git(root, ["rev-parse", "--verify", "HEAD"])
        status = run_git(
            root,
            [
                "-c",
                "core.excludesFile=/dev/null",
                "-c",
                "core.attributesFile=/dev/null",
                "status",
                "--porcelain",
                "--untracked-files=all",
            ],
        )
    except subprocess.TimeoutExpired:
        observed["error"] = "unusable"
        return observed
    if head.returncode != 0 or status.returncode != 0:
        observed["error"] = "unusable"
        return observed
    head_text = head.stdout.strip()
    if SHA_RE.fullmatch(head_text):
        observed["observedHead"] = head_text
    else:
        observed["error"] = "unusable"
        return observed
    observed["dirty"] = bool(status.stdout.strip())
    helper = helper_path(root)
    if helper is None:
        return observed
    try:
        digest = hashlib.sha256(helper.read_bytes()).hexdigest()
    except OSError:
        observed["error"] = "unusable"
        return observed
    observed["observedSha256"] = digest
    return observed


def pin_refusal(observed: dict[str, object]) -> str | None:
    if observed.get("recordMismatch") is True:
        return "shared guard pin record mismatch"
    if observed.get("error"):
        return "shared guard checkout is unusable"
    if observed.get("observedHead") != PINNED_COMMIT:
        return "shared guard head mismatch"
    if observed.get("dirty") is not False:
        return "shared guard checkout is dirty"
    if observed.get("observedSha256") != PINNED_SHA256:
        return "shared guard hash mismatch"
    return None


def empty_audit(args: argparse.Namespace, stage: str) -> dict[str, object]:
    pull_request = args.pr if type(args.pr) is int and args.pr > 0 else None
    return {
        "schema": AUDIT_SCHEMA,
        "stage": stage,
        "decision": "pending-evaluation",
        "reason": None,
        "repository": REPO,
        "pullRequest": pull_request,
        "head": args.head,
        "expectedMain": args.expected_main,
        "observedMain": None,
        "nativeEvidenceProvenance": "Unknown",
        "configurationProvenance": "Unknown",
        "receiptHash": None,
        "receiptSource": None,
        "evidenceSha256": None,
        "pin": None,
        "pullRequestObservation": None,
        "guardExit": None,
        "replayed": False,
    }


def public_check(item: dict[str, object]) -> dict[str, object]:
    name = item.get("name") if isinstance(item.get("name"), str) else item.get("context")
    status = item.get("status") if isinstance(item.get("status"), str) else ""
    conclusion = item.get("conclusion") if isinstance(item.get("conclusion"), str) else ""
    state = item.get("state") if isinstance(item.get("state"), str) else ""
    if not isinstance(name, str) or tainted(name):
        name = "redacted" if isinstance(name, str) else ""
    return {
        "name": name[:180],
        "status": status[:40],
        "conclusion": conclusion[:40],
        "state": state[:40],
    }


def check_outcome(item: dict[str, object]) -> str:
    status = str(item.get("status") or "").upper()
    conclusion = str(item.get("conclusion") or "").upper()
    state = str(item.get("state") or "").upper()
    if status:
        if status in RUNNING_STATUS:
            return "running"
        if status != "COMPLETED":
            return "unknown"
        if conclusion == "SUCCESS":
            return "green"
        if conclusion == "SKIPPED":
            return "skipped"
        if conclusion in NONZERO_CONCLUSIONS:
            return "nonzero"
        return "unknown"
    if state == "SUCCESS":
        return "green"
    if state in {"PENDING", "EXPECTED"}:
        return "running"
    if state in {"FAILURE", "ERROR"}:
        return "nonzero"
    return "unknown"


def check_name(item: dict[str, object]) -> str:
    name = item.get("name")
    if not isinstance(name, str) or not name:
        name = item.get("context")
    if not isinstance(name, str):
        return ""
    return name


def observe_pull_request(pull_request: int) -> dict[str, object]:
    stdout, failure = run_gh(
        [
            "pr",
            "view",
            str(pull_request),
            "--repo",
            REPO,
            "--json",
            "state,isDraft,headRefOid,baseRefName,mergeable,mergeStateStatus,statusCheckRollup",
        ]
    )
    observed: dict[str, object] = {
        "error": failure,
        "state": None,
        "isDraft": None,
        "headRefOid": None,
        "baseRefName": None,
        "mergeable": None,
        "mergeStateStatus": None,
        "checks": [],
    }
    if failure or stdout is None:
        return observed
    try:
        payload = json.loads(stdout)
    except json.JSONDecodeError:
        observed["error"] = "unparseable"
        return observed
    if not isinstance(payload, dict):
        observed["error"] = "unparseable"
        return observed
    for field in ("state", "headRefOid", "baseRefName", "mergeable", "mergeStateStatus"):
        value = payload.get(field)
        if isinstance(value, str):
            observed[field] = value
    if type(payload.get("isDraft")) is bool:
        observed["isDraft"] = payload.get("isDraft")
    rollup = payload.get("statusCheckRollup")
    checks: list[dict[str, object]] = []
    if isinstance(rollup, list) and len(rollup) <= MAX_CHECKS:
        for item in rollup:
            if isinstance(item, dict):
                checks.append(public_check(item))
        observed["checks"] = checks
        observed["rollupComplete"] = True
    else:
        observed["rollupComplete"] = False
    return observed


def observe_main() -> dict[str, object]:
    stdout, failure = run_gh(
        ["api", f"repos/{REPO}/commits/main", "--jq", ".sha"]
    )
    if failure or stdout is None:
        return {"error": failure or "failed", "sha": None}
    sha = stdout.strip()
    if not SHA_RE.fullmatch(sha):
        return {"error": "unparseable", "sha": None}
    return {"error": None, "sha": sha}


def peek_evidence(path_text: str) -> dict[str, object]:
    peeked: dict[str, object] = {
        "sha256": None,
        "oversize": False,
        "unreadable": False,
        "missing": False,
    }
    path = Path(path_text)
    if not path.is_file():
        peeked["missing"] = True
        return peeked
    try:
        data = path.read_bytes()
    except OSError:
        peeked["unreadable"] = True
        return peeked
    if len(data) > EVIDENCE_LIMIT:
        peeked["oversize"] = True
        return peeked
    peeked["sha256"] = hashlib.sha256(data).hexdigest()
    peeked["text"] = data.decode("utf-8", errors="replace")
    return peeked


def concrete_text(value: object, minimum: int, maximum: int) -> bool:
    if not isinstance(value, str):
        return False
    if value.strip() != value or len(value) < minimum or len(value) > maximum:
        return False
    if any(ord(char) < 32 for char in value):
        return False
    if tainted(value) or value.casefold() in VAGUE_REASONS or value.casefold() == "unknown":
        return False
    return True


def finding_refusal(finding: object) -> str | None:
    if not isinstance(finding, dict):
        return "finding is unknown"
    if set(finding) - FINDING_KEYS:
        return "finding is unknown"
    if "id" not in finding:
        return "finding is unknown"
    identifier = finding.get("id")
    if not isinstance(identifier, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:-]{0,63}", identifier):
        return "finding is unknown"
    if identifier.casefold() == "unknown":
        return "finding is unknown"
    if "disposition" not in finding or finding.get("disposition") is None:
        return "finding disposition is missing"
    disposition = finding.get("disposition")
    if disposition == "unresolved":
        return "finding disposition is unresolved"
    if disposition not in ALLOWED_DISPOSITIONS:
        return "finding disposition is unknown"
    reason = finding.get("reason")
    if reason is None or reason == "":
        return "finding reason is missing"
    if not concrete_text(reason, 12, 240):
        return "finding reason is unknown"
    return None


def evidence_refusal(document: dict[str, object], args: argparse.Namespace) -> str | None:
    if set(document) - EVIDENCE_KEYS:
        return "evidence field is unknown"
    if document.get("schema") != EVIDENCE_SCHEMA:
        return "evidence schema is unknown"
    if document.get("repository") != REPO:
        return "repository does not match"
    pull_request = document.get("pullRequest")
    if type(pull_request) is not int or pull_request != args.pr:
        return "pull request does not match"
    reviewed = document.get("reviewedCommit")
    if not isinstance(reviewed, str) or not SHA_RE.fullmatch(reviewed) or reviewed != args.head:
        return "reviewed commit does not match head"
    verdict = document.get("nativeVerdict")
    if verdict is None or verdict == "":
        return "native verdict is missing"
    if verdict not in ALLOWED_VERDICTS:
        return "native verdict is unknown"
    findings = document.get("findings")
    if findings is None:
        return "findings are missing"
    if not isinstance(findings, list):
        return "findings are unknown"
    if len(findings) > MAX_FINDINGS:
        return "findings are unknown"
    for finding in findings:
        reason = finding_refusal(finding)
        if reason:
            return reason
    if "pendingHolds" not in document or document.get("pendingHolds") is None:
        return "pending holds are absent"
    holds = document.get("pendingHolds")
    if not isinstance(holds, list):
        return "pending holds are unknown"
    if len(holds) != 0:
        return "pending holds are active"
    if "writableImplementer" not in document or document.get("writableImplementer") is None:
        return "writable implementer is absent"
    writer = document.get("writableImplementer")
    if type(writer) is not bool:
        return "writable implementer is unknown"
    if writer is not False:
        return "writable implementer is active"
    scope = document.get("scopeAuthorization")
    if scope is None:
        return "scope authorization is absent"
    if not isinstance(scope, dict) or set(scope) - SCOPE_KEYS:
        return "scope authorization is unknown"
    if scope.get("scope") != "source-only":
        return "scope is not source-only"
    reference = scope.get("reference")
    if not concrete_text(reference, 8, 200):
        return "scope reference is unknown"
    receipt = document.get("receipt")
    if receipt is None:
        return "receipt is absent"
    if not isinstance(receipt, dict) or set(receipt) - RECEIPT_KEYS:
        return "receipt is unknown"
    receipt_hash = receipt.get("hash")
    if not isinstance(receipt_hash, str) or not SHA256_RE.fullmatch(receipt_hash):
        return "receipt is unknown"
    source = receipt.get("source")
    if not concrete_text(source, 8, 200):
        return "receipt is unknown"
    return None


def grouped_checks(checks: object) -> tuple[dict[str, set[str]] | None, str | None]:
    if not isinstance(checks, list):
        return None, "unknown required CI payload"
    grouped: dict[str, set[str]] = {}
    for item in checks:
        if not isinstance(item, dict):
            return None, "unknown required CI payload"
        name = check_name(item)
        if not name or tainted(name):
            return None, "unknown check name"
        grouped.setdefault(name, set()).add(check_outcome(item))
    return grouped, None


def checks_refusal(observed: dict[str, object]) -> tuple[str | None, int]:
    if observed.get("rollupComplete") is not True:
        return "required check missing: CI - Success", 0
    grouped, problem = grouped_checks(observed.get("checks"))
    if problem or grouped is None:
        return problem or "unknown required CI payload", 0
    required = grouped.get(REQUIRED_CHECK)
    if not required:
        return f"required check missing: {REQUIRED_CHECK}", 0
    if required != {"green"}:
        if len(required) != 1:
            return f"conflicting duplicate check: {REQUIRED_CHECK}", 0
        only = next(iter(required))
        if only == "running":
            return f"required check running: {REQUIRED_CHECK}", 0
        if only == "nonzero":
            return f"required check failed: {REQUIRED_CHECK}", 0
        if only == "skipped":
            return f"required check skipped: {REQUIRED_CHECK}", 0
        return f"required check unknown: {REQUIRED_CHECK}", 0
    advisory_failures = 0
    for name, outcomes in grouped.items():
        if name == REQUIRED_CHECK:
            continue
        # Normalize only outcomes already exempted for this named optional
        # advisory. Keep every observed run in the audit and count any failure.
        allowed_advisory = {"green", "nonzero"}
        if name in CONDITIONAL_SKIP_CHECKS:
            allowed_advisory.add("skipped")
        if name in ADVISORY_CHECKS and outcomes <= allowed_advisory:
            advisory_failures += int("nonzero" in outcomes)
            continue
        if len(outcomes) != 1:
            return f"conflicting duplicate check: {name}", advisory_failures
        outcome = next(iter(outcomes))
        if outcome == "green":
            continue
        if outcome == "nonzero" and name in ADVISORY_CHECKS:
            advisory_failures += 1
            continue
        if outcome == "skipped" and name in CONDITIONAL_SKIP_CHECKS:
            continue
        if outcome == "running":
            return f"running check: {name}", advisory_failures
        if outcome == "nonzero":
            return f"failed check: {name}", advisory_failures
        if outcome == "skipped":
            return f"skipped check: {name}", advisory_failures
        return f"unknown check: {name}", advisory_failures
    return None, advisory_failures


def pull_request_refusal(observed: dict[str, object], args: argparse.Namespace) -> str | None:
    error = observed.get("error")
    if error == "timed out":
        return "pull request view timed out"
    if error == "could not be started":
        return "pull request view could not be started"
    if error == "failed":
        return "pull request view failed"
    if error == "unparseable":
        return "pull request view is unparseable"
    if error:
        return "pull request view is unknown"
    if observed.get("state") != "OPEN":
        return "pull request is not open"
    if observed.get("isDraft") is not False:
        return "pull request is draft" if observed.get("isDraft") is True else "pull request draft state is unknown"
    if observed.get("headRefOid") != args.head:
        return "pull request head mismatch"
    if observed.get("baseRefName") != "main":
        return "pull request base is unknown"
    mergeable = observed.get("mergeable")
    if mergeable == "CONFLICTING":
        return "mergeability is conflicting"
    if mergeable != "MERGEABLE":
        return "mergeability is unknown"
    check_reason, advisory_failures = checks_refusal(observed)
    if check_reason:
        return check_reason
    merge_state = observed.get("mergeStateStatus")
    if merge_state == "CLEAN":
        return None
    if merge_state == "UNSTABLE":
        if advisory_failures < 1:
            return "merge state is unstable without an advisory failure"
        return None
    named = {
        "BLOCKED": "merge state is blocked",
        "BEHIND": "merge state is behind",
        "DRAFT": "merge state is draft",
        "DIRTY": "merge state is conflicting",
        "UNKNOWN": "merge state is unknown",
        "HAS_HOOKS": "merge state is unknown",
    }
    if isinstance(merge_state, str) and merge_state in named:
        return named[merge_state]
    return "merge state is unknown"


def main_refusal(observed: dict[str, object], expected: str) -> str | None:
    error = observed.get("error")
    if error == "timed out":
        return "main view timed out"
    if error == "could not be started":
        return "main view could not be started"
    if error == "failed":
        return "main view failed"
    if error == "unparseable":
        return "main view is unparseable"
    if error:
        return "main view is unknown"
    if observed.get("sha") != expected:
        return "expected main mismatch"
    return None


def evidence_problem(peeked: dict[str, object], args: argparse.Namespace) -> tuple[str | None, dict[str, object] | None]:
    if peeked.get("missing") is True:
        return "evidence is missing", None
    if peeked.get("unreadable") is True:
        return "evidence is unreadable", None
    if peeked.get("oversize") is True:
        return "evidence is too large", None
    text = peeked.get("text")
    if not isinstance(text, str):
        return "evidence is unreadable", None
    try:
        document = parse_json_document(text)
    except DeliveryError as exc:
        return str(exc), None
    for key in document:
        if tainted(str(key)) and key not in EVIDENCE_KEYS:
            return "evidence contains a secret marker", None
    for value in walk_strings(document):
        if tainted(value):
            return "evidence contains a secret marker", None
    reason = evidence_refusal(document, args)
    if reason:
        return reason, None
    return None, document


def walk_strings(value: object) -> list[str]:
    found: list[str] = []
    if isinstance(value, str):
        found.append(value)
    elif isinstance(value, dict):
        for item in value.values():
            found.extend(walk_strings(item))
    elif isinstance(value, list):
        for item in value:
            found.extend(walk_strings(item))
    return found


def publish(path: Path, document: dict[str, object], decision: str, reason: str | None) -> int | None:
    document["decision"] = decision
    document["reason"] = reason
    try:
        write_audit(path, document)
    except OSError:
        print("deliver-dev-env-pr: audit write failed", file=sys.stderr)
        return 2
    return None


def finish_refusal(path: Path, document: dict[str, object], reason: str) -> int:
    failed = publish(path, document, "refused", reason)
    if failed is not None:
        return failed
    print(f"deliver-dev-env-pr: {reason}", file=sys.stderr)
    return 2


def validate_invocation(args: argparse.Namespace) -> str | None:
    if type(args.pr) is not int or args.pr < 1:
        return "pull request number is unknown"
    if not isinstance(args.head, str) or not SHA_RE.fullmatch(args.head):
        return "head is unknown"
    if not isinstance(args.expected_main, str) or not SHA_RE.fullmatch(args.expected_main):
        return "expected main is unknown"
    if not args.evidence or not args.shared_checkout or not args.audit_output:
        return "input is unknown"
    return None


def preflight(args: argparse.Namespace) -> int:
    audit_path = Path(args.audit_output)
    problem = validate_invocation(args)
    document = empty_audit(args, "preflight")
    if problem:
        try:
            write_audit(audit_path, document)
        except OSError:
            print("deliver-dev-env-pr: audit write failed", file=sys.stderr)
            return 2
        return finish_refusal(audit_path, document, problem)
    pin = observe_pin(args.shared_checkout)
    pull = observe_pull_request(args.pr)
    # A failed pull-request read is already enough to refuse. Do not spend
    # another gh bound before the observation audit is written.
    if pull.get("error"):
        main_obs = {"error": "not-read", "sha": None}
    else:
        main_obs = observe_main()
    peeked = peek_evidence(args.evidence)
    document["pin"] = {
        "expectedCommit": pin.get("expectedCommit"),
        "observedHead": pin.get("observedHead"),
        "expectedSha256": pin.get("expectedSha256"),
        "observedSha256": pin.get("observedSha256"),
        "dirty": pin.get("dirty"),
        "error": pin.get("error"),
    }
    document["pullRequestObservation"] = {
        "error": pull.get("error"),
        "state": pull.get("state"),
        "isDraft": pull.get("isDraft"),
        "headRefOid": pull.get("headRefOid"),
        "baseRefName": pull.get("baseRefName"),
        "mergeable": pull.get("mergeable"),
        "mergeStateStatus": pull.get("mergeStateStatus"),
        "checks": pull.get("checks"),
    }
    document["observedMain"] = main_obs.get("sha")
    document["evidenceSha256"] = peeked.get("sha256")
    try:
        write_audit(audit_path, document)
    except OSError:
        print("deliver-dev-env-pr: audit write failed", file=sys.stderr)
        return 2
    reason = (
        pin_refusal(pin)
        or pull_request_refusal(pull, args)
        or main_refusal(main_obs, args.expected_main)
    )
    parsed: dict[str, object] | None = None
    if reason is None:
        reason, parsed = evidence_problem(peeked, args)
    if reason:
        return finish_refusal(audit_path, document, reason)
    if parsed is None:
        return finish_refusal(audit_path, document, "evidence is unknown")
    receipt = parsed.get("receipt")
    if isinstance(receipt, dict):
        document["receiptHash"] = receipt.get("hash")
        document["receiptSource"] = receipt.get("source")
    failed = publish(audit_path, document, "preflight-passed", None)
    if failed is not None:
        return failed
    return 0


def guard_command(args: argparse.Namespace) -> list[str]:
    checkout = Path(args.shared_checkout).resolve()
    helper = helper_path(checkout)
    if helper is None:
        raise DeliveryError("shared guard checkout is unusable", 2)
    return [
        *python_command(),
        str(helper),
        "--repo",
        REPO,
        "--pr",
        str(args.pr),
        "--head",
        args.head,
        "--reviewed-head",
        args.head,
        "--preflight-timeout",
        str(PREFLIGHT_TIMEOUT_S),
        "--source-only",
        "--",
        *python_command(),
        str(Path(__file__).resolve()),
        "preflight",
        "--pr",
        str(args.pr),
        "--head",
        args.head,
        "--expected-main",
        args.expected_main,
        "--evidence",
        str(Path(args.evidence).resolve()),
        "--shared-checkout",
        str(checkout),
        "--audit-output",
        str(Path(args.audit_output).resolve()),
    ]


def invoke_guard(args: argparse.Namespace) -> int:
    try:
        command = guard_command(args)
    except DeliveryError:
        return 2
    for part in command:
        if part in {"--admin", "--force", "--auto", "--auto-merge", "--reconcile"}:
            return 2
    try:
        proc = subprocess.run(
            command,
            check=False,
            timeout=GUARD_INVOCATION_TIMEOUT_S,
            stdin=subprocess.DEVNULL,
        )
    except subprocess.TimeoutExpired:
        return 124
    except OSError:
        return 127
    if proc.returncode is None:
        return 1
    return int(proc.returncode)


def annotate_guard(path: Path, code: int) -> None:
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError, UnicodeError):
        document = {
            "schema": AUDIT_SCHEMA,
            "stage": "guard",
            "nativeEvidenceProvenance": "Unknown",
            "configurationProvenance": "Unknown",
            "replayed": False,
        }
    if not isinstance(document, dict):
        return
    document["guardExit"] = code
    document["replayed"] = False
    if code == 124:
        document["stage"] = "uncertain"
        document["decision"] = "uncertain"
        document["reason"] = "guard invocation timed out; merge was not replayed"
    elif code != 0 and document.get("decision") == "preflight-passed":
        document["decision"] = "guard-failed"
        document["reason"] = "guard exited; merge was not replayed"
    try:
        write_audit(path, document)
    except OSError:
        return


def deliver(args: argparse.Namespace) -> int:
    audit_path = Path(args.audit_output)
    problem = validate_invocation(args)
    document = empty_audit(args, "pin-check")
    if problem:
        try:
            write_audit(audit_path, document)
        except OSError:
            print("deliver-dev-env-pr: audit write failed", file=sys.stderr)
            return 2
        return finish_refusal(audit_path, document, problem)
    pin = observe_pin(args.shared_checkout)
    document["pin"] = {
        "expectedCommit": pin.get("expectedCommit"),
        "observedHead": pin.get("observedHead"),
        "expectedSha256": pin.get("expectedSha256"),
        "observedSha256": pin.get("observedSha256"),
        "dirty": pin.get("dirty"),
        "error": pin.get("error"),
    }
    try:
        write_audit(audit_path, document)
    except OSError:
        print("deliver-dev-env-pr: audit write failed", file=sys.stderr)
        return 2
    reason = pin_refusal(pin)
    if reason:
        return finish_refusal(audit_path, document, reason)
    code = invoke_guard(args)
    annotate_guard(audit_path, code)
    if code != 0:
        print(f"deliver-dev-env-pr: guard exited {code}; merge was not replayed", file=sys.stderr)
    return code


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="deliver_dev_env_pr.py")
    sub = parser.add_subparsers(dest="command", required=True)
    for name in ("deliver", "preflight"):
        command = sub.add_parser(name)
        command.add_argument("--pr", required=True, type=int)
        command.add_argument("--head", required=True)
        command.add_argument("--expected-main", required=True)
        command.add_argument("--evidence", required=True)
        command.add_argument("--shared-checkout", required=True)
        command.add_argument("--audit-output", required=True)
    return parser


def main(argv: list[str] | None = None) -> int:
    try:
        args = build_parser().parse_args(argv)
        if args.command == "preflight":
            return preflight(args)
        if args.command == "deliver":
            return deliver(args)
        return 2
    except DeliveryError as exc:
        print(f"deliver-dev-env-pr: {exc}", file=sys.stderr)
        return exc.code
    except Exception:
        print("deliver-dev-env-pr: unknown failure", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
