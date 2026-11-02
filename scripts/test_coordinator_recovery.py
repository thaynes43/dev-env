#!/usr/bin/env python3
"""Executable adapter tests. Fixtures stand in for agent-run and gh; no live CLI."""

import hashlib
import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ADAPTER = ROOT / "scripts" / "coordinator-recovery.py"
SPEC = importlib.util.spec_from_file_location("coordinator_recovery", ADAPTER)
mod = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(mod)

OLD = "a" * 40
NEW = "b" * 40
CUR = "c" * 40
OWNER = "dev-env-1102-021002"
SENDER = {"namespace": "dev-agents", "name": OWNER, "uid": "owner-uid-355"}
RECIPIENT = {"namespace": "dev-agents", "name": "review-355", "uid": "recipient-uid-355"}

FIXTURE = r'''#!/usr/bin/env python3
import fcntl, hashlib, json, os, sys, time
from pathlib import Path

def message_name(key, text):
    return "message.%s.%s" % (hashlib.sha256(key.encode()).hexdigest()[:32], hashlib.sha256(text.encode()).hexdigest()[:32])

def message_uid(key, text):
    return "u" + hashlib.sha256((key + "|" + text).encode()).hexdigest()[:32]

def load():
    path = Path(os.environ["OP355_FIXTURE_STORE"]) / "state.json"
    fd = path.open("a+", encoding="utf-8")
    fcntl.flock(fd.fileno(), fcntl.LOCK_EX)
    fd.seek(0)
    raw = fd.read()
    state = json.loads(raw) if raw.strip() else {}
    return fd, state

def save(fd, state):
    write_state(fd, state)
    fd.close()

def flags(args):
    out, pos, i = {}, [], 0
    while i < len(args):
        item = args[i]
        if item == "--sent":
            out["sent"] = True
            i += 1
        elif item in {"--project", "--limit", "--continue", "--recipient-namespace", "--recipient-name", "--recipient-uid", "--key", "-o"}:
            out["o" if item == "-o" else item[2:]] = args[i + 1]
            i += 2
        else:
            pos.append(item)
            i += 1
    return pos, out

def meta(item):
    copy = dict(item)
    copy.pop("text", None)
    return copy

TRACKED = (
    "DEV_ENV_API_URL", "DEV_ENV_API_TOKEN_FILE", "DEV_ENV_API_CA_FILE",
    "AGENTD_API_URL", "AGENTD_API_TOKEN_FILE", "AGENTD_API_CA_FILE",
    "KUBERNETES_SERVICE_HOST", "KUBERNETES_SERVICE_PORT",
)

def note_env(state):
    state["childEnv"] = {key: os.environ[key] for key in TRACKED if key in os.environ}
    state["decoyPresent"] = "OP355_DECOY_SECRET" in os.environ

def lookup_session(state, name):
    matches = []
    for key in ("show", "ownerShow"):
        doc = state.get(key)
        if isinstance(doc, dict) and doc.get("name") == name:
            matches.append(doc)
    if len(matches) != 1:
        return None
    return matches[0]

def list_owned(session):
    claims = session.get("taskClaims")
    if not isinstance(claims, dict):
        return session
    items = claims.get("items") if isinstance(claims.get("items"), list) else []
    owned = []
    for item in items:
        owner = item.get("owner") if isinstance(item, dict) else None
        if isinstance(owner, dict) and owner.get("uid") == session.get("uid"):
            owned.append(item)
    if owned:
        session["taskClaims"] = {"state": "Available", "reason": "Complete", "items": owned}
    else:
        session["taskClaims"] = {"state": "Unknown", "reason": "NoJoinedClaims", "items": []}
    return session

def write_state(fd, state):
    fd.seek(0)
    fd.truncate()
    json.dump(state, fd, sort_keys=True)
    fd.write("\n")
    fd.flush()
    os.fsync(fd.fileno())

def gh(state):
    argv = sys.argv[1:]
    expect = ["pr", "view", "--repo", state["repository"], str(state["pullRequest"]), "--json", "headRefOid"]
    if argv != expect:
        sys.stderr.write("gh usage\n")
        return 2
    body = {"headRefOid": state["headRefOid"]}
    if state.get("extraHeadField"):
        body["extra"] = state["extraHeadField"]
    json.dump(body, sys.stdout)
    sys.stdout.write("\n")
    return 0

def agent(state, fd):
    pos, opt = flags(sys.argv[1:])
    if not pos or pos[0] == "show":
        if len(pos) != 2 or pos[0] != "show" or opt.get("o") != "json":
            sys.stderr.write("show usage\n")
            return 2
        session = lookup_session(state, pos[1])
        if session is None:
            sys.stderr.write("show session was not found\n")
            return 4
        if state.get("showExit"):
            sys.stderr.write("show unconfirmed\n")
            return int(state["showExit"])
        body = json.loads(json.dumps(session))
        if not state.get("rawClaims"):
            body = list_owned(body)
        json.dump(body, sys.stdout)
        sys.stdout.write("\n")
        return 0
    if pos[0] != "inbox":
        sys.stderr.write("usage\n")
        return 2
    verb = pos[1] if len(pos) > 1 else ""
    if opt.get("project") != state.get("project", "dev-env"):
        sys.stderr.write("project refused\n")
        return 3
    if verb == "list":
        if opt.get("o") != "json":
            return 2
        rows = state["sent"] if opt.get("sent") else state["received"]
        limit = int(opt.get("limit", "20"))
        if state.get("stuckContinue"):
            page = [meta(item) for item in rows[:limit]]
            body = {"items": page, "continue": "stuck", "view": "thread"}
        else:
            offset = int(opt.get("continue", "0") or "0")
            page = [meta(item) for item in rows[offset:offset + limit]]
            nxt = offset + limit
            body = {"items": page, "view": "thread"}
            if nxt < len(rows):
                body["continue"] = str(nxt)
        if opt.get("sent"):
            body["box"] = "sent"
        json.dump(body, sys.stdout)
        sys.stdout.write("\n")
        return 0
    if verb == "read" and len(pos) == 3 and opt.get("o") == "json":
        for box in ("sent", "received"):
            for item in state[box]:
                if item["name"] == pos[2]:
                    json.dump(item, sys.stdout)
                    sys.stdout.write("\n")
                    return 0
        sys.stderr.write("inbox record or authorized scope was not found (404)\n")
        return 4
    if verb == "ack-status" and len(pos) == 3 and opt.get("o") == "json":
        forced = state.get("ackExit", {}).get(pos[2])
        if forced:
            sys.stderr.write("inbox ack-status unconfirmed\n" if int(forced) == 6 else "inbox record or authorized scope was not found (404)\n")
            return int(forced)
        for ack in state.get("acks", []):
            if ack["messageName"] == pos[2]:
                json.dump(ack, sys.stdout)
                sys.stdout.write("\n")
                return 0
        sys.stderr.write("inbox record or authorized scope was not found (404)\n")
        return 4
    if verb == "send" and pos == ["inbox", "send"] and opt.get("o") in {None, "json"}:
        text = sys.stdin.read()
        key = opt.get("key", "")
        mode = state.get("sendHang") or ""
        if mode == "before":
            write_state(fd, state)
            time.sleep(30)
            return 0
        if state.get("sendExit"):
            sys.stderr.write(state.get("sendStderr") or "inbox request was refused (403); check API authentication and project scope\n")
            return int(state["sendExit"])
        for item in state["sent"]:
            if item["key"] == key:
                if item["text"] != text:
                    sys.stderr.write("inbox intent conflicts with the existing key; no replacement key was generated\n")
                    return 1
                json.dump({"outcome": "Confirmed", "message": meta(item)}, sys.stdout)
                sys.stdout.write("\n")
                return 0
        item = {
            "name": message_name(key, text),
            "uid": message_uid(key, text),
            "project": opt["project"],
            "key": key,
            "sender": state["identity"],
            "recipient": {"namespace": opt["recipient-namespace"], "name": opt["recipient-name"], "uid": opt["recipient-uid"]},
            "createdAt": state["clock"],
            "kind": "plain",
            "text": text,
        }
        if mode == "after":
            state["sent"].append(item)
            write_state(fd, state)
            time.sleep(30)
            return 0
        state["sent"].append(item)
        json.dump({"outcome": "Created", "message": meta(item)}, sys.stdout)
        sys.stdout.write("\n")
        return 0
    sys.stderr.write("usage\n")
    return 2

def main():
    fd, state = load()
    state.setdefault("calls", []).append(sys.argv[1:])
    note_env(state)
    code = gh(state) if Path(sys.argv[0]).name == "gh" else agent(state, fd)
    save(fd, state)
    return code

if __name__ == "__main__":
    sys.exit(main())
'''


