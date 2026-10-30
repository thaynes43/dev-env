#!/usr/bin/env python3
"""Launch one fresh native Codex client; never resume, pair, or alter trust."""

import argparse
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import stat
import subprocess
import sys
import time

MAX_TASK = 4096
MAX_COMMAND_BYTES = 8192
COMMAND_TIMEOUT = 5


class Refusal(Exception):
    def __init__(self, state, detail, code=2):
        self.state, self.detail, self.code = state, detail, code


def run(argv):
    return subprocess.run(argv, stdin=subprocess.DEVNULL, capture_output=True,
                          text=True, timeout=COMMAND_TIMEOUT, check=False)


def executable(value):
    result = shutil.which(value)
    if not result:
        raise Refusal("prerequisite-unavailable", "Client or tmux executable unavailable; supply an installed executable.")
    return str(Path(result).resolve())


def prepare(args):
    for value in (args.repo, args.socket):
        if not Path(value).is_absolute() or "\0" in value:
            raise Refusal("invalid-input", "Repository and socket must be absolute local paths.")
    repo = Path(args.repo).resolve(strict=True)
    if not repo.is_dir():
        raise Refusal("invalid-input", "Repository must be an existing authorized directory.")
    if not stat.S_ISSOCK(Path(args.socket).stat().st_mode):
        raise Refusal("prerequisite-unavailable", "Socket path is not a Unix socket; use the existing daemon's socket.")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", args.name):
        raise Refusal("invalid-input", "Name must be 1..64 letters, digits, underscores or hyphens; use a unique task name.")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", args.model):
        raise Refusal("invalid-input", "Supply an explicit full model ID, without CLI options.")
    if args.task_file == "-":
        raw = sys.stdin.buffer.read(MAX_TASK + 1)
    else:
        with open(args.task_file, "rb") as handle:
            raw = handle.read(MAX_TASK + 1)
    if not raw.strip() or len(raw) > MAX_TASK or b"\0" in raw:
        raise Refusal("invalid-input", "Task must contain 1..4096 UTF-8 bytes without NUL; supply only the new task handoff.")
    task = raw.decode("utf-8")
    client, tmux = executable(args.client), executable(args.tmux)
    argv = [client, "--remote", "unix://" + args.socket, "-C", str(repo),
            "-m", args.model, "-c", f'model_reasoning_effort="{args.effort}"',
            "--no-alt-screen", "--", task]
    # Requires a POSIX tmux default-shell (documented prerequisite).
    # tmux's single shell-command is parsed by a shell. Quote EVERY argument,
    # including the executable and multiline task. Never send keys to a pane.
    command = "exec " + shlex.join(argv)
    # Bound the complete quoted tmux message before probes/name reservation.
    # Keep a conservative allowance for tmux framing, not just raw task bytes.
    message = ["new-session", "-d", "-P", "-F", "#{pane_id}", "-s",
               args.name, "-c", str(repo), command]
    message_bytes = 128 + sum(len(value.encode("utf-8")) + 1 for value in message)
    if message_bytes > MAX_COMMAND_BYTES:
        raise Refusal("invalid-input", "Quoted launch command exceeds the 8192-byte budget; shorten the handoff or explicit paths. No name reserved.")
    return repo, client, tmux, command


