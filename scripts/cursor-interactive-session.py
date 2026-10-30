#!/usr/bin/env python3
"""Start, inspect, and reply to one dedicated interactive Cursor session.

Uses tmux and the installed cursor-agent executable. It does not call persist,
does not pass trust or approval flags, and does not claim the served model.
"""

import argparse
from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import shutil
import subprocess
import sys
import tempfile

MAX_TEXT = 4096
MAX_CAPTURE = 8192
MAX_LINES = 80
MAX_COMMAND_BYTES = 8192
COMMAND_TIMEOUT = 5
OWNER_OPTION = "@cursor_interactive_owner"
IDENTITY_FORMAT = (
    "#{session_name}\t#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{pane_current_path}\t"
    "#{pane_current_command}\t#{@cursor_interactive_owner}\t#{cursor_y}\t#{pane_height}\t#{pane_in_mode}"
)
ALLOWED_FLAGS = ("--workspace", "--model")
MODALS = (
    ("modal-trust", ("trust this workspace", "trust the contents of", "workspace trust", "do you trust")),
    ("modal-permission", ("permission required", "needs permission", "requesting permission")),
    ("modal-approval", ("allow this command", "run this command?", "approval required",
                         "waiting for approval", "approve this command")),
    ("modal-unknown", ("(y/n)", "yes/no", "press enter to confirm", "are you sure")),
)
COMPOSER = re.compile(r"^[>›❯][ \t]?(.*)$")
RECEIPT_RE = re.compile(r"^v1:([0-9a-f]{32}):([0-9a-f]{64}):(%[0-9]+):([1-9][0-9]*)$")
CLASSIFICATION_DETAILS = {
    "active": (
        "Cursor row is only an empty composer prefix. This is not proof the UI is idle, "
        "that no modal is hidden, or that the served model matches the request. "
        "Echoed lines above that row are not a user draft."
    ),
    "user-draft": (
        "Cursor row has a nonempty composer. Echoed lines above that row are not a user draft."
    ),
    "unclassified": (
        "Cursor UI is unclassified. Echoed terminal text is not an unsent user draft. "
        "The helper only treats the cursor row as a composer when that row is a bare >, ›, or ❯ prompt. "
        "This screen does not match that shape or a known modal phrase. "
        "Absence of those phrases is not proof the screen is safe. "
        "Sending requires a fresh receipt and --observed-ready after manual confirmation of this exact receipt. "
        "--observed-ready is not a Cursor trust, approval, or permission grant."
    ),
    "modal-trust": "Known workspace-trust phrasing is in the captured window.",
    "modal-permission": "Known permission phrasing is in the captured window.",
    "modal-approval": "Known approval phrasing is in the captured window.",
    "modal-unknown": "An unrecognized confirmation prompt is in the captured window.",
}


class Refusal(Exception):
    def __init__(self, state, detail, code=2):
        self.state, self.detail, self.code = state, detail, code


def run(argv):
    return subprocess.run(
        argv, stdin=subprocess.DEVNULL, capture_output=True, text=True,
        encoding="utf-8", errors="strict", timeout=COMMAND_TIMEOUT, check=False)


def executable(value):
    if "\0" in value or "\n" in value or "\r" in value:
        raise Refusal("invalid-input", "Executable path must be a single line without NUL.")
    result = shutil.which(value)
    if not result:
        raise Refusal("prerequisite-unavailable", "cursor-agent or tmux is not an installed executable.")
    return str(Path(result).resolve())


def owner_marker(name, worktree):
    digest = hashlib.sha256(name.encode("utf-8") + b"\0" + worktree.encode("utf-8")).hexdigest()[:32]
    return "ci1-" + digest


def state_path(name):
    root = Path(os.environ.get("XDG_STATE_HOME", str(Path.home() / ".local" / "state")))
    if not root.is_absolute():
        raise Refusal("invalid-input", "XDG_STATE_HOME must be absolute when set.")
    return root / "cursor-interactive-session" / name


def state_directory(path):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(path.parent, 0o700)


