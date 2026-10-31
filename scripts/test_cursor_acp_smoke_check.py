import importlib.util
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location(
    "cursor_acp_smoke_check", Path(__file__).with_name("cursor-acp-smoke-check.py")
)
CHK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHK)
# Read only: the session's constants and a real event source for end-to-end checks.
SESSION_SPEC = importlib.util.spec_from_file_location(
    "cursor_acp_session", Path(__file__).with_name("cursor-acp-session.py")
)
ACP = importlib.util.module_from_spec(SESSION_SPEC)
SESSION_SPEC.loader.exec_module(ACP)

SESSION = "fixture-session"
MODEL = "grok-synthetic-requested"
DIGEST = "cd" * 32
QUESTION_TEXT = "Which fixture?\nKeep PRIVATE_QUESTION out"
OPTION_LABEL = "PRIVATE_OPTION_LABEL"


def line(event):
    return json.dumps(event, ensure_ascii=True, separators=(",", ":")) + "\n"


def receipt(*events):
    return "".join(line(event) for event in events)


def starting():
    return {
        "event": "starting",
        "modelRequested": MODEL,
        "servedModel": "Unknown",
        "promptSHA256": DIGEST,
    }


def session(session_id=SESSION):
    return {"event": "session-created", "sessionID": session_id}


def prompt(session_id=SESSION, stop="end_turn"):
    return {"event": "prompt-complete", "sessionID": session_id, "stopReason": stop}


def question(ident=40, session_id=SESSION, prompt_text=QUESTION_TEXT):
    return {
        "event": "question-required",
        "requestID": ident,
        "sessionID": session_id,
        "questions": [{
            "id": "q1",
            "prompt": prompt_text,
            "options": [{"id": "a", "label": OPTION_LABEL}, {"id": "b", "label": "Second"}],
            "allowMultiple": False,
        }],
    }


def decision(ident=40, session_id=SESSION, kind="question"):
    return {"event": "decision-sent", "requestID": ident, "sessionID": session_id, "kind": kind}


def permission(ident=41, session_id=SESSION):
    return {
        "event": "permission-required",
        "requestID": ident,
        "sessionID": session_id,
        "options": [
            {"optionID": "allow", "kind": "allow_once"},
            {"optionID": "deny", "kind": "reject_once"},
        ],
    }


def permission_with(context, ident=41, session_id=SESSION):
    event = permission(ident, session_id)
    event["context"] = context
    return event


def available(**extra):
    return dict({"state": "available", "kind": "execute", "title": "Run checks"}, **extra)


INSUFFICIENT = {"state": "insufficient-context"}
REDACTED_CONTEXT = {"state": "insufficient-context", "kind": "execute", "title": "[REDACTED]", "rawInput": "[REDACTED]"}
PRIVATE_CONTEXT = available(
    title="PRIVATE_TITLE",
    locations=[{"path": "PRIVATE_FILE.py", "line": 12}],
    rawInput={"command": "PRIVATE_COMMAND", "args": ["PRIVATE_ARG"], "path": "PRIVATE_PATH"},
)


def nested(depth):
    value = "x"
    for _ in range(depth):
        value = [value]
    return value


LIMITS = {
    "autonomousQuestionReply": False,
    "receiptSender": "client",
    "servedModel": "not-inferred",
    "permissionApproval": "not-inferred",
    "cost": "not-inferred",
    "providerUnderstanding": "not-inferred",
}


