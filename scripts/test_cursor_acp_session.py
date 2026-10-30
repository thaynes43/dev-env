import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest

# The approved child environment, stated independently of the code under test.
ALLOWLIST = ("PATH", "HOME", "LANG", "LC_ALL", "TERM", "TMPDIR", "XDG_RUNTIME_DIR")
SECRET_PARTS = ("TOKEN", "KEY", "SECRET", "PASSWORD")
SPEC = importlib.util.spec_from_file_location("cursor_acp_session", Path(__file__).with_name("cursor-acp-session.py"))
ACP = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ACP)


class ProtocolTests(unittest.TestCase):
    def setUp(self):
        self.sent, self.events = [], []
        self.session = ACP.Session(self.sent.append, self.events.append, "/synthetic/project", "Inspect fixture", "grok-synthetic")
        self.session.start()

    def ready(self):
        self.session.provider({"jsonrpc": "2.0", "id": 1, "result": {"protocolVersion": 1}})
        self.session.provider({"jsonrpc": "2.0", "id": 2, "result": {"sessionId": "fixture-session"}})

    def question(self, ident=40):
        self.session.provider({"jsonrpc": "2.0", "id": ident, "method": "cursor/ask_question", "params": {"toolCallId": "fixture-call", "questions": [{"id": "q1", "prompt": "Which fixture?", "options": [{"id": "a", "label": "First"}, {"id": "b", "label": "Second"}]}]}})

    def answer(self, ident=40, selected=None):
        return {"requestID": ident, "sessionID": "fixture-session", "kind": "question", "answers": [{"questionId": "q1", "selectedOptionIds": selected or ["a"]}]}

    def permission(self):
        self.session.provider({"jsonrpc": "2.0", "id": 41, "method": "session/request_permission", "params": {"sessionId": "fixture-session", "toolCall": {"title": "PRIVATE_TOOL_CONTENT", "rawInput": "PRIVATE_ARGUMENT"}, "options": [{"optionId": "yes", "kind": "allow_once", "name": "Allow"}, {"optionId": "no", "kind": "reject_once", "name": "Deny"}]}})

    def test_fresh_route_correlates_and_never_loads_history(self):
        self.ready()
        self.assertEqual([x["method"] for x in self.sent], ["initialize", "session/new", "session/prompt"])
        self.assertEqual(self.sent[1]["params"], {"cwd": "/synthetic/project", "mcpServers": []})
        self.assertEqual(self.sent[2]["params"]["sessionId"], "fixture-session")
        self.assertEqual(self.events[0]["servedModel"], "Unknown")
        self.assertNotIn("Inspect fixture", json.dumps(self.events))

    def test_question_reply_is_correlated_and_spent(self):
        self.ready()
        self.question()
        self.assertEqual(self.events[-1]["event"], "question-required")
        self.session.decision(self.answer())
        self.assertEqual(self.sent[-1], {"jsonrpc": "2.0", "id": 40, "result": {"outcome": {"outcome": "answered", "answers": self.answer()["answers"]}}})
        with self.assertRaisesRegex(ACP.Refusal, "stale"):
            self.session.decision(self.answer())
        with self.assertRaisesRegex(ACP.Refusal, "duplicate-request"):
            self.question()

    def test_bad_question_decisions_never_send(self):
        self.ready()
        self.question()
        for change in [dict(sessionID="foreign-session"), dict(requestID=42), dict(answers=[]), dict(answers=[{"questionId": "q1", "selectedOptionIds": ["missing"]}]), dict(answers=[{"questionId": "q1", "selectedOptionIds": ["a", "b"]}])]:
            value = self.answer()
            value.update(change)
            before = len(self.sent)
            with self.assertRaises(ACP.Refusal):
                self.session.decision(value)
            self.assertEqual(len(self.sent), before)
        self.session.decision(self.answer())

    def test_permission_only_explicit_reject_no_autoapproval(self):
        self.ready()
        self.permission()
        self.assertNotIn("PRIVATE", json.dumps(self.events))
        before = len(self.sent)
        with self.assertRaisesRegex(ACP.Refusal, "approval-not-supported"):
            self.session.decision({"requestID": 41, "sessionID": "fixture-session", "kind": "permission", "optionID": "yes"})
        self.assertEqual(len(self.sent), before)
        self.session.decision({"requestID": 41, "sessionID": "fixture-session", "kind": "permission", "optionID": "no"})
        self.assertEqual(self.sent[-1]["result"]["outcome"]["optionId"], "no")

    def test_foreign_request_and_updates_refused(self):
        self.ready()
        for message in [
            {"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "foreign", "update": {"sessionUpdate": "agent_message_chunk"}}},
            {"jsonrpc": "2.0", "id": 45, "method": "session/request_permission", "params": {"sessionId": "foreign", "options": []}},
            {"jsonrpc": "2.0", "id": 46, "method": "cursor/ask_question", "params": {"sessionId": "foreign", "questions": []}},
        ]:
            with self.assertRaisesRegex(ACP.Refusal, "cross-session"):
                self.session.provider(message)

    def test_raw_assistant_and_tool_notifications_not_emitted(self):
        self.ready()
        before = list(self.events)
        for kind in ["agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update"]:
            self.session.provider({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "fixture-session", "update": {"sessionUpdate": kind, "content": {"text": "DO_NOT_EXPORT"}, "rawInput": "DO_NOT_EXPORT"}}})
        self.assertEqual(self.events, before)

    def test_unknown_plan_is_error_never_accepted(self):
        self.ready()
        self.session.provider({"jsonrpc": "2.0", "id": 44, "method": "cursor/create_plan", "params": {"plan": "PRIVATE_PLAN"}})
        self.assertEqual(self.sent[-1]["error"]["code"], -32601)
        self.assertNotIn("PRIVATE_PLAN", json.dumps(self.events))

    def test_matching_prompt_completion_only(self):
        self.ready()
        self.session.provider({"jsonrpc": "2.0", "id": 3, "result": {"stopReason": "end_turn"}})
        self.assertTrue(self.session.complete)
        self.assertEqual(self.events[-1]["event"], "prompt-complete")

    def test_response_errors_and_correlation_fail_closed(self):
        for message in [
            {"jsonrpc": "2.0", "id": 9, "result": {}},
            {"jsonrpc": "2.0", "id": True, "result": {}},
            {"jsonrpc": "2.0", "id": 1, "result": {}, "error": {}},
            {"jsonrpc": "2.0", "id": 1, "error": {"message": "SECRET"}},
        ]:
            with self.assertRaises(ACP.Refusal) as caught:
                self.session.provider(message)
            self.assertNotIn("SECRET", str(caught.exception))

    def test_request_bounds_do_not_grow_unbounded(self):
        self.ready()
        for ident in range(16):
            self.question(ident)
        with self.assertRaisesRegex(ACP.Refusal, "request-bound"):
            self.question(16)

    def test_non_string_permission_kind_fails_closed(self):
        self.ready()
        for offset, kind in enumerate([["allow_once"], {"kind": "allow_once"}, 7, None, "allow-once"]):
            message = {"jsonrpc": "2.0", "id": 60 + offset, "method": "session/request_permission", "params": {"sessionId": "fixture-session", "options": [{"optionId": "no", "kind": kind}]}}
            with self.subTest(kind=kind), self.assertRaises(ACP.Refusal) as caught:
                self.session.provider(message)
            self.assertEqual(str(caught.exception), "invalid-permission-options")
        self.assertNotIn("permission-required", json.dumps(self.events))

    def test_malformed_request_id_has_its_own_reason(self):
        self.ready()
        for ident in ["bad id!", "", "-leading", "x" * 129, True, 1.5, None, -1, 2147483648, ["a"]]:
            with self.subTest(ident=ident), self.assertRaises(ACP.Refusal) as caught:
                self.session.provider({"jsonrpc": "2.0", "id": ident, "method": "cursor/ask_question", "params": {"questions": []}})
            self.assertEqual(str(caught.exception), "invalid-request-id")
        self.question(40)
        with self.assertRaises(ACP.Refusal) as caught:
            self.question(40)
        self.assertEqual(str(caught.exception), "duplicate-request")

    def test_session_update_before_session_new_fails_closed(self):
        update = {"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "fixture-session", "update": {"sessionUpdate": "available_commands_update"}}}
        with self.assertRaises(ACP.Refusal) as caught:
            self.session.provider(update)
        self.assertEqual(str(caught.exception), "early-session-update")
        self.session.provider({"jsonrpc": "2.0", "id": 1, "result": {"protocolVersion": 1}})
        with self.assertRaises(ACP.Refusal) as caught:
            self.session.provider(update)
        self.assertEqual(str(caught.exception), "early-session-update")
        self.session.provider({"jsonrpc": "2.0", "id": 2, "result": {"sessionId": "fixture-session"}})
        self.session.provider(update)  # Correlated updates after session/new are accepted, never emitted.

    def test_fs_and_terminal_callbacks_get_method_not_found(self):
        self.ready()
        before = list(self.events)
        methods = ["fs/read_text_file", "fs/write_text_file", "terminal/create", "terminal/output", "terminal/wait_for_exit", "terminal/kill", "terminal/release"]
        for offset, method in enumerate(methods):
            self.session.provider({"jsonrpc": "2.0", "id": 70 + offset, "method": method, "params": {"sessionId": "fixture-session", "path": "/PRIVATE_PATH", "command": "PRIVATE_COMMAND", "content": "PRIVATE_CONTENT"}})
            self.assertEqual(self.sent[-1], {"jsonrpc": "2.0", "id": 70 + offset, "error": {"code": -32601, "message": "Unsupported client operation"}})
        self.assertEqual([x["event"] for x in self.events[len(before):]], ["unsupported-request"] * len(methods))
        self.assertNotIn("PRIVATE", json.dumps(self.events))

    def test_child_environment_is_allowlist_only(self):
        source = {name: "synthetic" for name in ["PATH", "HOME", "TERM", "CLAUDE_CODE_OAUTH_TOKEN", "GH_TOKEN", "CURSOR_API_KEY", "SOME_SECRET", "DB_PASSWORD", "UNRELATED"]}
        self.assertEqual(sorted(ACP.child_environment(source)), ["HOME", "PATH", "TERM"])
        self.assertEqual(ACP.CHILD_ENVIRONMENT, ALLOWLIST)
        self.assertEqual(ACP.child_environment({}), {})

    def test_frame_size_duplicate_fields_and_nonobjects(self):
        for raw in [b'{"id":1,"id":2}', b"[]", b"x" * (ACP.MAX_FRAME + 1), b"\xff", b'{"id":NaN}', ("[" * 2000 + "]" * 2000).encode()]:
            with self.assertRaises(ACP.Refusal):
                ACP.strict_object(raw)


FAKE = '''#!/usr/bin/env python3
import json,os,sys,time
from pathlib import Path
mode=MODE
# Names only, never values. /proc gives the exact exec-time environment on Linux.
try:
 raw=open("/proc/self/environ","rb").read()
 names=sorted({x.split(b"=",1)[0].decode() for x in raw.split(b"\\0") if x})
except OSError:
 names=sorted(os.environ)
Path("fake-env-names.json").write_text(json.dumps(names))
if mode=="grandchild":
 import subprocess
 code="import os,signal,sys,time\\nsignal.signal(signal.SIGTERM,signal.SIG_IGN)\\nopen(sys.argv[1]+'.tmp','w').write(str(os.getpid()))\\nos.rename(sys.argv[1]+'.tmp',sys.argv[1])\\ntime.sleep(60)\\n"
 subprocess.Popen([sys.executable,"-I","-c",code,"grandchild.pid"],stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 end=time.monotonic()+5
 while not Path("grandchild.pid").exists() and time.monotonic()<end: time.sleep(0.02)
codes={}

def write(value):
 print(json.dumps(value),flush=True)
for line in sys.stdin:
 msg=json.loads(line)
 method=msg.get("method")
 if method=="initialize":
  if mode=="hang": time.sleep(60)
  if mode=="oversize": print("x"*70000,flush=True); continue
  write({"jsonrpc":"2.0","id":msg["id"],"result":{"protocolVersion":1}})
 elif method=="session/new":
  write({"jsonrpc":"2.0","id":msg["id"],"result":{"sessionId":"fixture-session"}})
 elif method=="session/prompt":
  if mode=="flood":
   Path("fake-provider.pid").write_text(str(os.getpid()))
   for n in range(5):
    write({"jsonrpc":"2.0","id":40+n,"method":"cursor/ask_question","params":{"questions":[{"id":"q"+str(q),"prompt":"Fixture choice?","options":[{"id":"a"+str(o),"label":"A"*256} for o in range(14)]} for q in range(8)]}})
   continue
  if mode=="error": write({"jsonrpc":"2.0","id":3,"error":{"message":"PRIVATE_PROVIDER_ERROR"}}); continue
  if mode=="exit": sys.exit(0)
  if mode in ("grandchild","env"): write({"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}); continue
  if mode=="permission":
   write({"jsonrpc":"2.0","id":41,"method":"session/request_permission","params":{"sessionId":"fixture-session","toolCall":{"toolCallId":"fixture-call","title":"PRIVATE_TOOL_CONTENT","rawInput":"PRIVATE_ARGUMENT"},"options":[{"optionId":"allow","kind":"allow_once","name":"Allow"},{"optionId":"deny","kind":"reject_once","name":"Deny"}]}})
   continue
  if mode=="callbacks":
   write({"jsonrpc":"2.0","id":50,"method":"fs/read_text_file","params":{"sessionId":"fixture-session","path":"/PRIVATE_PATH"}})
   write({"jsonrpc":"2.0","id":51,"method":"fs/write_text_file","params":{"sessionId":"fixture-session","path":"/PRIVATE_PATH","content":"PRIVATE_CONTENT"}})
   write({"jsonrpc":"2.0","id":52,"method":"terminal/create","params":{"sessionId":"fixture-session","command":"PRIVATE_COMMAND"}})
   continue
  write({"jsonrpc":"2.0","id":40,"method":"cursor/ask_question","params":{"questions":[{"id":"q1","prompt":"Fixture choice?","options":[{"id":"a","label":"First"}]}]}})
 elif mode=="permission" and msg.get("id")==41:
  if msg.get("result")=={"outcome":{"outcome":"selected","optionId":"deny"}}: write({"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}})
  else: write({"jsonrpc":"2.0","id":3,"error":{"message":"fixture expected rejection"}})
 elif mode=="callbacks" and msg.get("id") in (50,51,52):
  codes[msg["id"]]=msg.get("error",{}).get("code")
  if len(codes)==3:
   Path("callback-codes.json").write_text(json.dumps([codes[x] for x in (50,51,52)]))
   write({"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}})
 elif msg.get("id")==40:
  write({"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}})
'''

CLIENT = Path(__file__).with_name("cursor-acp-session.py")


def pid_gone(pid, limit=5.0):
    end = time.monotonic() + limit
    while time.monotonic() < end:
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            return True
        time.sleep(0.05)
    return False


class SubprocessTests(unittest.TestCase):
    def fake(self, directory, mode):
        client = Path(directory) / "fake-cursor"
        client.write_text(FAKE.replace("MODE", repr(mode)))
        client.chmod(0o700)
        prompt = Path(directory) / "prompt.txt"
        prompt.write_text("Synthetic offline task")
        return client, prompt

    def launch(self, directory, mode="question", timeout=10, client_arg=None, model=None, env=None, prefix=()):
        client, prompt = self.fake(directory, mode)
        model_args = ["--model", "fixture-model"] if model is None else model
        command = [sys.executable, "-I", "-B", *prefix, str(CLIENT), "run", "--client", client_arg or str(client), "--worktree", directory, *model_args, "--prompt-file", str(prompt), "--timeout", str(timeout)]
        return subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env)

    def finish(self, process, timeout=15):
        try:
            stdout, stderr = process.communicate(timeout=timeout)
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()
        return [json.loads(x) for x in stdout.splitlines()], stdout + stderr

    def test_real_fake_transport_question_reply_and_no_duplicate_launch(self):
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory)
            try:
                observed = []
                while True:
                    value = json.loads(process.stdout.readline())
                    observed.append(value)
                    if value["event"] == "question-required":
                        break
                process.stdin.write(json.dumps({"requestID": 40, "sessionID": "fixture-session", "kind": "question", "answers": [{"questionId": "q1", "selectedOptionIds": ["a"]}]}) + "\n")
                process.stdin.flush()
                stdout, stderr = process.communicate(timeout=15)
                observed.extend(json.loads(x) for x in stdout.splitlines())
                self.assertEqual(process.returncode, 0, stderr)
                self.assertEqual([x["event"] for x in observed], ["starting", "session-created", "question-required", "decision-sent", "prompt-complete"])
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait()

    def test_real_fake_transport_permission_is_rejected_only(self):
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory, "permission")
            try:
                observed = []
                while True:
                    value = json.loads(process.stdout.readline())
                    observed.append(value)
                    if value["event"] == "permission-required":
                        break
                self.assertEqual(value["options"], [{"optionID": "allow", "kind": "allow_once"}, {"optionID": "deny", "kind": "reject_once"}])
                process.stdin.write(json.dumps({"requestID": 41, "sessionID": "fixture-session", "kind": "permission", "optionID": "deny"}) + "\n")
                process.stdin.flush()
                stdout, stderr = process.communicate(timeout=15)
                observed.extend(json.loads(x) for x in stdout.splitlines())
                self.assertEqual(process.returncode, 0, stderr)
                self.assertEqual([x["event"] for x in observed], ["starting", "session-created", "permission-required", "decision-sent", "prompt-complete"])
                self.assertNotIn("PRIVATE", stdout + stderr)
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait()

    def test_real_fake_transport_permission_approval_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory, "permission")
            try:
                while json.loads(process.stdout.readline())["event"] != "permission-required":
                    pass
                process.stdin.write(json.dumps({"requestID": 41, "sessionID": "fixture-session", "kind": "permission", "optionID": "allow"}) + "\n")
                process.stdin.flush()
            finally:
                observed, output = self.finish(process)
            self.assertEqual(process.returncode, 2)
            self.assertEqual(observed[-1], {"event": "failed", "reason": "permission-approval-not-supported"})
            self.assertNotIn("decision-sent", output)

    def test_real_fake_transport_fs_and_terminal_get_method_not_found(self):
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory, "callbacks")
            observed, output = self.finish(process)
            self.assertEqual(process.returncode, 0, output)
            self.assertEqual([x["event"] for x in observed], ["starting", "session-created", "unsupported-request", "unsupported-request", "unsupported-request", "prompt-complete"])
            self.assertEqual(json.loads((Path(directory) / "callback-codes.json").read_text()), [-32601, -32601, -32601])
            self.assertNotIn("PRIVATE", output)

    def test_provider_exit_before_result_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory, "exit")
            observed, _ = self.finish(process)
            self.assertEqual(process.returncode, 2)
            self.assertEqual(observed[-1], {"event": "failed", "reason": "provider-ended-before-result"})

    def test_bad_inputs_never_start_the_provider(self):
        cases = [
            ("relative-client", dict(client_arg="fake-cursor"), "invalid-input"),
            ("model-joined-dashes", dict(model=["--model=--version"]), "invalid-input"),
            ("model-dash", dict(model=["--model=-v"]), "invalid-input"),
            ("model-split-dashes", dict(model=["--model", "--version"]), "invalid-arguments"),
        ]
        for name, change, reason in cases:
            with self.subTest(case=name), tempfile.TemporaryDirectory() as directory:
                process = self.launch(directory, **change)
                observed, output = self.finish(process)
                self.assertEqual(process.returncode, 2)
                self.assertEqual(observed, [{"event": "failed", "reason": reason}])
                self.assertNotIn("usage", output)
                self.assertFalse((Path(directory) / "fake-env-names.json").exists(), "provider started on bad input")

    def test_child_environment_names_are_allowlist_subset(self):
        # Synthetic stand-ins only; the child reports variable NAMES, never values.
        synthetic = {name: "synthetic-not-a-credential" for name in ["CLAUDE_CODE_OAUTH_TOKEN", "GH_TOKEN", "CURSOR_API_KEY", "OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY", "DB_PASSWORD", "UNRELATED_SETTING"]}
        allowed = {"HOME": None, "LANG": "C", "LC_ALL": "C", "TERM": "dumb", "TMPDIR": None, "XDG_RUNTIME_DIR": None}
        for name, keep in [("all-allowlisted", list(allowed)), ("only-path-and-locale", ["LC_ALL"])]:
            with self.subTest(case=name), tempfile.TemporaryDirectory() as directory:
                env = dict(synthetic, PATH=os.environ["PATH"])
                env.update({key: allowed[key] or directory for key in keep})
                process = self.launch(directory, "env", env=env)
                observed, output = self.finish(process)
                self.assertEqual(process.returncode, 0, output)
                names = json.loads((Path(directory) / "fake-env-names.json").read_text())
                self.assertEqual(names, sorted(["PATH", *keep]))
                self.assertTrue(set(names) <= set(ALLOWLIST))
                self.assertEqual([x for x in names if any(part in x for part in SECRET_PARTS)], [])

    def test_inherited_environment_never_reaches_child(self):
        # The test's own environment, by name only: the child sees the allowlisted subset.
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory, "env")
            observed, output = self.finish(process)
            self.assertEqual(process.returncode, 0, output)
            names = json.loads((Path(directory) / "fake-env-names.json").read_text())
            self.assertEqual(names, sorted(x for x in ALLOWLIST if x in os.environ))
            self.assertEqual([x for x in names if any(part in x for part in SECRET_PARTS)], [])

    def test_shutdown_kills_grandchild_that_ignores_sigterm(self):
        with tempfile.TemporaryDirectory() as directory:
            pid = None
            try:
                process = self.launch(directory, "grandchild")
                observed, output = self.finish(process)
                self.assertEqual(process.returncode, 0, output)
                self.assertEqual(observed[-1]["event"], "prompt-complete")
                pid = int((Path(directory) / "grandchild.pid").read_text())
                self.assertTrue(pid_gone(pid), "grandchild that ignores SIGTERM survived shutdown")
                pid = None
            finally:
                if pid is not None:
                    try:
                        os.kill(pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass

    def test_unexpected_exception_still_emits_fixed_failed_receipt(self):
        driver = "import importlib.util,sys\nspec=importlib.util.spec_from_file_location('acp',sys.argv[1])\nm=importlib.util.module_from_spec(spec)\nspec.loader.exec_module(m)\ndef boom(self,message): raise RuntimeError('PRIVATE_INTERNAL_DETAIL')\nm.Session.provider=boom\nraise SystemExit(m.main(sys.argv[2:]))\n"
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory, prefix=("-c", driver))
            observed, output = self.finish(process)
            self.assertEqual(process.returncode, 2)
            self.assertEqual(observed[-1], {"event": "failed", "reason": "internal-error"})
            self.assertNotIn("PRIVATE_INTERNAL_DETAIL", output)
            self.assertNotIn("Traceback", output)

    def test_deadline_error_and_oversize_terminate_without_raw_output(self):
        for mode, reason in [("hang", "deadline-exceeded"), ("error", "provider-error-prompt"), ("oversize", "invalid-frame")]:
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                process = self.launch(directory, mode, 5)
                stdout, stderr = process.communicate(timeout=20)
                self.assertEqual(process.returncode, 2)
                self.assertEqual(json.loads(stdout.splitlines()[-1]), {"event": "failed", "reason": reason})
                self.assertNotIn("PRIVATE_PROVIDER_ERROR", stdout + stderr)

    def test_undrained_stdout_does_not_extend_provider_lifetime(self):
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory, "flood", 5)
            try:
                # Keep stdin open and never drain stdout until the ownership deadline ends.
                process.wait(timeout=20)
                self.assertEqual(process.returncode, 2)
                pid_path = Path(directory) / "fake-provider.pid"
                self.assertTrue(pid_path.exists(), "fake provider never reached flood fixture")
                with self.assertRaises(ProcessLookupError):
                    os.kill(int(pid_path.read_text()), 0)
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait()
                process.communicate(timeout=5)

    def test_closed_coordinator_stops_pending_question(self):
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory)
            stdout, stderr = process.communicate(timeout=15)
            self.assertEqual(process.returncode, 2)
            self.assertEqual(json.loads(stdout.splitlines()[-1])["reason"], "decision-input-closed")


if __name__ == "__main__":
    unittest.main()
