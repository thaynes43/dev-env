#!/usr/bin/env python3
"""Requeue one exact-current-head read-only review through the existing inbox.

Default mode validates the request and starts nothing. --observe calls gh,
agent-run, and the existing workflow_handoff checker. It does not launch a
worker, send a notification, grant a credential, or keep a second queue.
"""

import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from datetime import datetime, timedelta, timezone
from pathlib import Path

SCHEMA = "dev-env.workflow-recovery/v1"
INTENT = "dev-env.workflow-recovery-intent/v1"
CACHE_SCHEMA = "dev-env.workflow-recovery-observation-cache/v1"
TIME_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$")
HEAD_RE = re.compile(r"^[0-9a-f]{40}$")
REPO_RE = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
CATALOG_RE = re.compile(r"^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$")
TOKEN_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$")
DNS_RE = re.compile(r"^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$")
UID_RE = re.compile(r"^[A-Za-z0-9-]{1,128}$")
MSG_RE = re.compile(r"^message\.[a-f0-9]{32}\.[a-f0-9]{32}$")
REF_RE = re.compile(r"^refs/(heads|tags)/[A-Za-z0-9._/-]{1,200}$")
PULL_REF_RE = re.compile(r"^refs/pull/([1-9][0-9]{0,8})/head$")
BACKOFF = (30, 120, 600)
GAPS = [
    "native review receiver is an OP354 dependency and is not started",
    "caller reducer observations and artifact references are attested display only",
    "completed, recovered, and accepted are not produced; runtime and UI acceptance remains open",
    "the cache records denied, unconfirmed, pending, uncertain, and not-sent retry receipts and is not a second queue",
    "show task exit is recipient-session evidence, not a review verdict by itself",
    "a complete inbox list above 8 rows is blocked, including unrelated history",
    "source fixtures are not installed or accepted",
]
CLI_ENV = (
    "DEV_ENV_API_URL",
    "DEV_ENV_API_TOKEN_FILE",
    "DEV_ENV_API_CA_FILE",
    "AGENTD_API_URL",
    "AGENTD_API_TOKEN_FILE",
    "AGENTD_API_CA_FILE",
    "KUBERNETES_SERVICE_HOST",
    "KUBERNETES_SERVICE_PORT",
)
NONREPLAY = {"denied", "unconfirmed", "pending", "conflict", "uncertain"}
COMPLETING = {"approved", "changes-requested", "rejected"}


class Stop(Exception):
    def __init__(self, result, code=0):
        self.result = result
        self.code = code


def main(argv):
    try:
        opts = parse_args(argv[1:])
    except ValueError as exc:
        refuse(str(exc))
        return 2
    try:
        raw = json.loads(Path(opts["request"]).read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError):
        refuse("recovery request is malformed")
        return 2
    if not isinstance(raw, dict):
        refuse("recovery request is malformed")
        return 2
    try:
        req = validate_request(raw)
    except ValueError as exc:
        refuse(str(exc))
        return 2
    if not opts["observe"]:
        result = base_result(req, status="pending", reason="source-only", executed=False)
        result["evidence"].append({"name": "source-only", "at": req["observedAt"], "detail": "no gh, agent-run, or checker process"})
        emit(result)
        return 0
    if not opts["cache"]:
        refuse("--observe requires --cache")
        return 2
    cache_path = Path(opts["cache"])
    if cache_path.exists() and cache_path.is_dir():
        refuse("cache must be a file")
        return 2
    lock_path = Path(str(cache_path) + ".lock")
    try:
        cache_path.parent.mkdir(parents=True, exist_ok=True)
        lock_fd = lock_path.open("a+", encoding="utf-8")
    except OSError:
        refuse("cache is unavailable")
        return 2
    try:
        import fcntl

        fcntl.flock(lock_fd.fileno(), fcntl.LOCK_EX)
        try:
            cache = load_cache(cache_path)
        except OSError:
            refuse("cache is unavailable")
            return 2
        except ValueError:
            refuse("cache is malformed")
            return 2

        def persist():
            persist_cache(cache_path, cache)

        try:
            result = observe(req, cache, persist)
            code = 0
        except Stop as stop:
            result = stop.result
            code = stop.code
        except Exception:
            try:
                persist()
            except OSError:
                pass
            raise
        try:
            persist()
        except OSError:
            refuse("cache is unavailable")
            return 2
    finally:
        lock_fd.close()
    emit(result)
    return code


def parse_args(argv):
    opts = {"request": "", "cache": "", "observe": False}
    i = 0
    while i < len(argv):
        item = argv[i]
        if item in {"--worker", "--worker-arg", "--store"}:
            raise ValueError("worker launch and a local recovery store are not supported")
        if item == "--request":
            opts["request"] = need(argv, i)
            i += 2
        elif item == "--cache":
            opts["cache"] = need(argv, i)
            i += 2
        elif item == "--observe":
            opts["observe"] = True
            i += 1
        else:
            raise ValueError("usage: coordinator-recovery.py --request <file> [--cache <file> --observe]")
    if not opts["request"]:
        raise ValueError("usage: coordinator-recovery.py --request <file> [--cache <file> --observe]")
    return opts


def need(argv, index):
    if index + 1 >= len(argv) or argv[index + 1].startswith("--"):
        raise ValueError("missing argument")
    return argv[index + 1]


