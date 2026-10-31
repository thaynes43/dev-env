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
