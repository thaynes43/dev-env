import importlib.util
import json
import os
from pathlib import Path
import queue
import signal
import subprocess
import sys
import tempfile
import threading
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

    def test_context_binding_and_every_non_reject_option_never_sends(self):
        self.ready()
        for ident, tool in enumerate([VALID_CONTEXT, None, dict(VALID_CONTEXT, title="x" * 257)], 80):
            options = [{"optionId": kind, "kind": kind} for kind in ["allow_once", "allow_always", "reject_once", "reject_always"]]
            message = {"jsonrpc": "2.0", "id": ident, "method": "session/request_permission", "params": {"sessionId": "fixture-session", "toolCall": tool, "options": options}}
            before = list(self.sent)
            self.session.provider(message)
            self.assertEqual(self.sent, before, "request must not send any automatic decision")
            for option in ["allow_once", "allow_always", "reject_always", "missing"]:
                with self.assertRaisesRegex(ACP.Refusal, "approval-not-supported"):
                    self.session.decision({"requestID": ident, "sessionID": "fixture-session", "kind": "permission", "optionID": option})
                self.assertEqual(self.sent, before)
            for change in [{"requestID": str(ident)}, {"requestID": 999}, {"sessionID": "foreign"}, {"kind": "question"}]:
                decision = {"requestID": ident, "sessionID": "fixture-session", "kind": "permission", "optionID": "reject_once"}
                decision.update(change)
                with self.assertRaises(ACP.Refusal):
                    self.session.decision(decision)
                self.assertEqual(self.sent, before)
            self.session.decision({"requestID": ident, "sessionID": "fixture-session", "kind": "permission", "optionID": "reject_once"})
            self.assertEqual(self.sent[-1], {"jsonrpc": "2.0", "id": ident, "result": {"outcome": {"outcome": "selected", "optionId": "reject_once"}}})
            with self.assertRaisesRegex(ACP.Refusal, "stale"):
                self.session.decision({"requestID": ident, "sessionID": "fixture-session", "kind": "permission", "optionID": "reject_once"})
        before_events = list(self.events)
        with self.assertRaisesRegex(ACP.Refusal, "cross-session-request"):
            self.session.provider({"jsonrpc": "2.0", "id": 99, "method": "session/request_permission", "params": {"sessionId": "foreign", "toolCall": VALID_CONTEXT}})
        self.assertEqual(self.events, before_events)

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


def json_frame(size, name="synthetic"):
    """One valid JSON object frame of exactly `size` bytes, newline included."""
    prefix, suffix = ('{"' + name + '":"').encode(), b'"}\n'
    return prefix + b"x" * (size - len(prefix) - len(suffix)) + suffix


class FrameReaderTests(unittest.TestCase):
    LIMIT = 4 * 1024 * 1024
    COORDINATOR_LIMIT = 64 * 1024

    def read(self, raw, source="provider", budget=None):
        events = queue.Queue()
        with tempfile.TemporaryFile() as stream:
            stream.write(raw)
            stream.seek(0)
            ACP.read_frames(stream, events, source, budget)
            consumed = stream.tell()
        return list(events.queue), consumed

    def assertFailure(self, raw, detail, source="provider", **metadata):
        events, consumed = self.read(raw, source)
        self.assertEqual(len(events), 1)
        kind, error = events[0]
        self.assertEqual(kind, "failure")
        self.assertIsInstance(error, ACP.Refusal)
        self.assertEqual(str(error), "invalid-frame")
        origin = "provider" if source == "provider" else "coordinator"
        self.assertEqual(error.metadata, dict(detail=detail, source=origin, **metadata))
        return consumed

    def test_valid_provider_frame_just_under_and_at_new_bound(self):
        for size in [self.LIMIT - 1, self.LIMIT]:
            with self.subTest(size=size):
                # With nothing outstanding, a frame at the limit is admitted, never held.
                budget = ACP.ByteBudget(self.LIMIT)
                events, consumed = self.read(json_frame(size), budget=budget)
                self.assertEqual([source for source, _ in events], ["provider", "provider-closed"])
                self.assertEqual(len(events[0][1]["synthetic"]), size - len(b'{"synthetic":""}\n'))
                self.assertEqual(consumed, size)
                self.assertEqual(budget.used, size)

    def test_provider_frame_bound_plus_one_is_counted_without_draining(self):
        for ending in [b"\n", b"x", b"x" * 4096]:
            with self.subTest(ending_size=len(ending)):
                consumed = self.assertFailure(b"x" * self.LIMIT + ending, "frame-too-large",
                                             byte_count=self.LIMIT + 1, limit=self.LIMIT)
                self.assertEqual(consumed, self.LIMIT + 1)

    def test_malformed_json_has_metadata_only_detail(self):
        for raw in [b'{"PRIVATE_FRAME":}\n', b'\xff\n', b'{"n":NaN}\n', b'[]\n',
                    ("[" * 2000 + "]" * 2000 + "\n").encode()]:
            with self.subTest(size=len(raw)):
                self.assertFailure(raw, "frame-malformed-json")

    def test_duplicate_key_has_metadata_only_detail(self):
        self.assertFailure(b'{"nested":{"PRIVATE_KEY":1,"PRIVATE_KEY":2}}\n', "frame-duplicate-key")

    def test_truncated_eof_counts_bytes_without_frame_content(self):
        for raw in [b'{"PRIVATE_FRAME":', '{"synthetic":"\u00e9"}'.encode(), b"x" * self.LIMIT]:
            with self.subTest(size=len(raw)):
                self.assertFailure(raw, "frame-truncated-eof", byte_count=len(raw))

    def test_coordinator_frame_bound_is_64_kib_including_newline(self):
        limit = self.COORDINATOR_LIMIT
        self.assertEqual(ACP.MAX_FRAME, limit)
        events, consumed = self.read(json_frame(limit), "decision")
        self.assertEqual([source for source, _ in events], ["decision", "decision-closed"])
        self.assertEqual(consumed, limit)
        # Valid JSON one byte over: refused for size alone, and nothing past it is read.
        consumed = self.assertFailure(json_frame(limit + 1) + json_frame(limit), "frame-too-large", "decision",
                                      byte_count=limit + 1, limit=limit)
        self.assertEqual(consumed, limit + 1)
        for raw, detail, metadata in [(b'{"PRIVATE_FRAME":}\n', "frame-malformed-json", {}),
                                      (b'{"PRIVATE_FRAME":', "frame-truncated-eof", {"byte_count": 17})]:
            with self.subTest(detail=detail):
                self.assertFailure(raw, detail, "decision", **metadata)

    def test_provider_budget_holds_reader_until_frames_are_handled(self):
        # Any two of these frames exceed the budget, so each waits for the one before.
        size = self.LIMIT // 2 + 1
        raw = b"".join(json_frame(size, name) for name in "abc")
        events, budget = queue.Queue(), ACP.ByteBudget(self.LIMIT)

        def held():
            """Wait until one frame is admitted and the reader waits for budget."""
            end = time.monotonic() + 10
            while time.monotonic() < end:
                with budget.condition:
                    if budget.waiting and budget.used:
                        return budget.used
                time.sleep(0.01)
            self.fail("reader never waited for budget")

        def handle():
            source, value = events.get_nowait()
            budget.release_next()
            return source, list(value)

        with tempfile.TemporaryFile() as stream:
            stream.write(raw)
            stream.seek(0)
            reader = threading.Thread(target=ACP.read_frames, args=(stream, events, "provider", budget), daemon=True)
            reader.start()
            try:
                # No consumer: after a short wait exactly one parsed frame is queued,
                # and the reader has stopped short of the rest of the stream.
                self.assertEqual(held(), size)
                time.sleep(0.2)
                self.assertEqual(events.qsize(), 1)
                self.assertEqual(budget.used, size)
                read = os.lseek(stream.fileno(), 0, os.SEEK_CUR)
                self.assertLess(read, len(raw))
                self.assertLessEqual(read, 2 * size + 4096)
                self.assertEqual(handle(), ("provider", ["a"]))
                # Handling the first admits the second; the third waits, so EOF is unseen.
                self.assertEqual(held(), size)
                time.sleep(0.2)
                self.assertEqual(events.qsize(), 1)
                self.assertEqual(handle(), ("provider", ["b"]))
                reader.join(timeout=10)
                self.assertFalse(reader.is_alive())
                self.assertEqual([(source, list(value or ())) for source, value in events.queue],
                                 [("provider", ["c"]), ("provider-closed", [])])
                self.assertEqual(budget.used, size)
                self.assertEqual(os.lseek(stream.fileno(), 0, os.SEEK_CUR), len(raw))
            finally:
                with budget.condition:
                    budget.limit = float("inf")  # A failed assertion must not strand the reader.
                    budget.condition.notify_all()
                reader.join(timeout=10)