def validate_request(raw):
    if raw.get("schema") != SCHEMA:
        raise ValueError("schema must be dev-env.workflow-recovery/v1")
    if raw.get("action") != "read-only-review":
        raise ValueError("unknown action")
    if raw.get("permission") != "read-only":
        raise ValueError("unknown permission")
    if "accessTTL" in raw or "authorizationTTL" in raw:
        raise ValueError("access or authorization TTL cannot renew a scheduling window")
    custody = raw.get("custody")
    if custody is not None:
        if not isinstance(custody, dict):
            raise ValueError("custody is malformed")
        if custody.get("source") in {"access-ttl", "authorization-ttl"}:
            raise ValueError("access or authorization TTL cannot renew a scheduling window")
    for banned in ("worker", "argv", "command"):
        if banned in raw:
            raise ValueError("worker launch is not supported")
    observed = parse_time(raw.get("observedAt"))
    if observed is None:
        raise ValueError("observedAt must be RFC3339 UTC without fractional seconds")
    deadline = parse_time(raw.get("deadline"))
    window = parse_time(raw.get("windowExpiresAt"))
    if deadline is None or window is None:
        raise ValueError("deadline and windowExpiresAt are required")
    if window > deadline:
        raise ValueError("windowExpiresAt must not be later than deadline")
    if not isinstance(raw.get("repository"), str) or not REPO_RE.fullmatch(raw["repository"]):
        raise ValueError("repository is invalid")
    pr = raw.get("pullRequest")
    if not isinstance(pr, int) or isinstance(pr, bool) or pr < 1 or pr > 999999999:
        raise ValueError("pullRequest is invalid")
    handoff_repo = raw.get("handoffRepository")
    if handoff_repo != "github.com/" + raw["repository"]:
        raise ValueError("handoffRepository must be github.com/<repository>")
    for field in ("project", "session"):
        if not isinstance(raw.get(field), str) or not DNS_RE.fullmatch(raw[field]):
            raise ValueError(field + " is invalid")
    for field in ("story", "task", "taskUID", "correlation", "owner"):
        if not isinstance(raw.get(field), str) or not TOKEN_RE.fullmatch(raw[field]):
            raise ValueError(field + " is invalid")
    if not isinstance(raw.get("relatedStories"), list) or len(raw["relatedStories"]) > 8:
        raise ValueError("relatedStories is invalid")
    for story in raw["relatedStories"]:
        if not isinstance(story, str) or not TOKEN_RE.fullmatch(story):
            raise ValueError("relatedStories is invalid")
    claim = raw.get("claim")
    sender = raw.get("authenticatedSender")
    recipient = raw.get("recipient")
    if not isinstance(claim, dict) or not isinstance(sender, dict) or not isinstance(recipient, dict):
        raise ValueError("claim, sender, or recipient is malformed")
    owner = claim.get("owner")
    if not isinstance(owner, dict):
        raise ValueError("claim owner is malformed")
    for party in (owner, sender, recipient):
        if not isinstance(party.get("namespace"), str) or not DNS_RE.fullmatch(party["namespace"]):
            raise ValueError("identity namespace is invalid")
        if not isinstance(party.get("name"), str) or not identity_name(party["name"]):
            raise ValueError("identity name is invalid")
        if not isinstance(party.get("uid"), str) or not UID_RE.fullmatch(party["uid"]):
            raise ValueError("identity uid is invalid")
    if sender != owner or raw["owner"] != owner["name"]:
        raise ValueError("authenticated sender must be the claim owner")
    if recipient["uid"] == owner["uid"]:
        raise ValueError("recipient must be an existing dedicated session")
    if raw.get("session") != recipient["name"]:
        raise ValueError("session must name the recipient session")
    for field in ("name", "uid", "taskKey", "project", "repository"):
        if not isinstance(claim.get(field), str) or not claim[field] or len(claim[field]) > 256:
            raise ValueError("claim " + field + " is invalid")
    if not TOKEN_RE.fullmatch(claim["name"]) or not UID_RE.fullmatch(claim["uid"]) or not TOKEN_RE.fullmatch(claim["taskKey"]):
        raise ValueError("claim identity is invalid")
    if not CATALOG_RE.fullmatch(claim["repository"]):
        raise ValueError("claim repository must be the catalog name")
    if claim["project"] != raw["project"]:
        raise ValueError("claim scope does not match the request")
    fetched = raw.get("fetchedRef", "")
    if fetched is None:
        fetched = ""
    if not isinstance(fetched, str):
        raise ValueError("fetchedRef is invalid")
    if fetched:
        if "FETCH_HEAD" in fetched or "," in fetched or any(ch.isspace() for ch in fetched):
            raise ValueError("fetchedRef must be one explicit ref")
        if not HEAD_RE.fullmatch(fetched) and not REF_RE.fullmatch(fetched) and not PULL_REF_RE.fullmatch(fetched):
            raise ValueError("fetchedRef must be one explicit ref")
        match = PULL_REF_RE.fullmatch(fetched)
        if match and int(match.group(1)) != pr:
            raise ValueError("fetchedRef pull request does not match")
    events = raw.get("handoffEvents", [])
    if not isinstance(events, list) or len(events) > 48:
        raise ValueError("handoffEvents is invalid")
    artifacts = raw.get("verdictArtifacts", [])
    if not isinstance(artifacts, list) or len(artifacts) > 8:
        raise ValueError("verdictArtifacts is invalid")
    custody_message = raw.get("custodyMessage", "")
    if custody_message is None:
        custody_message = ""
    if not isinstance(custody_message, str) or (custody_message and not MSG_RE.fullmatch(custody_message)):
        raise ValueError("custodyMessage is invalid")
    req = dict(raw)
    req["fetchedRef"] = fetched
    req["handoffEvents"] = events
    req["verdictArtifacts"] = artifacts
    req["custodyMessage"] = custody_message
    req["_observed"] = observed
    req["_deadline"] = deadline
    req["_window"] = window
    return req


def identity_name(value):
    if len(value) > 253:
        return False
    return all(DNS_RE.fullmatch(part) for part in value.split("."))


def parse_time(value):
    if not isinstance(value, str) or not TIME_RE.fullmatch(value):
        return None
    return datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)


def identity_lines(req, head):
    owner = req["claim"]["owner"]
    recipient = req["recipient"]
    lines = [
        req["repository"],
        req["claim"]["repository"],
        str(req["pullRequest"]),
        req["task"],
        req["taskUID"],
        req["claim"]["name"],
        req["claim"]["uid"],
        owner["namespace"],
        owner["name"],
        owner["uid"],
        recipient["namespace"],
        recipient["name"],
        recipient["uid"],
    ]
    if head is not None:
        lines.append(head)
    return lines


def family_key(req):
    return "fam-" + hashlib.sha256("\n".join(identity_lines(req, None)).encode("utf-8")).hexdigest()[:48]


def logical_key(req, head):
    return "ror-" + hashlib.sha256("\n".join(identity_lines(req, head)).encode("utf-8")).hexdigest()[:48]