@contextmanager
def state_lock(name):
    validate_name(name)
    path = state_path(name)
    state_directory(path)
    # Keep this inode: unlinking it would let another process lock a new inode.
    descriptor = os.open(path.with_name(path.name + ".lock"), os.O_CREAT | os.O_RDWR, 0o600)
    try:
        os.fchmod(descriptor, 0o600)
        fcntl.flock(descriptor, fcntl.LOCK_EX)
        yield
    finally:
        os.close(descriptor)


def write_state(path, data):
    state_directory(path)
    descriptor, scratch = tempfile.mkstemp(prefix=path.name + ".", suffix=".tmp", dir=path.parent)
    temporary = Path(scratch)
    try:
        with os.fdopen(descriptor, "wb") as handle:
            handle.write(json.dumps(data, sort_keys=True).encode("utf-8"))
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        temporary.unlink(missing_ok=True)


def read_state(path):
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError):
        raise Refusal("launch-unconfirmed", "Owner state is unreadable. Do not relaunch or adopt a pane.", 3)
    if not isinstance(data, dict) or data.get("version") != 1 or data.get("name") != path.name:
        raise Refusal("launch-unconfirmed", "Owner state does not match this task name. Do not adopt a pane.", 3)
    return data


def reserve(path, data):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(path.parent, 0o700)
    try:
        descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    except FileExistsError:
        raise Refusal("name-consumed", "This task name is already reserved. Inspect that launch; do not start another pane.")
    os.close(descriptor)
    write_state(path, data)


def allowed_commands(client):
    return {Path(client).name, "node", "cursor-agent", "agent"}


def validate_name(name):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", name):
        raise Refusal("invalid-input", "Name must be 1..64 letters, digits, underscores, or hyphens.")


def validate_model(model):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", model):
        raise Refusal("invalid-input", "Supply an explicit full model id, without CLI options.")


def validate_worktree(value):
    if not Path(value).is_absolute() or "\0" in value or "\n" in value or "\r" in value or "\t" in value:
        raise Refusal("invalid-input", "Worktree must be an absolute path without whitespace control characters.")
    worktree = Path(value).resolve(strict=True)
    if not worktree.is_dir():
        raise Refusal("invalid-input", "Worktree must be an existing directory.")
    return str(worktree)


def client_argv(client, worktree, model):
    argv = [client, "--workspace", worktree, "--model", model]
    for item in argv[1:]:
        if item.startswith("-") and item not in ALLOWED_FLAGS:
            raise Refusal("invalid-input", "Refusing a Cursor flag other than --workspace and --model.")
    denied = {"--force", "--yolo", "--auto-review", "--approve-mcps", "--trust", "--sandbox",
              "--print", "--api-key", "--header", "--endpoint", "--resume", "--continue", "persist"}
    if any(item in denied or item in {"-f", "-p"} for item in argv):
        raise Refusal("invalid-input", "Refusing force, yolo, auto-review, trust, approval, or print flags.")
    return argv


def prepare_launch(args):
    validate_name(args.name)
    validate_model(args.model)
    worktree = validate_worktree(args.worktree)
    client, tmux = executable(args.client), executable(args.tmux)
    argv = client_argv(client, worktree, args.model)
    # tmux parses one shell command. Quote every argument. Never interpolate.
    command = "exec " + shlex.join(argv)
    message = ["new-session", "-d", "-P", "-F", "#{pane_id}", "-s", args.name, "-c", worktree, command]
    message_bytes = 128 + sum(len(value.encode("utf-8")) + 1 for value in message)
    if message_bytes > MAX_COMMAND_BYTES:
        raise Refusal("invalid-input", "Quoted launch command exceeds the 8192-byte budget. No name reserved.")
    return {
        "name": args.name, "worktree": worktree, "model": args.model, "client": client,
        "tmux": tmux, "command": command, "owner": owner_marker(args.name, worktree),
        "argv": argv,
    }