VALID_CONTEXT = {
    "toolCallId": "fixture-call", "kind": "execute", "title": "Run fixture checks",
    "locations": [{"path": "/fixture-parent/project/check.py", "line": 12}],
    "rawInput": {"command": "python3", "args": ["check.py"], "path": "/fixture-parent/project/check.py"},
}
# Synthetic secret-shaped values; none is a real credential.
URL_SECRET = "https://admin:S3cr3tVal@example.com"
CONTEXT_FIXTURES = {
    "valid": VALID_CONTEXT,
    "missing": None,
    "oversized": dict(VALID_CONTEXT, title="x" * 257),
    "total": dict(VALID_CONTEXT, rawInput={"args": ["short words " * 20] * 8}),
    "raw-oversized": dict(VALID_CONTEXT, rawInput={"command": "x" * 257}),
    "source-oversized": dict(VALID_CONTEXT, unknown="x" * 4097),
    "raw-unknown": dict(VALID_CONTEXT, rawInput={"unknown": "DO_NOT_EXPORT"}),
    "raw-null": dict(VALID_CONTEXT, rawInput=None),
    "raw-scalar": dict(VALID_CONTEXT, rawInput=42),
    "location-control": dict(VALID_CONTEXT, locations=[{"path": "/fixture/\x7ffile.py"}]),
    "raw-control": dict(VALID_CONTEXT, rawInput={"command": "run\nfixture"}),
    "spaced-path-args": dict(VALID_CONTEXT, rawInput={"args": ["/a/customer b/f.py"]}),
    "spaced-path-command": dict(VALID_CONTEXT, rawInput={"command": 'python3 "/a/customer b/f.py"'}),
    "spaced-path-title": dict(VALID_CONTEXT, title='Read "/a/customer b/f.py"'),
    "explicit-spaced-path": dict(VALID_CONTEXT, rawInput={"path": "/a/customer b/f.py"}),
    "quoted-path-args": dict(VALID_CONTEXT, rawInput={"args": ["/a/customer'b/f.py"]}),
    "double-quoted-path-args": dict(VALID_CONTEXT, rawInput={"args": ['/a/customer"b/f.py']}),
    "angle-path-args": dict(VALID_CONTEXT, rawInput={"args": ["/a/customer<b/f.py"]}),
    "explicit-quoted-path": dict(VALID_CONTEXT, rawInput={"path": "/a/customer'b/f.py"}),
    "controls": dict(VALID_CONTEXT, title="Run\x1b[31m fixture"),
    "unicode-control": dict(VALID_CONTEXT, title="Run\u202e fixture"),
    "malformed": dict(VALID_CONTEXT, locations="check.py"),
    "unknown-kind": dict(VALID_CONTEXT, kind="invented"),
    "unknown-only": {"unrecognized": "DO_NOT_EXPORT"},
    "title-secret": dict(VALID_CONTEXT, title="Use Bearer synthetic-sensitive-value"),
    "raw-secret": dict(VALID_CONTEXT, rawInput={"command": "run", "args": ["--password=synthetic-sensitive-value"]}),
    "key-secret": dict(VALID_CONTEXT, rawInput={"command": {"apiToken": "synthetic-sensitive-value"}}),
    "hex-secret": dict(VALID_CONTEXT, rawInput={"command": "ab" * 32}),
    "base64-secret": dict(VALID_CONTEXT, rawInput={"command": "QWxhZGRpbjpvcGVuIHNlc2FtZUZpeHR1cmU="}),
    "unknown-fields": dict(VALID_CONTEXT, content="DO_NOT_EXPORT", rawOutput="DO_NOT_EXPORT", _meta={"command": "DO_NOT_EXPORT"}, rawInput={"command": "python3", "unknown": "DO_NOT_EXPORT"}),
    # Review of 6b9ae3d7, finding 1: URL userinfo survived basename reduction.
    "secret-url-args": dict(VALID_CONTEXT, rawInput={"command": "git", "args": ["clone", URL_SECRET]}),
    "secret-url-slash-args": dict(VALID_CONTEXT, rawInput={"command": "git", "args": ["clone", URL_SECRET + "/"]}),
    "secret-url-path": dict(VALID_CONTEXT, rawInput={"path": URL_SECRET}),
    "secret-url-location": dict(VALID_CONTEXT, locations=[{"path": URL_SECRET + "/"}]),
    "secret-url-title": dict(VALID_CONTEXT, title=URL_SECRET),
    "secret-url-username": dict(VALID_CONTEXT, rawInput={"args": ["https://S3cr3tVal@example.com"]}),
    "secret-bare-userinfo": dict(VALID_CONTEXT, rawInput={"args": ["admin:S3cr3tVal@example.com"]}),
    # Finding 2: short secrets with no keyword.
    "secret-passwd": dict(VALID_CONTEXT, rawInput={"args": ["--passwd=hunter2"]}),
    "secret-attached-p": dict(VALID_CONTEXT, rawInput={"command": "mysql", "args": ["-pS3cr3tVal"]}),
    "secret-curl-user-args": dict(VALID_CONTEXT, rawInput={"command": "curl", "args": ["-u", "admin:S3cr3tVal"]}),
    "secret-curl-user-command": dict(VALID_CONTEXT, rawInput={"command": "curl -u admin:S3cr3tVal"}),
    "secret-json-pw": dict(VALID_CONTEXT, rawInput={"command": 'curl -d \'{"pw":"hunter2"}\''}),
    "secret-split-password": dict(VALID_CONTEXT, rawInput={"args": ["--password", "hunter2"]}),
    # Optional hardening: percent-encoded and compatibility (fullwidth) forms.
    "secret-percent-userinfo": dict(VALID_CONTEXT, rawInput={"args": ["https://admin%3AS3cr3tVal%40example.com"]}),
    "secret-fullwidth-userinfo": dict(VALID_CONTEXT, rawInput={"args": ["https://admin\uff1aS3cr3tVal\uff20example.com"]}),
    "secret-fullwidth-name": dict(VALID_CONTEXT, title="\uff50\uff41\uff53\uff53\uff57\uff4f\uff52\uff44 hunter2"),
    "fullwidth-slash-args": dict(VALID_CONTEXT, rawInput={"args": ["/a/customer\uff0fb\uff0ff.py"]}),
    "combining-mark": dict(VALID_CONTEXT, title="Run\u0336 fixture checks"),
    "line-separator": dict(VALID_CONTEXT, title="Run\u2028fixture checks"),
    "paragraph-separator": dict(VALID_CONTEXT, rawInput={"command": "run\u2029fixture"}),
    # Re-review of 5792587b: underscore-joined names, short value flags, names split
    # across fields, and userinfo whose password holds a path character.
    "secret-db-pass": dict(VALID_CONTEXT, rawInput={"command": "env", "args": ["DB_PASS=hunter2"]}),
    "secret-zip-p": dict(VALID_CONTEXT, rawInput={"command": "zip", "args": ["-P", "hunter2", "out.zip"]}),
    "secret-openssl-k": dict(VALID_CONTEXT, rawInput={"command": "openssl enc -aes-256-cbc -k hunter2"}),
    "secret-redis-a": dict(VALID_CONTEXT, rawInput={"command": "redis-cli", "args": ["-a", "hunter2"]}),
    "secret-glued-name": dict(VALID_CONTEXT, rawInput={"command": "tool", "args": ["--pa", "ssword=hunter2"]}),
    "secret-url-slash-password": dict(VALID_CONTEXT, rawInput={"args": ["https://admin:hun/ter2@example.com"]}),
    # Unsettled percent-encoding and non-ASCII text fail closed without a redacted shape.
    "deep-percent-name": dict(VALID_CONTEXT, rawInput={"args": ["%252525252570assword=hunter2"]}),
    "deep-percent-userinfo": dict(VALID_CONTEXT, rawInput={"args": ["alice:hunter2%2525252540example.com"]}),
    "homoglyph-cyrillic": dict(VALID_CONTEXT, rawInput={"args": ["\u0440\u0430ssword=hunter2"]}),
    "homoglyph-filler": dict(VALID_CONTEXT, rawInput={"args": ["pass\u3164word=hunter2"]}),
    "non-ascii-title": dict(VALID_CONTEXT, title="Caf\u00e9 checks"),
}



def redacted_context(tool):
    """The one fail-closed shape after any redaction: kind kept, no projected text."""
    expected = {"state": "insufficient-context", "kind": tool["kind"], "title": "[REDACTED]"}
    if tool.get("rawInput") is not None:
        expected["rawInput"] = "[REDACTED]"
    return expected