def intent_text(req, head):
    owner = req["claim"]["owner"]
    recipient = req["recipient"]
    body = {
        "schema": INTENT,
        "action": "read-only-review",
        "permission": "read-only",
        "repository": req["repository"],
        "catalogRepository": req["claim"]["repository"],
        "pullRequest": req["pullRequest"],
        "head": head,
        "project": req["project"],
        "story": req["story"],
        "task": req["task"],
        "taskUID": req["taskUID"],
        "claimName": req["claim"]["name"],
        "claimUID": req["claim"]["uid"],
        "taskKey": req["claim"]["taskKey"],
        "correlation": req["correlation"],
        "owner": req["owner"],
        "ownerUID": owner["uid"],
        "recipientNamespace": recipient["namespace"],
        "recipientName": recipient["name"],
        "recipientUID": recipient["uid"],
    }
    return json.dumps(body, sort_keys=True, separators=(",", ":"))


def review_digest(view):
    payload = {
        "head": view.get("head", ""),
        "observation": view.get("observation", ""),
        "source": view.get("source", ""),
        "sourceTime": view.get("sourceTime", ""),
        "task": view.get("task", ""),
        "uid": view.get("uid", ""),
        "verdict": view.get("verdict", ""),
    }
    return hashlib.sha256(json.dumps(payload, sort_keys=True, separators=(",", ":")).encode("utf-8")).hexdigest()


def base_result(req, **overrides):
    result = {
        "schema": SCHEMA,
        "status": "pending",
        "reason": "",
        "owner": req.get("owner", ""),
        "correlation": req.get("correlation", ""),
        "observedAt": req.get("observedAt", ""),
        "repository": req.get("repository", ""),
        "pullRequest": req.get("pullRequest", 0),
        "head": "",
        "logicalKey": "",
        "publication": "not-authorized",
        "notificationsSent": 0,
        "checkerAuthority": "none",
        "deployment": "not-performed",
        "taskClaim": "unchanged",
        "executed": False,
        "escalated": False,
        "windowRenewed": False,
        "schedulingUntil": "",
        "ignoredCallerKey": isinstance(req.get("logicalKey"), str) and bool(req.get("logicalKey")),
        "message": None,
        "states": {
            "queued": False,
            "apiDelivered": False,
            "readAck": False,
            "question": "none",
            "nativeTerminal": "absent",
            "verdict": "absent",
        },
        "verdict": None,
        "staleHeadRefused": False,
        "evidence": [],
        "gaps": list(GAPS),
        "attempt": 0,
        "nextAttemptAt": "",
    }
    result.update(overrides)
    return result


def emit(result):
    for key in list(result):
        if key.startswith("_"):
            result.pop(key)
    json.dump(result, sys.stdout, sort_keys=True)
    sys.stdout.write("\n")


def refuse(reason):
    emit({
        "schema": SCHEMA,
        "status": "refused",
        "exitCode": 2,
        "reason": reason,
        "publication": "not-authorized",
        "notificationsSent": 0,
        "checkerAuthority": "none",
        "deployment": "not-performed",
        "taskClaim": "unchanged",
        "executed": False,
        "escalated": False,
        "gaps": list(GAPS),
    })
    print(reason, file=sys.stderr)


def child_env():
    keep = ("PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TZ", "OP355_FIXTURE_STORE", "WORKFLOW_HANDOFF_BIN") + CLI_ENV
    env = {key: os.environ[key] for key in keep if key in os.environ}
    env["GOMAXPROCS"] = "2"
    env["CGO_ENABLED"] = "0"
    env["GOTOOLCHAIN"] = "local"
    return env


def run_cmd(result, argv, stdin="", on_timeout=None):
    exe = shutil.which(argv[0])
    if not exe:
        stop(result, "blocked", "command-not-found", detail=argv[0])
    result["executed"] = True
    try:
        completed = subprocess.run(
            [exe, *argv[1:]],
            input=stdin,
            text=True,
            capture_output=True,
            timeout=10,
            env=child_env(),
            check=False,
        )
    except subprocess.TimeoutExpired:
        if on_timeout is not None:
            on_timeout()
        stop(result, "blocked", "command-timeout", detail=argv[0])
    detail = " ".join(completed.stderr.split())[:200]
    return completed.returncode, completed.stdout, detail


def load_cache(path):
    if not path.exists():
        return {"schema": CACHE_SCHEMA, "entries": {}}
    text = path.read_text(encoding="utf-8")
    if not text.strip():
        return {"schema": CACHE_SCHEMA, "entries": {}}
    try:
        loaded = json.loads(text)
    except json.JSONDecodeError as exc:
        raise ValueError("cache is malformed") from exc
    if not isinstance(loaded, dict) or loaded.get("schema") != CACHE_SCHEMA or not isinstance(loaded.get("entries"), dict):
        raise ValueError("cache is malformed")
    return loaded


def persist_cache(path, cache):
    temporary = Path(str(path) + ".tmp")
    handle = temporary.open("w", encoding="utf-8")
    try:
        json.dump(cache, handle, sort_keys=True)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    finally:
        handle.close()
    os.replace(temporary, path)
    directory = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def stop(result, status, reason, code=0, detail=""):
    result["status"] = status
    result["reason"] = reason
    if detail:
        result["evidence"].append({"name": reason, "at": result["observedAt"], "detail": detail})
    raise Stop(result, code)


def observe(req, cache, persist):
    result = base_result(req)
    head = observe_head(req, result)
    result["head"] = head
    key = logical_key(req, head)
    result["logicalKey"] = key
    text = intent_text(req, head)
    if req["fetchedRef"] and HEAD_RE.fullmatch(req["fetchedRef"]) and req["fetchedRef"] != head:
        stop(result, "blocked", "fetched-ref-mismatch", detail=req["fetchedRef"])
    claim_ok = match_claim(req, result)
    native = classify_native(req, result)
    reduction = run_checker(req, head, result)
    sent = list_inbox(req, result, sent=True)
    received = list_inbox(req, result, sent=False)
    question_state(req, received, result)
    messages = read_sent(req, sent, result)
    reconcile_proven_outcome(req, cache, messages, key, text)
    current = find_current(messages, key, text, req, result)
    if current is not None:
        result["message"] = {"name": current["name"], "uid": current["uid"], "key": current["key"]}
        result["states"]["queued"] = True
        result["states"]["apiDelivered"] = True
        classify_read_ack(req, current, result)
    result["_listed"] = sent + received
    if req["_observed"] < req["_window"]:
        result["schedulingUntil"] = req["windowExpiresAt"]
    else:
        result["_renew"] = True
    picked = picked_review(reduction, req, head, result)
    if picked is not None:
        finish_verdict(req, result, picked, current, native)
        return result
    if current is not None:
        finish_existing(req, result, current, native)
        return result
    publish(req, result, cache, key, text, messages, native, persist)
    return result