def parse_identity(stdout, fail_state, fail_code):
    if stdout.endswith("\n"):
        stdout = stdout[:-1]
    fields = stdout.split("\t")
    if len(fields) != 10 or any(field == "" or "\n" in field for field in fields):
        raise Refusal(fail_state, "tmux identity was incomplete. Nothing was sent.", fail_code)
    session, pane, pid, dead, path, command, owner, cursor_y, height, in_mode = fields
    if not re.fullmatch(r"%[0-9]+", pane) or not re.fullmatch(r"[1-9][0-9]*", pid):
        raise Refusal(fail_state, "tmux identity was not an exact pane and process. Nothing was sent.", fail_code)
    if dead not in {"0", "1"} or in_mode not in {"0", "1"}:
        raise Refusal(fail_state, "tmux dead or mode flag was not exact. Nothing was sent.", fail_code)
    if not re.fullmatch(r"[0-9]+", cursor_y) or not re.fullmatch(r"[1-9][0-9]*", height):
        raise Refusal(fail_state, "tmux cursor position was not exact. Nothing was sent.", fail_code)
    if not Path(path).is_absolute():
        raise Refusal(fail_state, "tmux worktree path was not absolute. Nothing was sent.", fail_code)
    return {
        "session": session, "pane": pane, "pid": pid, "dead": dead, "path": path,
        "command": command, "owner": owner, "cursor_y": cursor_y, "height": height,
        "in_mode": in_mode,
    }


def read_identity(tmux, pane, fail_state, fail_code):
    try:
        result = run([tmux, "display-message", "-p", "-t", pane, "-F", IDENTITY_FORMAT])
    except subprocess.TimeoutExpired:
        raise Refusal(fail_state, "tmux identity timed out. Re-inspect before any send. Nothing was sent.", fail_code)
    except (OSError, UnicodeError):
        raise Refusal(fail_state, "tmux identity could not be read. Nothing was sent.", fail_code)
    if result.returncode:
        raise Refusal("pane-missing" if fail_state != "launch-unconfirmed" else fail_state,
                      "Pane is missing. Nothing was sent and no replacement was launched.",
                      2 if fail_state != "launch-unconfirmed" else 3)
    return parse_identity(result.stdout, fail_state, fail_code)


def bound_text(text):
    normalized = text.replace("\r\n", "\n").replace("\r", "\n")
    lines = normalized.splitlines()
    truncated = len(lines) > MAX_LINES
    if truncated:
        lines = lines[-MAX_LINES:]
    body = "\n".join(lines)
    raw = body.encode("utf-8")
    if len(raw) > MAX_CAPTURE:
        truncated = True
        raw = raw[-MAX_CAPTURE:]
        while raw:
            try:
                body = raw.decode("utf-8")
                break
            except UnicodeDecodeError:
                raw = raw[1:]
        else:
            body = ""
    return body, truncated


def read_capture(tmux, pane):
    try:
        result = run([tmux, "capture-pane", "-p", "-t", pane, "-S", "-" + str(MAX_LINES)])
    except subprocess.TimeoutExpired:
        raise Refusal("inspect-unconfirmed", "Pane capture timed out. Re-inspect. Nothing was sent.", 3)
    except (OSError, UnicodeError):
        raise Refusal("inspect-unconfirmed", "Pane capture could not be read. Re-inspect. Nothing was sent.", 3)
    if result.returncode:
        raise Refusal("inspect-unconfirmed", "Pane capture failed. Re-inspect. Nothing was sent.", 3)
    return bound_text(result.stdout)


def modal_kind(text):
    found = []
    for line in text.splitlines():
        folded = line.casefold()
        for kind, phrases in MODALS:
            if any(phrase in folded for phrase in phrases):
                found.append(kind)
                break
    for kind in ("modal-trust", "modal-permission", "modal-approval", "modal-unknown"):
        if kind in found:
            return kind
    return None


