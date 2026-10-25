#!/usr/bin/env python3
"""Finite regressions for the dev-env delivery wrapper.

The canonical guard is the pinned haynes-ops file in DEV_ENV_SHARED_GUARD_CHECKOUT.
Tests verify that checkout's HEAD and SHA-256 before executing it. They do not
fetch or execute an unverified copy. gh and flux are temporary fakes.
"""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import deliver_dev_env_pr as delivery

CHECKOUT_ENV = "DEV_ENV_SHARED_GUARD_CHECKOUT"
HEAD = "c" * 40
MAIN = "d" * 40
OTHER = "e" * 40
PR = 545
RECEIPT = "ab" * 32
SECRET = "ghp_shouldnotappear"
PREPARE = """
DEV_ENV_SHARED_GUARD_CHECKOUT is unset or does not match the pinned guard.
Prepare the reviewed public checkout yourself, verify the hash, then run tests.
Do not let the test fetch or execute an unverified copy.

  pin=63f6b2a5fc54cde0f3df4b0a8456320f7ae12285
  dest=/path/to/haynes-ops-63f6b2a5
  git init "$dest"
  git -C "$dest" fetch --depth 1 https://github.com/thaynes43/haynes-ops.git "$pin"
  git -C "$dest" checkout --detach "$pin"
  sha256sum "$dest/scripts/upgrade-agent/deliver_reviewed_pr.py"
  # expect 1e451ca00c80d176d8f08a142f40cb491b94e8936610389e7c61137db90a21e0
  export DEV_ENV_SHARED_GUARD_CHECKOUT="$dest"
  nice -n 19 python3 -O -B -m unittest discover -s scripts/delivery -p 'test_*.py' -v
"""