def observe_head(req, result):
    code, out, detail = run_cmd(
        result,
        ["gh", "pr", "view", "--repo", req["repository"], str(req["pullRequest"]), "--json", "headRefOid"],
    )
    if code != 0:
        stop(result, "blocked", "head-observation-failed", detail=detail or "gh pr view failed")
    try:
        doc = json.loads(out)
    except json.JSONDecodeError:
        stop(result, "blocked", "head-observation-malformed")
    if not isinstance(doc, dict) or list(doc) != ["headRefOid"] or not isinstance(doc.get("headRefOid"), str) or not HEAD_RE.fullmatch(doc["headRefOid"]):
        stop(result, "blocked", "head-observation-ambiguous")
    result["evidence"].append({"name": "current-head", "at": result["observedAt"], "detail": doc["headRefOid"]})
    return doc["headRefOid"]


def match_claim(req, result):
    owner = show_session(req, result, req["claim"]["owner"]["name"])
    if owner.get("uid") != req["claim"]["owner"]["uid"]:
        stop(result, "blocked", "show-identity-mismatch", detail="claim-owner")
    observe_owned_claim(req, result, owner)
    recipient = show_session(req, result, req["session"])
    if recipient.get("name") != req["recipient"]["name"] or recipient.get("uid") != req["recipient"]["uid"]:
        stop(result, "blocked", "show-identity-mismatch", detail="recipient")
    result["_session"] = recipient
    result["evidence"].append({"name": "recipient-observed", "at": result["observedAt"], "detail": recipient.get("uid", "")})
    return True


def show_session(req, result, name):
    code, out, detail = run_cmd(result, ["agent-run", "show", name, "-o", "json"])
    if code == 6:
        stop(result, "blocked", "unconfirmed-show", detail=detail or name)
    if code != 0:
        stop(result, "blocked", "show-failed", detail=detail or name)
    try:
        session = json.loads(out)
    except json.JSONDecodeError:
        stop(result, "blocked", "show-malformed", detail=name)
    if not isinstance(session, dict) or session.get("name") != name or not isinstance(session.get("uid"), str) or not UID_RE.fullmatch(session["uid"]):
        stop(result, "blocked", "show-identity-mismatch", detail=name)
    return session


def observe_owned_claim(req, result, session):
    claims = session.get("taskClaims")
    if not isinstance(claims, dict):
        stop(result, "blocked", "task-claims-missing")
    state = claims.get("state")
    reason = claims.get("reason")
    items = claims.get("items")
    if reason == "NoJoinedClaims" or items == []:
        stop(result, "blocked", "claim-not-found")
    if state != "Available" or reason != "Complete" or not isinstance(items, list):
        stop(result, "blocked", "task-claims-unknown", detail="%s %s" % (state, reason))
    matches = []
    for item in items:
        if not isinstance(item, dict):
            stop(result, "blocked", "task-claims-unknown", detail="malformed item")
        owner = item.get("owner")
        if not isinstance(owner, dict) or owner.get("uid") != session.get("uid"):
            stop(result, "blocked", "claim-not-owned")
        if claim_matches(req, item):
            matches.append(item)
    if not matches:
        stop(result, "blocked", "claim-not-found")
    if len(matches) != 1:
        stop(result, "blocked", "claim-ambiguous")
    item = matches[0]
    created = parse_time(item.get("createdAt"))
    if created is None or created > req["_observed"]:
        stop(result, "blocked", "claim-time-invalid")
    if item.get("archived") is True:
        stop(result, "blocked", "claim-archived")
    owner_state = item.get("ownerState")
    if not isinstance(owner_state, str) or not owner_state or owner_state == "Unknown":
        stop(result, "blocked", "claim-owner-state-unknown")
    provenance = item.get("provenance")
    if not isinstance(provenance, dict):
        stop(result, "blocked", "claim-provenance-missing")
    github = provenance.get("github")
    if not isinstance(github, str) or github != req["repository"]:
        stop(result, "blocked", "claim-repository-mismatch")
    reported_at = provenance.get("reportedAt")
    if reported_at is not None:
        stamp = parse_time(reported_at)
        if stamp is None or stamp > req["_observed"]:
            stop(result, "blocked", "claim-provenance-future")
    reported_head = provenance.get("reportedHead", "")
    if reported_head:
        if not isinstance(reported_head, str) or not HEAD_RE.fullmatch(reported_head):
            stop(result, "blocked", "claim-reported-head-invalid")
        result["evidence"].append({"name": "claim-reported-head", "at": result["observedAt"], "detail": reported_head})
    result["evidence"].append({"name": "claim-observed", "at": item.get("createdAt"), "detail": item.get("uid", "")})


def claim_matches(req, item):
    identity = item.get("identity")
    owner = item.get("owner")
    claim = req["claim"]
    if not isinstance(identity, dict) or not isinstance(owner, dict):
        return False
    return (
        identity.get("project") == claim["project"]
        and identity.get("repository") == claim["repository"]
        and identity.get("taskKey") == claim["taskKey"]
        and item.get("name") == claim["name"]
        and item.get("uid") == claim["uid"]
        and owner == claim["owner"]
    )