def classify(text, cursor_y, pane_height):
    """Classify one bounded capture. Transcript lines are never user drafts."""
    kind = modal_kind(text)
    if kind:
        return kind
    lines = text.splitlines()
    try:
        cursor = int(cursor_y)
        height = int(pane_height)
    except (TypeError, ValueError):
        return "unclassified"
    if not lines or height < 1 or cursor != height - 1:
        return "unclassified"
    match = COMPOSER.match(lines[-1])
    if not match:
        return "unclassified"
    if match.group(1).strip():
        return "user-draft"
    return "active"


def snapshot_hash(ident, text, truncated):
    payload = "\n".join([
        ident["session"], ident["pane"], ident["pid"], ident["dead"], ident["path"],
        ident["command"], ident["owner"], ident["cursor_y"], ident["height"], ident["in_mode"],
        "1" if truncated else "0", text,
    ])
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


def make_receipt(receipt_id, digest, pane, pid):
    return "v1:" + receipt_id + ":" + digest + ":" + pane + ":" + pid


def parse_receipt(value):
    match = RECEIPT_RE.fullmatch(value or "")
    if not match:
        raise Refusal("invalid-input", "Receipt must be the exact token from the latest inspect.")
    return {"id": match.group(1), "digest": match.group(2), "pane": match.group(3), "pid": match.group(4)}


def load_owned(name, tmux):
    validate_name(name)
    path = state_path(name)
    if not path.is_file():
        raise Refusal("not-owned", "No owner marker exists for this task name. Refusing to select a pane.")
    state = read_state(path)
    if state.get("phase") != "running":
        raise Refusal("launch-unconfirmed", "This name has no confirmed pane. Do not adopt or relaunch automatically.", 3)
    resolved = executable(tmux)
    if resolved != state.get("tmux"):
        raise Refusal("not-owned", "tmux executable does not match the launch that owns this pane.")
    if state.get("owner") != owner_marker(state["name"], state["worktree"]):
        raise Refusal("not-owned", "Stored owner marker does not match this task and worktree.")
    for key in ("pane_id", "pane_pid", "worktree", "command", "client"):
        if not isinstance(state.get(key), str) or not state[key]:
            raise Refusal("launch-unconfirmed", "Owner marker is missing pane identity. Do not adopt a pane.", 3)
    return path, state


def require_same_pane(state, ident):
    if ident["dead"] != "0":
        raise Refusal("pane-dead", "Pane is dead. Nothing was sent and no new session was launched.")
    if ident["in_mode"] != "0":
        raise Refusal("pane-in-mode", "Pane is in a tmux mode. Nothing was sent.")
    if ident["owner"] != state["owner"]:
        raise Refusal("not-owned", "Pane owner marker does not match this task. Refusing to reuse it.")
    same = (
        ident["session"] == state["name"] and ident["pane"] == state["pane_id"]
        and ident["pid"] == state["pane_pid"] and ident["path"] == state["worktree"]
        and ident["command"] == state["command"]
    )
    if not same:
        raise Refusal("pane-replaced", "Pane, process, worktree, session, or command changed. Nothing was sent.")


def probe(client, tmux):
    client_help = run([client, "--help"])
    if client_help.returncode:
        raise Refusal("prerequisite-unavailable", "cursor-agent --help failed. No session was reserved.")
    if "--workspace" not in client_help.stdout or "--model" not in client_help.stdout:
        raise Refusal("unsupported-client", "Installed cursor-agent lacks --workspace and --model. No session was reserved.")
    tmux_version = run([tmux, "-V"])
    if tmux_version.returncode:
        raise Refusal("prerequisite-unavailable", "tmux -V failed. No session was reserved.")


def observe_started(prepared, pane):
    ident = read_identity(prepared["tmux"], pane, "launch-unconfirmed", 3)
    if ident["pane"] != pane or ident["session"] != prepared["name"] or ident["owner"] != prepared["owner"]:
        raise Refusal("launch-unconfirmed", "New pane identity or owner marker did not match. No retry.", 3)
    if ident["path"] != prepared["worktree"]:
        raise Refusal("launch-unconfirmed", "New pane worktree did not match the requested directory. No retry.", 3)
    if ident["dead"] != "0" or ident["in_mode"] != "0":
        raise Refusal("client-not-running", "New pane is dead or in a tmux mode. No retry.", 3)
    if ident["command"] not in allowed_commands(prepared["client"]):
        raise Refusal("client-not-running", "New pane command is not the installed Cursor client. No retry.", 3)
    return ident