FAKE_GH = r"""#!/usr/bin/env python3
import json, os, sys, time

log_path = os.environ["DELIVER_FAKE_LOG"]
count_path = os.environ["DELIVER_FAKE_COUNT"]
mode = os.environ.get("DELIVER_FAKE_MODE", "green")
head = os.environ["DELIVER_FAKE_HEAD"]
main_sha = os.environ["DELIVER_FAKE_MAIN"]
other = os.environ["DELIVER_FAKE_OTHER"]

with open(log_path, "a", encoding="utf-8") as handle:
    handle.write(json.dumps(sys.argv) + "\n")

argv = sys.argv[1:]
if mode == "gh_timeout":
    time.sleep(360)
    raise SystemExit(0)
if mode == "gh_fail":
    sys.stderr.write("temporary read failed\n")
    raise SystemExit(1)

def check(name, status="COMPLETED", conclusion="SUCCESS"):
    return {"name": name, "status": status, "conclusion": conclusion}

def checks():
    rows = [
        check("Changes"),
        check("Lint"),
        check("Test"),
        check("Build"),
        check("Check generated code"),
        check("Image (operator)"),
        check("Image (portal)"),
        check("Image (Codex login helper)"),
        check("Portal PostgreSQL gate / Portal PostgreSQL integration"),
        check("Image (agent)"),
        check("Image (Git wait fixture)", conclusion="SKIPPED"),
        check("Claude advisory review", conclusion="FAILURE"),
        check("Claude assistant (@claude)", conclusion="SKIPPED"),
        check("CI - Success"),
    ]
    if mode in {"all_green", "unstable_without_advisory", "postmerge_open", "postmerge_bad_head", "postmerge_timeout", "merge_nonzero"}:
        rows = [check(item["name"]) if item["name"] == "Claude advisory review" else item for item in rows]
    if mode == "advisory_success_no_ci":
        rows = [check(item["name"]) if item["name"] == "Claude advisory review" else item for item in rows]
        rows = [item for item in rows if item["name"] != "CI - Success"]
    if mode == "ci_missing":
        rows = [item for item in rows if item["name"] != "CI - Success"]
    if mode == "ci_running":
        rows = [check("CI - Success", status="IN_PROGRESS", conclusion=None) if item["name"] == "CI - Success" else item for item in rows]
    if mode == "ci_fail":
        rows = [check("CI - Success", conclusion="FAILURE") if item["name"] == "CI - Success" else item for item in rows]
    if mode == "ci_skipped":
        rows = [check("CI - Success", conclusion="SKIPPED") if item["name"] == "CI - Success" else item for item in rows]
    if mode == "ci_duplicate":
        rows.append(check("CI - Success", conclusion="FAILURE"))
    if mode == "lint_skipped":
        rows = [check("Lint", conclusion="SKIPPED") if item["name"] == "Lint" else item for item in rows]
    if mode == "lint_running":
        rows = [check("Lint", status="IN_PROGRESS", conclusion=None) if item["name"] == "Lint" else item for item in rows]
    if mode == "agent_skipped":
        rows = [check("Image (agent)", conclusion="SKIPPED") if item["name"] == "Image (agent)" else item for item in rows]
    if mode == "portal_skipped":
        rows = [check("Image (portal)", conclusion="SKIPPED") if item["name"] == "Image (portal)" else item for item in rows]
    if mode == "unknown_fail":
        rows.append(check("Custom job", conclusion="FAILURE"))
    if mode == "advisory_neutral":
        rows = [check("Claude advisory review", conclusion="NEUTRAL") if item["name"] == "Claude advisory review" else item for item in rows]
    return rows

def view_count():
    count = 1
    if os.path.exists(count_path):
        count = int(open(count_path, encoding="utf-8").read()) + 1
    with open(count_path, "w", encoding="utf-8") as handle:
        handle.write(str(count))
    return count

if argv[:2] == ["pr", "view"]:
    if mode == "unparseable":
        sys.stdout.write("not-json")
        raise SystemExit(0)
    count = view_count()
    if mode == "postmerge_timeout" and count >= 3:
        time.sleep(360)
        raise SystemExit(0)
    merged = count >= 3 and mode in {"green", "all_green", "agent_skipped", "postmerge_bad_head"}
    if mode == "postmerge_open" and count >= 3:
        merged = False
    if mode == "postmerge_bad_head" and count >= 3:
        merged = True
    payload = {
        "state": "MERGED" if merged else "OPEN",
        "isDraft": False,
        "headRefOid": head,
        "baseRefName": "main",
        "mergeable": "MERGEABLE",
        "mergeStateStatus": "UNSTABLE",
        "mergedAt": "2026-10-24T21:30:00Z" if merged else None,
        "statusCheckRollup": checks(),
    }
    if mode in {"all_green", "merge_nonzero", "postmerge_open", "postmerge_bad_head", "postmerge_timeout"}:
        payload["mergeStateStatus"] = "CLEAN"
    if mode == "unstable_without_advisory":
        payload["mergeStateStatus"] = "UNSTABLE"
    if mode == "head_mismatch":
        payload["headRefOid"] = other
    if mode == "draft":
        payload["isDraft"] = True
        payload["mergeStateStatus"] = "DRAFT"
    if mode == "closed":
        payload["state"] = "CLOSED"
    if mode == "mergeable_unknown":
        payload["mergeable"] = "UNKNOWN"
        payload["mergeStateStatus"] = "CLEAN"
    if mode == "blocked":
        payload["mergeStateStatus"] = "BLOCKED"
    if mode == "behind":
        payload["mergeStateStatus"] = "BEHIND"
    if mode == "conflicting":
        payload["mergeable"] = "CONFLICTING"
        payload["mergeStateStatus"] = "DIRTY"
    if mode == "dirty_state":
        payload["mergeStateStatus"] = "DIRTY"
    if mode == "has_hooks":
        payload["mergeStateStatus"] = "HAS_HOOKS"
    if mode == "state_unknown":
        payload["mergeable"] = "MERGEABLE"
        payload["mergeStateStatus"] = "UNKNOWN"
    if mode == "advisory_success_no_ci":
        payload["mergeStateStatus"] = "CLEAN"
    if merged and mode == "postmerge_bad_head":
        payload["state"] = "MERGED"
        payload["headRefOid"] = other
        payload["mergedAt"] = "2026-10-24T21:30:00Z"
    if mode == "postmerge_open" and count >= 3:
        payload["state"] = "OPEN"
        payload["mergedAt"] = None
    sys.stdout.write(json.dumps(payload))
    raise SystemExit(0)

if argv[:2] == ["api", "repos/thaynes43/dev-env/commits/main"]:
    if mode == "main_unparseable":
        sys.stdout.write("nope\n")
        raise SystemExit(0)
    if mode == "main_fail":
        raise SystemExit(1)
    sha = other if mode == "main_mismatch" else main_sha
    sys.stdout.write(sha + "\n")
    raise SystemExit(0)

if argv[:2] == ["pr", "merge"]:
    code = 1 if mode == "merge_nonzero" else 0
    if code:
        sys.stderr.write("merge refused\n")
    raise SystemExit(code)

sys.stderr.write("unexpected gh invocation\n")
raise SystemExit(99)
"""