def classify_native(req, result):
    session = result.get("_session") or {}
    conditions = session.get("conditions")
    if isinstance(conditions, list):
        for condition in conditions:
            if isinstance(condition, dict) and condition.get("type") == "Ready" and condition.get("status") == "True":
                result["evidence"].append({"name": "pod-ready-not-provider-proof", "at": result["observedAt"], "detail": session.get("phase", "")})
                break
    if "wrapperExit" in session:
        result["evidence"].append({"name": "wrapper-exit-not-provider-proof", "at": result["observedAt"], "detail": str(session.get("wrapperExit"))[:40]})
    if "outcome" in session and session.get("outcome") is None:
        result["evidence"].append({"name": "null-outcome-not-provider-proof", "at": result["observedAt"], "detail": "ignored"})
    agent = session.get("agentStatus")
    if not isinstance(agent, dict) or not isinstance(agent.get("task"), dict):
        result["states"]["nativeTerminal"] = "absent"
        return "absent"
    task = agent["task"]
    finished = parse_time(task.get("finishedAt"))
    if finished is None or finished > req["_observed"]:
        stop(result, "blocked", "future-native-evidence")
    exit_code = task.get("exitCode")
    if not isinstance(exit_code, int) or isinstance(exit_code, bool):
        result["states"]["nativeTerminal"] = "unknown"
        return "unknown"
    failed = exit_code != 0 or task.get("isError") is True or task.get("timedOut") is True or str(task.get("subtype", "")).startswith("error")
    if failed:
        result["states"]["nativeTerminal"] = "failed"
        result["evidence"].append({"name": "native-terminal-nonzero", "at": task["finishedAt"], "detail": "exit %s" % exit_code})
        result["_finished"] = finished
        return "failed"
    if exit_code == 0 and task.get("subtype") == "success" and task.get("isError") is not True and task.get("timedOut") is not True:
        result["states"]["nativeTerminal"] = "succeeded"
        result["evidence"].append({"name": "native-terminal-success", "at": task["finishedAt"], "detail": "agentStatus.task"})
        result["_finished"] = finished
        return "succeeded"
    result["states"]["nativeTerminal"] = "unknown"
    result["evidence"].append({"name": "native-terminal-unknown", "at": task.get("finishedAt", result["observedAt"]), "detail": "exit 0 without success"})
    return "unknown"


def run_checker(req, head, result):
    document = {
        "schema": "dev-env.workflow-handoff/v1",
        "story": req["story"],
        "relatedStories": req["relatedStories"],
        "repository": req["handoffRepository"],
        "pullRequest": req["pullRequest"],
        "head": head,
        "coordinator": req["owner"],
        "reviewBinding": {"task": req["task"], "uid": req["taskUID"]},
        "modelProvenance": {"serving": "Unknown", "cost": "Unknown", "configuredUI": ""},
        "events": req["handoffEvents"],
    }
    encoded = json.dumps(document).encode("utf-8")
    if len(encoded) > 128 * 1024:
        stop(result, "blocked", "handoff-too-large")
    root = Path(__file__).resolve().parents[1]
    binary = os.environ.get("WORKFLOW_HANDOFF_BIN", "")
    with tempfile.NamedTemporaryFile(prefix="handoff-", suffix=".json") as handle:
        handle.write(encoded)
        handle.flush()
        if binary:
            argv = [binary, handle.name]
        else:
            argv = ["go", "run", "./scripts/trials/workflow_handoff", handle.name]
        exe = shutil.which(argv[0])
        if not exe:
            stop(result, "blocked", "command-not-found", detail=argv[0])
        result["executed"] = True
        try:
            completed = subprocess.run(
                [exe, *argv[1:]],
                text=True,
                capture_output=True,
                timeout=30,
                cwd=str(root) if not binary else None,
                env=child_env(),
                check=False,
            )
        except subprocess.TimeoutExpired:
            stop(result, "blocked", "command-timeout", detail="workflow_handoff")
    if not completed.stdout.strip():
        stop(result, "blocked", "handoff-malformed", detail=" ".join(completed.stderr.split())[:200])
    try:
        reduced = json.loads(completed.stdout)
    except json.JSONDecodeError:
        stop(result, "blocked", "handoff-malformed")
    if not isinstance(reduced, dict):
        stop(result, "blocked", "handoff-malformed")
    if completed.returncode == 2 or reduced.get("status") == "refused":
        stop(result, "blocked", "handoff-refused", detail=str((reduced.get("refusals") or [""])[0])[:200])
    if completed.returncode not in (0, 3):
        stop(result, "blocked", "handoff-failed", detail="exit %s" % completed.returncode)
    refusals = reduced.get("refusals") if isinstance(reduced.get("refusals"), list) else []
    result["staleHeadRefused"] = any(isinstance(item, str) and item.startswith("wrong-head ") for item in refusals)
    if reduced.get("conflicts"):
        stop(result, "blocked", "handoff-conflict")
    result["evidence"].append({"name": "handoff-reduced", "at": result["observedAt"], "detail": "exit %s" % completed.returncode})
    return reduced


def list_inbox(req, result, sent):
    items = []
    cursor = ""
    seen = set()
    for _ in range(4):
        argv = ["agent-run", "inbox", "list", "--project", req["project"], "--limit", "20", "-o", "json"]
        if sent:
            argv.append("--sent")
        if cursor:
            argv.extend(["--continue", cursor])
        code, out, detail = run_cmd(result, argv)
        if code != 0:
            stop(result, "blocked", "inbox-list-failed", detail=detail or "inbox list failed")
        try:
            doc = json.loads(out)
        except json.JSONDecodeError:
            stop(result, "blocked", "inbox-list-malformed")
        if not isinstance(doc, dict) or doc.get("view") != "thread":
            stop(result, "blocked", "inbox-list-partial", detail="thread view missing")
        if sent and doc.get("box") != "sent":
            stop(result, "blocked", "inbox-list-partial", detail="sent box missing")
        batch = doc.get("items")
        if not isinstance(batch, list) or len(batch) > 20:
            stop(result, "blocked", "inbox-list-partial")
        for item in batch:
            if not message_meta_ok(item, req["project"]):
                stop(result, "blocked", "inbox-identity-unconfirmed")
        items.extend(batch)
        cont = doc.get("continue") or ""
        if not isinstance(cont, str):
            stop(result, "blocked", "inbox-list-partial")
        if not cont:
            if len(items) > 8:
                stop(result, "blocked", "inbox-history-unbounded")
            return items
        if cont in seen:
            stop(result, "blocked", "inbox-list-partial", detail="repeated continuation")
        seen.add(cont)
        cursor = cont
    stop(result, "blocked", "inbox-list-partial", detail="page cap")