class PermissionContextTests(unittest.TestCase):
    """Direct projection checks for the independent review of 6b9ae3d7."""

    def assertRedacted(self, tool, *secrets):
        context = ACP.permission_context(tool)
        self.assertEqual(context, redacted_context(tool))
        for secret in secrets:
            self.assertNotIn(secret, json.dumps(context, ensure_ascii=False))

    def test_url_userinfo_redacted_before_path_reduction(self):
        values = [URL_SECRET, URL_SECRET + "/", URL_SECRET + ":8443", "ssh://admin:S3cr3tVal@example.com/repo",
                  "https://S3cr3tVal@example.com", "//admin:S3cr3tVal@example.com", "admin:S3cr3tVal@example.com",
                  "https://admin:p@ss@example.com"]
        for value in values:
            for tool in [dict(VALID_CONTEXT, rawInput={"args": [value]}), dict(VALID_CONTEXT, rawInput={"path": value}),
                         dict(VALID_CONTEXT, rawInput={"paths": [value]}), dict(VALID_CONTEXT, locations=[{"path": value}]),
                         dict(VALID_CONTEXT, title=value)]:
                with self.subTest(value=value, tool=tool):
                    self.assertRedacted(tool, "S3cr3tVal", "p@ss")

    def test_short_secrets_without_old_keywords_redacted(self):
        arguments = [["--passwd=hunter2"], ["--passphrase", "hunter2"], ["--pass", "hunter2"], ["pwd=hunter2"],
                     ['{"pw":"hunter2"}'], ["--session", "hunter2"], ["--signature=hunter2"], ["--private", "hunter2"],
                     ["jwt=hunter2"], ["--bearer=hunter2"], ["--password", "hunter2"], ["-pS3cr3tVal"], ["-p", "hunter2"],
                     ["-u", "admin:S3cr3tVal"], ["--user=admin:S3cr3tVal"], ["--user", "admin:S3cr3tVal"],
                     ["--proxy-user", "admin:S3cr3tVal"], ["-U", "admin%S3cr3tVal"]]
        for args in arguments:
            with self.subTest(args=args):
                self.assertRedacted(dict(VALID_CONTEXT, rawInput={"command": "tool", "args": args}), "hunter2", "S3cr3tVal")
        for command in ["curl -u admin:S3cr3tVal", "curl --user=admin:S3cr3tVal", "mysql -pS3cr3tVal", "sshpass -p hunter2 ssh",
                        "curl -d '{\"pw\":\"hunter2\"}'", "tool --passwd hunter2"]:
            with self.subTest(command=command):
                self.assertRedacted(dict(VALID_CONTEXT, rawInput={"command": command}), "hunter2", "S3cr3tVal")
                self.assertRedacted(dict(VALID_CONTEXT, title=command), "hunter2", "S3cr3tVal")

    def test_short_flag_redaction_prefers_false_positives(self):
        # `-p` with any value is treated as a password argument, so harmless
        # forms fail closed too. The coordinator can still reject.
        for args in [["mkdir", "-p", "build"], ["patch", "-p1"]]:
            with self.subTest(args=args):
                self.assertRedacted(dict(VALID_CONTEXT, rawInput={"args": args}))

    def test_flag_and_value_split_across_strings_redacted(self):
        for tool in [dict(VALID_CONTEXT, title="Run curl -u", rawInput={"args": ["admin:S3cr3tVal"]}),
                     dict(VALID_CONTEXT, rawInput={"command": "curl -u", "args": ["admin:S3cr3tVal"]}),
                     dict(VALID_CONTEXT, rawInput={"command": "sshpass", "description": "-p", "args": ["hunter2"]}),
                     dict(VALID_CONTEXT, rawInput={"command": "curl", "args": ["-u", "admin:S3cr3tVal"]})]:
            with self.subTest(tool=tool):
                self.assertRedacted(tool, "hunter2", "S3cr3tVal")

    def test_any_redaction_fails_whole_context_closed(self):
        # Before the fix a redacted rawInput still reported "available" and the
        # title, locations and other raw fields were still projected beside it.
        for tool in [dict(VALID_CONTEXT, rawInput={"command": "python3", "args": ["--token=synthetic"]}),
                     dict(VALID_CONTEXT, rawInput={"command": {"apiToken": "synthetic"}}),
                     dict(VALID_CONTEXT, rawInput={"command": "python3", "args": ["ab" * 32]}),
                     dict(VALID_CONTEXT, locations=[{"path": "/fixture/secret/check.py"}]),
                     dict(VALID_CONTEXT, title="Use Bearer synthetic"),
                     dict(VALID_CONTEXT, title="Use Bearer synthetic", rawInput=None)]:
            with self.subTest(tool=tool):
                context = ACP.permission_context(tool)
                self.assertEqual(context, redacted_context(tool))
                self.assertNotIn("locations", context)
                self.assertNotIn("check.py", json.dumps(context))

    def test_encoded_and_compatibility_forms_cannot_hide_secrets(self):
        for value in ["https://admin%3AS3cr3tVal%40example.com", "https%3A%2F%2Fadmin%253AS3cr3tVal%2540example.com",
                      "--pass%77ord=S3cr3tVal", "https://admin：S3cr3tVal＠example.com",
                      "ｐａｓｓｗｏｒｄ=S3cr3tVal", "－ｐS3cr3tVal"]:
            with self.subTest(value=value):
                self.assertRedacted(dict(VALID_CONTEXT, rawInput={"args": [value]}), "S3cr3tVal")
                self.assertRedacted(dict(VALID_CONTEXT, title=value), "S3cr3tVal")

    def test_compatibility_separators_cannot_keep_parent_fragments(self):
        for raw in [{"args": ["/a/customer／b／f.py"]}, {"path": "/a/customer／b／f.py"}, {"paths": ["\\a\\customer＼b＼f.py"]}]:
            with self.subTest(raw=raw):
                context = ACP.permission_context(dict(VALID_CONTEXT, rawInput=raw))
                self.assertEqual(context["state"], "available")
                self.assertEqual(list(context["rawInput"].values()), [["f.py"]] if "path" not in raw else ["f.py"])
                self.assertNotIn("customer", json.dumps(context, ensure_ascii=False))
        # Non-ASCII display text fails closed by design, even with no secret: the
        # decomposed accent composes under NFKC and is still not printable ASCII.
        self.assertEqual(ACP.permission_context(dict(VALID_CONTEXT, title="Cafe\u0301 checks")), {"state": "insufficient-context"})

    def test_combining_marks_and_line_separators_rejected(self):
        for value in ["Run̶ fixture", "Run⃝ fixture", "é́", "Run fixture", "Run fixture"]:
            for tool in [dict(VALID_CONTEXT, title=value), dict(VALID_CONTEXT, rawInput={"command": value}),
                         dict(VALID_CONTEXT, locations=[{"path": "/fixture/" + value}])]:
                with self.subTest(value=value, tool=tool):
                    self.assertEqual(ACP.permission_context(tool), {"state": "insufficient-context"})

    def test_benign_context_stays_available(self):
        # No-false-positive guard for the new patterns; it passes before and after.
        for raw in [{"command": "git", "args": ["push", "-u", "origin", "main"]}, {"command": "ssh", "args": ["git@github.com"]},
                    {"command": "python3", "args": ["-m", "unittest"]}, {"query": "user@example.com"}]:
            with self.subTest(raw=raw):
                context = ACP.permission_context(dict(VALID_CONTEXT, rawInput=raw))
                self.assertEqual((context["state"], context["rawInput"]), ("available", raw))

    def assertClosed(self, tool, *secrets):
        """Any insufficient-context outcome, redacted shape or not, with no secret text."""
        context = ACP.permission_context(tool)
        self.assertIn(context, [{"state": "insufficient-context"}, redacted_context(tool)])
        for secret in secrets:
            self.assertNotIn(secret, json.dumps(context, ensure_ascii=False))

    def placements(self, value):
        return [dict(VALID_CONTEXT, rawInput={"command": "tool", "args": [value]}), dict(VALID_CONTEXT, rawInput={"command": value}),
                dict(VALID_CONTEXT, rawInput={"path": value}), dict(VALID_CONTEXT, locations=[{"path": value}]),
                dict(VALID_CONTEXT, title=value)]

    def test_underscore_and_camel_joined_short_names_redacted(self):
        # Re-review of 5792587b, gap 1: `\bpass\b` treated `_` as a word character.
        # The other short names were already unanchored substrings; they stay so.
        for value in ["DB_PASS=hunter2", "SMTP_PASS=hunter2", "PASS_DB=hunter2", "PASS1=hunter2", "--db-pass=hunter2",
                      "dbPass=hunter2", '{"smtpPass":"hunter2"}', "DB_PW=hunter2", "X_PWD=hunter2", "API_KEY=hunter2",
                      "GH_TOKEN=hunter2", "MY_AUTH=hunter2", "apiKey=hunter2", "apiToken=hunter2", "OLD_SECRET=hunter2"]:
            for tool in self.placements(value):
                with self.subTest(value=value, tool=tool):
                    self.assertRedacted(tool, "hunter2")

    def test_pass_inside_words_stays_available(self):
        for value in ["compass", "bypass", "BYPASS", "Compass", "passport", "passenger.py", "overpass"]:
            with self.subTest(value=value):
                context = ACP.permission_context(dict(VALID_CONTEXT, rawInput={"command": "tool", "args": [value]}))
                self.assertEqual((context["state"], context["rawInput"]), ("available", {"command": "tool", "args": [value]}))

    def test_unsettled_percent_encoding_fails_closed(self):
        # Gap 2: four rounds left deeper layers undecoded but the context "available".
        for value in ["%252525252570assword=hunter2", "alice:hunter2%2525252540example.com", "%25252570assword=hunter2"]:
            for tool in self.placements(value):
                with self.subTest(value=value, tool=tool):
                    self.assertEqual(ACP.permission_context(tool), {"state": "insufficient-context"})
        # Three layers still settle within the bound and are redacted by name.
        self.assertRedacted(dict(VALID_CONTEXT, rawInput={"args": ["%252570assword=hunter2"]}), "hunter2")

    def test_non_ascii_context_fails_closed(self):
        # Gap 3: homoglyphs (Cyrillic, Hangul filler U+3164) and any other text that
        # is not printable ASCII after NFKC fail closed, with or without a secret.
        for value in ["\u0440\u0430ssword=hunter2", "pass\u3164word=hunter2", "\u0440ass=hunter2", "Caf\u00e9 hunter2",
                      "%D1%80assword=hunter2", "pass%E3%85%A4word=hunter2", "caf%C3%A9=hunter2"]:
            for tool in self.placements(value):
                with self.subTest(value=value, tool=tool):
                    self.assertClosed(tool, "hunter2")
        # Non-ASCII in a dropped field or an object key fails the whole context closed.
        for raw in [{"command": "tool", "note": "\u0440\u0430ssword", "args": ["hunter2"]},
                    {"command": "tool", "\u0440\u0430ss": "x", "args": ["hunter2"]}]:
            with self.subTest(raw=raw):
                self.assertClosed(dict(VALID_CONTEXT, rawInput=raw), "hunter2")

    def test_short_value_flags_redacted(self):
        # Gap 4: zip -P, openssl enc -k/-K and redis-cli -a take a password value.
        for args in [["zip", "-P", "hunter2", "out.zip"], ["zip", "-Phunter2", "out.zip"],
                     ["openssl", "enc", "-aes-256-cbc", "-k", "hunter2"], ["openssl", "enc", "-K", "00ff00ff"],
                     ["redis-cli", "-h", "cache", "-a", "hunter2"], ["redis-cli", "-a", '"hunter2"']]:
            with self.subTest(args=args):
                self.assertRedacted(dict(VALID_CONTEXT, rawInput={"args": args}), "hunter2", "00ff00ff")
                self.assertRedacted(dict(VALID_CONTEXT, rawInput={"command": " ".join(args)}), "hunter2", "00ff00ff")
        self.assertRedacted(dict(VALID_CONTEXT, title="Run redis-cli -a", rawInput={"args": ["hunter2"]}), "hunter2")
        # Conservative: a spaced value after -k, -K or -a fails closed even when harmless.
        for command in ["curl -k https://example.com", "ls -a build", "grep -P x"]:
            with self.subTest(command=command):
                self.assertRedacted(dict(VALID_CONTEXT, rawInput={"command": command}))
        # Attached flag clusters and a following flag stay available.
        for command in ["ls -al", "curl -kL example.com", "git commit -a -m fixture"]:
            with self.subTest(command=command):
                self.assertEqual(ACP.permission_context(dict(VALID_CONTEXT, rawInput={"command": command}))["state"], "available")

    def test_names_and_userinfo_split_across_fields_redacted(self):
        # Optional finding 5: the fields are also checked concatenated with no separator.
        for raw in [{"command": "tool", "args": ["--pa", "ssword=hunter2"]}, {"command": "tool", "args": ["DB_PA", "SS=hunter2"]},
                    {"command": "curl", "args": ["https://admin:", "hunter2@example.com"]}]:
            with self.subTest(raw=raw):
                self.assertRedacted(dict(VALID_CONTEXT, rawInput=raw), "hunter2")
        # Conservative: gluing can also form a short name by accident (`cp` + `www`).
        self.assertRedacted(dict(VALID_CONTEXT, rawInput={"command": "cp", "args": ["www", "out"]}))

    def test_url_userinfo_with_path_characters_redacted(self):
        # Optional finding 6: a password holding `/`, `?` or `#` kept its tail visible.
        for value in ["https://admin:hun/ter2@example.com", "https://admin:hun?ter2@example.com",
                      "https://admin:hun#ter2@example.com", "https://hun/ter2@example.com"]:
            for tool in self.placements(value):
                with self.subTest(value=value, tool=tool):
                    self.assertRedacted(tool, "ter2")
        # Conservative: any `@` after `://` in the same token fails closed.
        self.assertRedacted(dict(VALID_CONTEXT, rawInput={"query": "https://example.com/a?mail=a@example.com"}))

    def test_permission_context_has_no_hidden_memory(self):
        secret = dict(VALID_CONTEXT, title="Use Bearer synthetic")
        self.assertEqual(ACP.permission_context(secret)["title"], "[REDACTED]")
        context = ACP.permission_context(VALID_CONTEXT)
        self.assertEqual(context["state"], "available")
        self.assertEqual(context["title"], "Run fixture checks")
        self.assertNotIn("Bearer", json.dumps(context))

    def test_structured_relative_argv_stays_visible(self):
        for args in (["test", "./..."], ["test", "./internal/..."]):
            tool = dict(VALID_CONTEXT, rawInput={"command": "go", "args": args})
            context = ACP.permission_context(tool)
            self.assertEqual(context["state"], "available")
            self.assertEqual(context["rawInput"], {"command": "go", "args": args})
            self.assertEqual(context["locations"], [{"path": "check.py", "line": 12}])
        absolute = ACP.permission_context(dict(VALID_CONTEXT, rawInput={"args": ["/a/customer/b/f.py"]}))
        self.assertEqual(absolute["rawInput"], {"args": ["f.py"]})
        self.assertNotIn("customer", json.dumps(absolute))

    def test_unstructured_command_text_is_not_inferred(self):
        for tool in [dict(VALID_CONTEXT, rawInput={"command": "go test ./..."}), dict(VALID_CONTEXT, title="go test ./...")]:
            context = ACP.permission_context(tool)
            self.assertEqual(context, {"state": "insufficient-context"})
            encoded = json.dumps(context)
            self.assertNotIn("go test", encoded)
            self.assertNotIn("./...", encoded)
            self.assertNotIn("python3", encoded)

    def test_shell_dynamic_and_foreign_argv_fail_closed(self):
        samples = [
            {"command": "go", "args": ["test", "./...;rm"]},
            {"command": "go", "args": ["test", "$(pwd)"]},
            {"command": "go", "args": ["test", "`id`"]},
            {"command": "go", "args": ["test", "./...", "&&"]},
            {"command": "go", "args": ["../secret"]},
            {"command": "go", "args": ["~/secret"]},
            {"command": "sh", "args": ["-c", "go test ./..."]},
            {"command": "go test $(pwd)"},
            {"command": "go", "args": ["test", "./...", "DB_PASS=hunter2"]},
            {"args": ["/private/proj/x"]},
        ]
        for raw in samples:
            context = ACP.permission_context(dict(VALID_CONTEXT, rawInput=raw))
            encoded = json.dumps(context, ensure_ascii=False)
            self.assertIn(context["state"], ("insufficient-context",))
            for hidden in ("hunter2", "./...", "secret", "private", "$(pwd)", "`id`"):
                self.assertNotIn(hidden, encoded)

    def test_nested_dynamic_syntax_and_backslash_parents_fail_closed(self):
        # Nested args/command values keep the shell and ".." checks. A backslash
        # parent segment must not fall through to a basename and look available.
        samples = [
            {"args": ["..\\parent\\file.py"]},
            {"args": ["foo\\..\\bar.py"]},
            {"args": ["foo/..\\bar.py"]},
            {"command": "go", "args": ["..\\parent\\file.py"]},
            {"command": "..\\parent\\file.py"},
            {"args": {"description": "echo;rm"}},
            {"args": {"description": "$(id)"}},
            {"command": {"description": "echo;rm"}},
            {"args": [{"description": "echo;rm"}]},
            {"args": {"description": "go test ./..."}},
        ]
        for raw in samples:
            context = ACP.permission_context(dict(VALID_CONTEXT, rawInput=raw))
            encoded = json.dumps(context)
            self.assertEqual(context["state"], "insufficient-context", raw)
            for hidden in ("parent", "file.py", "bar.py", "echo", "go test", "./...", "$(id)"):
                self.assertNotIn(hidden, encoded, raw)
        kept = ACP.permission_context(dict(VALID_CONTEXT, rawInput={"args": {"description": "check.py"}}))
        self.assertEqual(kept["state"], "available")
        self.assertEqual(kept["rawInput"], {"args": {"description": "check.py"}})
        relative = ACP.permission_context(dict(VALID_CONTEXT, rawInput={"command": "go", "args": ["test", "./..."]}))
        self.assertEqual(relative["rawInput"], {"command": "go", "args": ["test", "./..."]})
        nested_arg = ACP.permission_context(dict(VALID_CONTEXT, rawInput={"args": {"command": "./internal/..."}}))
        self.assertEqual(nested_arg["rawInput"], {"args": {"command": "./internal/..."}})
        # A command string is not an argv position, so the relative pattern reduces.
        command_only = ACP.permission_context(dict(VALID_CONTEXT, rawInput={"command": "./..."}))
        self.assertEqual(command_only["rawInput"], {"command": "..."})
        # Explicit path fields still reduce, including a backslash parent.
        reduced = ACP.permission_context(dict(VALID_CONTEXT, rawInput={"path": "..\\other\\file.py"}))
        self.assertEqual(reduced["state"], "available")
        self.assertEqual(reduced["rawInput"], {"path": "file.py"})
        self.assertNotIn("other", json.dumps(reduced))

    def test_nested_path_keeps_inherited_argv_and_command_checks(self):
        # path=True still basename-reduces. It must not clear argv/command checks
        # inherited from an enclosing args or command field.
        samples = [
            {"args": [{"path": "$(id)"}]},
            {"args": [{"path": "`id`"}]},
            {"args": [{"paths": ["echo;rm"]}]},
            {"args": [{"path": "../other"}]},
            {"args": [{"path": "..\\parent\\file.py"}]},
            {"command": {"paths": ["$(id)"]}},
            {"command": {"paths": ["echo;rm"]}},
            {"command": [{"path": "~/other"}]},
            {"command": {"path": "foo/../bar.py"}},
        ]
        for raw in samples:
            with self.subTest(raw=raw):
                with self.assertRaises(ValueError):
                    ACP.context_input(raw)
                context = ACP.permission_context(dict(VALID_CONTEXT, rawInput=raw))
                encoded = json.dumps(context)
                self.assertEqual(context, {"state": "insufficient-context"})
                for hidden in ("$(id)", "`id`", "echo", "other", "parent", "file.py", "bar.py"):
                    self.assertNotIn(hidden, encoded)
        self.assertEqual(ACP.context_input({"path": "..\\other\\file.py"}), {"path": "file.py"})
        self.assertEqual(ACP.context_input({"path": "/var/fixture/check.py"}), {"path": "check.py"})
        self.assertEqual(ACP.context_input({"args": [{"path": "/var/fixture/check.py"}]}), {"args": [{"path": "check.py"}]})
        outside = ACP.permission_context(dict(VALID_CONTEXT, rawInput={"path": "/var/fixture/check.py"}))
        self.assertEqual(outside["state"], "available")
        self.assertEqual(outside["rawInput"], {"path": "check.py"})
        self.assertNotIn("/var/fixture", json.dumps(outside))
        nested = ACP.permission_context(dict(VALID_CONTEXT, rawInput={"args": [{"path": "/var/fixture/check.py"}]}))
        self.assertEqual(nested["state"], "available")
        self.assertEqual(nested["rawInput"], {"args": [{"path": "check.py"}]})

    def test_adversarial_source_is_bounded_in_time(self):
        # A guard against quadratic patterns on a 4096-byte source, with 10x headroom
        # over the recorded 100 ms budget so a loaded worker does not flake it.
        def filled(unit):
            tool = dict(VALID_CONTEXT, rawInput={"command": "python3", "args": ["check.py"], "note": ""})
            room = 4096 - len(json.dumps(tool, ensure_ascii=True, separators=(",", ":")))
            tool["rawInput"]["note"] = (unit * room)[:room]
            self.assertEqual(len(json.dumps(tool, ensure_ascii=True, separators=(",", ":"))), 4096)
            return tool
        for unit in [":a", "a:", "a::", "a:.", "://", "://.", "=-u.", " -a.", ".", "%25.", "a" * 31 + "."]:
            tool = filled(unit)
            with self.subTest(unit=unit):
                self.assertLess(min(self.timed(tool) for _ in range(3)), 1.0)

    def timed(self, tool):
        start = time.perf_counter()
        ACP.permission_context(tool)
        return time.perf_counter() - start