FAKE_FLUX = r"""#!/usr/bin/env python3
import json, os, sys
with open(os.environ["DELIVER_FAKE_LOG"], "a", encoding="utf-8") as handle:
    handle.write(json.dumps(sys.argv) + "\n")
raise SystemExit(99)
"""


def evidence(**overrides: object) -> dict[str, object]:
    document: dict[str, object] = {
        "schema": "dev-env.delivery-evidence/v1",
        "repository": "thaynes43/dev-env",
        "pullRequest": PR,
        "reviewedCommit": HEAD,
        "nativeVerdict": "PASS",
        "findings": [],
        "scopeAuthorization": {
            "reference": "story198-source-only-2026-10-24",
            "scope": "source-only",
        },
        "pendingHolds": [],
        "writableImplementer": False,
        "receipt": {"hash": RECEIPT, "source": "native-result-story198"},
    }
    document.update(overrides)
    return document


class DeliveryTests(unittest.TestCase):
    checkout: Path

    @classmethod
    def setUpClass(cls) -> None:
        raw = os.environ.get(CHECKOUT_ENV, "")
        if not raw:
            raise AssertionError(PREPARE)
        checkout = Path(raw).resolve()
        helper = checkout / delivery.PINNED_PATH
        if not helper.is_file():
            raise AssertionError(PREPARE)
        head = subprocess.run(
            ["git", "-C", str(checkout), "rev-parse", "--verify", "HEAD"],
            check=False,
            capture_output=True,
            text=True,
        )
        status = subprocess.run(
            [
                "git",
                "-C",
                str(checkout),
                "status",
                "--porcelain",
                "--",
                delivery.PINNED_PATH,
            ],
            check=False,
            capture_output=True,
            text=True,
        )
        digest = hashlib.sha256(helper.read_bytes()).hexdigest()
        if (
            head.returncode != 0
            or head.stdout.strip() != delivery.PINNED_COMMIT
            or status.returncode != 0
            or status.stdout.strip()
            or digest != delivery.PINNED_SHA256
        ):
            raise AssertionError(PREPARE + f"\nobserved head={head.stdout.strip()} sha256={digest}\n")
        if "source-only: reconcile skipped" not in helper.read_text(encoding="utf-8"):
            raise AssertionError("pinned guard does not contain its source-only marker")
        cls.checkout = checkout

    def setUp(self) -> None:
        parent = self.checkout.parent
        if parent.stat().st_dev != self.checkout.stat().st_dev:
            parent = Path.cwd()
        self.temp = Path(tempfile.mkdtemp(prefix="dev-env-delivery-", dir=str(parent)))
        self.addCleanup(shutil.rmtree, self.temp, True)
        self.bin = self.temp / "bin"
        self.bin.mkdir()
        self.log = self.temp / "log"
        self.count = self.temp / "count"
        self.audit = self.temp / "audit.json"
        self.evidence_path = self.temp / "evidence.json"
        gh = self.bin / "gh"
        flux = self.bin / "flux"
        gh.write_text(FAKE_GH, encoding="utf-8")
        flux.write_text(FAKE_FLUX, encoding="utf-8")
        gh.chmod(gh.stat().st_mode | stat.S_IEXEC)
        flux.chmod(flux.stat().st_mode | stat.S_IEXEC)
        self.mode = "green"

    def write_evidence(self, document: dict[str, object] | None = None, raw: str | None = None) -> None:
        if raw is None:
            raw = json.dumps(document if document is not None else evidence())
        self.evidence_path.write_text(raw, encoding="utf-8")

    def command(self, subcommand: str, checkout: Path | None = None) -> list[str]:
        root = checkout or self.checkout
        return [
            *delivery.python_command(),
            str(Path(delivery.__file__).resolve()),
            subcommand,
            "--pr",
            str(PR),
            "--head",
            HEAD,
            "--expected-main",
            MAIN,
            "--evidence",
            str(self.evidence_path),
            "--shared-checkout",
            str(root),
            "--audit-output",
            str(self.audit),
        ]

    def run_wrapper(
        self,
        subcommand: str = "deliver",
        checkout: Path | None = None,
        optimize: bool = False,
        proc_timeout: int = 30,
    ) -> subprocess.CompletedProcess[str]:
        argv = self.command(subcommand, checkout)
        if optimize and not sys.flags.optimize:
            argv.insert(len(delivery.python_command()), "-O")
        env = os.environ.copy()
        env["PATH"] = str(self.bin) + os.pathsep + env.get("PATH", "")
        env["DELIVER_FAKE_LOG"] = str(self.log)
        env["DELIVER_FAKE_COUNT"] = str(self.count)
        env["DELIVER_FAKE_MODE"] = self.mode
        env["DELIVER_FAKE_HEAD"] = HEAD
        env["DELIVER_FAKE_MAIN"] = MAIN
        env["DELIVER_FAKE_OTHER"] = OTHER
        env["GH_TOKEN"] = SECRET
        env["REQUIRED_CHECKS"] = "Flux Local - Success"
        for key in ("GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_PAT"):
            env.pop(key, None)
        return subprocess.run(
            argv,
            check=False,
            capture_output=True,
            text=True,
            timeout=proc_timeout,
            env=env,
            stdin=subprocess.DEVNULL,
        )

    def rows(self) -> list[list[str]]:
        if not self.log.exists():
            return []
        return [json.loads(line) for line in self.log.read_text(encoding="utf-8").splitlines() if line]

    def merges(self) -> list[list[str]]:
        return [row for row in self.rows() if row[1:3] == ["pr", "merge"]]

    def flux_rows(self) -> list[list[str]]:
        return [row for row in self.rows() if Path(row[0]).name == "flux"]

    def assert_no_mutation(self) -> None:
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.flux_rows(), [])

    def assert_one_guarded_merge(self, proc: subprocess.CompletedProcess[str]) -> None:
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn(f"merged thaynes43/dev-env#{PR} at {HEAD}", proc.stdout)
        self.assertIn("source-only: reconcile skipped", proc.stdout)
        self.assertNotIn(SECRET, proc.stdout + proc.stderr)
        self.assertEqual(len(self.merges()), 1, self.rows())
        self.assertEqual(
            self.merges()[0][1:],
            [
                "pr",
                "merge",
                str(PR),
                "--repo",
                "thaynes43/dev-env",
                "--squash",
                "--match-head-commit",
                HEAD,
            ],
        )
        self.assertNotIn("--delete-branch", self.merges()[0])
        self.assertEqual(self.flux_rows(), [])
        audit = json.loads(self.audit.read_text(encoding="utf-8"))
        self.assertEqual(audit["decision"], "preflight-passed")
        self.assertEqual(audit["guardExit"], 0)
        self.assertIs(audit["replayed"], False)
        self.assertEqual(audit["nativeEvidenceProvenance"], "Unknown")
        self.assertEqual(audit["configurationProvenance"], "Unknown")
        self.assertEqual(audit["receiptHash"], RECEIPT)
        self.assertEqual(audit["receiptSource"], "native-result-story198")
        self.assertEqual(audit["observedMain"], MAIN)
        self.assertEqual(audit["head"], HEAD)
        self.assertNotIn(SECRET, self.audit.read_text(encoding="utf-8"))

    def assert_refused(self, proc: subprocess.CompletedProcess[str], snippet: str) -> None:
        self.assertNotEqual(proc.returncode, 0, proc.stdout)
        self.assert_no_mutation()
        self.assertNotIn(SECRET, proc.stdout + proc.stderr)
        self.assertIn(snippet, proc.stderr)
        audit = json.loads(self.audit.read_text(encoding="utf-8"))
        self.assertEqual(audit["nativeEvidenceProvenance"], "Unknown")
        self.assertIs(audit["replayed"], False)
        self.assertIn(snippet, json.dumps(audit))
        self.assertNotIn(SECRET, self.audit.read_text(encoding="utf-8"))
        self.assertEqual(audit["head"], HEAD)

    def test_pinned_guard_content_merges_once(self) -> None:
        self.write_evidence()
        proc = self.run_wrapper()
        self.assert_one_guarded_merge(proc)
        helper = self.checkout / delivery.PINNED_PATH
        self.assertEqual(hashlib.sha256(helper.read_bytes()).hexdigest(), delivery.PINNED_SHA256)
        pin = json.loads(self.audit.read_text(encoding="utf-8"))["pin"]
        self.assertEqual(pin["observedHead"], delivery.PINNED_COMMIT)
        self.assertEqual(pin["observedSha256"], delivery.PINNED_SHA256)
        self.assertIs(pin["dirty"], False)

    def test_pass_with_minors_and_conditional_skip_merge_once(self) -> None:
        self.mode = "agent_skipped"
        self.write_evidence(
            evidence(
                nativeVerdict="PASS-with-minors",
                findings=[
                    {
                        "id": "F1",
                        "disposition": "resolved",
                        "reason": "fixed before this head",
                    },
                    {
                        "id": "F2",
                        "disposition": "not-applicable",
                        "reason": "concerns a file this change does not touch",
                    },
                ],
            )
        )
        proc = self.run_wrapper()
        self.assert_one_guarded_merge(proc)

    def test_preflight_alone_does_not_merge(self) -> None:
        self.write_evidence()
        proc = self.run_wrapper("preflight")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assert_no_mutation()
        audit = json.loads(self.audit.read_text(encoding="utf-8"))
        self.assertEqual(audit["decision"], "preflight-passed")
        self.assertIsNone(audit["guardExit"])

    def test_evidence_rejections_do_not_merge(self) -> None:
        cases = {
            "reviewed head": (evidence(reviewedCommit=OTHER), "reviewed commit does not match head"),
            "repository": (evidence(repository="thaynes43/haynes-ops"), "repository does not match"),
            "pull request": (evidence(pullRequest=PR + 1), "pull request does not match"),
            "unresolved": (
                evidence(
                    findings=[
                        {
                            "id": "F1",
                            "disposition": "unresolved",
                            "reason": "still open on this head",
                        }
                    ]
                ),
                "finding disposition is unresolved",
            ),
            "missing disposition": (
                evidence(findings=[{"id": "F1", "reason": "fixed before this head"}]),
                "finding disposition is missing",
            ),
            "unknown disposition": (
                evidence(
                    findings=[
                        {
                            "id": "F1",
                            "disposition": "unknown",
                            "reason": "fixed before this head",
                        }
                    ]
                ),
                "finding disposition is unknown",
            ),
            "unknown verdict": (evidence(nativeVerdict="APPROVED"), "native verdict is unknown"),
            "absent holds": (
                {key: value for key, value in evidence().items() if key != "pendingHolds"},
                "pending holds are absent",
            ),
            "absent writer": (
                {key: value for key, value in evidence().items() if key != "writableImplementer"},
                "writable implementer is absent",
            ),
            "active hold": (evidence(pendingHolds=["merge"]), "pending holds are active"),
            "active writer": (evidence(writableImplementer=True), "writable implementer is active"),
            "writer zero": (evidence(writableImplementer=0), "writable implementer is unknown"),
            "holds null": (evidence(pendingHolds=None), "pending holds are absent"),
            "writer string": (evidence(writableImplementer="false"), "writable implementer is unknown"),
            "vague reason": (
                evidence(
                    findings=[
                        {
                            "id": "F1",
                            "disposition": "not-applicable",
                            "reason": "unknown",
                        }
                    ]
                ),
                "finding reason is unknown",
            ),
            "scope": (
                evidence(scopeAuthorization={"reference": "story198-source-only-2026-10-24", "scope": "deploy"}),
                "scope is not source-only",
            ),
            "receipt absent": (
                {key: value for key, value in evidence().items() if key != "receipt"},
                "receipt is absent",
            ),
            "extra field": (evidence(model="hidden"), "evidence field is unknown"),
            "false pull request": (evidence(pullRequest=False), "pull request does not match"),
        }
        for name, (document, snippet) in cases.items():
            with self.subTest(name=name):
                self.log.write_text("", encoding="utf-8")
                if self.count.exists():
                    self.count.unlink()
                self.write_evidence(document)
                proc = self.run_wrapper(optimize=True)
                self.assert_refused(proc, snippet)

    def test_secret_marker_is_not_retained(self) -> None:
        leaked = "password=super-secret-value"
        self.write_evidence(
            evidence(
                findings=[
                    {
                        "id": "F1",
                        "disposition": "resolved",
                        "reason": f"leaked {leaked} in the note",
                    }
                ]
            )
        )
        proc = self.run_wrapper()
        self.assert_refused(proc, "evidence contains a secret marker")
        self.assertNotIn("super-secret-value", proc.stdout + proc.stderr)
        self.assertNotIn("super-secret-value", self.audit.read_text(encoding="utf-8"))

    def test_bad_evidence_bytes_do_not_merge(self) -> None:
        cases = {
            "{": "evidence is unparseable",
            json.dumps(evidence()) + "\n{": "evidence has trailing data",
            json.dumps(evidence()).replace(
                '"schema"',
                '"schema": "dev-env.delivery-evidence/v1", "schema"',
                1,
            ): "evidence is unparseable",
        }
        for raw, snippet in cases.items():
            with self.subTest(snippet=snippet):
                self.log.write_text("", encoding="utf-8")
                if self.count.exists():
                    self.count.unlink()
                self.write_evidence(raw=raw)
                proc = self.run_wrapper()
                self.assert_refused(proc, snippet)

    def test_github_rejections_do_not_merge(self) -> None:
        cases = {
            "head_mismatch": "pull request head mismatch",
            "main_mismatch": "expected main mismatch",
            "draft": "pull request is draft",
            "closed": "pull request is not open",
            "mergeable_unknown": "mergeability is unknown",
            "blocked": "merge state is blocked",
            "behind": "merge state is behind",
            "conflicting": "mergeability is conflicting",
            "dirty_state": "merge state is conflicting",
            "has_hooks": "merge state is unknown",
            "state_unknown": "merge state is unknown",
            "ci_missing": "required check missing: CI - Success",
            "ci_running": "required check running: CI - Success",
            "ci_fail": "required check failed: CI - Success",
            "ci_duplicate": "conflicting duplicate check: CI - Success",
            "ci_skipped": "required check skipped: CI - Success",
            "lint_skipped": "skipped check: Lint",
            "lint_running": "running check: Lint",
            "portal_skipped": "skipped check: Image (portal)",
            "unknown_fail": "failed check: Custom job",
            "advisory_neutral": "unknown check: Claude advisory review",
            "advisory_success_no_ci": "required check missing: CI - Success",
            "unstable_without_advisory": "merge state is unstable without an advisory failure",
            "unparseable": "pull request view is unparseable",
            "gh_fail": "pull request view failed",
            "main_unparseable": "main view is unparseable",
            "main_fail": "main view failed",
        }
        for mode, snippet in cases.items():
            with self.subTest(mode=mode):
                self.mode = mode
                self.log.write_text("", encoding="utf-8")
                if self.count.exists():
                    self.count.unlink()
                self.write_evidence(evidence(nativeVerdict="PASS"))
                proc = self.run_wrapper()
                self.assert_refused(proc, snippet)
                audit = json.loads(self.audit.read_text(encoding="utf-8"))
                self.assertIn("pullRequestObservation", audit)
                self.assertEqual(audit["stage"], "preflight")

    def test_advisory_success_does_not_replace_native_evidence(self) -> None:
        self.mode = "all_green"
        self.write_evidence(evidence(nativeVerdict="PASS-with-comments"))
        proc = self.run_wrapper()
        self.assert_refused(proc, "native verdict is unknown")

    def test_hash_only_pin_mismatch_is_a_refusal(self) -> None:
        reason = delivery.pin_refusal(
            {
                "recordMismatch": False,
                "error": None,
                "observedHead": delivery.PINNED_COMMIT,
                "dirty": False,
                "observedSha256": "ab" * 32,
            }
        )
        self.assertEqual(reason, "shared guard hash mismatch")

    def test_head_mismatch_does_not_merge(self) -> None:
        repo = self.temp / "tiny"
        repo.mkdir()
        subprocess.run(["git", "init", str(repo)], check=True, capture_output=True)
        (repo / "README").write_text("x\n", encoding="utf-8")
        subprocess.run(["git", "-C", str(repo), "add", "README"], check=True)
        subprocess.run(
            [
                "git",
                "-C",
                str(repo),
                "-c",
                "user.name=delivery-test",
                "-c",
                "user.email=delivery-test@example.com",
                "commit",
                "-m",
                "init",
            ],
            check=True,
            capture_output=True,
        )
        self.write_evidence()
        proc = self.run_wrapper(checkout=repo)
        self.assert_refused(proc, "shared guard head mismatch")
        self.assertEqual(self.rows(), [])

    def test_dirty_helper_does_not_merge(self) -> None:
        clone = self.temp / "clone"
        proc = subprocess.run(
            ["git", "clone", "--local", "--no-checkout", str(self.checkout), str(clone)],
            capture_output=True,
            text=True,
        )
        if proc.returncode != 0:
            shutil.rmtree(clone, ignore_errors=True)
            subprocess.run(
                ["git", "clone", "--shared", "--no-checkout", str(self.checkout), str(clone)],
                check=True,
            )
        subprocess.run(
            ["git", "-C", str(clone), "checkout", "HEAD", "--", delivery.PINNED_PATH],
            check=True,
        )
        helper = clone / delivery.PINNED_PATH
        helper.write_bytes(helper.read_bytes() + b"\n")
        self.write_evidence()
        proc = self.run_wrapper(checkout=clone)
        self.assert_refused(proc, "shared guard checkout is dirty")
        self.assertEqual(self.rows(), [])
        audit = json.loads(self.audit.read_text(encoding="utf-8"))
        self.assertNotEqual(audit["pin"]["observedSha256"], delivery.PINNED_SHA256)
        self.assertEqual(audit["pin"]["observedHead"], delivery.PINNED_COMMIT)
        self.assertIs(audit["pin"]["dirty"], True)

    def test_audit_write_error_does_not_merge(self) -> None:
        locked = self.temp / "locked"
        locked.mkdir()
        self.audit = locked / "audit.json"
        locked.chmod(0o500)
        self.addCleanup(locked.chmod, 0o700)
        self.write_evidence()
        proc = self.run_wrapper()
        self.assertNotEqual(proc.returncode, 0)
        self.assert_no_mutation()
        self.assertIn("audit write failed", proc.stderr)
        self.assertEqual(self.rows(), [])

    def test_merge_nonzero_and_unknown_terminal_do_not_retry(self) -> None:
        for mode in ("merge_nonzero", "postmerge_open", "postmerge_bad_head"):
            with self.subTest(mode=mode):
                self.mode = mode
                self.log.write_text("", encoding="utf-8")
                if self.count.exists():
                    self.count.unlink()
                self.write_evidence()
                proc = self.run_wrapper()
                self.assertNotEqual(proc.returncode, 0, proc.stdout + proc.stderr)
                self.assertEqual(len(self.merges()), 1, self.rows())
                self.assertEqual(self.flux_rows(), [])
                self.assertIn("merge was not replayed", proc.stderr)
                audit = json.loads(self.audit.read_text(encoding="utf-8"))
                self.assertIs(audit["replayed"], False)
                self.assertNotEqual(audit["guardExit"], 0)

    def test_guard_command_has_one_source_only_preflight(self) -> None:
        self.write_evidence()
        namespace = delivery.build_parser().parse_args(self.command("deliver")[len(delivery.python_command()) + 1 :])
        command = delivery.guard_command(namespace)
        self.assertEqual(command.count("--source-only"), 1)
        self.assertIn("preflight", command)
        self.assertNotIn("gh", command)
        self.assertNotIn("flux", command)
        for flag in ("--admin", "--force", "--auto", "--auto-merge", "--reconcile", "--delete-branch"):
            self.assertNotIn(flag, command)
        python = delivery.python_command()
        self.assertEqual(command[: len(python)], python)
        self.assertTrue(command[len(python)].endswith(delivery.PINNED_PATH))
        preflight = command.index("preflight")
        self.assertEqual(command[preflight - len(python) - 1 : preflight - 1], python)

    def test_runtime_gates_are_not_asserts(self) -> None:
        for lineno, line in enumerate(Path(delivery.__file__).read_text(encoding="utf-8").splitlines(), start=1):
            stripped = line.lstrip()
            self.assertFalse(stripped.startswith("assert ") or stripped.startswith("assert("), lineno)

    def test_representative_optimize_refusal(self) -> None:
        self.write_evidence(evidence(nativeVerdict="PASS", pendingHolds=["security"]))
        proc = self.run_wrapper(optimize=True)
        self.assert_refused(proc, "pending holds are active")

    def test_untracked_helper_sibling_refuses_before_any_command(self) -> None:
        clone = self.temp / "clone"
        subprocess.run(["git", "clone", "--local", str(self.checkout), str(clone)], check=True, capture_output=True)
        sibling = clone / "scripts" / "upgrade-agent" / "subprocess.py"
        sibling.write_text("raise SystemExit('shadow executed')\n", encoding="utf-8")
        self.write_evidence()
        proc = self.run_wrapper(checkout=clone, optimize=True)
        self.assert_refused(proc, "shared guard checkout is dirty")
        self.assertEqual(self.rows(), [])

    def test_isolated_pythonpath_keeps_valid_and_invalid_paths_bounded(self) -> None:
        poison = self.temp / "poison"
        poison.mkdir()
        (poison / "subprocess.py").write_text("raise SystemExit('PYTHONPATH shadow executed')\n", encoding="utf-8")
        original = os.environ.get("PYTHONPATH")
        self.addCleanup(
            lambda: os.environ.__setitem__("PYTHONPATH", original)
            if original is not None
            else os.environ.pop("PYTHONPATH", None)
        )
        os.environ["PYTHONPATH"] = str(poison)
        self.write_evidence()
        proc = self.run_wrapper(optimize=True)
        self.assert_one_guarded_merge(proc)
        self.log.write_text("", encoding="utf-8")
        self.count.unlink()
        self.write_evidence(evidence(pendingHolds=["security"]))
        proc = self.run_wrapper(optimize=True)
        self.assert_refused(proc, "pending holds are active")

    def test_prereq_timeout_does_not_merge(self) -> None:
        self.mode = "gh_timeout"
        self.write_evidence()
        proc = self.run_wrapper(proc_timeout=180)
        self.assert_refused(proc, "pull request view timed out")

    def test_postmerge_timeout_does_not_retry(self) -> None:
        self.mode = "postmerge_timeout"
        self.write_evidence()
        proc = self.run_wrapper(proc_timeout=180)
        self.assertEqual(proc.returncode, 124, proc.stderr)
        self.assertEqual(len(self.merges()), 1, self.rows())
        self.assertEqual(self.flux_rows(), [])
        audit = json.loads(self.audit.read_text(encoding="utf-8"))
        self.assertEqual(audit["decision"], "uncertain")
        self.assertIs(audit["replayed"], False)
        self.assertEqual(audit["guardExit"], 124)
        self.assertIn("not replayed", str(audit["reason"]))
        self.assertEqual(audit["observedMain"], MAIN)
        self.assertNotIn(SECRET, self.audit.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