def message_name(key, text):
    return "message.%s.%s" % (hashlib.sha256(key.encode()).hexdigest()[:32], hashlib.sha256(text.encode()).hexdigest()[:32])


def message_uid(key, text):
    return "u" + hashlib.sha256((key + "|" + text).encode()).hexdigest()[:32]


def review_event(identity, head, at, verdict="approved"):
    return {
        "kind": "review-observation",
        "identity": identity,
        "sourceTime": at,
        "task": "native-review-1",
        "uid": "uid-review-1",
        "head": head,
        "source": "native-review-log",
        "phase": "terminal",
        "verdict": verdict,
    }


def artifact(identity, head, at, verdict="approved"):
    view = {
        "head": head,
        "observation": identity,
        "source": "native-review-log",
        "sourceTime": at,
        "task": "native-review-1",
        "uid": "uid-review-1",
        "verdict": verdict,
    }
    return {"observation": identity, "head": head, "digest": mod.review_digest(view), "reference": "artifact://review/" + identity}


def request(**overrides):
    body = {
        "schema": "dev-env.workflow-recovery/v1",
        "observedAt": "2026-11-02T03:05:00Z",
        "action": "read-only-review",
        "permission": "read-only",
        "repository": "thaynes43/dev-env",
        "handoffRepository": "github.com/thaynes43/dev-env",
        "pullRequest": 355,
        "project": "dev-env",
        "story": "355",
        "relatedStories": ["177"],
        "task": "native-review-1",
        "taskUID": "uid-review-1",
        "correlation": "corr-355",
        "owner": OWNER,
        "authenticatedSender": SENDER,
        "recipient": RECIPIENT,
        "session": "review-355",
        "deadline": "2026-11-02T04:10:00Z",
        "windowExpiresAt": "2026-11-02T04:00:00Z",
        "claim": {
            "name": "claim-355",
            "uid": "claim-uid-355",
            "project": "dev-env",
            "repository": "dev-env",
            "taskKey": "task:op355",
            "owner": dict(SENDER),
        },
        "handoffEvents": [],
        "verdictArtifacts": [],
        "custodyMessage": "",
        "fetchedRef": "",
    }
    body.update(overrides)
    return body