def start_session(args):
    if not 0 <= args.observe_seconds <= 5:
        raise Refusal("invalid-input", "Observation delay must be 0..5 seconds.")
    prepared = prepare_launch(args)
    path = state_path(prepared["name"])
    if path.exists():
        raise Refusal("name-consumed", "This task name is already reserved. Inspect that launch; do not start another pane.")
    if args.dry_run:
        return {"state": "dry-run", "name": prepared["name"], "owner": prepared["owner"],
                "detail": "Inputs checked. No tmux session, Cursor process, or conversation was created."}
    probe(prepared["client"], prepared["tmux"])
    record = {
        "version": 1, "phase": "reserved", "name": prepared["name"], "worktree": prepared["worktree"],
        "model": prepared["model"], "owner": prepared["owner"], "client": prepared["client"],
        "tmux": prepared["tmux"], "pane_id": "", "pane_pid": "", "command": "",
        "receipt_id": "", "snapshot_sha256": "",
    }
    reserve(path, record)
    try:
        launched = run([prepared["tmux"], "new-session", "-d", "-P", "-F", "#{pane_id}",
                        "-s", prepared["name"], "-c", prepared["worktree"], prepared["command"]])
    except (subprocess.TimeoutExpired, OSError, UnicodeError):
        record["phase"] = "unconfirmed"
        write_state(path, record)
        raise Refusal("launch-unconfirmed", "tmux launch outcome is unknown. Inspect this exact name. No automatic retry.", 3)
    if launched.returncode:
        record["phase"] = "unconfirmed"
        write_state(path, record)
        raise Refusal("launch-unconfirmed", "tmux refused or failed, which may be a name collision. Inspect this exact name. No automatic retry.", 3)
    pane = launched.stdout.strip()
    if not re.fullmatch(r"%[0-9]+", pane):
        record["phase"] = "unconfirmed"
        write_state(path, record)
        raise Refusal("launch-unconfirmed", "tmux returned no valid new pane id. Inspect this exact name. No automatic retry.", 3)
    try:
        option = run([prepared["tmux"], "set-option", "-t", prepared["name"], OWNER_OPTION, prepared["owner"]])
    except (subprocess.TimeoutExpired, OSError, UnicodeError):
        record["phase"] = "unconfirmed"
        write_state(path, record)
        raise Refusal("launch-unconfirmed", "Owner marker was not confirmed on the new session. No automatic retry.", 3)
    if option.returncode:
        record["phase"] = "unconfirmed"
        write_state(path, record)
        raise Refusal("launch-unconfirmed", "tmux did not set the owner marker. No automatic retry.", 3)
    if args.observe_seconds:
        import time
        time.sleep(args.observe_seconds)
    try:
        ident = observe_started(prepared, pane)
    except Refusal:
        record["phase"] = "unconfirmed"
        record["pane_id"] = pane
        write_state(path, record)
        raise
    record.update({
        "phase": "running", "pane_id": ident["pane"], "pane_pid": ident["pid"], "command": ident["command"],
    })
    write_state(path, record)
    return {
        "state": "client-running-unverified", "name": prepared["name"], "pane_id": ident["pane"],
        "pane_pid": ident["pid"], "owner": prepared["owner"], "model_requested": prepared["model"],
        "detail": (
            "Dedicated tmux pane is alive with this owner marker. UI, provider, and served model are unverified. "
            "The requested model is only the --model argument. Inspect before any reply. "
            "Nothing was typed and Enter was not sent."
        ),
    }


def inspect_session(args):
    with state_lock(args.name):
        return inspect_locked(args)