class CheckerTests(unittest.TestCase):
    def assert_omits(self, value, *needles):
        raw = json.dumps(value)
        for needle in needles:
            self.assertNotIn(needle, raw)

    def classify(self, raw):
        verdict = CHK.classify(raw)
        self.assert_omits(verdict, QUESTION_TEXT, OPTION_LABEL, MODEL, DIGEST, "PRIVATE")
        self.assertIs(verdict["autonomousQuestionReply"], False)
        return verdict

    def reason(self, raw):
        with self.assertRaises(CHK.Refusal) as caught:
            CHK.classify(raw)
        self.assertNotIn("PRIVATE", str(caught.exception))
        self.assertRegex(caught.exception.reason, r"^[a-z0-9-]{1,64}$")
        return caught.exception.reason

    def test_end_turn_without_question_is_not_autonomous_question_reply(self):
        verdict = self.classify(receipt(starting(), session(), prompt()))
        self.assertEqual(verdict, {
            "stages": ["fresh-session-transport-established", "prompt-complete-without-question"],
            "transportEstablished": True,
            "questionDecisionSent": False,
            "promptCompleteAfterDecision": False,
            "promptCompleteWithoutQuestion": True,
            "permissionDecisionSent": False,
            "pendingRequests": 0,
            **LIMITS,
            "autonomousQuestionReplyReason": "no-correlated-question-decision",
            "sessionID": SESSION,
            "stopReason": "end_turn",
        })
        self.assertNotIn("correlated-question-decision-sent", verdict["stages"])
        self.assertNotIn("prompt-complete-after-decision", verdict["stages"])

    def test_three_stages_stay_distinct_for_question_decision_and_later_prompt(self):
        verdict = self.classify(receipt(
            starting(), session(), question(), decision(), prompt(),
        ))
        self.assertEqual(verdict["stages"], [
            "fresh-session-transport-established",
            "correlated-question-decision-sent",
            "prompt-complete-after-decision",
        ])
        self.assertEqual(verdict["questionOrigin"], "provider-request")
        self.assertEqual(verdict["decisionOrigin"], "coordinator-decision")
        self.assertEqual(verdict["decisionMeaning"], "transport-send-only")
        self.assertEqual(verdict["questionRequestIDs"], [40])
        self.assertEqual(verdict["autonomousQuestionReplyReason"], "decision-sent-is-transport-only")
        self.assertIs(verdict["promptCompleteWithoutQuestion"], False)
        self.assertEqual(verdict["stopReason"], "end_turn")
        self.assertNotIn("answers", verdict)
        self.assert_omits(verdict, "Which fixture", "Keep PRIVATE_QUESTION out")

    def test_decision_without_later_prompt_is_not_completion(self):
        verdict = self.classify(receipt(starting(), session(), question(), decision()))
        self.assertEqual(verdict["stages"], [
            "fresh-session-transport-established",
            "correlated-question-decision-sent",
        ])
        self.assertIs(verdict["promptCompleteAfterDecision"], False)
        self.assertIs(verdict["promptCompleteWithoutQuestion"], False)
        self.assertNotIn("stopReason", verdict)
        self.assertEqual(verdict["decisionMeaning"], "transport-send-only")

    def test_unanswered_question_is_provider_origin_without_a_decision(self):
        verdict = self.classify(receipt(starting(), session(), question()))
        self.assertEqual(verdict["stages"], ["fresh-session-transport-established"])
        self.assertIs(verdict["questionDecisionSent"], False)
        self.assertEqual(verdict["questionOrigin"], "provider-request")
        self.assertNotIn("decisionOrigin", verdict)
        self.assertEqual(verdict["pendingRequests"], 1)
        self.assertEqual(verdict["autonomousQuestionReplyReason"], "no-correlated-question-decision")

    def test_permission_send_is_not_question_reply_or_approval(self):
        verdict = self.classify(receipt(
            starting(), session(), permission(), decision(41, kind="permission"), prompt(),
        ))
        self.assertEqual(verdict["stages"], [
            "fresh-session-transport-established",
            "permission-decision-sent",
            "prompt-complete-without-question",
        ])
        self.assertIs(verdict["questionDecisionSent"], False)
        self.assertIs(verdict["promptCompleteAfterDecision"], False)
        self.assertIs(verdict["permissionDecisionSent"], True)
        self.assertEqual(verdict["permissionApproval"], "not-inferred")
        self.assertEqual(verdict["permissionRequestIDs"], [41])
        self.assertNotIn("questionOrigin", verdict)
        self.assertEqual(verdict["decisionOrigin"], "coordinator-decision")
        self.assertEqual(verdict["decisionMeaning"], "transport-send-only")
        self.assertEqual(verdict["autonomousQuestionReplyReason"], "no-correlated-question-decision")

    def test_question_and_permission_both_sent_then_prompt(self):
        verdict = self.classify(receipt(
            starting(),
            session(),
            question(),
            decision(),
            permission(),
            decision(41, kind="permission"),
            prompt(),
        ))
        self.assertEqual(verdict["stages"], [
            "fresh-session-transport-established",
            "correlated-question-decision-sent",
            "permission-decision-sent",
            "prompt-complete-after-decision",
        ])
        self.assertEqual(verdict["questionRequestIDs"], [40])
        self.assertEqual(verdict["permissionRequestIDs"], [41])
        self.assertIs(verdict["promptCompleteWithoutQuestion"], False)

    def test_decisions_correlate_out_of_request_order(self):
        first = question(40)
        second = question(41)
        second["questions"][0]["id"] = "q2"
        verdict = self.classify(receipt(
            starting(), session(), first, second, decision(41), decision(40), prompt(),
        ))
        self.assertEqual(verdict["questionRequestIDs"], [41, 40])
        self.assertEqual(verdict["pendingRequests"], 0)
        self.assertIn("prompt-complete-after-decision", verdict["stages"])

    def test_int_and_string_request_ids_stay_distinct(self):
        self.assertEqual(self.reason(receipt(
            starting(), session(), question("40"), decision(40),
        )), "stale-decision")
        verdict = self.classify(receipt(
            starting(), session(), question("req-1"), decision("req-1"),
        ))
        self.assertEqual(verdict["questionRequestIDs"], ["req-1"])

    def test_unsupported_requests_do_not_become_question_acceptance(self):
        plain = self.classify(receipt(starting(), session(), prompt()))
        with_unsupported = self.classify(receipt(
            starting(),
            session(),
            {"event": "unsupported-request", "sessionID": SESSION},
            {"event": "unsupported-request", "sessionID": SESSION},
            prompt(),
        ))
        self.assertEqual(with_unsupported, plain)

    def test_helper_failure_is_not_transport_or_question_acceptance(self):
        only = self.classify(line({"event": "failed", "reason": "invalid-input"}))
        self.assertEqual(only["stages"], ["helper-failed"])
        self.assertIs(only["transportEstablished"], False)
        self.assertEqual(only["helperFailed"], "invalid-input")
        self.assertIs(only["autonomousQuestionReply"], False)
        after_start = self.classify(receipt(starting(), {"event": "failed", "reason": "provider-error-initialize"}))
        self.assertEqual(after_start["stages"], ["helper-failed"])
        self.assertIs(after_start["transportEstablished"], False)
        self.assertEqual(after_start["helperFailed"], "provider-error-initialize")

    def test_enriched_frame_failure_round_trip_from_the_session_client(self):
        # Build each receipt exactly as cursor-acp-session.py's main() does, from its own FrameRefusal.
        cases = [
            (ACP.FrameRefusal("frame-too-large", source="provider", byte_count=ACP.MAX_PROVIDER_FRAME + 1, limit=ACP.MAX_PROVIDER_FRAME),
             {"detail": "frame-too-large", "source": "provider", "byteCount": ACP.MAX_PROVIDER_FRAME + 1, "limit": ACP.MAX_PROVIDER_FRAME}),
            (ACP.FrameRefusal("frame-too-large", source="coordinator", byte_count=ACP.MAX_FRAME + 1, limit=ACP.MAX_FRAME),
             {"detail": "frame-too-large", "source": "coordinator", "byteCount": ACP.MAX_FRAME + 1, "limit": ACP.MAX_FRAME}),
            (ACP.FrameRefusal("frame-truncated-eof", source="provider", byte_count=17),
             {"detail": "frame-truncated-eof", "source": "provider", "byteCount": 17}),
            (ACP.FrameRefusal("frame-malformed-json", source="coordinator"), {"detail": "frame-malformed-json", "source": "coordinator"}),
            (ACP.FrameRefusal("frame-duplicate-key", source="provider"), {"detail": "frame-duplicate-key", "source": "provider"}),
        ]
        for error, expected in cases:
            with self.subTest(detail=expected["detail"], source=expected["source"]):
                event = {"event": "failed", "reason": str(error)}
                event.update(error.metadata)
                out = self.classify(receipt(starting(), event))
                self.assertEqual(out["stages"], ["helper-failed"])
                self.assertEqual(out["helperFailed"], "invalid-frame")
                self.assertEqual(out["helperFailureDetail"], expected)
                self.assertIs(out["transportEstablished"], False)
        self.assertEqual(set(ACP.FRAME_SOURCES.values()), CHK.FRAME_SOURCES)
        plain = self.classify(line({"event": "failed", "reason": "invalid-frame"}))
        self.assertNotIn("helperFailureDetail", plain)
        # source is optional: a receipt written before it existed still passes.
        before = self.classify(line({"event": "failed", "reason": "invalid-frame", "detail": "frame-malformed-json"}))
        self.assertEqual(before["helperFailureDetail"], {"detail": "frame-malformed-json"})

    def test_enriched_frame_failure_refusals(self):
        too_large = {"event": "failed", "reason": "invalid-frame", "detail": "frame-too-large", "byte_count": 70000, "limit": 65536}
        cases = [
            ("unknown-field", dict(too_large, frame="PRIVATE_FRAME"), "unexpected-field"),
            ("detail-on-other-reason", {"event": "failed", "reason": "invalid-input", "detail": "frame-too-large"}, "unexpected-field"),
            ("count-without-detail", {"event": "failed", "reason": "invalid-frame", "byte_count": 5}, "unexpected-field"),
            ("unknown-detail", dict(too_large, detail="PRIVATE detail"), "invalid-failure-detail"),
            ("bool-count", dict(too_large, byte_count=True), "invalid-failure-detail"),
            ("string-count", dict(too_large, byte_count="70000"), "invalid-failure-detail"),
            ("zero-count", {"event": "failed", "reason": "invalid-frame", "detail": "frame-truncated-eof", "byte_count": 0}, "invalid-failure-detail"),
            ("huge-count", dict(too_large, byte_count=(1 << 31) + 1), "invalid-failure-detail"),
            ("too-large-without-limit", {"event": "failed", "reason": "invalid-frame", "detail": "frame-too-large", "byte_count": 70000}, "invalid-failure-detail"),
            ("not-over-limit", dict(too_large, byte_count=65536), "invalid-failure-detail"),
            ("limit-on-eof", {"event": "failed", "reason": "invalid-frame", "detail": "frame-truncated-eof", "byte_count": 9, "limit": 65536}, "invalid-failure-detail"),
            ("eof-without-count", {"event": "failed", "reason": "invalid-frame", "detail": "frame-truncated-eof"}, "invalid-failure-detail"),
            ("count-on-malformed", {"event": "failed", "reason": "invalid-frame", "detail": "frame-malformed-json", "byte_count": 9}, "invalid-failure-detail"),
            # Unhashable kinds were a TypeError crash before the type check.
            ("detail-list", dict(too_large, detail=[]), "invalid-failure-detail"),
            ("detail-object", dict(too_large, detail={}), "invalid-failure-detail"),
            ("detail-null", dict(too_large, detail=None), "invalid-failure-detail"),
            ("source-unknown", dict(too_large, source="PRIVATE source"), "invalid-failure-detail"),
            ("source-internal-name", dict(too_large, source="decision"), "invalid-failure-detail"),
            ("source-list", dict(too_large, source=["provider"]), "invalid-failure-detail"),
            ("source-object", dict(too_large, source={}), "invalid-failure-detail"),
            ("source-null", dict(too_large, source=None), "invalid-failure-detail"),
            ("source-without-detail", {"event": "failed", "reason": "invalid-frame", "source": "provider"}, "unexpected-field"),
            ("source-on-other-reason", {"event": "failed", "reason": "invalid-input", "source": "provider"}, "unexpected-field"),
        ]
        for name, event, expected in cases:
            with self.subTest(case=name):
                self.assertEqual(self.reason(line(event)), expected)
        for name in ("detail-list", "detail-object", "source-list", "source-object"):
            event = next(event for case, event, _ in cases if case == name)
            with self.subTest(cli=name):
                code, output = run_main(["-"], stdin=line(event).encode())
                self.assertEqual(code, 2)
                self.assertEqual(output, '{"event":"refused","reason":"invalid-failure-detail"}\n')
                self.assertNotIn("Traceback", output)

    def test_refusals_for_malformed_duplicate_stale_and_cross_session(self):
        cases = [
            ("empty", "", "empty-input"),
            ("blank", "\n", "invalid-json"),
            ("array", "[]\n", "object-required"),
            ("truncated", "{", "invalid-json"),
            ("nan", '{"event":NaN}\n', "invalid-json"),
            ("binary", b"\xff\n", "invalid-json"),
            ("duplicate-field", b'{"event":"failed","reason":"invalid-input","reason":"PRIVATE_DUP"}\n', "duplicate-field"),
            ("unknown", line({"event": "agent-message", "text": "PRIVATE_TEXT"}), "unknown-event"),
            ("order", line(session()), "invalid-order"),
            ("duplicate-start", receipt(starting(), starting()), "duplicate-event"),
            ("duplicate-session", receipt(starting(), session(), session()), "duplicate-event"),
            ("stale", receipt(starting(), session(), decision()), "stale-decision"),
            ("spent", receipt(starting(), session(), question(), decision(), decision()), "stale-decision"),
            ("duplicate-request", receipt(starting(), session(), question(40), question(40)), "duplicate-request"),
            ("cross-question", receipt(starting(), session(), question(session_id="other-session")), "cross-session"),
            ("cross-decision", receipt(starting(), session(), question(), decision(session_id="other-session")), "cross-session"),
            ("cross-prompt", receipt(starting(), session(), prompt("other-session")), "cross-session"),
            ("kind", receipt(starting(), session(), question(), decision(kind="permission")), "decision-kind-mismatch"),
            ("pending", receipt(starting(), session(), question(), prompt()), "prompt-complete-with-pending"),
            ("after", receipt(starting(), session(), prompt(), question()), "event-after-terminal"),
            ("bool-id", receipt(starting(), session(), question(True)), "invalid-request-id"),
            ("extra-answer", receipt(starting(), session(), question(), dict(decision(), answers=[{"questionId": "q1", "selectedOptionIds": ["PRIVATE_ANSWER"]}])), "unexpected-field"),
            ("cost", receipt(starting(), session(), dict(prompt(), cost="12.50")), "unexpected-field"),
            ("served", receipt({
                "event": "starting",
                "modelRequested": MODEL,
                "servedModel": "PRIVATE_SERVED_MODEL",
                "promptSHA256": DIGEST,
            }), "served-model-claim"),
            ("stop-prose", receipt(starting(), session(), prompt(stop="PRIVATE STOP")), "invalid-stop-reason"),
            ("bad-reason", line({"event": "failed", "reason": "PRIVATE provider text"}), "invalid-reason"),
            ("question-text", receipt(starting(), session(), question(prompt_text="PRIVATE\x00")), "invalid-question"),
            ("permission-prose", receipt(starting(), session(), {
                "event": "permission-required",
                "requestID": 41,
                "sessionID": SESSION,
                "options": [{"optionID": "deny", "kind": "reject_once", "name": "PRIVATE_TOOL"}],
            }), "invalid-permission"),
            ("permission-spelling", receipt(starting(), session(), {
                "event": "permission-required",
                "requestID": 41,
                "sessionID": SESSION,
                "options": [{"optionID": "deny", "kind": "allow-once"}],
            }), "invalid-permission"),
        ]
        for name, raw, expected in cases:
            with self.subTest(case=name):
                self.assertEqual(self.reason(raw), expected)

    def test_bounds_refuse_without_a_partial_verdict(self):
        pending = [starting(), session()]
        pending.extend(question(ident) for ident in range(16))
        pending.append(question(16))
        self.assertEqual(self.reason(receipt(*pending)), "event-bound-exceeded")
        unsupported = [starting(), session()]
        unsupported.extend({"event": "unsupported-request", "sessionID": SESSION} for _ in range(MAX_UNSUPPORTED_PLUS))
        self.assertEqual(self.reason(receipt(*unsupported)), "event-bound-exceeded")
        self.assertEqual(self.reason(b"{" + b"x" * CHK.MAX_FRAME), "frame-too-large")

    def test_main_file_stdin_and_bad_arguments(self):
        raw = receipt(starting(), session(), prompt())
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "receipt.jsonl"
            path.write_text(raw)
            code, output = run_main([str(path)])
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(output), self.classify(raw))
        self.assert_omits(json.loads(output), MODEL, DIGEST)

        code, output = run_main(["-"], stdin=raw.encode())
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(output)["stages"], [
            "fresh-session-transport-established",
            "prompt-complete-without-question",
        ])

        code, output = run_main([])
        self.assertEqual(code, 2)
        self.assertEqual(json.loads(output), {"event": "refused", "reason": "invalid-arguments"})

        with tempfile.TemporaryDirectory() as directory:
            missing = Path(directory) / "missing-receipt.jsonl"
            code, output = run_main([str(missing)])
        self.assertEqual(code, 2)
        self.assertEqual(json.loads(output), {"event": "refused", "reason": "unreadable-input"})

        secret = receipt(starting(), session(), question(), decision(session_id="other-session"))
        code, output = run_main(["-"], stdin=secret.encode())
        self.assertEqual(code, 2)
        self.assertEqual(output, '{"event":"refused","reason":"cross-session"}\n')
        self.assertNotIn("PRIVATE", output)
        self.assertNotIn(QUESTION_TEXT, output)

    def test_cli_refuses_list_and_object_kinds_at_both_sites(self):
        permission_list = {
            "event": "permission-required",
            "requestID": 41,
            "sessionID": SESSION,
            "options": [{"optionID": "deny", "kind": ["reject_once"]}],
        }
        permission_object = {
            "event": "permission-required",
            "requestID": 41,
            "sessionID": SESSION,
            "options": [{"optionID": "deny", "kind": {"value": "reject_once"}}],
        }
        cases = [
            (
                "permission-kind-list",
                receipt(starting(), session(), permission_list),
                '{"event":"refused","reason":"invalid-permission"}\n',
            ),
            (
                "permission-kind-object",
                receipt(starting(), session(), permission_object),
                '{"event":"refused","reason":"invalid-permission"}\n',
            ),
            (
                "decision-kind-list",
                receipt(starting(), session(), question(), decision(kind=["question"])),
                '{"event":"refused","reason":"invalid-decision"}\n',
            ),
            (
                "decision-kind-object",
                receipt(starting(), session(), question(), decision(kind={"value": "question"})),
                '{"event":"refused","reason":"invalid-decision"}\n',
            ),
        ]
        for name, raw, expected in cases:
            with self.subTest(case=name):
                code, output = run_main(["-"], stdin=raw.encode())
                self.assertEqual(code, 2)
                self.assertEqual(output, expected)
                self.assertNotIn("Traceback", output)
                self.assertNotIn("TypeError", output)
                self.assertNotIn("PRIVATE", output)
                self.assertNotIn(QUESTION_TEXT, output)

    # --- dev-env #758 permission context -----------------------------------

    def permission_stream(self, context=None, with_context=True):
        event = permission_with(context) if with_context else permission()
        return receipt(starting(), session(), event, decision(41, kind="permission"), prompt())

    def test_context_states_are_recorded_without_text(self):
        for name, context, with_context, expected in [
            ("available", PRIVATE_CONTEXT, True, "available"),
            ("available-minimal", available(), True, "available"),
            ("insufficient", INSUFFICIENT, True, "insufficient"),
            ("insufficient-redacted", REDACTED_CONTEXT, True, "insufficient"),
            ("insufficient-redacted-no-raw", {"state": "insufficient-context", "kind": "read", "title": "[REDACTED]"}, True, "insufficient"),
            ("absent", None, False, "absent"),
        ]:
            with self.subTest(case=name):
                verdict = self.classify(self.permission_stream(context, with_context))
                self.assertEqual(verdict["permissionContexts"], [{"requestID": 41, "permissionContext": expected}])
                self.assert_omits(verdict, "PRIVATE", "Run checks", "execute", "locations", "rawInput")

    def test_each_permission_request_gets_its_own_context_state(self):
        verdict = self.classify(receipt(
            starting(), session(),
            permission_with(available(), 41), permission_with(INSUFFICIENT, 42), permission(43),
            decision(43, kind="permission"), decision(41, kind="permission"), decision(42, kind="permission"),
            prompt(),
        ))
        self.assertEqual(verdict["permissionContexts"], [
            {"requestID": 41, "permissionContext": "available"},
            {"requestID": 42, "permissionContext": "insufficient"},
            {"requestID": 43, "permissionContext": "absent"},
        ])

    def test_unanswered_permission_still_records_its_context_state(self):
        verdict = self.classify(receipt(starting(), session(), permission_with(available())))
        self.assertEqual(verdict["permissionContexts"], [{"requestID": 41, "permissionContext": "available"}])
        self.assertEqual(verdict["pendingRequests"], 1)
        self.assertIs(verdict["permissionDecisionSent"], False)
        self.assertNotIn("permissionDecisionMeaning", verdict)

    def test_streams_without_permission_requests_gain_no_permission_fields(self):
        verdict = self.classify(receipt(starting(), session(), question(), decision(), prompt()))
        self.assertNotIn("permissionContexts", verdict)
        self.assertNotIn("permissionDecisionMeaning", verdict)

    def test_context_bounds_exceeded_by_one_are_refused(self):
        cases = [
            ("string-257-chars", available(title="a" * 257)),
            ("raw-string-257-chars", available(rawInput={"command": "a" * 257})),
            ("location-path-257-chars", available(locations=[{"path": "a" * 257}])),
            ("locations-9", available(locations=[{"path": "f%d.py" % n} for n in range(9)])),
            ("list-9", available(rawInput={"args": ["a"] * 9})),
            ("list-empty", available(rawInput={"args": []})),
            ("map-empty", available(rawInput={})),
            ("map-unknown-key", available(rawInput={"command": "a", "env": "b"})),
            ("depth-4", available(rawInput={"args": nested(3)})),
            ("scalar-raw-input", available(rawInput=42)),
            ("null-raw-input", available(rawInput=None)),
            ("bool-line", available(locations=[{"path": "a.py", "line": True}])),
            ("negative-line", available(locations=[{"path": "a.py", "line": -1}])),
            ("line-too-big", available(locations=[{"path": "a.py", "line": 2147483648}])),
            ("location-extra-key", available(locations=[{"path": "a.py", "column": 1}])),
            ("location-no-path", available(locations=[{"line": 1}])),
            ("location-not-object", available(locations=["a.py"])),
            ("locations-not-list", available(locations="a.py")),
            ("empty-title", available(title="")),
            ("non-string-title", available(title=7)),
            ("unknown-kind", available(kind="invented")),
            ("list-kind", available(kind=["execute"])),
            ("missing-title", {"state": "available", "kind": "execute"}),
            ("missing-kind", {"state": "available", "title": "Run checks"}),
        ]
        for name, context in cases:
            with self.subTest(case=name):
                self.assertEqual(self.reason(self.permission_stream(context)), "invalid-permission-context")
        # Boundary values just inside each bound are accepted.
        accepted = [
            ("string-256-chars", available(title="a" * 256)),
            ("locations-8", available(locations=[{"path": "f%d.py" % n} for n in range(8)])),
            ("locations-empty", available(locations=[])),
            ("list-8", available(rawInput={"args": ["a"] * 8})),
            ("depth-3", available(rawInput={"args": nested(2)})),
            ("all-six-raw-keys", available(rawInput={"command": "a", "args": ["b"], "path": "c", "paths": ["d"], "query": "e", "description": "f"})),
            ("line-max", available(locations=[{"path": "a.py", "line": 2147483647}])),
        ]
        for name, context in accepted:
            with self.subTest(case=name):
                self.assertEqual(self.classify(self.permission_stream(context))["permissionContexts"][0]["permissionContext"], "available")

    def test_context_total_size_bound_is_exact(self):
        def sized(last):
            return available(rawInput={"args": ["a" * 250] * 7 + ["a" * last]})

        def size(context):
            return len(json.dumps(context, ensure_ascii=True, separators=(",", ":")))

        last = CHK.CONTEXT_MAX - size(sized(0))
        self.assertTrue(1 <= last < CHK.CONTEXT_STRING_MAX)
        self.assertEqual(size(sized(last)), CHK.CONTEXT_MAX)
        self.assertEqual(size(sized(last + 1)), CHK.CONTEXT_MAX + 1)
        self.assertEqual(self.classify(self.permission_stream(sized(last)))["permissionContexts"][0]["permissionContext"], "available")
        self.assertEqual(self.reason(self.permission_stream(sized(last + 1))), "invalid-permission-context")

    def test_context_text_forms_are_refused(self):
        cases = [
            ("non-ascii", available(title="Caf\u00e9 checks")),
            ("non-ascii-raw", available(rawInput={"command": "caf\u00e9"})),
            ("fullwidth", available(title="\uff52\uff55\uff4e")),
            ("combining", available(title="Run\u0336 fixture")),
            ("control-nul", available(title="Run\x00fixture")),
            ("control-newline", available(title="Run\nfixture")),
            ("control-tab", available(title="Run\tfixture")),
            ("control-escape", available(rawInput={"command": "run\x1b[31m"})),
            ("del", available(title="Run\x7ffixture")),
            ("bidi-format", available(title="Run\u202efixture")),
            ("line-separator", available(title="Run\u2028fixture")),
            ("control-in-location", available(locations=[{"path": "a\x07.py"}])),
            ("absolute-path", available(locations=[{"path": "/fixture/parent/check.py"}])),
            ("relative-path", available(locations=[{"path": "dir/check.py"}])),
            ("backslash-path", available(locations=[{"path": "dir\\check.py"}])),
            ("dot-path", available(locations=[{"path": ".."}])),
            ("path-in-raw-input", available(rawInput={"path": "/fixture/parent/check.py"})),
            ("path-in-title", available(title="Read /fixture/parent/check.py")),
            ("path-in-args", available(rawInput={"args": ["../up.py"]})),
        ]
        for name, context in cases:
            with self.subTest(case=name):
                self.assertEqual(self.reason(self.permission_stream(context)), "invalid-permission-context")

    def test_unknown_or_malformed_context_is_refused(self):
        cases = [
            ("unknown-key", dict(available(), content="PRIVATE_CONTENT")),
            ("unknown-key-insufficient", dict(INSUFFICIENT, title="PRIVATE_TITLE")),
            ("insufficient-extra", dict(INSUFFICIENT, note="PRIVATE_NOTE")),
            ("insufficient-kind-only", {"state": "insufficient-context", "kind": "execute"}),
            ("redacted-wrong-title", dict(REDACTED_CONTEXT, title="Run checks")),
            ("redacted-wrong-raw", dict(REDACTED_CONTEXT, rawInput="PRIVATE_RAW")),
            ("redacted-bad-kind", dict(REDACTED_CONTEXT, kind="invented")),
            ("redacted-list-kind", dict(REDACTED_CONTEXT, kind=["execute"])),
            ("redacted-locations", dict(REDACTED_CONTEXT, locations=[])),
            ("unknown-state", {"state": "approved"}),
            ("list-state", {"state": ["available"]}),
            ("no-state", {"kind": "execute", "title": "Run checks"}),
            ("empty", {}),
            ("null", None),
            ("string", "available"),
            ("list", [available()]),
            ("number", 1),
        ]
        for name, context in cases:
            with self.subTest(case=name):
                self.assertEqual(self.reason(self.permission_stream(context)), "invalid-permission-context")

    def test_context_text_is_never_in_a_refusal(self):
        code, output = run_main(["-"], stdin=self.permission_stream(available(title="PRIVATE_TITLE" + "x" * 300)).encode())
        self.assertEqual(code, 2)
        self.assertEqual(output, '{"event":"refused","reason":"invalid-permission-context"}\n')

    def test_no_context_text_reaches_cli_output(self):
        code, output = run_main(["-"], stdin=self.permission_stream(PRIVATE_CONTEXT).encode())
        self.assertEqual(code, 0)
        self.assertNotIn("PRIVATE", output)
        self.assertEqual(json.loads(output)["permissionContexts"], [{"requestID": 41, "permissionContext": "available"}])

    def test_context_does_not_loosen_the_rest_of_the_permission_event(self):
        extra = permission_with(available())
        extra["note"] = "PRIVATE_NOTE"
        self.assertEqual(self.reason(receipt(starting(), session(), extra)), "unexpected-field")
        self.assertEqual(self.reason(receipt(starting(), session(), permission_with(available(), session_id="other-session"))), "cross-session")
        bad_options = permission_with(available())
        bad_options["options"] = [{"optionID": "deny", "kind": "allow-once"}]
        self.assertEqual(self.reason(receipt(starting(), session(), bad_options)), "invalid-permission")
        self.assertEqual(self.reason(receipt(starting(), session(), permission_with(available()), permission_with(available()))), "duplicate-request")
        self.assertEqual(self.reason(receipt(starting(), session(), permission_with(available()), prompt())), "prompt-complete-with-pending")

    def test_context_on_question_or_decision_events_is_still_refused(self):
        with_context = question()
        with_context["context"] = available()
        self.assertEqual(self.reason(receipt(starting(), session(), with_context)), "unexpected-field")
        self.assertEqual(self.reason(receipt(
            starting(), session(), permission_with(available()), dict(decision(41, kind="permission"), context=available()),
        )), "unexpected-field")

    def test_constants_match_the_session(self):
        self.assertEqual(CHK.TOOL_KINDS, ACP.TOOL_KINDS)
        self.assertEqual(CHK.RAW_FIELDS, ACP.RAW_FIELDS)
        self.assertEqual(CHK.CONTEXT_MAX, ACP.MAX_CONTEXT)
        self.assertEqual(CHK.REDACTED, ACP.REDACTED)

    # --- the three decisions stay apart ------------------------------------

    def test_reject_once_is_not_question_reply_acceptance(self):
        for context, with_context in [(available(), True), (INSUFFICIENT, True), (None, False)]:
            with self.subTest(context=context):
                verdict = self.classify(self.permission_stream(context, with_context))
                self.assertIs(verdict["permissionDecisionSent"], True)
                self.assertEqual(verdict["permissionDecisionMeaning"], "client-reject-once-only-not-approval")
                self.assertIs(verdict["permissionCountedAsQuestionReply"], False)
                self.assertEqual(verdict["permissionApproval"], "not-inferred")
                self.assertIs(verdict["questionDecisionSent"], False)
                self.assertIs(verdict["promptCompleteAfterDecision"], False)
                self.assertNotIn("correlated-question-decision-sent", verdict["stages"])
                self.assertNotIn("prompt-complete-after-decision", verdict["stages"])
                self.assertNotIn("questionRequestIDs", verdict)
                self.assertNotIn("questionOrigin", verdict)
                self.assertEqual(verdict["autonomousQuestionReplyReason"], "no-correlated-question-decision")
                self.assertIs(verdict["autonomousQuestionReply"], False)

    def test_question_stage_needs_a_question_decision_even_beside_a_permission_one(self):
        verdict = self.classify(receipt(
            starting(), session(), question(), permission_with(available()),
            decision(41, kind="permission"), decision(40), prompt(),
        ))
        self.assertEqual(verdict["questionRequestIDs"], [40])
        self.assertEqual(verdict["permissionRequestIDs"], [41])
        self.assertIs(verdict["permissionCountedAsQuestionReply"], False)
        # A permission decision cannot stand in for the question's decision.
        self.assertEqual(self.reason(receipt(
            starting(), session(), question(), permission_with(available()), decision(40, kind="permission"),
        )), "decision-kind-mismatch")
        self.assertEqual(self.reason(receipt(
            starting(), session(), permission_with(available()), decision(41, kind="question"),
        )), "decision-kind-mismatch")

    def test_an_approve_decision_cannot_be_carried_by_a_receipt(self):
        # decision-sent has no option field. The session never sends allow_once or
        # allow_always, so a receipt that names one is refused, never counted.
        for option in ("allow", "allow_once", "allow_always", "deny", "reject_once"):
            for key in ("optionID", "optionId", "kind_detail"):
                with self.subTest(option=option, key=key):
                    event = dict(decision(41, kind="permission"))
                    event[key] = option
                    self.assertEqual(self.reason(receipt(
                        starting(), session(), permission_with(available()), event,
                    )), "unexpected-field")
        for kind in ("allow_once", "allow_always", "approve", "reject_once"):
            with self.subTest(kind=kind):
                self.assertEqual(self.reason(receipt(
                    starting(), session(), permission_with(available()), decision(41, kind=kind),
                )), "invalid-decision")

    # --- receipts from the real session -------------------------------------

    def session_events(self, tool, ident=41):
        sent, events = [], []
        live = ACP.Session(sent.append, events.append, "/synthetic/project", "Inspect fixture", "grok-synthetic")
        live.start()
        live.provider({"jsonrpc": "2.0", "id": 1, "result": {"protocolVersion": 1}})
        live.provider({"jsonrpc": "2.0", "id": 2, "result": {"sessionId": SESSION}})
        message = {"sessionId": SESSION, "options": [
            {"optionId": "yes", "kind": "allow_once", "name": "Allow"},
            {"optionId": "no", "kind": "reject_once", "name": "Deny"},
        ]}
        if tool is not None:
            message["toolCall"] = tool
        live.provider({"jsonrpc": "2.0", "id": ident, "method": "session/request_permission", "params": message})
        live.decision({"requestID": ident, "sessionID": SESSION, "kind": "permission", "optionID": "no"})
        live.provider({"jsonrpc": "2.0", "id": 3, "result": {"stopReason": "end_turn"}})
        return events

    def test_real_session_receipts_pass_end_to_end(self):
        good = {
            "toolCallId": "fixture-call", "kind": "execute", "title": "Run fixture checks",
            "locations": [{"path": "/fixture-parent/project/check.py", "line": 12}],
            "rawInput": {"command": "python3", "args": ["check.py"], "path": "/fixture-parent/project/check.py"},
        }
        cases = [
            ("available", good, "available"),
            ("missing-toolcall", None, "insufficient"),
            ("unknown-kind", dict(good, kind="invented"), "insufficient"),
            ("oversized-title", dict(good, title="x" * 257), "insufficient"),
            ("non-ascii-title", dict(good, title="Caf\u00e9 checks"), "insufficient"),
            ("control-title", dict(good, title="Run\x1b[31m fixture"), "insufficient"),
            ("secret-title", dict(good, title="Use Bearer synthetic-sensitive-value"), "insufficient"),
            ("secret-raw-input", dict(good, rawInput={"command": "run", "args": ["--password=synthetic-sensitive-value"]}), "insufficient"),
            ("dot-argument", dict(good, rawInput={"command": "rg", "args": ["TODO", "."]}), "available"),
            ("dot-title", dict(good, title=".."), "available"),
            ("dot-command", dict(good, rawInput={"command": "."}), "available"),
        ]
        for name, tool, expected in cases:
            with self.subTest(case=name):
                events = self.session_events(tool)
                raw = receipt(*events)
                emitted = [e for e in events if e["event"] == "permission-required"][0]
                self.assertIn("context", emitted)
                # The checker must record exactly the state the session emitted.
                self.assertEqual(expected, {"available": "available", "insufficient-context": "insufficient"}[emitted["context"]["state"]])
                verdict = CHK.classify(raw)
                self.assertEqual(verdict["permissionContexts"], [{"requestID": 41, "permissionContext": expected}])
                self.assertIs(verdict["permissionDecisionSent"], True)
                self.assertIs(verdict["questionDecisionSent"], False)
                self.assertIs(verdict["autonomousQuestionReply"], False)
                self.assert_omits(verdict, "Run ", "checks", "synthetic-sensitive-value", "check.py", "python3")

    def test_real_session_context_fixtures_are_all_accepted(self):
        # Every projection the session's own context tests produce must pass.
        fixtures = importlib.util.spec_from_file_location(
            "test_cursor_acp_session", Path(__file__).with_name("test_cursor_acp_session.py")
        )
        module = importlib.util.module_from_spec(fixtures)
        fixtures.loader.exec_module(module)
        self.assertGreater(len(module.CONTEXT_FIXTURES), 40)
        for name, tool in module.CONTEXT_FIXTURES.items():
            with self.subTest(fixture=name):
                events = self.session_events(tool)
                verdict = CHK.classify(receipt(*events))
                state = verdict["permissionContexts"][0]["permissionContext"]
                emitted = [e for e in events if e["event"] == "permission-required"][0]["context"]["state"]
                self.assertEqual(state, {"available": "available", "insufficient-context": "insufficient"}[emitted])
                self.assert_omits(verdict, "DO_NOT_EXPORT", "customer")


MAX_UNSUPPORTED_PLUS = CHK.MAX_UNSUPPORTED + 1


class Stdin:
    def __init__(self, data):
        self.buffer = io.BytesIO(data)


def run_main(argv, stdin=b""):
    old_in, old_out = sys.stdin, sys.stdout
    sys.stdin = Stdin(stdin)
    sys.stdout = io.StringIO()
    try:
        code = CHK.main(argv)
        return code, sys.stdout.getvalue()
    finally:
        sys.stdin, sys.stdout = old_in, old_out


if __name__ == "__main__":
    unittest.main()