def task_result(exit_code, subtype, at, error=False):
    body = {"exitCode": exit_code, "finishedAt": at, "subtype": subtype, "numTurns": 1}
    if error:
        body["isError"] = True
    return body


def claim_item(owner=None, repository="dev-env", github="thaynes43/dev-env"):
    return {
        "name": "claim-355",
        "uid": "claim-uid-355",
        "identity": {"project": "dev-env", "repository": repository, "taskKey": "task:op355"},
        "owner": owner or dict(SENDER),
        "actor": {"kind": "session", "serviceAccount": "dev-agents/dev-env", "podName": "owner-pod", "podUID": "pod-uid-355"},
        "provenance": {
            "catalogRevision": "a" * 64,
            "rulesRevision": "b" * 64,
            "github": github,
            "defaultBranch": "main",
            "initialPinState": "Unknown",
        },
        "createdAt": "2026-11-02T01:00:00Z",
        "ownerState": "Running",
        "stopState": "Unknown",
    }


def show(task=None):
    return {
        "name": "review-355",
        "uid": RECIPIENT["uid"],
        "phase": "Running",
        "repo": "thaynes43/dev-env",
        "agent": "cursor",
        "mode": "task",
        "model": "grok-4.7",
        "createdAt": "2026-11-02T01:00:00Z",
        "conditions": [{"type": "Ready", "status": "True"}],
        "outcome": None,
        "wrapperExit": 0,
        "agentStatus": {"state": "running", "lastHeartbeat": "2026-11-02T03:04:30Z", "task": task},
        "taskClaims": {"state": "Unknown", "reason": "NoJoinedClaims", "items": []},
    }


def owner_show(item=None):
    item = item or claim_item()
    return {
        "name": item["owner"]["name"],
        "uid": item["owner"]["uid"],
        "phase": "Running",
        "taskClaims": {"state": "Available", "reason": "Complete", "items": [item]},
    }


def fresh(head, session, **extra):
    owner = extra.pop("ownerShow", None)
    state = {
        "repository": "thaynes43/dev-env",
        "project": "dev-env",
        "pullRequest": 355,
        "headRefOid": head,
        "show": session,
        "ownerShow": owner or owner_show(),
        "sent": [],
        "received": [],
        "acks": [],
        "ackExit": {},
        "identity": dict(SENDER),
        "clock": "2026-11-02T03:05:00Z",
        "sendExit": 0,
        "calls": [],
    }
    state.update(extra)
    return state


def add_intent(state, req, head, created=None):
    text = mod.intent_text(req, head)
    key = mod.logical_key(req, head)
    item = {
        "name": message_name(key, text),
        "uid": message_uid(key, text),
        "project": req["project"],
        "key": key,
        "sender": dict(SENDER),
        "recipient": req["recipient"],
        "createdAt": created or state["clock"],
        "kind": "plain",
        "text": text,
    }
    state["sent"].append(item)
    return item


def add_ack(state, message, recipient, created):
    state["acks"].append({
        "name": "ack-" + message["uid"][:20],
        "uid": "ackuid-" + message["uid"][:12],
        "project": message["project"],
        "messageName": message["name"],
        "messageUID": message["uid"],
        "recipient": recipient,
        "actor": {"kind": "Session", "serviceAccount": "dev-env", "podName": "p", "podUID": "pod-1"},
        "createdAt": created,
    })


def add_custody(state, req, created="2026-11-02T02:00:00Z"):
    text = json.dumps({
        "repository": req["repository"],
        "pullRequest": req["pullRequest"],
        "task": req["task"],
        "correlation": req["correlation"],
        "ownerUID": req["claim"]["owner"]["uid"],
    }, sort_keys=True, separators=(",", ":"))
    key = "custody-" + req["correlation"]
    item = {
        "name": message_name(key, text),
        "uid": message_uid(key, text),
        "project": req["project"],
        "key": key,
        "sender": dict(RECIPIENT),
        "recipient": dict(SENDER),
        "createdAt": created,
        "kind": "plain",
        "text": text,
    }
    state["received"].append(item)
    return item


def add_question(state, thread_state):
    key = "q-corr-355"
    text = "{\"correlation\":\"corr-355\"}"
    item = {
        "name": message_name(key, text),
        "uid": message_uid(key, text),
        "project": "dev-env",
        "key": key,
        "sender": dict(RECIPIENT),
        "recipient": dict(SENDER),
        "createdAt": "2026-11-02T03:00:00Z",
        "kind": "question",
        "text": text,
        "thread": {"state": thread_state, "question": {"name": message_name(key, text), "uid": message_uid(key, text)}},
    }
    state["received"].append(item)
    return item


class RecoveryAdapterTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.bin_dir = Path(tempfile.mkdtemp(prefix="op355-bin-"))
        fixture = cls.bin_dir / "fixture-cli.py"
        fixture.write_text(FIXTURE, encoding="utf-8")
        fixture.chmod(0o755)
        for name in ("gh", "agent-run"):
            os.symlink(fixture, cls.bin_dir / name)
        cls.checker = cls.bin_dir / "workflow_handoff"
        go = "/home/dev/.local/share/dev-env-go1273/go/bin/go"
        if not Path(go).exists():
            go = "go"
        env = os.environ.copy()
        env["GOMAXPROCS"] = "2"
        env["CGO_ENABLED"] = "0"
        env["GOTOOLCHAIN"] = "local"
        subprocess.run(
            ["nice", "-n", "19", go, "build", "-o", str(cls.checker), "./scripts/trials/workflow_handoff"],
            cwd=ROOT, env=env, check=True,
        )

    def write_state(self, store, state):
        (store / "state.json").write_text(json.dumps(state), encoding="utf-8")

    def load_state(self, store):
        return json.loads((store / "state.json").read_text(encoding="utf-8"))

    def run_adapter(self, store, body, observe=True, extra_env=None):
        path = store / "request.json"
        path.write_text(json.dumps(body), encoding="utf-8")
        command = [sys.executable, "-I", "-B", str(ADAPTER), "--request", str(path)]
        if observe:
            command.extend(["--cache", str(store / "cache.json"), "--observe"])
        env = os.environ.copy()
        env["PATH"] = str(self.bin_dir) + os.pathsep + env.get("PATH", "")
        env["OP355_FIXTURE_STORE"] = str(store)
        env["WORKFLOW_HANDOFF_BIN"] = str(self.checker)
        env["GOMAXPROCS"] = "2"
        env["GOTOOLCHAIN"] = "local"
        env["CGO_ENABLED"] = "0"
        if extra_env:
            env.update(extra_env)
        completed = subprocess.run(command, cwd=ROOT, env=env, text=True, capture_output=True, check=False)
        self.assertTrue(completed.stdout, completed.stderr)
        return completed.returncode, json.loads(completed.stdout)

    def sends(self, store):
        state = self.load_state(store)
        return [call for call in state["calls"] if call[:2] == ["inbox", "send"]], state

    def test_source_only_and_refusals_call_nothing(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            self.write_state(store, fresh(CUR, show()))
            code, result = self.run_adapter(store, request(), observe=False)
            self.assertEqual(code, 0)
            self.assertEqual(result["status"], "pending")
            self.assertEqual(result["reason"], "source-only")
            self.assertFalse(result["executed"])
            self.assertEqual(result["notificationsSent"], 0)
            self.assertEqual(self.load_state(store)["calls"], [])
            code, result = self.run_adapter(store, request(action="deploy"), observe=True)
            self.assertEqual(code, 2)
            self.assertEqual(result["status"], "refused")
            code, result = self.run_adapter(store, request(accessTTL="30m"), observe=True)
            self.assertEqual(code, 2)
            self.assertEqual(result["reason"], "access or authorization TTL cannot renew a scheduling window")
            code, refused = self.run_adapter(store, request(fetchedRef="FETCH_HEAD"), observe=True)
            self.assertEqual(code, 2)
            self.assertIn("fetchedRef", refused["reason"])
            command = [sys.executable, "-I", "-B", str(ADAPTER), "--request", str(store / "request.json"), "--worker", "python3"]
            completed = subprocess.run(command, cwd=ROOT, text=True, capture_output=True, check=False)
            self.assertEqual(completed.returncode, 2)
            self.assertEqual(self.load_state(store)["calls"], [])

    def test_current_verdict_with_stale_history_does_not_requeue(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            current = review_event("obs-current", CUR, "2026-11-02T03:00:00Z")
            stale = review_event("obs-old", OLD, "2026-11-02T01:00:00Z")
            body = request(handoffEvents=[stale, current], verdictArtifacts=[artifact("obs-current", CUR, "2026-11-02T03:00:00Z")])
            state = fresh(CUR, show(task_result(0, "success", "2026-11-02T03:04:00Z")))
            add_intent(state, body, CUR)
            self.write_state(store, state)
            code, result = self.run_adapter(store, body)
            sends, saved = self.sends(store)
            self.assertEqual(code, 0)
            self.assertEqual(sends, [])
            self.assertEqual(result["status"], "pending")
            self.assertEqual(result["reason"], "attested-unverified")
            self.assertNotIn(result["status"], {"completed", "recovered", "accepted"})
            self.assertFalse(result["verdict"]["verified"])
            self.assertEqual(result["verdict"]["receiver"], "absent")
            self.assertTrue(result["staleHeadRefused"])
            self.assertEqual(result["states"]["verdict"], "attested-unverified")
            self.assertEqual(result["verdict"]["head"], CUR)
            self.assertEqual(result["publication"], "not-authorized")
            self.assertEqual(result["notificationsSent"], 0)
            self.assertEqual(result["taskClaim"], "unchanged")
            self.assertEqual(len(saved["sent"]), 1)
            bare = fresh(CUR, show(task_result(0, "success", "2026-11-02T03:04:00Z")))
            self.write_state(store, bare)
            code, missing = self.run_adapter(store, body)
            self.assertEqual(code, 0)
            self.assertEqual(missing["status"], "pending")
            self.assertEqual(missing["reason"], "attested-unverified")
            self.assertNotIn(missing["status"], {"completed", "recovered", "accepted"})
            self.assertEqual(self.sends(store)[0], [])

    def test_head_change_keeps_old_message_and_sends_one_new(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            body = request()
            state = fresh(OLD, show())
            self.write_state(store, state)
            code, first = self.run_adapter(store, body)
            self.assertEqual(code, 0)
            self.assertEqual(first["status"], "pending")
            self.assertEqual(first["head"], OLD)
            saved = self.load_state(store)
            self.assertEqual(len(saved["sent"]), 1)
            saved["headRefOid"] = NEW
            self.write_state(store, saved)
            code, second = self.run_adapter(store, body)
            saved = self.load_state(store)
            self.assertEqual(code, 0)
            self.assertEqual(second["head"], NEW)
            self.assertEqual(second["status"], "pending")
            self.assertNotEqual(second["logicalKey"], first["logicalKey"])
            self.assertEqual(len(saved["sent"]), 2)
            code, third = self.run_adapter(store, body)
            saved = self.load_state(store)
            self.assertEqual(third["message"]["name"], second["message"]["name"])
            self.assertEqual(len(saved["sent"]), 2)
            self.assertEqual(len([call for call in saved["calls"] if call[:2] == ["inbox", "send"]]), 2)

    def test_expiry_rejects_future_and_foreign_ack_and_accepts_owner_ack(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            body = request(observedAt="2026-11-02T04:30:00Z", windowExpiresAt="2026-11-02T03:00:00Z", deadline="2026-11-02T05:00:00Z")
            state = fresh(CUR, show())
            custody = add_custody(state, body)
            body = request(**{**body, "custodyMessage": custody["name"]})
            add_ack(state, custody, dict(SENDER), "2026-11-02T04:45:00Z")
            self.write_state(store, state)
            code, future = self.run_adapter(store, body)
            self.assertEqual(code, 0)
            self.assertEqual(future["status"], "blocked")
            self.assertEqual(future["reason"], "future-ack-rejected")
            self.assertFalse(future["windowRenewed"])
            self.assertEqual(self.sends(store)[0], [])

            state = self.load_state(store)
            state["acks"][0]["createdAt"] = "2026-11-02T04:00:00Z"
            state["acks"][0]["recipient"] = {"namespace": "dev-agents", "name": "other", "uid": "foreign-uid"}
            state["calls"] = []
            self.write_state(store, state)
            code, foreign = self.run_adapter(store, body)
            self.assertEqual(foreign["status"], "blocked")
            self.assertEqual(foreign["reason"], "foreign-ack-rejected")
            self.assertEqual(self.sends(store)[0], [])

            state = self.load_state(store)
            state["acks"][0]["recipient"] = dict(SENDER)
            state["calls"] = []
            self.write_state(store, state)
            code, renewed = self.run_adapter(store, body)
            self.assertEqual(code, 0)
            self.assertTrue(renewed["windowRenewed"])
            self.assertEqual(renewed["schedulingUntil"], "2026-11-02T05:00:00Z")
            self.assertNotIn("accessTTL", renewed)
            self.assertEqual(renewed["status"], "pending")
            self.assertEqual(len(self.sends(store)[0]), 1)
            self.assertEqual(len(self.load_state(store)["sent"]), 1)

    def test_native_nonzero_while_pod_ready_is_not_completion(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            crashed = task_result(2, "error", "2026-11-02T03:04:00Z", error=True)
            self.write_state(store, fresh(CUR, show(crashed)))
            code, first = self.run_adapter(store, request())
            self.assertEqual(code, 0)
            self.assertEqual(first["status"], "retrying")
            self.assertEqual(first["reason"], "native-terminal-nonzero")
            self.assertNotEqual(first["status"], "completed")
            self.assertEqual(first["states"]["nativeTerminal"], "failed")
            self.assertTrue(any(item["name"] == "pod-ready-not-provider-proof" for item in first["evidence"]))
            self.assertTrue(any(item["name"] == "wrapper-exit-not-provider-proof" for item in first["evidence"]))
            self.assertEqual(len(self.load_state(store)["sent"]), 1)
            code, second = self.run_adapter(store, request())
            self.assertEqual(second["status"], "pending")
            self.assertNotEqual(second["status"], "completed")
            self.assertEqual(len([call for call in self.load_state(store)["calls"] if call[:2] == ["inbox", "send"]]), 1)
            state = self.load_state(store)
            state["show"]["agentStatus"]["task"]["finishedAt"] = "2026-11-02T03:06:00Z"
            self.write_state(store, state)
            code, third = self.run_adapter(store, request(observedAt="2026-11-02T03:07:00Z"))
            self.assertEqual(third["status"], "blocked")
            self.assertTrue(third["escalated"])
            self.assertEqual(third["reason"], "native-terminal-nonzero")
            self.assertEqual(len(self.load_state(store)["sent"]), 1)

    def test_lost_ack_and_unanswered_question_do_not_send(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            body = request()
            state = fresh(CUR, show())
            message = add_intent(state, body, CUR)
            state["ackExit"][message["name"]] = 6
            self.write_state(store, state)
            code, lost = self.run_adapter(store, body)
            self.assertEqual(lost["status"], "blocked")
            self.assertEqual(lost["reason"], "lost-ack")
            self.assertTrue(lost["escalated"])
            self.assertEqual(lost["notificationsSent"], 0)
            self.assertEqual(self.sends(store)[0], [])

            state = fresh(CUR, show())
            add_question(state, "Unanswered")
            self.write_state(store, state)
            code, quiet = self.run_adapter(store, body)
            self.assertEqual(quiet["status"], "blocked")
            self.assertEqual(quiet["reason"], "unanswered-question")
            self.assertEqual(quiet["notificationsSent"], 0)
            self.assertEqual(self.sends(store)[0], [])

            state = fresh(CUR, show())
            add_question(state, "Answered")
            self.write_state(store, state)
            code, answered = self.run_adapter(store, body)
            self.assertEqual(answered["states"]["question"], "answered")
            self.assertNotIn(answered["status"], {"completed", "recovered"})
            self.assertEqual(answered["notificationsSent"], 0)

    def test_restart_and_concurrent_ticks_reuse_one_message(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            self.write_state(store, fresh(CUR, show()))
            code, first = self.run_adapter(store, request())
            code, second = self.run_adapter(store, request())
            self.assertEqual(first["message"]["name"], second["message"]["name"])
            self.assertEqual(second["status"], "pending")
            self.assertEqual(len(self.load_state(store)["sent"]), 1)
            self.assertEqual(len(self.sends(store)[0]), 1)

        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            self.write_state(store, fresh(CUR, show()))
            body = request()
            with ThreadPoolExecutor(max_workers=2) as pool:
                one, two = pool.map(lambda _: self.run_adapter(store, body), (1, 2))
            self.assertEqual(one[0], 0)
            self.assertEqual(two[0], 0)
            self.assertEqual(one[1]["message"]["name"], two[1]["message"]["name"])
            self.assertEqual(len(self.load_state(store)["sent"]), 1)
            self.assertNotIn(one[1]["status"], {"completed", "recovered"})

    def test_denial_and_unconfirmed_are_not_replayed(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            state = fresh(CUR, show(), sendExit=3)
            self.write_state(store, state)
            code, denied = self.run_adapter(store, request(logicalKey="caller-key-a"))
            self.assertEqual(denied["status"], "blocked")
            self.assertEqual(denied["reason"], "denied-not-replayed")
            state = self.load_state(store)
            state["sendExit"] = 0
            self.write_state(store, state)
            code, again = self.run_adapter(store, request(logicalKey="caller-key-b", correlation="corr-other"))
            self.assertEqual(again["reason"], "denied-not-replayed")
            self.assertEqual(again["logicalKey"], denied["logicalKey"])
            self.assertNotEqual(again["logicalKey"], "caller-key-b")
            self.assertTrue(again["ignoredCallerKey"])
            self.assertEqual(len(self.sends(store)[0]), 1)
            self.assertEqual(self.load_state(store)["sent"], [])
            state = self.load_state(store)
            state["headRefOid"] = NEW
            self.write_state(store, state)
            code, moved = self.run_adapter(store, request(logicalKey="caller-key-c", correlation="corr-later"))
            self.assertEqual(moved["reason"], "denied-not-replayed")
            self.assertNotEqual(moved["logicalKey"], denied["logicalKey"])
            self.assertEqual(len(self.sends(store)[0]), 1)
            self.assertEqual(self.load_state(store)["sent"], [])

        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            state = fresh(CUR, show(), sendExit=6, sendStderr="inbox outcome Unknown; no request was repeated\n")
            self.write_state(store, state)
            body = request()
            code, unknown = self.run_adapter(store, body)
            self.assertEqual(unknown["reason"], "unconfirmed-not-replayed")
            state = self.load_state(store)
            state["sendExit"] = 0
            self.write_state(store, state)
            code, replay = self.run_adapter(store, request(correlation="corr-other", logicalKey="caller-key-u"))
            self.assertEqual(replay["reason"], "unconfirmed-not-replayed")
            self.assertEqual(replay["logicalKey"], unknown["logicalKey"])
            self.assertEqual(len(self.sends(store)[0]), 1)
            state = self.load_state(store)
            add_intent(state, body, CUR)
            self.write_state(store, state)
            code, seen = self.run_adapter(store, body)
            self.assertEqual(seen["status"], "pending")
            self.assertEqual(seen["states"]["apiDelivered"], True)
            self.assertEqual(len(self.sends(store)[0]), 1)

    def test_ack_and_exit_zero_without_verdict_is_not_completed(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            body = request()
            state = fresh(CUR, show(task_result(0, "success", "2026-11-02T03:04:00Z")))
            message = add_intent(state, body, CUR)
            add_ack(state, message, dict(RECIPIENT), "2026-11-02T03:04:30Z")
            self.write_state(store, state)
            code, result = self.run_adapter(store, body)
            self.assertEqual(code, 0)
            self.assertEqual(result["states"]["readAck"], True)
            self.assertEqual(result["states"]["nativeTerminal"], "succeeded")
            self.assertEqual(result["states"]["verdict"], "absent")
            self.assertEqual(result["status"], "pending")
            self.assertNotIn(result["status"], {"completed", "recovered"})
            self.assertEqual(self.sends(store)[0], [])

    def test_cross_owner_and_partial_observation_block(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            other = dict(SENDER)
            other["uid"] = "other-owner-uid"
            state = fresh(CUR, show(), ownerShow=owner_show(claim_item(other)))
            foreign = request()
            foreign["claim"] = dict(foreign["claim"])
            foreign["claim"]["owner"] = other
            foreign["authenticatedSender"] = other
            foreign["owner"] = other["name"]
            add_intent(state, foreign, CUR)
            before = len(state["sent"])
            self.write_state(store, state)
            code, result = self.run_adapter(store, request(logicalKey="not-the-derived-key"))
            self.assertEqual(result["status"], "blocked")
            self.assertEqual(result["reason"], "show-identity-mismatch")
            self.assertEqual(len(self.load_state(store)["sent"]), before)
            self.assertEqual(self.sends(store)[0], [])

            state = fresh(CUR, show(), stuckContinue=True)
            self.write_state(store, state)
            code, partial = self.run_adapter(store, request())
            self.assertEqual(partial["status"], "blocked")
            self.assertEqual(partial["reason"], "inbox-list-partial")
            self.assertEqual(self.load_state(store)["sent"], [])

    def test_claim_contract_rejects_wrong_scope_and_missing_views(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            self.write_state(store, fresh(CUR, show()))
            slug = dict(request())
            slug["claim"] = dict(slug["claim"])
            slug["claim"]["repository"] = "thaynes43/dev-env"
            code, refused = self.run_adapter(store, slug, observe=False)
            self.assertEqual(code, 2)
            self.assertEqual(refused["reason"], "claim repository must be the catalog name")
            self.assertEqual(self.load_state(store)["calls"], [])

            state = fresh(CUR, show(), ownerShow=owner_show(claim_item(github="thaynes43/other")))
            self.write_state(store, state)
            code, mismatched = self.run_adapter(store, request())
            self.assertEqual(mismatched["status"], "blocked")
            self.assertEqual(mismatched["reason"], "claim-repository-mismatch")
            self.assertEqual(self.sends(store)[0], [])

            state = fresh(CUR, show(), ownerShow=owner_show(claim_item(repository="other-repo")))
            self.write_state(store, state)
            code, scope = self.run_adapter(store, request())
            self.assertEqual(scope["reason"], "claim-not-found")
            self.assertEqual(self.sends(store)[0], [])

            foreign = claim_item()
            foreign["owner"] = {"namespace": "dev-agents", "name": "review-355", "uid": RECIPIENT["uid"]}
            state = fresh(CUR, show())
            state["ownerShow"]["taskClaims"]["items"] = [foreign]
            state["rawClaims"] = True
            self.write_state(store, state)
            code, owned = self.run_adapter(store, request())
            self.assertEqual(owned["reason"], "claim-not-owned")
            self.assertEqual(self.load_state(store)["sent"], [])

            state = fresh(CUR, show())
            state.pop("ownerShow")
            self.write_state(store, state)
            code, missing_owner = self.run_adapter(store, request())
            self.assertEqual(missing_owner["reason"], "show-failed")
            state = fresh(CUR, show())
            state.pop("show")
            self.write_state(store, state)
            code, missing_recipient = self.run_adapter(store, request())
            self.assertEqual(missing_recipient["reason"], "show-failed")
            self.assertEqual(self.load_state(store)["sent"], [])

    def test_correlation_and_head_do_not_reset_retry_budget(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            self.write_state(store, fresh(CUR, show(), sendExit=5))
            code, first = self.run_adapter(store, request(logicalKey="caller-key-a"))
            self.assertEqual(first["status"], "retrying")
            self.assertEqual(first["reason"], "not-sent-retry")
            self.assertEqual(first["attempt"], 1)
            self.assertEqual(len(self.sends(store)[0]), 1)
            code, changed = self.run_adapter(store, request(correlation="corr-other", logicalKey="caller-key-b"))
            self.assertEqual(changed["reason"], "correlation-conflict")
            self.assertEqual(len(self.sends(store)[0]), 1)
            saved = self.load_state(store)
            saved["headRefOid"] = NEW
            self.write_state(store, saved)
            code, moved = self.run_adapter(store, request(logicalKey="caller-key-c"))
            self.assertEqual(moved["reason"], "backoff")
            self.assertEqual(moved["attempt"], 1)
            self.assertNotEqual(moved["logicalKey"], first["logicalKey"])
            self.assertEqual(len(self.sends(store)[0]), 1)
            saved = self.load_state(store)
            saved["sendExit"] = 0
            saved["headRefOid"] = NEW
            self.write_state(store, saved)
            code, later = self.run_adapter(store, request(observedAt="2026-11-02T03:06:00Z"))
            self.assertEqual(later["status"], "pending")
            self.assertEqual(later["head"], NEW)
            self.assertEqual(len(self.load_state(store)["sent"]), 1)
            self.assertEqual(len(self.sends(store)[0]), 2)

    def test_other_task_is_not_covered_by_a_denial(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            self.write_state(store, fresh(CUR, show(), sendExit=3))
            code, denied = self.run_adapter(store, request())
            self.assertEqual(denied["reason"], "denied-not-replayed")
            state = self.load_state(store)
            state["sendExit"] = 0
            self.write_state(store, state)
            code, other = self.run_adapter(store, request(task="native-review-2", taskUID="uid-review-2"))
            self.assertEqual(other["status"], "pending")
            self.assertEqual(other["reason"], "inbox-delivered")
            self.assertEqual(len(self.load_state(store)["sent"]), 1)
            self.assertEqual(len(self.sends(store)[0]), 2)

    def test_forged_attestation_and_old_success_do_not_complete(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            current = review_event("obs-current", CUR, "2026-11-02T03:00:00Z")
            forged = artifact("obs-current", CUR, "2026-11-02T03:00:00Z")
            forged["digest"] = "f" * 64
            forged["reference"] = "forged://not-a-receiver-result"
            body = request(handoffEvents=[current], verdictArtifacts=[forged])
            state = fresh(CUR, show(task_result(0, "success", "2026-11-02T01:00:00Z")))
            message = add_intent(state, body, CUR)
            add_ack(state, message, dict(RECIPIENT), "2026-11-02T03:04:30Z")
            self.write_state(store, state)
            code, result = self.run_adapter(store, body)
            self.assertEqual(code, 0)
            self.assertEqual(result["status"], "pending")
            self.assertEqual(result["reason"], "attested-unverified")
            self.assertNotIn(result["status"], {"completed", "recovered", "accepted"})
            self.assertFalse(result["verdict"]["verified"])
            self.assertEqual(result["verdict"]["receiver"], "absent")
            self.assertEqual(result["verdict"]["reference"], "")
            self.assertEqual(result["states"]["readAck"], True)
            self.assertEqual(result["states"]["nativeTerminal"], "succeeded")
            self.assertEqual(self.sends(store)[0], [])

    def test_unknown_send_fence_survives_timeout_and_restart(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            self.write_state(store, fresh(CUR, show(), sendHang="before"))
            code, hung = self.run_adapter(store, request())
            self.assertEqual(hung["status"], "blocked")
            self.assertEqual(hung["reason"], "uncertain-not-replayed")
            self.assertTrue(any(item["name"] == "unknown-send" for item in hung["evidence"]))
            self.assertEqual(self.load_state(store)["sent"], [])
            cache = json.loads((store / "cache.json").read_text(encoding="utf-8"))
            entry = next(iter(cache["entries"].values()))
            self.assertEqual(entry["class"], "uncertain")
            self.assertEqual(entry["intentKey"], hung["logicalKey"])
            state = self.load_state(store)
            state["sendHang"] = ""
            state["sendExit"] = 0
            self.write_state(store, state)
            code, replay = self.run_adapter(store, request(correlation="corr-other", logicalKey="caller-key-z"))
            self.assertEqual(replay["reason"], "uncertain-not-replayed")
            self.assertEqual(len(self.sends(store)[0]), 1)
            self.assertEqual(self.load_state(store)["sent"], [])

        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            self.write_state(store, fresh(CUR, show(), sendHang="after"))
            code, hung = self.run_adapter(store, request())
            self.assertEqual(hung["reason"], "uncertain-not-replayed")
            self.assertEqual(len(self.load_state(store)["sent"]), 1)
            state = self.load_state(store)
            state["sendHang"] = ""
            self.write_state(store, state)
            code, restart = self.run_adapter(store, request())
            self.assertEqual(restart["status"], "pending")
            self.assertEqual(restart["states"]["apiDelivered"], True)
            self.assertEqual(len(self.load_state(store)["sent"]), 1)
            self.assertEqual(len(self.sends(store)[0]), 1)

        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            body = request()
            self.write_state(store, fresh(CUR, show()))
            key = mod.logical_key(body, CUR)
            text = mod.intent_text(body, CUR)
            cache = {
                "schema": mod.CACHE_SCHEMA,
                "entries": {
                    mod.family_key(body): {
                        "class": "uncertain",
                        "exit": None,
                        "at": body["observedAt"],
                        "attempts": 1,
                        "nextAttemptAt": "",
                        "intentKey": key,
                        "digest": hashlib.sha256(text.encode("utf-8")).hexdigest(),
                        "correlation": body["correlation"],
                    }
                },
            }
            (store / "cache.json").write_text(json.dumps(cache), encoding="utf-8")
            with ThreadPoolExecutor(max_workers=2) as pool:
                one, two = pool.map(lambda _: self.run_adapter(store, body), (1, 2))
            self.assertEqual(one[1]["reason"], "uncertain-not-replayed")
            self.assertEqual(two[1]["reason"], "uncertain-not-replayed")
            self.assertEqual(self.load_state(store)["sent"], [])
            self.assertEqual(self.sends(store)[0], [])

    def test_child_env_keeps_existing_cli_selectors(self):
        with tempfile.TemporaryDirectory() as raw:
            store = Path(raw)
            self.write_state(store, fresh(CUR, show()))
            synthetic = {
                "DEV_ENV_API_URL": "https://api.example.test:8443",
                "DEV_ENV_API_TOKEN_FILE": "/tmp/op355-synthetic-token",
                "DEV_ENV_API_CA_FILE": "/tmp/op355-synthetic-ca.pem",
                "AGENTD_API_URL": "https://agentd.example.test:9443",
                "AGENTD_API_TOKEN_FILE": "/tmp/op355-synthetic-agentd-token",
                "AGENTD_API_CA_FILE": "/tmp/op355-synthetic-agentd-ca.pem",
                "KUBERNETES_SERVICE_HOST": "10.0.0.1",
                "KUBERNETES_SERVICE_PORT": "443",
                "OP355_DECOY_SECRET": "synthetic-decoy-value",
            }
            code, result = self.run_adapter(store, request(), extra_env=synthetic)
            self.assertEqual(code, 0)
            self.assertEqual(result["status"], "pending")
            saved = self.load_state(store)
            expected = {key: value for key, value in synthetic.items() if key != "OP355_DECOY_SECRET"}
            self.assertEqual(saved["childEnv"], expected)
            self.assertFalse(saved["decoyPresent"])
            self.assertNotIn("synthetic-decoy-value", json.dumps(saved["childEnv"]))


if __name__ == "__main__":
    unittest.main()