def inspect_locked(args):
    path, state = load_owned(args.name, args.tmux)
    ident = read_identity(state["tmux"], state["pane_id"], "inspect-unconfirmed", 3)
    require_same_pane(state, ident)
    text, truncated = read_capture(state["tmux"], state["pane_id"])
    kind = classify(text, ident["cursor_y"], ident["height"])
    digest = snapshot_hash(ident, text, truncated)
    receipt_id = secrets.token_hex(16)
    state["receipt_id"] = receipt_id
    state["snapshot_sha256"] = digest
    write_state(path, state)
    return {
        "state": "inspected", "name": state["name"], "pane_id": ident["pane"], "pane_pid": ident["pid"],
        "owner": state["owner"], "worktree": state["worktree"], "model_requested": state["model"],
        "classification": kind, "classification_detail": CLASSIFICATION_DETAILS[kind],
        "receipt": make_receipt(receipt_id, digest, ident["pane"], ident["pid"]),
        "truncated": truncated, "text": text,
        "detail": "Receipt matches this pane, process, worktree, and bounded capture only. It is stale after the next inspect or any pane change.",
    }


def read_instruction(path):
    if path == "-":
        raw = sys.stdin.buffer.read(MAX_TEXT + 1)
    else:
        with open(path, "rb") as handle:
            raw = handle.read(MAX_TEXT + 1)
    if not raw.strip() or len(raw) > MAX_TEXT:
        raise Refusal("invalid-input", "Instruction must contain 1..4096 UTF-8 bytes.")
    try:
        instruction = raw.decode("utf-8")
    except UnicodeError:
        raise Refusal("invalid-input", "Instruction must be UTF-8.")
    if any(ord(character) < 32 or 127 <= ord(character) <= 159 for character in instruction):
        raise Refusal("invalid-input", "Instruction must be a single line without C0, DEL, or C1 control characters. Nothing was sent.")
    return instruction


def clear_receipt(path, state):
    state["receipt_id"] = ""
    state["snapshot_sha256"] = ""
    write_state(path, state)


def send_instruction(tmux, pane, text, submit):
    literal = [tmux, "send-keys", "-t", pane, "-l", "--", text]
    try:
        sent = run(literal)
    except (subprocess.TimeoutExpired, OSError, UnicodeError):
        raise Refusal("send-unconfirmed", "Literal send outcome is unknown. Do not retry this receipt. Re-inspect. Enter was not sent by this helper.", 3)
    if sent.returncode:
        raise Refusal("send-unconfirmed", "tmux did not accept the literal text. Do not retry this receipt. Re-inspect.", 3)
    if not submit:
        return False
    try:
        entered = run([tmux, "send-keys", "-t", pane, "Enter"])
    except (subprocess.TimeoutExpired, OSError, UnicodeError):
        raise Refusal("send-unconfirmed", "Enter outcome is unknown after literal text. Do not send another Enter. Re-inspect.", 3)
    if entered.returncode:
        raise Refusal("send-unconfirmed", "tmux did not accept Enter after literal text. Do not send another Enter. Re-inspect.", 3)
    return True


def reply_session(args):
    # stdin and FIFOs may block. Finish all input reads before final verification.
    instruction = read_instruction(args.text_file)
    with state_lock(args.name):
        return reply_locked(args, instruction)


