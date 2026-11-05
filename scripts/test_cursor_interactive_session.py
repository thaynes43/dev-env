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
import json, os, re, sys
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
    }
    # Like tmux, print only the variables the -F format names, in its order.
    template = args[args.index("-F") + 1]
    print("\t".join(values[key] for key in re.findall(r"#\{([^}]*)\}", template)))
    sys.exit(0)
if args and args[0] == "capture-pane":
    sys.stdout.buffer.write(Path(os.environ["FAKE_CAPTURE"]).read_bytes())
    sys.exit(0)
if args and args[0] == "send-keys":
    if "-l" in args and os.environ.get("FAKE_SEND_READY"):
        with open(os.environ["FAKE_SEND_READY"], "wb", buffering=0) as ready:
            ready.write(b"1")
        with open(os.environ["FAKE_SEND_GATE"], "rb", buffering=0) as gate:
            gate.read(1)
    if mode == "send-fail":
        sys.exit(1)
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
                          "pane_in_mode": "0", "history_size": "0"}
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
        original_capture, original_flock = helper.read_capture, helper.fcntl.flock
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
                mock.patch.object(helper, "read_capture", paused_capture), \
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


TRUST_CAPTURE = (
    "Tip: You can start the Cursor CLI with `agent`.\n"
    "  \u256d\u2500\u2500\u2500\u2500\u256e\n"
    "  \u2502  \u26a0 Workspace Trust Required                      \u2502\n"
    "  \u2502  Cursor Agent can execute code and access files.   \u2502\n"
    "  \u2502  Do you trust the contents of this directory?      \u2502\n"
    "  \u2502    /home/dev/work/example-task                      \u2502\n"
    "  \u2502  \u25b6 [a] Trust this workspace                       \u2502\n"
    "  \u2502    [q] Quit                                        \u2502\n"
    "  \u2570\u2500\u2500\u2500\u2500\u256f\n"
)
# The visible screen as tmux prints it: the prompt box, then blank rows.
TRUST_SCREEN = TRUST_CAPTURE + "\n\n"
# Fixture text names this path; each answer test replaces it with its real worktree.
EXAMPLE_PATH = "/home/dev/work/example-task"
TRUST_PATH_ROW = "  \u2502    " + EXAMPLE_PATH + "                      \u2502\n"
assert TRUST_PATH_ROW in TRUST_CAPTURE
# The whole prompt, row for row, as the screen shows it inside the border.
TRUST_EXPECT = (
    "\u26a0 Workspace Trust Required\n"
    "Cursor Agent can execute code and access files.\n"
    "Do you trust the contents of this directory?\n"
    "/home/dev/work/example-task\n"
    "\u25b6 [a] Trust this workspace\n"
    "[q] Quit\n"
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
        return self.fix(TRUST_SCREEN).replace(self.fix(TRUST_PATH_ROW), path_rows)

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

    def test_help_says_the_caller_owns_the_approval(self):
        result = subprocess.run([sys.executable, str(SCRIPT), "answer", "--help"], capture_output=True,
                                text=True, timeout=10)
        self.assertEqual(result.returncode, 0)
        text = " ".join(result.stdout.split())
        self.assertIn("The caller is responsible for a verified approval", text)
        self.assertIn("does not verify", text)


if __name__ == "__main__":
    unittest.main()
