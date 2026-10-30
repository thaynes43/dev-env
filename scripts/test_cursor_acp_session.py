import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest

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

    def test_frame_size_duplicate_fields_and_nonobjects(self):
        for raw in [b'{"id":1,"id":2}', b"[]", b"x" * (ACP.MAX_FRAME + 1), b"\xff", b'{"id":NaN}', ("[" * 2000 + "]" * 2000).encode()]:
            with self.assertRaises(ACP.Refusal):
                ACP.strict_object(raw)


FAKE = '''#!/usr/bin/env python3
import json,sys,time
mode=MODE

def write(value):
 print(json.dumps(value),flush=True)
for line in sys.stdin:
 msg=json.loads(line)
 method=msg.get("method")
 if method=="initialize":
  if mode=="hang": time.sleep(5)
  if mode=="oversize": print("x"*70000,flush=True); continue
  write({"jsonrpc":"2.0","id":msg["id"],"result":{"protocolVersion":1}})
 elif method=="session/new":
  write({"jsonrpc":"2.0","id":msg["id"],"result":{"sessionId":"fixture-session"}})
 elif method=="session/prompt":
  if mode=="flood":
   from pathlib import Path
   Path("fake-provider.pid").write_text(str(__import__("os").getpid()))
   for n in range(5):
    write({"jsonrpc":"2.0","id":40+n,"method":"cursor/ask_question","params":{"questions":[{"id":"q"+str(q),"prompt":"Fixture choice?","options":[{"id":"a"+str(o),"label":"A"*256} for o in range(14)]} for q in range(8)]}})
   continue
  if mode=="error": write({"jsonrpc":"2.0","id":3,"error":{"message":"PRIVATE_PROVIDER_ERROR"}}); continue
  write({"jsonrpc":"2.0","id":40,"method":"cursor/ask_question","params":{"questions":[{"id":"q1","prompt":"Fixture choice?","options":[{"id":"a","label":"First"}]}]}})
 elif msg.get("id")==40:
  write({"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}})
'''


class SubprocessTests(unittest.TestCase):
    def launch(self, directory, mode="question", timeout=2):
        client = Path(directory) / "fake-cursor"
        client.write_text(FAKE.replace("MODE", repr(mode)))
        client.chmod(0o700)
        prompt = Path(directory) / "prompt.txt"
        prompt.write_text("Synthetic offline task")
        return subprocess.Popen([sys.executable, str(Path(__file__).with_name("cursor-acp-session.py")), "run", "--client", str(client), "--worktree", directory, "--model", "fixture-model", "--prompt-file", str(prompt), "--timeout", str(timeout)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

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
                stdout, stderr = process.communicate(timeout=5)
                observed.extend(json.loads(x) for x in stdout.splitlines())
                self.assertEqual(process.returncode, 0, stderr)
                self.assertEqual([x["event"] for x in observed], ["starting", "session-created", "question-required", "decision-sent", "prompt-complete"])
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait()

    def test_deadline_error_and_oversize_terminate_without_raw_output(self):
        for mode, reason in [("hang", "deadline-exceeded"), ("error", "provider-error-prompt"), ("oversize", "invalid-frame")]:
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                process = self.launch(directory, mode, 1)
                stdout, stderr = process.communicate(timeout=5)
                self.assertEqual(process.returncode, 2)
                self.assertEqual(json.loads(stdout.splitlines()[-1]), {"event": "failed", "reason": reason})
                self.assertNotIn("PRIVATE_PROVIDER_ERROR", stdout + stderr)

    def test_undrained_stdout_does_not_extend_provider_lifetime(self):
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory, "flood", 1)
            try:
                # Keep stdin open and never drain stdout until the ownership deadline ends.
                process.wait(timeout=4)
                self.assertEqual(process.returncode, 2)
                pid_path = Path(directory) / "fake-provider.pid"
                self.assertTrue(pid_path.exists(), "fake provider never reached flood fixture")
                with self.assertRaises(ProcessLookupError):
                    os.kill(int(pid_path.read_text()), 0)
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait()
                process.communicate(timeout=2)

    def test_closed_coordinator_stops_pending_question(self):
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory)
            stdout, stderr = process.communicate(timeout=5)
            self.assertEqual(process.returncode, 2)
            self.assertEqual(json.loads(stdout.splitlines()[-1])["reason"], "decision-input-closed")


if __name__ == "__main__":
    unittest.main()
