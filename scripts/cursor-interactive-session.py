#!/usr/bin/env python3
"""Start, inspect, reply to, and answer one prompt in a dedicated interactive Cursor session.

Uses tmux and the installed cursor-agent executable. It does not call persist,
does not pass trust or approval flags, and does not claim the served model.
`answer` sends one approved prompt choice; it does not verify the approval.
"""

import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import shutil
import stat
import subprocess
import sys
import tempfile

MAX_TEXT = 4096
MAX_CAPTURE = 8192
MAX_LINES = 80
MAX_COMMAND_BYTES = 8192
MAX_EXPECT = 4096
MAX_EXPECT_LINES = 20
MAX_ANSWERS = 32
ANSWER_KINDS = ("modal-trust", "modal-permission", "modal-approval")
APPROVAL_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:/@#-]{7,199}")
MAX_TRUST_PATH = 1024
TOP_CORNERS = "\u256d\u250c\u2554\u250f"
TOP_RIGHT_CORNERS = "\u256e\u2510\u2557\u2513"
BOTTOM_CORNERS = "\u2570\u2514\u255a\u2517"
BOTTOM_RIGHT_CORNERS = "\u256f\u2518\u255d\u251b"
CORNERS = TOP_CORNERS + TOP_RIGHT_CORNERS + BOTTOM_CORNERS + BOTTOM_RIGHT_CORNERS
SIDE_BARS = "\u2502\u2551\u2503"
TRUST_PATH_RE = re.compile(r"(?:/[A-Za-z0-9_.@+-]+)+")
COMMAND_TIMEOUT = 5
OWNER_OPTION = "@cursor_interactive_owner"
IDENTITY_FORMAT = (
    "#{session_name}\t#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{pane_current_path}\t"
    "#{pane_current_command}\t#{@cursor_interactive_owner}\t#{cursor_y}\t#{pane_height}\t#{pane_in_mode}\t"
    "#{history_size}\t#{pane_width}"
)
ALLOWED_FLAGS = ("--workspace", "--model")
# Cursor's command-approval prompt (the live G1 capture, 2026-11-05T19:30:52Z) is not a box: it
# sits directly under a divider row of this character across the pane, and writes its choices
# as (key). The helper answers it only with y, Run (once). Cursor's status line for the pending
# command, "Waiting for approval...", may show above the divider.
DIVIDER = "\u2500"
COMMAND_CHOICE = "y"
PENDING_STATUS = "waiting for approval"
# Cursor's command prompt with more than one command waiting: a counter row under the divider
# and a switch hint after the Skip row. Run (once), y, answers only the approval shown.
QUEUE_HEADER = re.compile(r"Approval ([1-9][0-9]?) of ([1-9][0-9]?)")
QUEUE_HINT = "\u2190/\u2192 to switch approval"
# Rejecting the shown command: one Ctrl+C, which Cursor (raw mode) reads as reject for the
# active decision only. It is never sent outside the command prompt.
REJECT_CHOICE = "ctrl-c"
# Cursor draws a turn error under the prompt as "Error: <title>" (Agent stopped retrying,
# Connection failed, ...). With any row after the Skip row or the switch hint, only a
# rejection may be answered, and those rows must start with such an error line; approving a
# command its turn may no longer await is refused.
ERROR_PREFIX = "Error: "
SKIP_SUFFIX = "(esc or n)"
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
        "Echoed lines above that row are not a user draft. "
        "Prompt text in the history rows above the visible screen is not counted."
    ),
    "user-draft": (
        "Cursor row, or the row above a blank cursor row, has a nonempty composer. "
        "Echoed lines above that row are not a user draft."
    ),
    "unclassified": (
        "Cursor UI is unclassified. Echoed terminal text is not an unsent user draft. "
        "The helper only treats the cursor row as a composer when that row is a bare >, ›, or ❯ prompt. "
        "This screen does not match that shape or a known modal phrase. "
        "Absence of those phrases is not proof the screen is safe. "
        "Sending requires a fresh receipt and --observed-ready after manual confirmation of this exact receipt. "
        "--observed-ready is not a Cursor trust, approval, or permission grant."
    ),
    "modal-trust": "Known workspace-trust phrasing is on the visible screen.",
    "modal-permission": "Known permission phrasing is on the visible screen.",
    "modal-approval": "Known approval phrasing is on the visible screen.",
    "modal-unknown": "An unrecognized confirmation prompt is on the visible screen.",
}
# When the visible screen cannot be located, a phrase may be anywhere in the capture.
UNLOCATED_DETAILS = {
    "modal-trust": "Known workspace-trust phrasing is in the captured window.",
    "modal-permission": "Known permission phrasing is in the captured window.",
    "modal-approval": "Known approval phrasing is in the captured window.",
    "modal-unknown": "An unrecognized confirmation prompt is in the captured window.",
}
UNLOCATED_DETAIL = " The visible screen cannot be located, so the whole captured window was read."
ANSWERED_DETAIL = (
    " A trust prompt box on the visible screen was not counted: this pane's ledger holds a sent answer to it "
    "under approval reference {}, and the box shows that prompt with Cursor's status row. The ledger records "
    "that the key was sent, not that Cursor accepted it."
)
# How an answered trust box differs from its prompt (the live G1 capture, 2026-11-05): the
# selection marker is gone from every row, and the status row stands where the prompt's last
# row, this navigation hint, was. Nothing else may differ.
ANSWERED_STATUS = "\u23f3 Trusting workspace..."
ANSWERED_HINT = "Use arrow keys to navigate, Enter to select, or press the key shown"
# Cursor's own composer, empty: its arrow and placeholder, nothing typed (the live G1 capture).
CURSOR_COMPOSER_ARROW = "\u2192"
CURSOR_COMPOSER_EMPTY = "\u2192 Plan, search, build anything"
SELECTION_MARKER = "\u25b6 "
LEDGER_FORMAT = 2
LEDGER_FORMAT_KEYS = ("format", "worktree", "box_rows_sha256", "box_marker_rows")
MAX_LEDGER = 4096
SHA256_RE = re.compile(r"[0-9a-f]{64}")


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
def state_lock(name, wait=True):
    validate_name(name)
    path = state_path(name)
    state_directory(path)
    # Keep this inode: unlinking it would let another process lock a new inode.
    descriptor = os.open(path.with_name(path.name + ".lock"), os.O_CREAT | os.O_RDWR, 0o600)
    try:
        os.fchmod(descriptor, 0o600)
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX if wait else fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Refusal("lock-busy", "Another helper call holds this task's lock. Nothing was sent. Inspect again later.")
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
    if len(fields) != 12 or any(field == "" or "\n" in field for field in fields):
        raise Refusal(fail_state, "tmux identity was incomplete. Nothing was sent.", fail_code)
    session, pane, pid, dead, path, command, owner, cursor_y, height, in_mode, history, width = fields
    if not re.fullmatch(r"%[0-9]+", pane) or not re.fullmatch(r"[1-9][0-9]*", pid):
        raise Refusal(fail_state, "tmux identity was not an exact pane and process. Nothing was sent.", fail_code)
    if dead not in {"0", "1"} or in_mode not in {"0", "1"}:
        raise Refusal(fail_state, "tmux dead or mode flag was not exact. Nothing was sent.", fail_code)
    if not re.fullmatch(r"[0-9]+", cursor_y) or not re.fullmatch(r"[1-9][0-9]*", height):
        raise Refusal(fail_state, "tmux cursor position was not exact. Nothing was sent.", fail_code)
    if not re.fullmatch(r"[0-9]+", history):
        raise Refusal(fail_state, "tmux history size was not exact. Nothing was sent.", fail_code)
    if not re.fullmatch(r"[1-9][0-9]*", width):
        raise Refusal(fail_state, "tmux pane width was not exact. Nothing was sent.", fail_code)
    if not Path(path).is_absolute():
        raise Refusal(fail_state, "tmux worktree path was not absolute. Nothing was sent.", fail_code)
    return {
        "session": session, "pane": pane, "pid": pid, "dead": dead, "path": path,
        "command": command, "owner": owner, "cursor_y": cursor_y, "height": height,
        "in_mode": in_mode, "history": history, "width": width,
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


def capture_rows(text):
    """Rows tmux printed, counted the way bound_text splits them."""
    return len(text.replace("\r\n", "\n").replace("\r", "\n").splitlines())


def read_capture_rows(tmux, pane):
    try:
        result = run([tmux, "capture-pane", "-p", "-t", pane, "-S", "-" + str(MAX_LINES)])
    except subprocess.TimeoutExpired:
        raise Refusal("inspect-unconfirmed", "Pane capture timed out. Re-inspect. Nothing was sent.", 3)
    except (OSError, UnicodeError):
        raise Refusal("inspect-unconfirmed", "Pane capture could not be read. Re-inspect. Nothing was sent.", 3)
    if result.returncode:
        raise Refusal("inspect-unconfirmed", "Pane capture failed. Re-inspect. Nothing was sent.", 3)
    text, truncated = bound_text(result.stdout)
    return text, truncated, capture_rows(result.stdout)


def modal_kind(rows):
    found = []
    for line in rows:
        folded = line.casefold()
        for kind, phrases in MODALS:
            if any(phrase in folded for phrase in phrases):
                found.append(kind)
                break
    for kind in ("modal-trust", "modal-permission", "modal-approval", "modal-unknown"):
        if kind in found:
            return kind
    return None


def visible_screen(text, truncated, rows_total, ident):
    """The visible screen: the last pane_height rows of one bounded capture.

    capture-pane -S -80 prints min(history_size, 80) history rows, then every
    visible row, blank ones included. The row count is checked against tmux's
    own history_size and pane_height, so the last pane_height rows are the
    visible screen, and the bounded text must still hold all of it. Otherwise
    the screen cannot be located: prompt-unclear."""
    rows = text.split("\n")
    rows_needed = int(ident["height"])
    if rows_total != min(int(ident["history"]), MAX_LINES) + rows_needed:
        raise Refusal("prompt-unclear", "The capture's row count does not match tmux's history size and pane height, "
                                        "so the visible screen cannot be located. Nothing was sent.")
    if len(rows) < rows_needed or (truncated and len(rows) == rows_needed):
        raise Refusal("prompt-unclear", "The visible screen cannot be established from the bounded capture. Nothing was sent.")
    return rows[-rows_needed:]


def classify(text, truncated, rows_total, ident, answered=None):
    """(kind, located, answered reference) for one bounded capture. Transcript lines are never user drafts.

    Modal phrases count only on the visible screen: prompt text in the history
    rows above it is not counted. When the screen cannot be located, the whole
    capture is scanned, and the result is never active. A trust box counts as
    answered only with the ledger evidence in `answered` (answered_trust) and
    only when answered_box binds it; the rest of the screen is still read."""
    rows = text.split("\n")
    try:
        scanned, located = visible_screen(text, truncated, rows_total, ident), True
    except Refusal:
        scanned, located = rows, False
    kind, used = modal_kind(scanned), None
    if kind == "modal-trust" and located and answered:
        # The box may start in the history rows: the receipt binds the whole bounded capture.
        first = len(rows) - len(scanned)
        box = answered_box(rows, first, answered, truncated)
        cursor = int(ident["cursor_y"])
        if (box and cursor == int(ident["height"]) - 1 and first + cursor > box[1]
                and cursor_composer_empty(rows[box[1] + 1:first + cursor + 1])):
            kind = modal_kind(rows[first:max(first, box[0])] + rows[box[1] + 1:])
            used = None if kind else answered["approval_ref"]
    kind = kind or composer_kind(rows, ident["cursor_y"], ident["height"])
    if not located and kind == "active":
        kind = "unclassified"
    return kind, located, used


def composer_kind(rows, cursor_y, pane_height):
    """The composer at the cursor. The capture's last row is the last pane row."""
    try:
        cursor = int(cursor_y)
        height = int(pane_height)
    except (TypeError, ValueError):
        return "unclassified"
    if not rows or height < 1 or cursor != height - 1:
        return "unclassified"
    match = COMPOSER.match(rows[-1])
    if not match and rows[-1] == "" and len(rows) > 1:
        # tmux prints a blank row under the composer. A draft just above that
        # blank cursor row is still a draft (older parsing dropped the row).
        above = COMPOSER.match(rows[-2])
        if above and above.group(1).strip():
            return "user-draft"
    if not match:
        return "unclassified"
    if match.group(1).strip():
        return "user-draft"
    return "active"


def snapshot_hash(ident, text, truncated):
    payload = "\n".join([
        ident["session"], ident["pane"], ident["pid"], ident["dead"], ident["path"],
        ident["command"], ident["owner"], ident["cursor_y"], ident["height"], ident["in_mode"],
        ident["history"], ident["width"], "1" if truncated else "0", text,
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
    # A FIFO may block. Read the answered text before the lock.
    answered_text = read_answered_text(getattr(args, "answered_text_file", None))
    with state_lock(args.name):
        return inspect_locked(args, answered_text)


def inspect_locked(args, answered_text=None):
    path, state = load_owned(args.name, args.tmux)
    ident = read_identity(state["tmux"], state["pane_id"], "inspect-unconfirmed", 3)
    require_same_pane(state, ident)
    answered = answered_evidence(state, answered_text)
    text, truncated, rows_total = read_capture_rows(state["tmux"], state["pane_id"])
    kind, located, used = classify(text, truncated, rows_total, ident, answered)
    digest = snapshot_hash(ident, text, truncated)
    receipt_id = secrets.token_hex(16)
    state["receipt_id"] = receipt_id
    state["snapshot_sha256"] = digest
    # The answered box this classification relied on, if any: reply must rely on the same.
    state["receipt_answered"] = used or "none"
    write_state(path, state)
    return {
        "state": "inspected", "name": state["name"], "pane_id": ident["pane"], "pane_pid": ident["pid"],
        "owner": state["owner"], "worktree": state["worktree"], "model_requested": state["model"],
        "classification": kind,
        "classification_detail": (CLASSIFICATION_DETAILS[kind] + (ANSWERED_DETAIL.format(used) if used else "")
                                  if located
                                  else UNLOCATED_DETAILS.get(kind, CLASSIFICATION_DETAILS[kind]) + UNLOCATED_DETAIL),
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
    state["receipt_answered"] = ""
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
    answered_text = read_answered_text(getattr(args, "answered_text_file", None))
    with state_lock(args.name):
        return reply_locked(args, instruction, answered_text)


def reply_locked(args, instruction, answered_text=None):
    path, state = load_owned(args.name, args.tmux)
    receipt = parse_receipt(args.receipt)
    if not state.get("receipt_id") or receipt["id"] != state["receipt_id"]:
        raise Refusal("stale-receipt", "Pane receipt is stale or is not the latest inspect. Re-inspect. Nothing was sent.")
    if receipt["pane"] != state["pane_id"] or receipt["pid"] != state["pane_pid"] or receipt["digest"] != state["snapshot_sha256"]:
        raise Refusal("stale-receipt", "Pane receipt does not match the stored inspect. Re-inspect. Nothing was sent.")
    ident = read_identity(state["tmux"], state["pane_id"], "inspect-unconfirmed", 3)
    require_same_pane(state, ident)
    text, truncated, rows_total = read_capture_rows(state["tmux"], state["pane_id"])
    digest = snapshot_hash(ident, text, truncated)
    if digest != state["snapshot_sha256"] or digest != receipt["digest"]:
        raise Refusal("stale-receipt", "Pane capture changed since inspect. Re-inspect. Nothing was sent.")
    kind, _located, used = classify(text, truncated, rows_total, ident, answered_evidence(state, answered_text))
    if (used or "none") != state.get("receipt_answered", "none"):
        raise Refusal("stale-receipt", "This reply's answered-box evidence differs from the inspect that gave the receipt, "
                                       "so the classification would differ. Inspect again with the same "
                                       "--answered-text-file, or none. Nothing was sent.")
    if kind.startswith("modal-"):
        label = kind.split("-", 1)[1]
        raise Refusal(kind, "Refusing to type or send Enter into a " + label + " modal. --observed-ready does not override it. Re-inspect after the pane is clear.")
    if kind == "user-draft":
        raise Refusal("user-draft", "The cursor row, or the row above a blank cursor row, is a nonempty composer. Nothing was sent, and Enter was not sent. Echoed lines above the cursor row are not a draft to submit.")
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


def read_expected(path):
    try:
        with open(path, "rb") as handle:
            raw = handle.read(MAX_EXPECT + 1)
    except OSError:
        raise Refusal("invalid-input", "Expected prompt text file cannot be read. Nothing was sent.")
    if len(raw) > MAX_EXPECT:
        raise Refusal("invalid-input", "Expected prompt text must be at most 4096 UTF-8 bytes. Nothing was sent.")
    try:
        text = raw.decode("utf-8")
    except UnicodeError:
        raise Refusal("invalid-input", "Expected prompt text must be UTF-8. Nothing was sent.")
    if any((ord(character) < 32 and character != "\n") or 127 <= ord(character) <= 159 for character in text):
        raise Refusal("invalid-input", "Expected prompt text must have no control characters except newlines. Nothing was sent.")
    lines = [prompt_text(line) for line in text.split("\n") if prompt_text(line)]
    if not lines or len(lines) > MAX_EXPECT_LINES:
        raise Refusal("invalid-input", "Expected prompt text must have 1..20 nonblank lines. Nothing was sent.")
    return raw, lines


def prompt_shape(expect_kind, choice, lines):
    """"box" when the expected text shows the choice as [key]; "divider" for Cursor's command
    prompt, a modal-approval whose expected text shows (y) at the end of exactly one row and
    the choice is y; otherwise None."""
    if choice != REJECT_CHOICE and any("[" + choice + "]" in line for line in lines):
        return "box"
    if expect_kind == "modal-approval" and choice in (COMMAND_CHOICE, REJECT_CHOICE) and sum(
            line.endswith(" (" + COMMAND_CHOICE + ")") for line in prompt_rows(lines)) == 1:
        return "divider"
    return None


def prompt_end(lines):
    """The index after the command prompt's last row: the switch hint, else the Skip row, else
    the end of the lines."""
    ends = [index for index, line in enumerate(lines) if line == QUEUE_HINT or line.endswith(SKIP_SUFFIX)]
    return ends[-1] + 1 if ends else len(lines)


def prompt_rows(lines):
    """The expected lines without the rows after the prompt (a turn error footer)."""
    return lines[:prompt_end(lines)]


def check_queue_text(lines):
    """A queued command prompt's expected text starts with its one "Approval N of M" row and
    ends with its one switch hint, with 1 <= N <= M and M >= 2; a single prompt has neither."""
    headers = [index for index, line in enumerate(lines) if QUEUE_HEADER.fullmatch(line)]
    hints = [index for index, line in enumerate(lines) if line == QUEUE_HINT]
    if not headers and not hints:
        return
    if headers != [0] or hints != [len(lines) - 1]:
        raise Refusal("invalid-input", "A queued command prompt's expected text must start with its one \"Approval N of "
                                       "M\" row and end with its one switch hint. Nothing was sent.")
    shown, count = (int(number) for number in QUEUE_HEADER.fullmatch(lines[0]).groups())
    if not 1 <= shown <= count or count < 2:
        raise Refusal("invalid-input", "A queued command prompt shows approval N of M with 1 <= N <= M and M >= 2. "
                                       "Nothing was sent.")


def validate_answer(args):
    if not APPROVAL_RE.fullmatch(args.approval_ref or ""):
        raise Refusal("invalid-input", "Approval reference must be 8..200 characters of letters, digits and ._:/@#-. Nothing was sent.")
    if args.expect_kind not in ANSWER_KINDS:
        raise Refusal("invalid-input", "Expected kind must be modal-trust, modal-permission or modal-approval. Nothing was sent.")
    choice = args.choice or ""
    if choice != REJECT_CHOICE and (len(choice) != 1 or not 33 <= ord(choice) <= 126):
        raise Refusal("invalid-input", "Choice must be exactly one printable ASCII key, or ctrl-c to reject Cursor's "
                                       "shown command. Nothing was sent.")
    raw, lines = read_expected(args.expect_text_file)
    if prompt_shape(args.expect_kind, choice, lines) is None:
        raise Refusal("invalid-input", "Choice must appear as [" + choice + "] in the expected prompt text. For Cursor's "
                                       "command prompt (modal-approval) the only choice is y, shown once as (y): never the "
                                       "allowlist (tab), Run Everything (shift+tab) or Skip (esc or n). Nothing was sent.")
    if prompt_shape(args.expect_kind, choice, lines) == "divider" and any(
            line.strip(" ") != prompt_text(line) for line in raw.decode("utf-8").split("\n") if line.strip(" ")):
        raise Refusal("invalid-input", "A command prompt's expected rows must not start or end with a box-drawing or "
                                       "non-space whitespace character. Nothing was sent.")
    if prompt_shape(args.expect_kind, choice, lines) == "divider":
        # Rows after the Skip row or the hint can only be one turn error footer, and only a
        # rejection is answered under it. An error line inside the prompt is refused too.
        tail = lines[prompt_end(lines):]
        errors = sum(line.startswith(ERROR_PREFIX) for line in lines)
        if (any(line.startswith(ERROR_PREFIX) for line in prompt_rows(lines))
                or tail and (choice != REJECT_CHOICE or not tail[0].startswith(ERROR_PREFIX) or errors != 1)):
            raise Refusal("invalid-input", "Rows after the Skip row or the switch hint must be one turn error footer "
                                           "(\"Error: ...\"), and only a ctrl-c rejection may be answered under it. "
                                           "Nothing was sent.")
        check_queue_text(prompt_rows(lines))
    trust_path = getattr(args, "trust_path", None)
    if args.expect_kind == "modal-trust":
        if not trust_path:
            raise Refusal("invalid-input", "A trust answer needs --trust-path, the one directory the approval covers. Nothing was sent.")
        if (len(trust_path.encode("utf-8")) > MAX_TRUST_PATH or not TRUST_PATH_RE.fullmatch(trust_path)
                or any(part in {".", ".."} for part in trust_path.split("/"))):
            raise Refusal("invalid-input", "Trust path must be an absolute, normalized path of letters, digits and _.@+- "
                                           "between single slashes, at most 1024 bytes. Nothing was sent.")
        if not any(trust_path in line.split() for line in lines):
            raise Refusal("invalid-input", "The expected prompt text must contain the trust path as a whole word. Nothing was sent.")
    elif trust_path is not None:
        raise Refusal("invalid-input", "--trust-path applies only to a modal-trust answer. Nothing was sent.")
    parse_receipt(args.receipt)
    return choice, raw, lines, trust_path


def prompt_text(row):
    """A row without its border and padding: whitespace and box-drawing characters at its ends only."""
    start, end = 0, len(row)
    while start < end and is_separator(row[start]):
        start += 1
    while end > start and is_separator(row[end - 1]):
        end -= 1
    return row[start:end]


def border(row, text):
    """("top" or "bottom", left column, right column, titled) for a border row, "bad"
    for a row of box-drawing characters that has a corner but cannot be a border,
    and None for any other row.

    With spaces and side bars taken off both ends, a border row starts with a left
    corner and ends with the matching right corner, with no other corner between.
    Text between the corners is a title."""
    start, end = 0, len(row)
    while start < end and (row[start].isspace() or row[start] in SIDE_BARS):
        start += 1
    while end > start and (row[end - 1].isspace() or row[end - 1] in SIDE_BARS):
        end -= 1
    core = row[start:end]
    if len(core) >= 2 and not any(character in CORNERS for character in core[1:-1]):
        for kind, lefts, rights in (("top", TOP_CORNERS, TOP_RIGHT_CORNERS),
                                    ("bottom", BOTTOM_CORNERS, BOTTOM_RIGHT_CORNERS)):
            if core[0] in lefts and core[-1] in rights:
                return kind, start, end - 1, any(not is_separator(character) for character in core)
    if not text and any(character in CORNERS for character in row):
        return "bad"
    return None


def visible_boxes(rows, texts):
    """(top row, bottom row, left column, right column, titled) for every box on
    the visible screen, or None when its borders do not pair.

    Reading down, each bottom border closes the latest open top border, and the
    two must have their corners in the same columns."""
    stack, boxes = [], []
    for index, (row, text) in enumerate(zip(rows, texts)):
        found = border(row, text)
        if found is None:
            continue
        if found == "bad":
            return None
        kind, left, right, titled = found
        if kind == "top":
            stack.append((index, left, right, titled))
            continue
        if not stack or stack[-1][1:3] != (left, right):
            return None
        top, _left, _right, top_titled = stack.pop()
        boxes.append((top, index, left, right, top_titled or titled))
    return None if stack else boxes


def prompt_blocks(texts, lines):
    """(first, last) for each place the expected lines are consecutive trimmed rows.

    Exact and case-sensitive. Rows that trim to nothing (borders, padding,
    empty rows) may sit between them."""
    blocks = []
    for first, text in enumerate(texts):
        if text != lines[0]:
            continue
        index = first
        for expected in lines[1:]:
            index += 1
            while index < len(texts) and texts[index] == "":
                index += 1
            if index == len(texts) or texts[index] != expected:
                break
        else:
            blocks.append((first, index))
    return blocks


def is_separator(character):
    return character.isspace() or "\u2500" <= character <= "\u257f"


def modal_kinds(rows):
    kinds = set()
    for row in rows:
        folded = row.casefold()
        for kind, phrases in MODALS:
            if any(phrase in folded for phrase in phrases):
                kinds.add(kind)
    return kinds


def row_words(row):
    """(start, end, word) for each run between whitespace and box-drawing characters."""
    words, start = [], None
    for index, character in enumerate(row):
        if is_separator(character):
            if start is not None:
                words.append((start, index, row[start:index]))
                start = None
        elif start is None:
            start = index
    if start is not None:
        words.append((start, len(row), row[start:]))
    return words


def check_trust_path(visible, trust_path):
    """Every path-like word on the visible screen must be exactly the trust path."""
    named = False
    for row in visible:
        for start, end, word in row_words(row):
            if "/" not in word and not word.startswith("~"):
                continue
            # tmux prints a wrapped line as separate rows: a word at a row edge may continue.
            if start == 0 or end == len(row):
                raise Refusal("trust-path-unclear", "A path on the visible screen starts or ends its row, so it may "
                                                    "continue on another row. Nothing was sent.")
            if word != trust_path:
                raise Refusal("trust-path-mismatch", "The visible screen names a path other than the trust path. Nothing was sent.")
            named = True
    if not named:
        raise Refusal("trust-path-missing", "The visible screen does not show the trust path as one word. Nothing was sent.")


def current_prompt(text, truncated, rows_total, ident, expect_kind, lines, trust_path=None, shape="box"):
    """Require the expected prompt to be the current bottom-of-pane state.

    The visible screen is the one visible_screen locates, and it must be
    complete. It must show exactly one prompt kind (the expected one), name
    exactly the trust path for a trust prompt, show the expected lines exactly,
    once, as every row of the one paired box around them (check_box_prompt) or,
    for Cursor's command prompt, under its divider (check_divider_prompt), and
    have no composer row after the prompt or under the cursor. Anything else
    fails closed."""
    rows = text.split("\n")
    visible = visible_screen(text, truncated, rows_total, ident)
    kinds = modal_kinds(visible)
    # The expected prompt exists in the capture, but maybe not as the current state.
    stale = expect_kind in modal_kinds(rows) and bool(prompt_blocks([prompt_text(row) for row in rows], lines))
    if not kinds:
        if stale:
            raise Refusal("stale-prompt", "The expected prompt is only in older captured text, not on the visible screen. Nothing was sent.")
        raise Refusal("no-modal", "No prompt is showing in the inspected pane. Nothing was sent.")
    if len(kinds) > 1:
        raise Refusal("prompt-unclear", "More than one prompt kind is on the visible screen. Nothing was sent.")
    (kind,) = kinds
    if kind != expect_kind:
        raise Refusal("modal-mismatch", "The visible prompt is " + kind + ", not " + expect_kind + ". Nothing was sent.")
    if expect_kind == "modal-trust":
        check_trust_path(visible, trust_path)
    # The expected lines are the whole prompt: exact rows, once, with nothing next to them.
    texts = [prompt_text(row) for row in visible]
    blocks = prompt_blocks(texts, lines)
    if not blocks:
        if stale:
            raise Refusal("stale-prompt", "The expected prompt text is not all on the visible screen. Nothing was sent.")
        raise Refusal("modal-mismatch", "The expected prompt rows are not shown exactly, in order, on the visible screen. Nothing was sent.")
    if len(blocks) > 1:
        raise Refusal("prompt-unclear", "The expected prompt is shown more than once on the visible screen. Nothing was sent.")
    ((first, last),) = blocks
    boxes = visible_boxes(visible, texts)
    if boxes is None:
        raise Refusal("prompt-unclear", "The box borders on the visible screen do not pair up. Nothing was sent.")
    if shape == "divider":
        check_divider_prompt(visible, boxes, first, last, int(ident["width"]), lines)
    else:
        check_box_prompt(visible, texts, boxes, first, last)
    cursor = int(ident["cursor_y"])
    below = visible[last + 1:]
    if any(COMPOSER.match(row) for row in below) or (0 <= cursor < len(visible) and COMPOSER.match(visible[cursor])):
        raise Refusal("stale-prompt", "A composer row follows the prompt or holds the cursor, so the prompt is not current. Nothing was sent.")
    return kind


def check_box_prompt(visible, texts, boxes, first, last):
    """The expected rows first..last are every row of one paired, untitled box."""
    # The block must be the whole of one box, paired by its corner columns. Padding rows
    # sit between a box's rows, so the rows next to the block say nothing: check them all.
    around = [box for box in boxes if box[0] < first and last < box[1]]
    if not around:
        raise Refusal("prompt-unclear", "The expected prompt is not inside a box on the visible screen. Nothing was sent.")
    # The outermost box around the block: the one that opens first.
    top, bottom, left, _right, titled = min(around)
    if titled:
        raise Refusal("prompt-unclear", "The prompt's box has a titled border, and the title is not part of the expected "
                                        "text. Nothing was sent.")
    if any(top < box[0] and box[1] < bottom for box in boxes):
        raise Refusal("prompt-unclear", "The prompt's box holds another box, so the prompt cannot be bound to one box. "
                                        "Nothing was sent.")
    if any(row[:left].strip() for row in visible[top:bottom + 1]):
        raise Refusal("prompt-unclear", "Something is drawn to the left of the prompt's box, so it may sit inside "
                                        "another box. Nothing was sent.")
    if any(texts[index] for index in range(top + 1, first)) or any(texts[index] for index in range(last + 1, bottom)):
        raise Refusal("modal-mismatch", "The prompt's box has a row the expected text leaves out: the expected text must "
                                        "be every row in the box. Nothing was sent.")


def phrases(row):
    folded = row.casefold()
    return {phrase for _kind, group in MODALS for phrase in group if phrase in folded}


def check_divider_prompt(visible, boxes, first, last, width, lines):
    """Cursor's command prompt: rows first..last are not in a box, sit directly under a
    divider row that spans the pane, and have only blank rows after them to the pane
    bottom. No other row on the visible screen holds a prompt phrase, except Cursor's
    status line for the pending command.

    The command is not in a box, so only spaces pad its rows: with spaces alone taken
    off, the rows must be the expected lines. A box-drawing or other whitespace
    character at a row's end is part of the command, not a border."""
    if any(box[0] < first and last < box[1] for box in boxes):
        raise Refusal("prompt-unclear", "Cursor's command prompt is not drawn in a box, but these rows are inside one. "
                                        "Nothing was sent.")
    if first < 1 or visible[first - 1] != DIVIDER * width:
        raise Refusal("prompt-unclear", "The command prompt is not directly under a divider row across the pane. "
                                        "Nothing was sent.")
    if any(row.strip(" ") for row in visible[last + 1:]):
        raise Refusal("prompt-unclear", "Something follows the command prompt before the pane bottom. Nothing was sent.")
    if any(phrases(row) - {PENDING_STATUS} for row in visible[:first] + visible[last + 1:]):
        raise Refusal("prompt-unclear", "Another prompt is on the visible screen. Nothing was sent.")
    if [row.strip(" ") for row in visible[first:last + 1] if row.strip(" ")] != lines:
        raise Refusal("modal-mismatch", "The command rows differ from the expected text once only spaces are taken "
                                        "off their ends. Nothing was sent.")


def approval_path(name, approval_ref):
    digest = hashlib.sha256(approval_ref.encode("utf-8")).hexdigest()
    return state_path(name).parent / "approvals" / digest


def rows_digest(rows):
    return hashlib.sha256("\n".join(rows).encode("utf-8")).hexdigest()


def box_evidence(lines):
    """What the ledger keeps to know an answered box again: a hash of the prompt
    rows with the selection marker taken off, and which rows had it. No prompt text."""
    marked = [index for index, line in enumerate(lines) if line.startswith(SELECTION_MARKER)]
    rows = [line[len(SELECTION_MARKER):] if index in marked else line for index, line in enumerate(lines)]
    return {"box_rows_sha256": rows_digest(rows), "box_marker_rows": marked}


def answered_trust(state, answered_text=None):
    """The ledger evidence for this pane's last answer, or None. Nothing is written.

    The last answer in the task's state must be a sent trust answer whose ledger
    entry names this task, pane, process and owner marker, has the worktree as
    its trust path, and is the state's record plus its name. A format-2 entry
    carries the box evidence and names the worktree. An entry written before
    format 2 is evidence only with `answered_text`, the (raw, lines) of the exact
    expected-text file of that answer: its SHA-256 must be the entry's
    expected_text_sha256, and the box evidence is derived from its lines as
    answer derives it. An entry that cannot be read is not evidence. The screen
    is never evidence."""
    answers = state.get("answers")
    record = answers[-1] if isinstance(answers, list) and answers else None
    ref = record.get("approval_ref") if isinstance(record, dict) else None
    if not isinstance(ref, str) or not APPROVAL_RE.fullmatch(ref):
        return None
    entry = read_ledger(approval_path(state["name"], ref))
    required = {
        "kind": "modal-trust", "outcome": "sent", "approval_ref": ref,
        "name": state["name"], "pane_id": state["pane_id"], "pane_pid": state["pane_pid"],
        "owner": state["owner"], "trust_path": state["worktree"],
    }
    if not isinstance(entry, dict) or any(entry.get(key) != value for key, value in required.items()):
        return None
    # The task's own record must be this same answer.
    if {key: value for key, value in entry.items() if key != "name"} != record:
        return None
    derived = None
    if answered_text is not None:
        text_raw, text_lines = answered_text
        if hashlib.sha256(text_raw).hexdigest() != entry.get("expected_text_sha256"):
            return None
        derived = box_evidence(text_lines)
    if entry.get("format") == LEDGER_FORMAT and entry.get("worktree") == state["worktree"]:
        evidence = {"box_rows_sha256": entry.get("box_rows_sha256"), "box_marker_rows": entry.get("box_marker_rows")}
        if derived is not None and derived != evidence:
            return None
    elif derived is not None and not any(key in entry for key in LEDGER_FORMAT_KEYS):
        evidence = derived
    else:
        return None
    digest, marked = evidence["box_rows_sha256"], evidence["box_marker_rows"]
    if not isinstance(digest, str) or not SHA256_RE.fullmatch(digest):
        return None
    if (not isinstance(marked, list) or not all(type(index) is int for index in marked)
            or marked != sorted(set(marked)) or any(not 0 <= index < MAX_EXPECT_LINES for index in marked)):
        return None
    return {"approval_ref": ref, "box_rows_sha256": digest, "box_marker_rows": marked}


def read_ledger(path):
    """One ledger entry for reading, or None. Only a regular file is read: the open
    does not follow a symbolic link and does not block (a FIFO, for example)."""
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
    except OSError:
        return None
    try:
        if not stat.S_ISREG(os.fstat(descriptor).st_mode):
            return None
        chunks, size = [], 0
        while size <= MAX_LEDGER:
            chunk = os.read(descriptor, MAX_LEDGER + 1 - size)
            if not chunk:
                break
            chunks.append(chunk)
            size += len(chunk)
        raw = b"".join(chunks)
        return json.loads(raw.decode("utf-8")) if len(raw) <= MAX_LEDGER else None
    except (OSError, UnicodeError, ValueError):
        return None
    finally:
        os.close(descriptor)


def read_answered_text(path):
    """--answered-text-file, read and checked like answer's --expect-text-file. None when absent."""
    if path is None:
        return None
    try:
        return read_expected(path)
    except Refusal as refusal:
        raise Refusal(refusal.state, "--answered-text-file: " + refusal.detail)


def answered_evidence(state, answered_text):
    answered = answered_trust(state, answered_text)
    if answered_text is not None and answered is None:
        raise Refusal("answered-text-unaccepted", (
            "--answered-text-file is not evidence for this task's last answer. It must be the exact expected-text "
            "file of a sent trust answer for this task, pane, process and owner, whose trust path is the worktree "
            "and whose ledger entry matches the task's record. Nothing was sent."))
    return answered


def cursor_composer_empty(rows):
    """True when exactly one of `rows` is Cursor's composer (its first character, after
    whitespace, is the arrow) and that row is the empty composer: arrow and placeholder only.

    Cursor parks its cursor on the blank last row, so the cursor row alone does not show
    that Cursor drew its composer after the answer."""
    composers = [row.strip() for row in rows if row.strip().startswith(CURSOR_COMPOSER_ARROW)]
    return composers == [CURSOR_COMPOSER_EMPTY]


def answered_box(rows, first_visible, answered, truncated):
    """(top, bottom) of the answered trust box in the bounded capture, or None.

    `rows` is the whole bounded capture, which the receipt binds; rows[first_visible:]
    is the visible screen. Every border in the capture must pair. The box must be the
    only box with a prompt phrase, untitled, neither inside nor around another box,
    with nothing to its left. Its top border may be in history, but not on the first
    row of a truncated capture, which may be cut. Its bottom border and its status
    row must be on the visible screen. Its nonblank rows, trimmed as in rule 4, must
    be the answered prompt's rows without the selection marker, with the last row,
    ANSWERED_HINT, replaced by one ANSWERED_STATUS row. No row may show the marker,
    and no history row outside the box may hold a prompt phrase."""
    texts = [prompt_text(row) for row in rows]
    boxes = visible_boxes(rows, texts)
    phrased = [box for box in boxes or () if modal_kinds(rows[box[0]:box[1] + 1])]
    if len(phrased) != 1:
        return None
    ((top, bottom, left, _right, titled),) = phrased
    if titled or any(box != phrased[0] and box[0] <= bottom and top <= box[1] for box in boxes):
        return None
    if bottom < first_visible or (truncated and top == 0):
        return None
    if any(row[:left].strip() for row in rows[top:bottom + 1]):
        return None
    inner = [(index, texts[index]) for index in range(top + 1, bottom) if texts[index]]
    if not inner or inner[-1][1] != ANSWERED_STATUS or inner[-1][0] < first_visible:
        return None
    body = [text for _index, text in inner[:-1]]
    # Cursor drops the selection marker once the answer is taken: it must be gone.
    if ANSWERED_STATUS in body or any(SELECTION_MARKER.strip() in text for text in body):
        return None
    if rows_digest(body + [ANSWERED_HINT]) != answered["box_rows_sha256"]:
        return None
    if modal_kinds(rows[:min(top, first_visible)]):
        return None
    return top, bottom


def spend_approval(path, entry):
    """Create the approval's ledger entry once. An existing entry refuses."""
    state_directory(path)
    try:
        descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    except FileExistsError:
        raise Refusal("approval-spent", "This approval reference was already used. One approval answers one prompt once. Nothing was sent.")
    with os.fdopen(descriptor, "wb") as handle:
        handle.write(json.dumps(entry, sort_keys=True).encode("utf-8"))
        handle.flush()
        os.fsync(handle.fileno())
    directory = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def utc_now():
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def answer_session(args):
    # Read and validate every input before the lock and before any tmux call.
    choice, raw, lines, trust_path = validate_answer(args)
    with state_lock(args.name, wait=False):
        return answer_locked(args, choice, raw, lines, trust_path)


def answer_locked(args, choice, raw, lines, trust_path):
    ledger = approval_path(args.name, args.approval_ref)
    if ledger.exists():
        raise Refusal("approval-spent", "This approval reference was already used. One approval answers one prompt once. Nothing was sent.")
    path, state = load_owned(args.name, args.tmux)
    # The approval covers one directory: the worktree this task's start recorded.
    if args.expect_kind == "modal-trust" and trust_path != state["worktree"]:
        raise Refusal("trust-path-mismatch", "--trust-path is not the worktree this task's start recorded. Nothing was sent.")
    receipt = parse_receipt(args.receipt)
    if not state.get("receipt_id") or receipt["id"] != state["receipt_id"]:
        raise Refusal("stale-receipt", "Pane receipt is stale or is not the latest inspect. Re-inspect. Nothing was sent.")
    if receipt["pane"] != state["pane_id"] or receipt["pid"] != state["pane_pid"] or receipt["digest"] != state["snapshot_sha256"]:
        raise Refusal("stale-receipt", "Pane receipt does not match the stored inspect. Re-inspect. Nothing was sent.")
    ident = read_identity(state["tmux"], state["pane_id"], "inspect-unconfirmed", 3)
    require_same_pane(state, ident)
    text, truncated, rows_total = read_capture_rows(state["tmux"], state["pane_id"])
    digest = snapshot_hash(ident, text, truncated)
    if digest != state["snapshot_sha256"] or digest != receipt["digest"]:
        raise Refusal("stale-receipt", "Pane capture changed since inspect. Re-inspect. Nothing was sent.")
    shape = prompt_shape(args.expect_kind, choice, lines)
    kind = current_prompt(text, truncated, rows_total, ident, args.expect_kind, lines, trust_path, shape)
    record = {
        "answered_at": utc_now(), "approval_ref": args.approval_ref, "kind": kind, "choice": choice,
        "expected_text_sha256": hashlib.sha256(raw).hexdigest(), "pane_id": ident["pane"],
        "pane_pid": ident["pid"], "owner": state["owner"], "outcome": "sending",
    }
    if trust_path is not None:
        record["trust_path"] = trust_path
    # Evidence that inspect and reply need to know this box again once it is answered.
    record.update(box_evidence(lines), format=LEDGER_FORMAT, worktree=state["worktree"])
    entry = dict(record, name=state["name"])
    # Spend the approval durably, then the receipt, before the key is sent.
    spend_approval(ledger, entry)
    answers = state.get("answers") if isinstance(state.get("answers"), list) else []
    state["answers"] = (answers + [record])[-MAX_ANSWERS:]
    clear_receipt(path, state)
    # The writes above take time. Look at the pane again right before the key:
    # same pane and process, the same capture as verified, and the same prompt.
    try:
        again = read_identity(state["tmux"], state["pane_id"], "inspect-unconfirmed", 3)
        require_same_pane(state, again)
        text_again, truncated_again, rows_again = read_capture_rows(state["tmux"], state["pane_id"])
        if snapshot_hash(again, text_again, truncated_again) != digest:
            raise Refusal("stale-receipt", "Pane capture changed after the approval was spent.")
        current_prompt(text_again, truncated_again, rows_again, again, args.expect_kind, lines, trust_path, shape)
    except Refusal as refusal:
        record["outcome"] = entry["outcome"] = "not-sent"
        write_state(path, state)
        write_state(ledger, entry)
        raise Refusal("prompt-changed", "The pane changed after the approval was spent (" + refusal.state + "). "
                      "Nothing was sent. The approval stays spent; a new approval is needed.")
    try:
        keys = ["C-c"] if choice == REJECT_CHOICE else ["-l", "--", choice]
        sent = run([state["tmux"], "send-keys", "-t", state["pane_id"]] + keys)
        failed = sent.returncode != 0
    except (subprocess.TimeoutExpired, OSError, UnicodeError):
        failed = True
    # An unknown outcome stays uncertain. It is never answered again under this approval.
    record["outcome"] = entry["outcome"] = "uncertain" if failed else "sent"
    write_state(path, state)
    write_state(ledger, entry)
    if failed:
        raise Refusal("answer-unconfirmed", "The key's outcome is unknown. Do not retry this receipt or send another key. Re-inspect.", 3)
    return {
        "state": "answered", "name": state["name"], "kind": kind, "choice": choice,
        "approval_ref": args.approval_ref, "answered_at": record["answered_at"],
        "detail": (
            "One key was sent to the owned pane whose receipt matched; Enter was not sent. "
            "The helper recorded the approval reference but did not verify the approval. "
            "This receipt is now spent. Inspect again before any reply."
        ),
    }


ANSWERED_TEXT_HELP = ("for a ledger entry written before format 2: the exact --expect-text-file of the task's "
                      "last trust answer; its SHA-256 must match the entry. Read only; it never writes the ledger")


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
    inspect.add_argument("--answered-text-file", help=ANSWERED_TEXT_HELP)

    reply = sub.add_parser("reply", help="send literal text only to the inspected pane")
    reply.add_argument("--name", required=True)
    reply.add_argument("--tmux", required=True)
    reply.add_argument("--receipt", required=True)
    reply.add_argument("--text-file", required=True)
    reply.add_argument("--submit", action="store_true", help="send one Enter after literal text")
    reply.add_argument("--observed-ready", action="store_true",
                       help="manual confirmation of this exact unclassified receipt; not a Cursor trust flag")
    reply.add_argument("--answered-text-file", help=ANSWERED_TEXT_HELP)

    answer = sub.add_parser(
        "answer", help="send one approved prompt choice to the inspected pane",
        description=(
            "Send one key that answers the current prompt in the inspected pane, once per approval. "
            "The caller is responsible for a verified approval of this exact action, choice and target. "
            "The helper does not verify any approval: --approval-ref is audit metadata, not proof."))
    answer.add_argument("--name", required=True)
    answer.add_argument("--tmux", required=True)
    answer.add_argument("--receipt", required=True)
    answer.add_argument("--approval-ref", required=True,
                        help="audit reference to the caller's verified approval; recorded once, never verified")
    answer.add_argument("--expect-kind", required=True, help="modal-trust, modal-permission or modal-approval")
    answer.add_argument("--expect-text-file", required=True, help="exact prompt lines that must be shown, in order")
    answer.add_argument("--choice", required=True, help="one key shown as [key] in the expected text; on Cursor's "
                        "command prompt, y (Run once) or ctrl-c (reject the shown command)")
    answer.add_argument("--trust-path", help="for modal-trust: the one directory the approval covers; must be the recorded worktree")
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
        elif args.command == "answer":
            payload = answer_session(args)
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