class ManualClock:
    def __init__(self):
        self.now = 0.0

    def __call__(self):
        return self.now


class ToolCacheTests(unittest.TestCase):
    """Same-session tool_call correlation. Reject-once and question replies stay unchanged."""

    def setUp(self):
        self.sent, self.events = [], []
        self.clock = ManualClock()
        self.session = ACP.Session(self.sent.append, self.events.append, "/synthetic/project", "Inspect fixture", "grok-synthetic", clock=self.clock)
        self.session.start()

    def ready(self):
        self.session.provider({"jsonrpc": "2.0", "id": 1, "result": {"protocolVersion": 1}})
        self.session.provider({"jsonrpc": "2.0", "id": 2, "result": {"sessionId": "fixture-session"}})

    def announce(self, tool_id="fixture_1", kind="execute", title="Run fixture checks", raw=None, status="pending", session_update="tool_call", session_id="fixture-session", extra=None):
        update = {"sessionUpdate": session_update, "toolCallId": tool_id, "status": status}
        if kind is not None:
            update["kind"] = kind
        if title is not None:
            update["title"] = title
        if raw is not None:
            update["rawInput"] = raw
        if extra:
            update.update(extra)
        before = list(self.events)
        self.session.provider({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": session_id, "update": update}})
        self.assertEqual(self.events, before)

    def permit(self, ident, tool):
        options = [{"optionId": kind, "kind": kind} for kind in ["allow_once", "allow_always", "reject_once", "reject_always"]]
        before = list(self.sent)
        self.session.provider({"jsonrpc": "2.0", "id": ident, "method": "session/request_permission", "params": {"sessionId": "fixture-session", "toolCall": tool, "options": options}})
        self.assertEqual(self.sent, before)
        self.assertEqual(self.events[-1]["event"], "permission-required")
        self.assertEqual(self.events[-1]["requestID"], ident)
        self.assertEqual(self.events[-1]["sessionID"], "fixture-session")
        return self.events[-1]

    def assert_reject_only(self, ident):
        before = list(self.sent)
        for option in ("allow_once", "allow_always", "reject_always", "missing"):
            with self.assertRaisesRegex(ACP.Refusal, "approval-not-supported"):
                self.session.decision({"requestID": ident, "sessionID": "fixture-session", "kind": "permission", "optionID": option})
            self.assertEqual(self.sent, before)
        self.session.decision({"requestID": ident, "sessionID": "fixture-session", "kind": "permission", "optionID": "reject_once"})
        self.assertEqual(self.sent[-1], {"jsonrpc": "2.0", "id": ident, "result": {"outcome": {"outcome": "selected", "optionId": "reject_once"}}})

    def test_public_partial_id_uses_same_session_tool_call(self):
        self.ready()
        raw = {"command": "go", "args": ["test", "./..."]}
        self.announce(raw=raw)
        event = self.permit(41, {"toolCallId": "fixture_1"})
        self.assertEqual(event["context"], {"state": "available", "kind": "execute", "title": "Run fixture checks", "rawInput": raw})
        self.assertNotIn("./...", json.dumps(self.sent))
        self.assert_reject_only(41)

    def test_explicit_tool_call_matches_direct_projection_with_or_without_cache(self):
        self.ready()
        direct = ACP.permission_context(VALID_CONTEXT)
        self.assertEqual(self.permit(41, VALID_CONTEXT)["context"], direct)
        self.session.decision({"requestID": 41, "sessionID": "fixture-session", "kind": "permission", "optionID": "reject_once"})
        update = {"sessionUpdate": "tool_call", "toolCallId": "fixture-call", "status": "pending", "kind": "execute", "title": "Run fixture checks", "locations": VALID_CONTEXT["locations"], "rawInput": VALID_CONTEXT["rawInput"]}
        self.session.provider({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "fixture-session", "update": update}})
        self.assertEqual(self.permit(42, VALID_CONTEXT)["context"], direct)

    def test_permission_before_update_unknown_and_other_session_fail_closed(self):
        self.ready()
        early = self.permit(41, {"toolCallId": "fixture_1"})
        self.assertEqual(early["context"], {"state": "insufficient-context", "reason": "no-correlated-tool-call"})
        self.assert_reject_only(41)
        self.announce(raw={"command": "go", "args": ["test", "./..."]})
        unknown = self.permit(42, {"toolCallId": "missing-call"})
        self.assertEqual(unknown["context"]["reason"], "no-correlated-tool-call")
        self.assertNotIn("Run fixture checks", json.dumps(unknown["context"]))
        with self.assertRaisesRegex(ACP.Refusal, "cross-session-update"):
            self.announce(session_id="other-session", title="Use Bearer synthetic")
        self.assertNotIn("Bearer", json.dumps(self.events))
        sent, events = [], []
        other = ACP.Session(sent.append, events.append, "/synthetic/project", "Inspect fixture", "grok-synthetic", clock=self.clock)
        other.start()
        other.provider({"jsonrpc": "2.0", "id": 1, "result": {"protocolVersion": 1}})
        other.provider({"jsonrpc": "2.0", "id": 2, "result": {"sessionId": "other-session"}})
        other.provider({"jsonrpc": "2.0", "id": 43, "method": "session/request_permission", "params": {"sessionId": "other-session", "toolCall": {"toolCallId": "fixture_1"}, "options": [{"optionId": "reject_once", "kind": "reject_once"}]}})
        self.assertEqual(events[-1]["context"]["reason"], "no-correlated-tool-call")
        self.assertNotIn("./...", json.dumps(events))
        later = self.permit(44, {"toolCallId": "fixture_1"})
        self.assertEqual(later["context"]["rawInput"], {"command": "go", "args": ["test", "./..."]})

    def test_distinct_request_ids_keep_their_own_tool_context(self):
        self.ready()
        self.announce("alpha", title="Run alpha checks", raw={"command": "go", "args": ["test", "./..."]})
        self.announce("beta", title="Run beta checks", raw={"command": "python3", "args": ["check.py"]})
        alpha = self.permit(50, {"toolCallId": "alpha"})
        beta = self.permit(51, {"toolCallId": "beta"})
        self.assertEqual(alpha["context"]["title"], "Run alpha checks")
        self.assertEqual(alpha["context"]["rawInput"]["args"], ["test", "./..."])
        self.assertEqual(beta["context"]["title"], "Run beta checks")
        self.assertEqual(beta["context"]["rawInput"], {"command": "python3", "args": ["check.py"]})
        self.assert_reject_only(50)
        self.assert_reject_only(51)

    def test_conflict_redaction_and_completion_cannot_restore_stale_context(self):
        self.ready()
        raw = {"command": "go", "args": ["test", "./..."]}
        self.announce(raw=raw)
        self.announce(title="Other fixture checks", session_update="tool_call_update")
        conflict = self.permit(41, {"toolCallId": "fixture_1"})
        self.assertEqual(conflict["context"], {"state": "insufficient-context", "reason": "tool-call-conflict"})
        self.assertNotIn("Run fixture checks", json.dumps(conflict))
        self.assertNotIn("./...", json.dumps(conflict))
        fresh = ACP.Session(self.sent.append, self.events.append, "/synthetic/project", "Inspect fixture", "grok-synthetic", clock=self.clock)
        fresh.start()
        fresh.provider({"jsonrpc": "2.0", "id": 1, "result": {"protocolVersion": 1}})
        fresh.provider({"jsonrpc": "2.0", "id": 2, "result": {"sessionId": "fixture-session"}})
        self.session = fresh
        self.announce(raw=raw)
        self.announce(title="Use Bearer synthetic", session_update="tool_call_update")
        redacted = self.permit(42, {"toolCallId": "fixture_1"})
        self.assertEqual(redacted["context"]["reason"], "tool-call-unavailable")
        self.assertNotIn("Bearer", json.dumps(redacted))
        self.assertNotIn("./...", json.dumps(redacted["context"]))
        blocked = self.permit(43, {"toolCallId": "fixture_1", "kind": "execute", "title": "Run fixture checks", "rawInput": raw})
        self.assertEqual(blocked["context"]["reason"], "tool-call-unavailable")
        self.assertNotIn("./...", json.dumps(blocked["context"]))
        self.session = ACP.Session(self.sent.append, self.events.append, "/synthetic/project", "Inspect fixture", "grok-synthetic", clock=self.clock)
        self.session.start()
        self.ready()
        self.announce(raw=raw)
        explicit = self.permit(44, {"toolCallId": "fixture_1", "kind": "execute", "title": "Different fixture checks"})
        self.assertEqual(explicit["context"]["reason"], "tool-call-conflict")
        self.assertNotIn("Different fixture checks", json.dumps(explicit))
        self.assertNotIn("./...", json.dumps(explicit["context"]))
        self.session = ACP.Session(self.sent.append, self.events.append, "/synthetic/project", "Inspect fixture", "grok-synthetic", clock=self.clock)
        self.session.start()
        self.ready()
        self.announce(raw=raw)
        self.announce(kind=None, title=None, raw=None, status="completed", session_update="tool_call_update")
        closed = self.permit(45, {"toolCallId": "fixture_1"})
        self.assertEqual(closed["context"]["reason"], "tool-call-closed")
        self.announce(raw=raw)
        self.assertEqual(self.permit(46, {"toolCallId": "fixture_1"})["context"]["reason"], "tool-call-closed")
        self.assert_reject_only(45)

    def test_null_update_keeps_prior_and_secret_permission_poisons_it(self):
        self.ready()
        raw = {"command": "go", "args": ["test", "./..."]}
        self.announce(raw=raw)
        self.session.provider({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": "fixture-session", "update": {"sessionUpdate": "tool_call_update", "toolCallId": "fixture_1", "rawInput": None, "content": [{"type": "text", "text": "TRANSCRIPT_SECRET_PHRASE"}]}}})
        kept = self.permit(41, {"toolCallId": "fixture_1"})
        self.assertEqual(kept["context"]["rawInput"], raw)
        self.assertNotIn("TRANSCRIPT_SECRET_PHRASE", json.dumps(self.events))
        omitted = self.permit(44, {"toolCallId": "fixture_1", "kind": "execute", "title": "Run fixture checks", "rawInput": None})
        self.assertEqual(omitted["context"]["rawInput"], raw)
        poisoned = self.permit(42, {"toolCallId": "fixture_1", "kind": "execute", "title": "Use Bearer synthetic"})
        self.assertEqual(poisoned["context"]["title"], "[REDACTED]")
        self.assertEqual(poisoned["context"]["rawInput"], "[REDACTED]")
        self.assertNotIn("./...", json.dumps(poisoned["context"]))
        self.assertNotIn("Bearer", json.dumps(poisoned["context"]))
        follow = self.permit(43, {"toolCallId": "fixture_1"})
        self.assertEqual(follow["context"]["reason"], "tool-call-unavailable")
        self.assertNotIn("./...", json.dumps(follow["context"]))

    def test_later_invalid_update_drops_the_benign_snapshot(self):
        self.ready()
        self.announce(raw={"command": "go", "args": ["test", "./..."]})
        self.announce(title="x" * 257, session_update="tool_call_update")
        event = self.permit(41, {"toolCallId": "fixture_1"})
        self.assertEqual(event["context"]["reason"], "tool-call-unavailable")
        self.assertNotIn("Run fixture checks", json.dumps(event["context"]))
        self.session = ACP.Session(self.sent.append, self.events.append, "/synthetic/project", "Inspect fixture", "grok-synthetic", clock=self.clock)
        self.session.start()
        self.ready()
        self.announce(title="Run fixture checks")
        self.announce(title="Caf\u00e9 checks", session_update="tool_call_update")
        event = self.permit(42, {"toolCallId": "fixture_1"})
        self.assertEqual(event["context"]["reason"], "tool-call-unavailable")
        self.assertNotIn("Run fixture checks", json.dumps(event["context"]))

    def test_cache_count_bytes_and_expiry_fail_closed(self):
        self.ready()
        for index in range(ACP.MAX_TOOL_CACHE):
            self.announce(f"call-{index}", title=f"Run check {index}")
        self.announce("call-extra", title="Run extra check")
        extra = self.permit(70, {"toolCallId": "call-extra"})
        self.assertEqual(extra["context"]["reason"], "tool-cache-bound")
        self.assertNotIn("Run extra check", json.dumps(extra["context"]))
        first = self.permit(71, {"toolCallId": "call-0"})
        self.assertEqual(first["context"]["state"], "available")
        self.assertEqual(first["context"]["title"], "Run check 0")
        original = ACP.MAX_TOOL_CACHE_BYTES
        try:
            ACP.MAX_TOOL_CACHE_BYTES = 280
            self.session = ACP.Session(self.sent.append, self.events.append, "/synthetic/project", "Inspect fixture", "grok-synthetic", clock=self.clock)
            self.session.start()
            self.ready()
            wide = "Aa1." * 50
            self.announce("wide-1", title=wide)
            self.announce("wide-2", title="Bb2." * 50)
            self.assertEqual(self.permit(72, {"toolCallId": "wide-2"})["context"]["reason"], "tool-cache-bound")
            self.assertEqual(self.permit(73, {"toolCallId": "wide-1"})["context"]["title"], wide)
        finally:
            ACP.MAX_TOOL_CACHE_BYTES = original
        self.session = ACP.Session(self.sent.append, self.events.append, "/synthetic/project", "Inspect fixture", "grok-synthetic", clock=self.clock)
        self.session.start()
        self.ready()
        self.announce(raw={"command": "go", "args": ["test", "./..."]})
        self.clock.now = ACP.TOOL_CACHE_TTL + 1
        expired = self.permit(74, {"toolCallId": "fixture_1"})
        self.assertEqual(expired["context"]["reason"], "tool-call-expired")
        self.assertNotIn("./...", json.dumps(expired["context"]))
        self.announce(raw={"command": "go", "args": ["test", "./..."]})
        self.assertEqual(self.permit(75, {"toolCallId": "fixture_1"})["context"]["reason"], "tool-call-expired")

    def test_invalid_status_tombstones_and_blocks_later_benign(self):
        raw = {"command": "go", "args": ["test", "./..."]}
        explicit = {"toolCallId": "fixture_1", "kind": "execute", "title": "Run fixture checks", "rawInput": raw}
        for status in (None, ["completed"], {"bad": True}):
            with self.subTest(status=status):
                self.session = ACP.Session(self.sent.append, self.events.append, "/synthetic/project", "Inspect fixture", "grok-synthetic", clock=self.clock)
                self.session.start()
                self.ready()
                self.announce(raw=raw)
                self.announce(kind=None, title=None, raw=None, status=status, session_update="tool_call_update")
                event = self.permit(41, explicit)
                self.assertEqual(event["context"], {"state": "insufficient-context", "reason": "tool-call-unavailable"})
                self.assertNotIn("./...", json.dumps(event["context"]))
                self.assertNotIn("Run fixture checks", json.dumps(event["context"]))
        self.session = ACP.Session(self.sent.append, self.events.append, "/synthetic/project", "Inspect fixture", "grok-synthetic", clock=self.clock)
        self.session.start()
        self.ready()
        self.announce("fresh-call", kind=None, title=None, raw=None, status=["pending"])
        fresh = self.permit(42, {"toolCallId": "fresh-call", "kind": "execute", "title": "Run fixture checks", "rawInput": raw})
        self.assertEqual(fresh["context"]["reason"], "tool-call-unavailable")
        self.assertNotIn("./...", json.dumps(fresh["context"]))

    def test_dropped_id_saturation_fails_untracked_ids_closed(self):
        self.ready()
        raw = {"command": "go", "args": ["test", "./..."]}
        for index in range(ACP.MAX_TOOL_CACHE):
            self.announce(f"cached-{index}", title=f"Run check {index}")
        for index in range(ACP.MAX_TOOL_DROPPED):
            self.announce(f"dropped-{index}", title=f"Run drop {index}")
        self.announce("poisoned-extra", status=["nope"], raw=raw)
        for index in range(8):
            self.announce(f"later-{index}", title=f"Run later {index}", raw=raw)
        self.assertEqual(len(self.session.tool_cache), ACP.MAX_TOOL_CACHE)
        self.assertEqual(len(self.session.tool_dropped), ACP.MAX_TOOL_DROPPED)
        self.assertTrue(self.session.tool_untracked_closed)
        explicit = {"kind": "execute", "title": "Run benign checks", "rawInput": raw}
        poisoned = self.permit(90, dict(explicit, toolCallId="poisoned-extra"))
        self.assertEqual(poisoned["context"], {"state": "insufficient-context", "reason": "tool-cache-bound"})
        unseen = self.permit(91, dict(explicit, toolCallId="never-tracked"))
        self.assertEqual(unseen["context"]["reason"], "tool-cache-bound")
        later = self.permit(92, dict(explicit, toolCallId="later-0"))
        self.assertEqual(later["context"]["reason"], "tool-cache-bound")
        for event in (poisoned, unseen, later):
            encoded = json.dumps(event["context"])
            self.assertNotIn("./...", encoded)
            self.assertNotIn("Run benign", encoded)
        kept = self.permit(93, {"toolCallId": "cached-0"})
        self.assertEqual(kept["context"]["state"], "available")
        self.assertEqual(kept["context"]["title"], "Run check 0")
        dropped = self.permit(94, {"toolCallId": "dropped-0"})
        self.assertEqual(dropped["context"]["reason"], "tool-cache-bound")
        self.assert_reject_only(90)

    def test_completed_tombstones_saturate_and_reject_once_still_sends(self):
        # More than 32 completed ids: 16 closed tombstones, 16 dropped, then the latch.
        # A later explicit context is tool-cache-bound. reject_once still sends.
        self.ready()
        total = ACP.MAX_TOOL_CACHE + ACP.MAX_TOOL_DROPPED + 1
        self.assertGreater(total, 32)
        for index in range(total):
            self.announce(f"done-{index}", title=f"Run check {index}", status="completed")
        self.assertEqual(len(self.session.tool_cache), ACP.MAX_TOOL_CACHE)
        self.assertEqual(len(self.session.tool_dropped), ACP.MAX_TOOL_DROPPED)
        self.assertTrue(self.session.tool_untracked_closed)
        for entry in self.session.tool_cache.values():
            self.assertEqual(entry["state"], "closed")
            self.assertEqual(entry["fields"], {})
        kept = self.permit(89, {
            "toolCallId": "done-0",
            "kind": "execute",
            "title": "Run benign checks",
            "rawInput": {"command": "python3", "args": ["check.py"]},
        })
        self.assertEqual(kept["context"], {"state": "insufficient-context", "reason": "tool-call-closed"})
        explicit = {
            "toolCallId": "later-explicit",
            "kind": "execute",
            "title": "Run benign checks",
            "rawInput": {"command": "python3", "args": ["check.py"]},
        }
        later = self.permit(90, explicit)
        self.assertEqual(later["context"], {"state": "insufficient-context", "reason": "tool-cache-bound"})
        for event in (kept, later):
            encoded = json.dumps(event["context"])
            self.assertNotIn("check.py", encoded)
            self.assertNotIn("Run benign", encoded)
        self.assertEqual(len(self.session.tool_cache), ACP.MAX_TOOL_CACHE)
        self.assertEqual(len(self.session.tool_dropped), ACP.MAX_TOOL_DROPPED)
        self.assertTrue(self.session.tool_untracked_closed)
        self.assert_reject_only(90)

    def test_prompt_completion_clears_cache_and_question_ack_is_unchanged(self):
        self.ready()
        self.announce(raw={"command": "go", "args": ["test", "./..."]})
        self.session.provider({"jsonrpc": "2.0", "id": 40, "method": "cursor/ask_question", "params": {"questions": [{"id": "q1", "prompt": "Which fixture?", "options": [{"id": "a", "label": "First"}, {"id": "b", "label": "Second"}]}]}})
        self.assertEqual(self.events[-1]["event"], "question-required")
        self.assertNotIn("context", self.events[-1])
        self.session.decision({"requestID": 40, "sessionID": "fixture-session", "kind": "question", "answers": [{"questionId": "q1", "selectedOptionIds": ["a"]}]})
        self.assertEqual(self.sent[-1]["result"]["outcome"]["outcome"], "answered")
        self.session.provider({"jsonrpc": "2.0", "id": 3, "result": {"stopReason": "end_turn"}})
        self.assertTrue(self.session.complete)
        self.assertEqual(self.session.tool_cache, {})
        self.assertTrue(self.session.tool_cache_closed)
        self.announce(raw={"command": "go", "args": ["test", "./..."]})
        self.assertEqual(self.session.tool_cache, {})
        with self.assertRaisesRegex(ACP.Refusal, "request-outside-prompt"):
            self.permit(81, {"toolCallId": "fixture_1"})

    def test_explicit_raw_input_is_not_replaced_by_cached_args(self):
        self.ready()
        self.announce(raw={"command": "go", "args": ["test", "./..."]})
        event = self.permit(41, {"toolCallId": "fixture_1", "kind": "execute", "title": "Run fixture checks", "rawInput": {"command": "python3", "args": ["check.py"]}})
        self.assertEqual(event["context"]["rawInput"], {"command": "python3", "args": ["check.py"]})
        self.assertNotIn("./...", json.dumps(event["context"]))
        follow = self.permit(42, {"toolCallId": "fixture_1"})
        self.assertEqual(follow["context"]["rawInput"], {"command": "python3", "args": ["check.py"]})


FAKE = '''#!/usr/bin/env python3
import json,os,sys,time
from pathlib import Path
mode=MODE
tool_context=CONTEXT
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
  if mode.startswith("frame-"):
   Path("fake-provider.pid").write_text(str(os.getpid()))
   frames={"frame-too-large":b"x"*(4*1024*1024)+b"\\n",
           "frame-malformed-json":b'{"PRIVATE_FRAME":}\\n',
           "frame-duplicate-key":b'{"PRIVATE_KEY":1,"PRIVATE_KEY":2}\\n',
           "frame-truncated-eof":b'{"PRIVATE_FRAME":'}
   sys.stdout.buffer.write(frames[mode]); sys.stdout.buffer.flush()
   os.close(sys.stdout.fileno())
   time.sleep(60)
  write({"jsonrpc":"2.0","id":msg["id"],"result":{"protocolVersion":1}})
 elif method=="session/new":
  write({"jsonrpc":"2.0","id":msg["id"],"result":{"sessionId":"fixture-session"}})
 elif method=="session/prompt":
  if mode=="large-update":
   write({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"tool_call_update","content":[{"type":"content","content":{"type":"text","text":"PRIVATE_LARGE_UPDATE"+"x"*(200*1024)}}]}}})
   write({"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}})
   continue
  if mode=="budget-updates":
   for n in range(3):
    update={"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"tool_call_update","content":[{"type":"content","content":{"type":"text","text":"PRIVATE_BUDGET_UPDATE"}}]}}}
    room=2*1024*1024+1-len(json.dumps(update))-1
    update["params"]["update"]["content"][0]["content"]["text"]+="x"*room
    write(update)
   write({"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}})
   continue
  if mode=="flood":
   Path("fake-provider.pid").write_text(str(os.getpid()))
   for n in range(5):
    write({"jsonrpc":"2.0","id":40+n,"method":"cursor/ask_question","params":{"questions":[{"id":"q"+str(q),"prompt":"Fixture choice?","options":[{"id":"a"+str(o),"label":"A"*256} for o in range(14)]} for q in range(8)]}})
   continue
  if mode=="error": write({"jsonrpc":"2.0","id":3,"error":{"message":"PRIVATE_PROVIDER_ERROR"}}); continue
  if mode=="exit": sys.exit(0)
  if mode in ("grandchild","env"): write({"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}); continue
  if mode=="partial-id":
   write({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"tool_call","toolCallId":"fixture_1","title":"Run fixture checks","kind":"execute","status":"pending","rawInput":{"command":"go","args":["test","./..."]}}}})
   write({"jsonrpc":"2.0","id":41,"method":"session/request_permission","params":{"sessionId":"fixture-session","toolCall":{"toolCallId":"fixture_1"},"options":[{"optionId":"allow","kind":"allow_once","name":"Allow"},{"optionId":"deny","kind":"reject_once","name":"Deny"}]}})
   continue
  if mode=="permission" or mode.startswith("context-"):
   write({"jsonrpc":"2.0","id":41,"method":"session/request_permission","params":{"sessionId":"fixture-session","toolCall":tool_context,"options":[{"optionId":"allow","kind":"allow_once","name":"Allow"},{"optionId":"deny","kind":"reject_once","name":"Deny"}]}})
   continue
  if mode=="callbacks":
   write({"jsonrpc":"2.0","id":50,"method":"fs/read_text_file","params":{"sessionId":"fixture-session","path":"/PRIVATE_PATH"}})
   write({"jsonrpc":"2.0","id":51,"method":"fs/write_text_file","params":{"sessionId":"fixture-session","path":"/PRIVATE_PATH","content":"PRIVATE_CONTENT"}})
   write({"jsonrpc":"2.0","id":52,"method":"terminal/create","params":{"sessionId":"fixture-session","command":"PRIVATE_COMMAND"}})
   continue
  write({"jsonrpc":"2.0","id":40,"method":"cursor/ask_question","params":{"questions":[{"id":"q1","prompt":"Fixture choice?","options":[{"id":"a","label":"First"}]}]}})
 elif (mode=="permission" or mode=="partial-id" or mode.startswith("context-")) and msg.get("id")==41:
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
        client.write_text(FAKE.replace("MODE", repr(mode)).replace("CONTEXT", repr(CONTEXT_FIXTURES.get(mode.removeprefix("context-")))), encoding="utf-8")
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
                checked = subprocess.run([sys.executable, "-I", "-B", str(CLIENT.with_name("cursor-acp-smoke-check.py")), "-"], input="".join(json.dumps(x) + "\n" for x in observed), text=True, capture_output=True, timeout=5)
                self.assertEqual(checked.returncode, 0, checked.stdout)
                self.assertTrue(json.loads(checked.stdout)["questionDecisionSent"])
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

    def test_fake_transport_partial_id_emits_cached_relative_argv(self):
        # Reads the session events directly. It does not approve or execute the command.
        # The smoke checker accepts this relative argv shape in its own fixtures.
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory, "partial-id")
            try:
                observed = []
                while True:
                    event = json.loads(process.stdout.readline())
                    observed.append(event)
                    if event["event"] == "permission-required":
                        break
                self.assertEqual(event["requestID"], 41)
                self.assertEqual(event["options"], [{"optionID": "allow", "kind": "allow_once"}, {"optionID": "deny", "kind": "reject_once"}])
                self.assertEqual(event["context"], {"state": "available", "kind": "execute", "title": "Run fixture checks", "rawInput": {"command": "go", "args": ["test", "./..."]}})
                process.stdin.write(json.dumps({"requestID": 41, "sessionID": "fixture-session", "kind": "permission", "optionID": "deny"}) + "\n")
                process.stdin.flush()
                stdout, stderr = process.communicate(timeout=15)
                observed.extend(json.loads(line) for line in stdout.splitlines())
                self.assertEqual(process.returncode, 0, stderr)
                self.assertEqual([item["event"] for item in observed], ["starting", "session-created", "permission-required", "decision-sent", "prompt-complete"])
                self.assertNotIn("decision-sent", json.dumps(observed[2]))
                blob = json.dumps(observed) + stderr
                self.assertIn("./...", blob)
                self.assertNotIn("go test ./...", blob)
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait()

    def test_permission_context_fixtures_over_fake_cli(self):
        # Every redaction fails closed (review of 6b9ae3d7, finding 3).
        insufficient = {"quoted-path-args", "double-quoted-path-args", "angle-path-args", "spaced-path-args", "spaced-path-command", "spaced-path-title", "raw-oversized", "source-oversized", "raw-unknown", "raw-scalar", "location-control", "raw-control", "missing", "oversized", "total", "controls", "unicode-control", "malformed", "unknown-kind", "unknown-only", "combining-mark", "line-separator", "paragraph-separator",
                        "deep-percent-name", "deep-percent-userinfo", "homoglyph-cyrillic", "homoglyph-filler", "non-ascii-title"}
        insufficient |= {name for name in CONTEXT_FIXTURES if "secret" in name}
        for name in CONTEXT_FIXTURES:
            with self.subTest(fixture=name), tempfile.TemporaryDirectory() as directory:
                process = self.launch(directory, "context-" + name)
                observed = []
                try:
                    while True:
                        event = json.loads(process.stdout.readline())
                        observed.append(event)
                        if event["event"] == "permission-required":
                            break
                    # A request alone never produces a provider response.
                    self.assertEqual(event["requestID"], 41)
                    self.assertEqual(event["sessionID"], "fixture-session")
                    context = event["context"]
                    self.assertEqual(context["state"], "insufficient-context" if name in insufficient else "available")
                    self.assertLessEqual(len(json.dumps(context, ensure_ascii=True, separators=(",", ":"))), 2048)
                    if name == "valid":
                        self.assertEqual(context["title"], "Run fixture checks")
                        self.assertEqual(context["kind"], "execute")
                        self.assertEqual(context["locations"], [{"path": "check.py", "line": 12}])
                        self.assertEqual(context["rawInput"], {"command": "python3", "args": ["check.py"], "path": "check.py"})
                    if name in {"explicit-spaced-path", "explicit-quoted-path"}:
                        self.assertEqual(context["rawInput"], {"path": "f.py"})
                    if name.startswith("spaced-path-"):
                        self.assertNotIn("customer", json.dumps(context))
                    if "secret" in name:
                        self.assertIn("[REDACTED]", json.dumps(context))
                        self.assertEqual(context, redacted_context(CONTEXT_FIXTURES[name]))
                    if name == "fullwidth-slash-args":
                        self.assertEqual(context["rawInput"], {"args": ["f.py"]})
                    decision = {"requestID": 41, "sessionID": "fixture-session", "kind": "permission", "optionID": "deny"}
                    process.stdin.write(json.dumps(decision) + "\n")
                    process.stdin.flush()
                    stdout, stderr = process.communicate(timeout=15)
                    observed.extend(json.loads(x) for x in stdout.splitlines())
                    self.assertEqual(process.returncode, 0, stderr)
                    output = json.dumps(observed) + stderr
                    for private in ["synthetic-sensitive-value", "ab" * 32, "QWxhZGRpbjpvcGVuIHNlc2FtZUZpeHR1cmU=", "DO_NOT_EXPORT", "/fixture-parent/project", "S3cr3tVal", "hunter2", "customer", "\x1b", "\u202e"]:
                        self.assertNotIn(private, output)
                    self.assertEqual(observed[-2]["event"], "decision-sent")
                    # The OP314 checker accepts the bounded context and records only its state.
                    checked = subprocess.run([sys.executable, "-I", "-B", str(CLIENT.with_name("cursor-acp-smoke-check.py")), "-"], input="".join(json.dumps(x) + "\n" for x in observed), text=True, capture_output=True, timeout=5)
                    self.assertEqual(checked.returncode, 0, checked.stdout)
                    self.assertEqual(json.loads(checked.stdout)["permissionContexts"], [{"requestID": 41, "permissionContext": "insufficient" if name in insufficient else "available"}])
                    self.assertIs(json.loads(checked.stdout)["questionDecisionSent"], False)
                finally:
                    if process.poll() is None:
                        process.kill()
                        process.wait()
                    for stream in (process.stdin, process.stdout, process.stderr):
                        stream.close()

    def test_fake_context_binding_approval_and_no_decision_fail_closed(self):
        cases = [
            ({"requestID": "41"}, "stale-or-cross-session-decision"),
            ({"sessionID": "foreign"}, "stale-or-cross-session-decision"),
            ({"kind": "question"}, "decision-kind-mismatch"),
            ({"optionID": "allow"}, "permission-approval-not-supported"),
            (None, "decision-input-closed"),
        ]
        for mode in ["context-valid", "context-missing"]:
            for change, reason in cases:
                with self.subTest(mode=mode, change=change), tempfile.TemporaryDirectory() as directory:
                    process = self.launch(directory, mode)
                    try:
                        while json.loads(process.stdout.readline())["event"] != "permission-required":
                            pass
                        if change is not None:
                            decision = {"requestID": 41, "sessionID": "fixture-session", "kind": "permission", "optionID": "deny"}
                            decision.update(change)
                            process.stdin.write(json.dumps(decision) + "\n")
                            process.stdin.flush()
                        events, output = self.finish(process)
                        self.assertEqual(process.returncode, 2)
                        self.assertEqual(events[-1], {"event": "failed", "reason": reason})
                        self.assertNotIn("decision-sent", output)
                    finally:
                        if process.poll() is None:
                            process.kill()
                            process.wait()
                        for stream in (process.stdin, process.stdout, process.stderr):
                            stream.close()

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

    def checked(self, observed):
        """The real receipt stream through the smoke checker; its verdict."""
        checked = subprocess.run([sys.executable, "-I", "-B", str(CLIENT.with_name("cursor-acp-smoke-check.py")), "-"], input="".join(json.dumps(x) + "\n" for x in observed), text=True, capture_output=True, timeout=5)
        self.assertEqual(checked.returncode, 0, checked.stdout)
        return json.loads(checked.stdout)

    def test_frame_failure_receipts_and_provider_cleanup(self):
        for detail, metadata in [
            ("frame-too-large", {"byte_count": 4194305, "limit": 4194304}),
            ("frame-malformed-json", {}),
            ("frame-duplicate-key", {}),
            ("frame-truncated-eof", {"byte_count": len(b'{"PRIVATE_FRAME":')}),
        ]:
            with self.subTest(detail=detail), tempfile.TemporaryDirectory() as directory:
                process = self.launch(directory, detail)
                observed, output = self.finish(process)
                self.assertEqual(process.returncode, 2)
                self.assertEqual(observed[-1], dict(event="failed", reason="invalid-frame", detail=detail, source="provider", **metadata))
                self.assertNotIn("PRIVATE", output)
                self.assertNotIn("Traceback", output)
                self.assertTrue(pid_gone(int((Path(directory) / "fake-provider.pid").read_text())))
                verdict = self.checked(observed)
                self.assertEqual(verdict["helperFailed"], "invalid-frame")
                counts = {{"byte_count": "byteCount"}.get(key, key): value for key, value in metadata.items()}
                self.assertEqual(verdict["helperFailureDetail"], dict(detail=detail, source="provider", **counts))
                self.assertNotIn("PRIVATE", json.dumps(verdict))

    def test_coordinator_frame_bound_and_failure_receipts(self):
        for raw, detail, metadata in [
            (b"x" * 65536 + b"\n", "frame-too-large", {"byte_count": 65537, "limit": 65536}),
            (b'{"PRIVATE_FRAME":}\n', "frame-malformed-json", {}),
            (b'{"PRIVATE_KEY":1,"PRIVATE_KEY":2}\n', "frame-duplicate-key", {}),
            (b'{"PRIVATE_FRAME":', "frame-truncated-eof", {"byte_count": len(b'{"PRIVATE_FRAME":')}),
        ]:
            with self.subTest(detail=detail), tempfile.TemporaryDirectory() as directory:
                process = self.launch(directory, "hang")
                stdout, stderr = process.communicate(raw.decode(), timeout=15)
                self.assertEqual(process.returncode, 2)
                observed = [json.loads(x) for x in stdout.splitlines()]
                self.assertEqual(observed[-1], dict(event="failed", reason="invalid-frame", detail=detail, source="coordinator", **metadata))
                self.assertNotIn("PRIVATE", stdout + stderr)
                self.assertEqual(self.checked(observed)["helperFailureDetail"]["source"], "coordinator")

    def test_provider_updates_past_the_byte_budget_complete(self):
        # Three updates of just over half the budget: each must be handled and its
        # bytes released before the next is parsed, or the run stalls to its deadline.
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory, "budget-updates", 30)
            observed, output = self.finish(process, timeout=40)
            self.assertEqual(process.returncode, 0, output)
            self.assertEqual([x["event"] for x in observed], ["starting", "session-created", "prompt-complete"])
            self.assertNotIn("PRIVATE_BUDGET_UPDATE", output)

    def test_large_valid_session_update_completes_without_content_receipts(self):
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory, "large-update")
            observed, output = self.finish(process)
            self.assertEqual(process.returncode, 0, output)
            self.assertEqual([x["event"] for x in observed], ["starting", "session-created", "prompt-complete"])
            self.assertNotIn("PRIVATE_LARGE_UPDATE", output)

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

    def test_sigterm_to_client_still_stops_provider_group(self):
        with tempfile.TemporaryDirectory() as directory:
            process = self.launch(directory, "hang", 30)
            child = None
            try:
                children = Path(f"/proc/{process.pid}/task/{process.pid}/children")
                end = time.monotonic() + 10
                while child is None and time.monotonic() < end:
                    try:
                        listed = children.read_text().split()
                    except OSError:
                        listed = []
                    child = int(listed[0]) if listed else None
                    if child is None:
                        time.sleep(0.05)
                self.assertIsNotNone(child, "provider child never started")
                process.send_signal(signal.SIGTERM)
                process.communicate(timeout=15)
                self.assertNotEqual(process.returncode, 0)
                self.assertTrue(pid_gone(child), "provider survived SIGTERM to the client")
                child = None
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait()
                if child is not None:
                    try:
                        os.killpg(child, signal.SIGKILL)
                    except ProcessLookupError:
                        pass

    def test_signal_during_launch_still_stops_provider_group(self):
        # The real Popen has created the child but has not returned, so the client
        # does not own it yet; the signal must be replayed once it does.
        for signum in (signal.SIGTERM, signal.SIGHUP):
            with self.subTest(signal=signum.name), tempfile.TemporaryDirectory() as directory:
                pidfile = Path(directory) / "launched.pid"
                driver = (
                    "import importlib.util,os,sys,time\n"
                    "spec=importlib.util.spec_from_file_location('acp',sys.argv[1])\n"
                    "m=importlib.util.module_from_spec(spec)\n"
                    "spec.loader.exec_module(m)\n"
                    "real=m.subprocess.Popen\n"
                    "def launch(*a,**k):\n"
                    " p=real(*a,**k)\n"
                    f" open({str(pidfile)!r},'w').write(str(p.pid))\n"
                    f" os.kill(os.getpid(),{int(signum)})\n"
                    " time.sleep(0.2)\n"
                    " return p\n"
                    "m.subprocess.Popen=launch\n"
                    "raise SystemExit(m.main(sys.argv[2:]))\n"
                )
                # This provider ignores end of input, so only the client's cleanup can stop it.
                sleeper = Path(directory) / "sleeper"
                sleeper.write_text(f"#!{sys.executable} -I\nimport time\ntime.sleep(60)\n")
                sleeper.chmod(0o700)
                process = self.launch(directory, "hang", 30, client_arg=str(sleeper), prefix=("-c", driver))
                child = None
                try:
                    process.communicate(timeout=20)
                    self.assertEqual(process.returncode, 128 + int(signum))
                    child = int(pidfile.read_text())
                    self.assertTrue(pid_gone(child), "provider launched during a signal survived")
                    child = None
                finally:
                    if process.poll() is None:
                        process.kill()
                        process.wait()
                    if child is not None:
                        try:
                            os.killpg(child, signal.SIGKILL)
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
                expected = {"event": "failed", "reason": reason}
                if mode == "oversize":
                    # Above the old 64 KiB limit, now parsed and refused as malformed JSON.
                    expected.update(detail="frame-malformed-json", source="provider")
                self.assertEqual(json.loads(stdout.splitlines()[-1]), expected)
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