def message_meta_ok(item, project):
    if not isinstance(item, dict):
        return False
    sender = item.get("sender")
    recipient = item.get("recipient")
    return (
        item.get("project") == project
        and isinstance(item.get("name"), str)
        and MSG_RE.fullmatch(item["name"])
        and isinstance(item.get("uid"), str)
        and UID_RE.fullmatch(item["uid"])
        and isinstance(item.get("key"), str)
        and isinstance(sender, dict)
        and isinstance(recipient, dict)
        and party_ok(sender)
        and party_ok(recipient)
    )


def party_ok(party):
    return (
        isinstance(party.get("namespace"), str)
        and isinstance(party.get("name"), str)
        and isinstance(party.get("uid"), str)
        and UID_RE.fullmatch(party["uid"])
    )


def read_sent(req, sent, result):
    messages = []
    for item in sent:
        code, out, detail = run_cmd(result, ["agent-run", "inbox", "read", item["name"], "--project", req["project"], "-o", "json"])
        if code == 6:
            stop(result, "blocked", "unconfirmed-read", detail=item["name"])
        if code != 0:
            stop(result, "blocked", "inbox-read-failed", detail=detail or item["name"])
        try:
            doc = json.loads(out)
        except json.JSONDecodeError:
            stop(result, "blocked", "inbox-read-malformed")
        if not isinstance(doc, dict) or doc.get("name") != item["name"] or doc.get("uid") != item["uid"] or doc.get("key") != item["key"]:
            stop(result, "blocked", "inbox-identity-unconfirmed")
        if not isinstance(doc.get("text"), str) or not doc["text"]:
            stop(result, "blocked", "inbox-read-malformed")
        if "sender" in doc and doc["sender"] != item["sender"]:
            stop(result, "blocked", "inbox-provenance-mismatch")
        if "recipient" in doc and doc["recipient"] != item["recipient"]:
            stop(result, "blocked", "inbox-provenance-mismatch")
        doc.setdefault("sender", item["sender"])
        doc.setdefault("recipient", item["recipient"])
        if not party_ok(doc.get("sender", {})) or not party_ok(doc.get("recipient", {})):
            stop(result, "blocked", "inbox-identity-unconfirmed")
        messages.append(doc)
    return messages


def find_current(messages, key, text, req, result):
    found = [item for item in messages if item.get("key") == key]
    if not found:
        return None
    if len(found) != 1:
        stop(result, "blocked", "duplicate-inbox-message")
    current = found[0]
    if current.get("text") != text:
        stop(result, "blocked", "inbox-key-conflict")
    if current.get("sender") != req["authenticatedSender"] or current.get("recipient") != req["recipient"]:
        stop(result, "blocked", "inbox-provenance-mismatch")
    return current


def question_state(req, received, result):
    key = "q-" + req["correlation"]
    matched = [item for item in received if item.get("key") == key and item.get("kind") == "question"]
    if not matched:
        return
    if len(matched) != 1:
        stop(result, "blocked", "question-ambiguous")
    item = matched[0]
    code, out, detail = run_cmd(result, ["agent-run", "inbox", "read", item["name"], "--project", req["project"], "-o", "json"])
    if code != 0:
        stop(result, "blocked", "question-unconfirmed", detail=detail)
    try:
        doc = json.loads(out)
    except json.JSONDecodeError:
        stop(result, "blocked", "question-unconfirmed")
    thread = doc.get("thread") if isinstance(doc, dict) else None
    state = thread.get("state") if isinstance(thread, dict) else ""
    if state in {"Answered", "Acknowledged"}:
        result["states"]["question"] = "answered"
        result["evidence"].append({"name": "question-answered", "at": result["observedAt"], "detail": state})
        return
    if state in {"Unanswered", "Stale"}:
        result["states"]["question"] = "unanswered"
        stop(result, "blocked", "unanswered-question", detail=state)
    result["states"]["question"] = "unknown"
    stop(result, "blocked", "unknown-question")


def classify_read_ack(req, current, result):
    ack, kind = read_ack(req, current["name"], result)
    if kind == "unconfirmed":
        result["escalated"] = True
        stop(result, "blocked", "lost-ack", detail=current["name"])
    if kind == "missing":
        result["states"]["readAck"] = False
        return
    if kind != "ok":
        stop(result, "blocked", "ack-status-failed", detail=kind)
    if ack_time_problem(req, ack):
        result["evidence"].append({"name": "future-ack-rejected", "at": ack.get("createdAt", ""), "detail": current["name"]})
        return
    if ack.get("recipient") != req["recipient"] or ack.get("messageUID") != current.get("uid"):
        result["evidence"].append({"name": "foreign-ack-rejected", "at": ack.get("createdAt", ""), "detail": current["name"]})
        return
    result["states"]["readAck"] = True
    result["evidence"].append({"name": "read-ack", "at": ack.get("createdAt", ""), "detail": ack.get("uid", "")})


def read_ack(req, name, result):
    code, out, detail = run_cmd(result, ["agent-run", "inbox", "ack-status", name, "--project", req["project"], "-o", "json"])
    if code == 4:
        return None, "missing"
    if code == 6:
        return None, "unconfirmed"
    if code != 0:
        return None, detail or "failed"
    try:
        ack = json.loads(out)
    except json.JSONDecodeError:
        return None, "malformed"
    if not isinstance(ack, dict) or ack.get("project") != req["project"] or ack.get("messageName") != name:
        return None, "malformed"
    if not isinstance(ack.get("uid"), str) or not ack["uid"] or not isinstance(ack.get("messageUID"), str):
        return None, "malformed"
    if not isinstance(ack.get("recipient"), dict) or not party_ok(ack["recipient"]):
        return None, "malformed"
    if parse_time(ack.get("createdAt")) is None:
        return None, "malformed"
    return ack, "ok"


def ack_time_problem(req, ack):
    created = parse_time(ack.get("createdAt"))
    return created is None or created > req["_observed"]


