"""Offline fixtures. No installed Cursor client, tmux server, or account is contacted."""

import argparse
from concurrent.futures import ThreadPoolExecutor
import importlib.util
import json
import os
from pathlib import Path
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
import json, os, sys
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
    print("\t".join([session, pane, pid, dead, path, command, owner, cursor_y, height, in_mode]))
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
                line = "\t".join([self.name, "%7", "123", "0", str(self.repo.resolve()), "client",
                                  owner, "1", "2", "0"])
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


if __name__ == "__main__":
    unittest.main()