def reply_locked(args, instruction):
    path, state = load_owned(args.name, args.tmux)
    receipt = parse_receipt(args.receipt)
    if not state.get("receipt_id") or receipt["id"] != state["receipt_id"]:
        raise Refusal("stale-receipt", "Pane receipt is stale or is not the latest inspect. Re-inspect. Nothing was sent.")
    if receipt["pane"] != state["pane_id"] or receipt["pid"] != state["pane_pid"] or receipt["digest"] != state["snapshot_sha256"]:
        raise Refusal("stale-receipt", "Pane receipt does not match the stored inspect. Re-inspect. Nothing was sent.")
    ident = read_identity(state["tmux"], state["pane_id"], "inspect-unconfirmed", 3)
    require_same_pane(state, ident)
    text, truncated = read_capture(state["tmux"], state["pane_id"])
    digest = snapshot_hash(ident, text, truncated)
    if digest != state["snapshot_sha256"] or digest != receipt["digest"]:
        raise Refusal("stale-receipt", "Pane capture changed since inspect. Re-inspect. Nothing was sent.")
    kind = classify(text, ident["cursor_y"], ident["height"])
    if kind.startswith("modal-"):
        label = kind.split("-", 1)[1]
        raise Refusal(kind, "Refusing to type or send Enter into a " + label + " modal. --observed-ready does not override it. Re-inspect after the pane is clear.")
    if kind == "user-draft":
        raise Refusal("user-draft", "The cursor row is a nonempty composer. Nothing was sent, and Enter was not sent. Echoed lines above the cursor row are not a draft to submit.")
    if kind == "unclassified" and not args.observed_ready:
        raise Refusal("unclassified-ui", CLASSIFICATION_DETAILS["unclassified"] + " Nothing was sent.")
    if kind not in {"active", "unclassified"}:
        raise Refusal("unclassified-ui", "Cursor UI cannot be classified safely. Nothing was sent.")
    clear_receipt(path, state)
    submitted = send_instruction(state["tmux"], state["pane_id"], instruction, args.submit)
    return {
        "state": "sent", "name": state["name"], "classification": kind, "submitted": submitted,
        "bytes": len(instruction.encode("utf-8")),
        "detail": (
            "Literal text was sent to the owned pane whose receipt matched. "
            + ("One Enter followed it." if submitted else "Enter was not sent.")
            + " Requested model " + state["model"] + " is not evidence of the served model. This receipt is now spent."
        ),
    }


def build_parser():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    start = sub.add_parser("start", help="launch one dedicated interactive Cursor pane")
    start.add_argument("--client", required=True)
    start.add_argument("--worktree", required=True)
    start.add_argument("--model", required=True)
    start.add_argument("--name", required=True)
    start.add_argument("--tmux", default="tmux")
    start.add_argument("--observe-seconds", type=float, default=1)
    start.add_argument("--dry-run", action="store_true")

    inspect = sub.add_parser("inspect", help="capture bounded text and a fresh pane receipt")
    inspect.add_argument("--name", required=True)
    inspect.add_argument("--tmux", required=True)

    reply = sub.add_parser("reply", help="send literal text only to the inspected pane")
    reply.add_argument("--name", required=True)
    reply.add_argument("--tmux", required=True)
    reply.add_argument("--receipt", required=True)
    reply.add_argument("--text-file", required=True)
    reply.add_argument("--submit", action="store_true", help="send one Enter after literal text")
    reply.add_argument("--observed-ready", action="store_true",
                       help="manual confirmation of this exact unclassified receipt; not a Cursor trust flag")
    return parser


def main(argv=None):
    parser = build_parser()
    args = parser.parse_args(argv)
    code = 0
    try:
        if args.command == "start":
            payload = start_session(args)
        elif args.command == "inspect":
            payload = inspect_session(args)
        else:
            payload = reply_session(args)
    except Refusal as error:
        payload = {"state": error.state, "name": getattr(args, "name", ""), "detail": error.detail}
        code = error.code
    except subprocess.TimeoutExpired:
        if args.command == "start":
            payload = {"state": "prerequisite-unavailable", "name": args.name,
                       "detail": "Client or tmux probe timed out before reservation. No launch was attempted."}
            code = 2
        else:
            payload = {"state": "inspect-unconfirmed", "name": args.name,
                       "detail": "Timed out while reading the pane. Nothing was sent."}
            code = 3
    except (OSError, UnicodeError, ValueError):
        payload = {"state": "prerequisite-unavailable", "name": getattr(args, "name", ""),
                   "detail": "Cannot read local input or execute the helper. No automatic retry."}
        code = 2
    print(json.dumps(payload, sort_keys=True))
    return code


if __name__ == "__main__":
    sys.exit(main())
