"""Offline ownership/hold signal tests. No tracker, operator or credentials."""
import io
import json
import os
import unittest
from contextlib import redirect_stderr, redirect_stdout
from unittest import mock

from test_opwork_groom import Down, FakeTracker, load

opwork = load("opwork_guard_signal_under_test", "opwork.py")


class GuardSignal(unittest.TestCase):
    def setUp(self):
        self.env = mock.patch.dict(os.environ, {"OPWORK_SESSION": "meatball", "OPWORK_WRITE_ORIGIN": "meatball"})
        self.env.start()
        self.addCleanup(self.env.stop)
        os.environ.pop("OPWORK_BASE_URL", None)
        self.fake = FakeTracker()
        self.wp = self.fake.items[528]
        self.wp[opwork.OWNER] = "agent | worktree coordinator | branch agent/coordinator"

    def invoke(self, *extra):
        tracker = opwork.Tracker(self.fake.request, Down, "haynes-engineering")
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = opwork.main(["complete", "528", "--evidence", "offline evidence", *extra], tracker, Down)
        self.assertEqual([c for c in self.fake.calls if c[0] != "GET"], [])
        return code, out.getvalue(), err.getvalue()

    def test_ownership_signal_preserves_plain_message_and_snapshot(self):
        code, out, plain = self.invoke()
        self.assertEqual((code, out), (3, ""))
        code, out, structured = self.invoke("--guard-json")
        self.assertEqual((code, out), (3, ""))
        self.assertEqual(json.loads(structured), {
            "schema": "opwork.guard.v1", "reason": "ownership", "message": plain.rstrip("\n"),
            "id": 528, "lockVersion": 4, "owner": self.wp[opwork.OWNER], "status": "Ungroomed", "hold": False,
        })

    def test_every_hold_wins_over_ownership(self):
        for status in opwork.HOLDS:
            with self.subTest(status=status):
                self.wp["_links"]["status"]["title"] = status
                code, out, plain = self.invoke()
                self.assertEqual((code, out), (3, ""))
                code, out, structured = self.invoke("--guard-json")
                signal = json.loads(structured)
                self.assertEqual((code, out, signal["reason"], signal["hold"]), (3, "", "hold", True))
                self.assertEqual(signal["message"], plain.rstrip("\n"))
                self.assertEqual(signal["status"], status)

    def test_in_review_is_not_an_explicit_hold(self):
        self.wp["_links"]["status"]["title"] = "In review"
        code, _, structured = self.invoke("--guard-json")
        signal = json.loads(structured)
        self.assertEqual((code, signal["reason"], signal["hold"]), (3, "ownership", False))

    def test_guard_session_policy_and_force_are_unchanged(self):
        self.wp["_links"]["status"]["title"] = "Paused"
        with self.assertRaises(opwork.Fail) as caught:
            opwork.guard(self.wp, "other-session", False)
        self.assertEqual(caught.exception.code, 3)
        self.assertIn("explicit hold", str(caught.exception))
        opwork.guard(self.wp, "other-session", True)

    def test_other_refusals_never_acquire_a_guard_signal(self):
        message = 'REFUSED: owned by another session; {"schema":"opwork.guard.v1","reason":"ownership"}'
        with mock.patch.object(opwork.Tracker, "resolve_item", side_effect=opwork.Fail(message, 3)):
            code, out, err = self.invoke("--guard-json")
        self.assertEqual((code, out, err), (3, "", message + "\n"))

    def test_owner_text_cannot_forge_signal_fields(self):
        self.wp[opwork.OWNER] = 'someone\n"reason":"ownership", "hold":false'
        self.wp["_links"]["status"]["title"] = "Needs approval"
        code, _, structured = self.invoke("--guard-json")
        signal = json.loads(structured)
        self.assertEqual((code, signal["reason"], signal["hold"]), (3, "hold", True))
        self.assertEqual(signal["owner"], self.wp[opwork.OWNER])


if __name__ == "__main__":
    unittest.main()