def renew_window(req, listed, result):
    if req["_observed"] < req["_window"]:
        result["schedulingUntil"] = req["windowExpiresAt"]
        return
    name = req["custodyMessage"]
    if not name:
        stop(result, "blocked", "scheduling-expired")
    meta = next((item for item in listed if item.get("name") == name), None)
    if meta is None:
        stop(result, "blocked", "scheduling-expired", detail="custody message not listed")
    code, out, detail = run_cmd(result, ["agent-run", "inbox", "read", name, "--project", req["project"], "-o", "json"])
    if code != 0:
        stop(result, "blocked", "custody-unconfirmed", detail=detail)
    try:
        message = json.loads(out)
    except json.JSONDecodeError:
        stop(result, "blocked", "custody-unconfirmed")
    if not isinstance(message, dict) or not custody_body_ok(req, message.get("text", "")):
        stop(result, "blocked", "custody-scope-mismatch")
    if message.get("recipient") != req["claim"]["owner"] and meta.get("recipient") != req["claim"]["owner"]:
        stop(result, "blocked", "foreign-ack-rejected")
    ack, kind = read_ack(req, name, result)
    if kind == "missing":
        stop(result, "blocked", "scheduling-expired", detail="custody ack absent")
    if kind == "unconfirmed":
        result["escalated"] = True
        stop(result, "blocked", "lost-ack", detail=name)
    if kind != "ok":
        stop(result, "blocked", "ack-status-failed", detail=kind)
    created = parse_time(ack.get("createdAt"))
    if created > req["_observed"]:
        stop(result, "blocked", "future-ack-rejected")
    if ack.get("recipient") != req["claim"]["owner"] or ack.get("messageUID") not in {message.get("uid"), meta.get("uid")}:
        stop(result, "blocked", "foreign-ack-rejected")
    if created <= req["_window"]:
        stop(result, "blocked", "scheduling-expired", detail="ack does not postdate the window")
    if req["_deadline"] <= req["_observed"]:
        stop(result, "blocked", "deadline-elapsed")
    result["windowRenewed"] = True
    result["schedulingUntil"] = req["deadline"]
    result["evidence"].append({"name": "scheduling-renewed", "at": ack["createdAt"], "detail": req["deadline"]})


def custody_body_ok(req, text):
    try:
        body = json.loads(text)
    except json.JSONDecodeError:
        return False
    if not isinstance(body, dict):
        return False
    return (
        body.get("repository") == req["repository"]
        and body.get("pullRequest") == req["pullRequest"]
        and body.get("task") == req["task"]
        and body.get("correlation") == req["correlation"]
        and body.get("ownerUID") == req["claim"]["owner"]["uid"]
    )


def picked_review(reduction, req, head, result):
    reviews = reduction.get("reviews") if isinstance(reduction.get("reviews"), list) else []
    picked = [item for item in reviews if isinstance(item, dict) and item.get("picked") is True]
    if not picked:
        return None
    if len(picked) != 1:
        stop(result, "blocked", "verdict-ambiguous")
    one = picked[0]
    if one.get("head") != head or one.get("task") != req["task"] or one.get("uid") != req["taskUID"]:
        stop(result, "blocked", "verdict-binding-mismatch")
    result["states"]["verdict"] = "picked"
    result["evidence"].append({"name": "verdict-picked", "at": one.get("sourceTime", result["observedAt"]), "detail": one.get("verdict", "")})
    return one


def artifact_for(req, view):
    want = review_digest(view)
    matches = []
    for item in req["verdictArtifacts"]:
        if not isinstance(item, dict):
            continue
        if item.get("observation") == view.get("observation") and item.get("head") == view.get("head") and item.get("digest") == want:
            reference = item.get("reference")
            if isinstance(reference, str) and 1 <= len(reference) <= 256 and reference.strip() == reference and "\n" not in reference:
                matches.append(item)
    return matches


def finish_verdict(req, result, picked, current, native):
    if picked.get("verdict") not in COMPLETING:
        result["states"]["verdict"] = "attested-unverified"
        stop(result, "blocked", "verdict-not-actionable", detail=str(picked.get("verdict", "")))
    digest = review_digest(picked)
    matched = [item for item in artifact_for(req, picked) if item.get("digest") == digest]
    reference = matched[0].get("reference", "") if len(matched) == 1 else ""
    result["states"]["verdict"] = "attested-unverified"
    result["verdict"] = {
        "task": picked.get("task"),
        "uid": picked.get("uid"),
        "head": picked.get("head"),
        "verdict": picked.get("verdict"),
        "observation": picked.get("observation"),
        "sourceTime": picked.get("sourceTime"),
        "digest": digest,
        "reference": reference,
        "attestation": "caller-reducer",
        "verified": False,
        "receiver": "absent",
    }
    result["evidence"].append({
        "name": "attestation-not-completion",
        "at": result["observedAt"],
        "detail": "caller reducer observation is display only; authenticated receiver evidence is absent",
    })
    result["status"] = "pending"
    result["reason"] = "attested-unverified"
    return result


def finish_existing(req, result, current, native):
    created = parse_time(current.get("createdAt")) if isinstance(current.get("createdAt"), str) else None
    finished = result.get("_finished")
    if native == "failed" and created is not None and finished is not None and finished > created:
        result["escalated"] = True
        stop(result, "blocked", "native-terminal-nonzero", detail="failure after the inbox intent")
    result["status"] = "pending"
    result["reason"] = "awaiting-recipient" if native != "failed" else "awaiting-recipient-after-native-failure"
    if result["states"]["verdict"] == "absent":
        result["evidence"].append({"name": "verdict-missing", "at": result["observedAt"], "detail": "ack and process exit are not a verdict"})
    return result


def intent_body(message):
    try:
        body = json.loads(message.get("text", ""))
    except json.JSONDecodeError:
        return None
    if not isinstance(body, dict) or body.get("schema") != INTENT:
        return None
    return body


def same_family(req, message):
    body = intent_body(message)
    if body is None:
        return False
    owner = req["claim"]["owner"]
    recipient = req["recipient"]
    return (
        body.get("repository") == req["repository"]
        and body.get("catalogRepository") == req["claim"]["repository"]
        and body.get("pullRequest") == req["pullRequest"]
        and body.get("task") == req["task"]
        and body.get("taskUID") == req["taskUID"]
        and body.get("ownerUID") == owner["uid"]
        and body.get("recipientUID") == recipient["uid"]
        and body.get("claimUID") == req["claim"]["uid"]
    )


def correlation_conflicts(req, family, entry):
    if isinstance(entry, dict):
        bound = entry.get("correlation")
        if isinstance(bound, str) and bound and bound != req["correlation"]:
            return True
    for item in family:
        body = intent_body(item)
        if body is None:
            continue
        bound = body.get("correlation")
        if isinstance(bound, str) and bound != req["correlation"]:
            return True
    return False


