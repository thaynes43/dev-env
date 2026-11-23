"""Offline coverage of the scoped handoff projection."""
import io
import json
import os
import unittest
from contextlib import redirect_stderr, redirect_stdout
from datetime import datetime, timezone
from pathlib import Path
from unittest import mock

from test_opwork_groom import Down, FakeTracker, load

opwork = load("opwork_handoff_under_test", "opwork.py")
OWNER = "agent | worktree worker | branch agent/worker"
ITEM = "/api/v3/work_packages/528"
PRIVATE = "private-upstream-text"


def checkpoint(author=OWNER, **fields):
    fields = {"check_after": "2099-01-01T00:00:00Z", "next_action": PRIVATE, "evidence": PRIVATE, **fields}
    raw = "**[%s]** Structured checkpoint\n\nopwork:checkin=%s\n\n(opwork:note=0123456789ab)" % (author, json.dumps(fields))
    return {"id": 90, "createdAt": "2025-01-01T00:00:00Z", "comment": {"raw": raw},
            "_links": {"user": {"href": "/api/v3/users/5", "title": PRIVATE}}}


class HandoffProjection(unittest.TestCase):
    def setUp(self):
        self.env = mock.patch.dict(os.environ, {"OPWORK_BASE_URL": ""})
        self.env.start()
        self.addCleanup(self.env.stop)
        self.fake = FakeTracker()
        self.item = self.fake.items[528]
        self.item.update(updatedAt="2025-01-01T00:00:00Z", customField2=OWNER, subject=PRIVATE)
        self.item["_links"]["status"]["title"] = "In progress"

    def invoke(self, request=None, extra=()):
        out, err = io.StringIO(), io.StringIO()
        tracker = opwork.Tracker(request or self.fake.request, Down)
        with redirect_stdout(out), redirect_stderr(err):
            code = opwork.main(["show", "528", "--handoff-json", *extra], tracker, Down)
        self.assertTrue(all(method == "GET" for method, _, _ in self.fake.calls))
        self.assertNotIn(PRIVATE, out.getvalue() + err.getvalue())
        return code, out.getvalue(), err.getvalue().strip()

    def document(self, request=None):
        code, out, err = self.invoke(request)
        self.assertEqual((code, err), (0, ""))
        return json.loads(out)

    def test_shared_handoff_contract(self):
        # TestSharedHandoffContract in resources_test.go reads the same fixture.
        root = Path(__file__).resolve().parents[4]
        fixture = json.loads((root / "internal/frontdoor/testdata/handoff-contract.json").read_text(encoding="utf-8"))
        self.assertEqual(opwork.BASE_URL, fixture["trackerBaseURL"])

        self.item["_links"]["status"]["title"] = PRIVATE
        statuses = opwork.HANDOFF_STATES | {self.document()["workState"]["status"]}
        self.assertEqual(statuses, set(fixture["statuses"]))

        now = datetime(2026, 11, 23, tzinfo=timezone.utc)
        histories = (
            ([], True),
            ([checkpoint()], True),
            ([checkpoint(stale=True)], True),
            ([checkpoint(ack=False)], True),
            ([checkpoint(author="previous-worker")], True),
            ([checkpoint(check_after="tomorrow")], True),
            ([], False),
        )
        states = {opwork.handoff_checkpoint(acts, complete, OWNER, now)[0]
                  for acts, complete in histories}
        self.assertEqual(states, set(fixture["checkpointStates"]))

        actions = {opwork.handoff_next_action(status, owner, state)
                   for status in statuses for owner in (OWNER, "Unknown") for state in states}
        self.assertEqual(actions, set(fixture["nextActions"]))

    def test_project_fence_and_metadata_only_projection(self):
        self.fake.acts[528] = [checkpoint()]
        doc = self.document()
        self.assertEqual(doc["schema"], "dev-env.work-item-handoff")
        self.assertEqual(doc["version"], 1)
        self.assertEqual(doc["owner"], OWNER)
        self.assertEqual(doc["targetScope"], {"project": "haynes-engineering", "item": 528})
        self.assertEqual(doc["workState"], {"status": "In progress", "hold": False})
        self.assertEqual(doc["source"]["checkpoint"], "present")
        self.assertEqual(doc["source"]["lockVersion"], 4)
        self.assertIsNotNone(opwork.utc_time(doc["capturedAt"]))
        self.assertEqual(doc["evidence"], [{"kind": "structured-checkpoint", "activityId": 90,
                                          "createdAt": "2025-01-01T00:00:00+00:00"}])
        self.assertIn("Confirm custody with the recorded owner", doc["nextAction"])
        self.assertEqual([path for _, path, _ in self.fake.calls], [
            ITEM, "/api/v3/projects/haynes-engineering", ITEM + "/activities?pageSize=100&offset=1", ITEM])

    def test_same_label_from_another_tracker_user_is_present(self):
        earlier = checkpoint()
        later = checkpoint()
        later.update(id=91, createdAt="2025-01-02T00:00:00Z")
        later["_links"]["user"]["href"] = "/api/v3/users/6"
        self.fake.acts[528] = [earlier, later]
        doc = self.document()
        self.assertEqual(doc["source"]["checkpoint"], "present")
        self.assertEqual(doc["evidence"][0]["activityId"], 91)
        self.assertEqual(doc["nextAction"],
                         "A checkpoint carries the recorded owner's label. Its author is unverified. "
                         "Confirm custody with the recorded owner before acting.")

    def test_explicit_hold_has_priority(self):
        self.fake.acts[528] = [checkpoint()]
        self.item["_links"]["status"]["title"] = "Needs approval"
        doc = self.document()
        self.assertTrue(doc["workState"]["hold"])
        self.assertIn("Keep the recorded hold", doc["nextAction"])

    def test_stale_missing_malformed_or_unmatched_checkpoint(self):
        for acts, expected in (([], "absent"), ([checkpoint(stale=True)], "stale"),
                               ([checkpoint(check_after="2025-01-02T00:00:00Z")], "stale"),
                               ([checkpoint(ack=False)], "blocked"),
                               ([checkpoint(author="previous-worker")], "owner-unconfirmed"),
                               ([checkpoint(check_after="tomorrow")], "malformed")):
            with self.subTest(expected=expected):
                self.fake.acts[528] = acts
                doc = self.document()
                self.assertEqual(doc["source"]["checkpoint"], expected)
                self.assertNotIn("Its author is unverified.", doc["nextAction"])

    def test_blocked_or_missing_ack_takes_priority_over_stale(self):
        for flags in ({"blocked": True}, {"ack": False}):
            with self.subTest(flags=flags):
                self.fake.acts[528] = [checkpoint(check_after="2025-01-02T00:00:00Z", **flags)]
                doc = self.document()
                self.assertEqual(doc["source"]["checkpoint"], "blocked")
                self.assertEqual(doc["nextAction"],
                                 "Keep the work paused until its current state and next action are verified.")

    def test_incomplete_history_reports_uncertainty(self):
        self.fake.acts[528] = [checkpoint()]

        def request(method, path, payload=None):
            code, doc = self.fake.request(method, path, payload)
            if "/activities?" in path:
                doc["total"] = 101
            return code, doc

        doc = self.document(request)
        self.assertEqual(doc["source"]["checkpoint"], "incomplete")
        self.assertEqual(doc["evidence"], [])

    def test_complete_history_can_include_visible_journal(self):
        note = checkpoint()
        note["comment"] = {"raw": PRIVATE}
        self.fake.acts[528] = [note]
        doc = self.document()
        self.assertEqual(doc["source"]["checkpoint"], "absent")
        self.assertEqual(doc["evidence"], [{"kind": "visible-journal", "activityId": 90,
                                          "createdAt": "2025-01-01T00:00:00+00:00"}])

    def test_unknown_workflow_values_are_not_returned(self):
        self.item["customField2"] = PRIVATE
        self.item["_links"]["status"]["title"] = PRIVATE
        doc = self.document()
        self.assertEqual(doc["owner"], "Unknown")
        self.assertEqual(doc["workState"], {"status": "Unknown", "hold": None})
        self.assertIn("paused", doc["nextAction"])

    def test_cross_project_stops_before_activity_read(self):
        self.item["_links"]["project"]["href"] = "/api/v3/projects/2"
        self.assertEqual(self.invoke(), (3, "", "handoff read refused"))
        self.assertFalse(any("/activities" in path for _, path, _ in self.fake.calls))

    def test_refused_absent_and_unavailable_are_distinct(self):
        for status, route, exit_code, reason in ((401, ITEM, 3, "handoff read refused"),
                                                 (403, ITEM + "/activities", 3, "handoff read refused"),
                                                 (404, ITEM, 4, "resource not found"),
                                                 (404, ITEM + "/activities", 5, "handoff tracker unavailable"),
                                                 (503, ITEM, 5, "handoff tracker unavailable")):
            with self.subTest(status=status, route=route):
                def request(method, path, payload=None):
                    if path.split("?")[0] == route:
                        return status, {"message": PRIVATE}
                    return self.fake.request(method, path, payload)
                self.assertEqual(self.invoke(request), (exit_code, "", reason))

    def test_changed_item_is_unavailable(self):
        def request(method, path, payload=None):
            if "/activities?" in path:
                self.item["lockVersion"] += 1
            return self.fake.request(method, path, payload)
        self.assertEqual(self.invoke(request), (5, "", "handoff tracker unavailable"))

    def test_legacy_history_flags_cannot_broaden_projection(self):
        result = self.invoke(extra=("--history",))
        self.assertEqual((result[0], result[1]), (1, ""))
        self.assertFalse(self.fake.calls)