def launch(args):
    repo, client, tmux, command = prepare(args)
    state_root = Path(os.environ.get("XDG_STATE_HOME", str(Path.home() / ".local" / "state")))
    if not state_root.is_absolute():
        raise Refusal("invalid-input", "XDG_STATE_HOME must be absolute when set.")
    reservation = state_root / "codex-clean-session" / args.name
    if reservation.exists():
        raise Refusal("name-consumed", "This task name was already reserved. Inspect its prior launch; do not reuse or blindly choose a replacement.")
    if args.dry_run:
        return "dry-run", "Inputs checked; no subprocess, socket connection or conversation created. Daemon/client readiness unverified."
    for tool, flag in ((client, "--help"), (tmux, "-V")):
        result = run([tool, flag])
        if result.returncode:
            raise Refusal("prerequisite-unavailable", "Client/tmux probe failed; inspect the explicit executable before launching.")
        if tool == client and not all(option in result.stdout for option in ("--remote", "--no-alt-screen")):
            raise Refusal("unsupported-client", "Installed client lacks required native flags; no launch attempted.")
    # Keep an empty exclusive name marker, including uncertain outcomes. This
    # also prevents accidental reuse after tmux has removed an exited client.
    reservation.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    try:
        descriptor = os.open(reservation, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    except FileExistsError:
        raise Refusal("name-consumed", "Another launch reserved this task name; inspect the prior launch before proceeding.")
    os.close(descriptor)
    # new-session atomically refuses duplicate names. Do not attach, kill, rename,
    # resume or retry on failure: even an uncertain response may have launched.
    try:
        result = run([tmux, "new-session", "-d", "-P", "-F", "#{pane_id}", "-s", args.name, "-c", str(repo), command])
    except (subprocess.TimeoutExpired, OSError, UnicodeError):
        raise Refusal("launch-unconfirmed", "tmux launch outcome unknown; inspect this task's exact name before any retry. No automatic retry.", 3)
    if result.returncode:
        # Do not print subprocess output: it may contain task text or credentials.
        raise Refusal("launch-unconfirmed", "tmux refused or failed (possible name collision); inspect this exact task name. Never reuse an existing client; no retry.", 3)
    pane = result.stdout.strip()
    if not re.fullmatch(r"%[0-9]+", pane):
        raise Refusal("launch-unconfirmed", "tmux returned no valid new pane identity; inspect this exact task name. No automatic retry.", 3)
    time.sleep(args.observe_seconds)
    try:
        observed = run([tmux, "display-message", "-p", "-t", pane,
                        "#{pane_dead}:#{pane_pid}"])
    except (subprocess.TimeoutExpired, OSError, UnicodeError):
        raise Refusal("launch-unconfirmed", "Client observation failed; inspect this exact task name. Do not relaunch automatically.", 3)
    if observed.returncode or not re.fullmatch(r"0:[1-9][0-9]*\n?", observed.stdout):
        raise Refusal("client-not-running", "Client did not survive observation or status is unknown; inspect this task's tmux pane locally. No fallback or retry.", 3)
    return "client-running-unverified", (
        "tmux pane is alive only. Daemon connection, new thread, task acceptance and provider readiness are unverified. "
        "Inspect this task's client for errors or genuine folder-trust prompts; an authorized person must decide trust. "
        "Never send TUI input while a native question modal is pending.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("repo", "socket", "model", "name", "client", "task-file"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--effort", required=True, choices=("low", "medium", "high", "xhigh", "max"))
    parser.add_argument("--tmux", default="tmux", help="installed tmux executable (default: PATH)")
    parser.add_argument("--observe-seconds", type=float, default=1, help="process-only observation delay, 0..5 seconds")
    parser.add_argument("--dry-run", action="store_true", help="validate local inputs only; invoke no tools")
    args = parser.parse_args()
    code = 0
    try:
        if not 0 <= args.observe_seconds <= 5:
            raise Refusal("invalid-input", "Observation delay must be 0..5 seconds.")
        state, detail = launch(args)
    except Refusal as error:
        state, detail, code = error.state, error.detail, error.code
    except (OSError, UnicodeError, ValueError):
        state, detail, code = "prerequisite-unavailable", "Cannot read local input/socket or execute client; check the explicit paths and UTF-8 task. No automatic retry.", 2
    except subprocess.TimeoutExpired:
        state, detail, code = "prerequisite-unavailable", "Client/tmux probe timed out; no launch attempted.", 2
    print(json.dumps({"state": state, "name": args.name, "detail": detail}))
    return code


if __name__ == "__main__":
    sys.exit(main())