def reconcile_proven_outcome(req, cache, messages, key, text):
    fam = family_key(req)
    entry = cache["entries"].get(fam)
    if not isinstance(entry, dict) or entry.get("class") not in {"uncertain", "unconfirmed"}:
        return
    if entry.get("intentKey") != key:
        return
    digest = entry.get("digest")
    if not isinstance(digest, str) or hashlib.sha256(text.encode("utf-8")).hexdigest() != digest:
        return
    proved = [
        item for item in messages
        if item.get("key") == key
        and item.get("text") == text
        and item.get("sender") == req["authenticatedSender"]
        and item.get("recipient") == req["recipient"]
    ]
    if len(proved) != 1:
        return
    cache["entries"].pop(fam, None)


def publish(req, result, cache, key, text, messages, native, persist):
    if req["_observed"] >= req["_deadline"]:
        stop(result, "blocked", "deadline-elapsed")
    if result.get("_renew"):
        renew_window(req, result.get("_listed") or [], result)
    family = [item for item in messages if same_family(req, item)]
    if len(family) >= 3:
        result["escalated"] = True
        stop(result, "blocked", "persistent-failure", detail="inbox history cap")
    fam = family_key(req)
    entry = cache["entries"].get(fam)
    if isinstance(entry, dict) and entry.get("class") in NONREPLAY:
        result["escalated"] = entry.get("class") in {"unconfirmed", "pending", "uncertain"}
        if entry.get("class") == "uncertain":
            result["evidence"].append({
                "name": "unknown-send",
                "at": entry.get("at") or result["observedAt"],
                "detail": entry.get("intentKey") or key,
            })
        stop(result, "blocked", entry["class"] + "-not-replayed")
    if correlation_conflicts(req, family, entry):
        stop(result, "blocked", "correlation-conflict")
    attempt = 0
    if isinstance(entry, dict) and entry.get("class") == "retry":
        attempt = entry.get("attempts") if isinstance(entry.get("attempts"), int) else 0
        nxt = parse_time(entry.get("nextAttemptAt"))
        if attempt >= 3:
            result["escalated"] = True
            stop(result, "blocked", "persistent-failure", detail="not-sent retry cap")
        if nxt is not None and req["_observed"] < nxt:
            result["status"] = "retrying"
            result["reason"] = "backoff"
            result["attempt"] = attempt
            result["nextAttemptAt"] = entry["nextAttemptAt"]
            return result
    remember(cache, req, key, text, "uncertain", None, attempts=max(attempt, 1))
    persist()

    def on_timeout():
        result["escalated"] = True
        result["evidence"].append({"name": "unknown-send", "at": result["observedAt"], "detail": key})
        stop(result, "blocked", "uncertain-not-replayed", detail="timeout")

    code, out, detail = run_cmd(
        result,
        [
            "agent-run", "inbox", "send",
            "--project", req["project"],
            "--recipient-namespace", req["recipient"]["namespace"],
            "--recipient-name", req["recipient"]["name"],
            "--recipient-uid", req["recipient"]["uid"],
            "--key", key,
        ],
        stdin=text,
        on_timeout=on_timeout,
    )
    if code == 0:
        try:
            receipt = json.loads(out)
        except json.JSONDecodeError:
            receipt = None
        message = receipt.get("message") if isinstance(receipt, dict) else None
        if not isinstance(receipt, dict) or receipt.get("outcome") not in {"Created", "Confirmed"} or not isinstance(message, dict) or message.get("key") != key:
            remember(cache, req, key, text, "unconfirmed", 6)
            result["escalated"] = True
            stop(result, "blocked", "unconfirmed-not-replayed")
        if message.get("sender") != req["authenticatedSender"] or message.get("recipient") != req["recipient"]:
            remember(cache, req, key, text, "unconfirmed", 6)
            stop(result, "blocked", "inbox-provenance-mismatch")
        result["message"] = {"name": message.get("name"), "uid": message.get("uid"), "key": key}
        result["states"]["queued"] = True
        result["states"]["apiDelivered"] = True
        result["evidence"].append({"name": "inbox-sent", "at": result["observedAt"], "detail": message.get("name", "")})
        if native == "failed":
            result["status"] = "retrying"
            result["reason"] = "native-terminal-nonzero"
        else:
            result["status"] = "pending"
            result["reason"] = "inbox-delivered"
        cache["entries"].pop(fam, None)
        return result
    if code == 5:
        attempt += 1
        delay = BACKOFF[min(attempt - 1, len(BACKOFF) - 1)]
        nxt = (req["_observed"] + timedelta(seconds=delay)).strftime("%Y-%m-%dT%H:%M:%SZ")
        remember(cache, req, key, text, "retry", 5, attempts=attempt, nxt=nxt)
        if attempt >= 3:
            result["escalated"] = True
            result["status"] = "blocked"
            result["reason"] = "persistent-failure"
        else:
            result["status"] = "retrying"
            result["reason"] = "not-sent-retry"
        result["attempt"] = attempt
        result["nextAttemptAt"] = nxt
        return result
    if code == 6:
        remember(cache, req, key, text, "unconfirmed", 6)
        result["escalated"] = True
        stop(result, "blocked", "unconfirmed-not-replayed", detail=detail)
    if code == 7:
        remember(cache, req, key, text, "pending", 7)
        result["escalated"] = True
        stop(result, "blocked", "pending-not-replayed", detail=detail)
    if code == 1 and "conflict" in detail:
        remember(cache, req, key, text, "conflict", 1)
        stop(result, "blocked", "conflict-not-replayed", detail=detail)
    remember(cache, req, key, text, "denied", code)
    stop(result, "blocked", "denied-not-replayed", detail=detail or "exit %s" % code)


def remember(cache, req, key, text, klass, exit_code, attempts=1, nxt=""):
    cache["entries"][family_key(req)] = {
        "class": klass,
        "exit": exit_code,
        "at": req["observedAt"],
        "attempts": attempts,
        "nextAttemptAt": nxt,
        "intentKey": key,
        "digest": hashlib.sha256(text.encode("utf-8")).hexdigest(),
        "correlation": req["correlation"],
    }


if __name__ == "__main__":
    sys.exit(main(sys.argv))
