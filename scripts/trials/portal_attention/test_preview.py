"""Finite, offline presentation threat fixtures; no auth/runtime acceptance."""

import copy
import contextlib
import hashlib
import base64
import io
from html.parser import HTMLParser
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import preview


class Page(HTMLParser):
    def __init__(self, source):
        super().__init__()
        self.tags = []
        self.words = []
        self.feed(source)

    def handle_starttag(self, tag, attrs):
        self.tags.append((tag, dict(attrs)))

    def handle_data(self, data):
        self.words.append(data)


class PreviewTests(unittest.TestCase):
    def setUp(self):
        self.document = preview.load(Path(__file__).with_name("fixtures.json"))

    def test_all_four_groups_and_required_evidence(self):
        page = Page(preview.render(self.document))
        words = " ".join(page.words)
        for group in preview.GROUPS.values():
            self.assertIn(group, words)
        for field in ("Owner", "Next actor", "Next action", "Source age", "Last verified", "Reviewed head"):
            self.assertEqual(page.words.count(field), 4)
        ids = [attrs["id"] for _, attrs in page.tags if "id" in attrs]
        self.assertEqual(len(ids), len(set(ids)))

    def test_worker_exit_does_not_decide_task_outcome_or_group(self):
        row = self.document["items"][3]
        for state in ("Exited", "Failed", "Busy", "Unknown"):
            row["workerState"] = state
            page = preview.render(self.document)
            self.assertIn("Unverified; handoff missing", page)
            self.assertIn("Handoff missing · 1", page)

    def test_all_metadata_strings_are_inert_html(self):
        attack = '<script>alert("fixture")</script><img src="https://invalid.example/x" onerror="alert(1)">'
        for field in ("title", "owner", "nextActor", "nextAction", "workerState", "taskOutcome"):
            with self.subTest(field=field):
                doc = copy.deepcopy(self.document)
                doc["items"][0][field] = attack
                page = Page(preview.render(doc))
                self.assertIn(attack, " ".join(page.words))
                self.assertFalse({"script", "img"} & {tag for tag, _ in page.tags})
                self.assertFalse(any(key.startswith("on") for _, attrs in page.tags for key in attrs))

    def test_terminal_and_direction_controls_are_not_interpreted(self):
        self.document["items"][0]["title"] = "\x1b[31mReview\x1b[0m\x1b]52;synthetic\x07\u202e"
        page = preview.render(self.document)
        self.assertNotIn("\x1b", page)
        self.assertNotIn("\x07", page)
        self.assertNotIn("\u202e", page)
        self.assertIn("Review", page)

    def test_control_only_ownership_cannot_become_a_blank_owner(self):
        self.document["items"][0]["owner"] = "\x1b[0m\x00\u202e"
        page = preview.render(self.document)
        self.assertIn("<dt>Owner</dt><dd>Unknown</dd>", page)

    def test_format_only_or_empty_ownership_is_unknown_and_legitimate_text_survives(self):
        for value in ("", " ", "\u200b", "\u200e", "\ufeff", "\u2060", "\u200c\u200d"):
            with self.subTest(value=repr(value)):
                self.document["items"][0]["owner"] = value
                self.assertIn("<dt>Owner</dt><dd>Unknown</dd>", preview.render(self.document))
        self.document["items"][0]["owner"] = "Renée – 漢字 🚀"
        self.assertIn("<dt>Owner</dt><dd>Renée – 漢字 🚀</dd>", preview.render(self.document))

    def test_link_accessible_names_include_distinct_row_context(self):
        page = Page(preview.render(self.document))
        labels = [attrs["aria-label"] for tag, attrs in page.tags if tag == "a"]
        self.assertEqual(len(set(labels)), 4)
        for row, label in zip(self.document["items"], labels):
            self.assertIn(row["title"], label)
            self.assertIn(row["id"], label)

    def test_observation_time_has_machine_readable_datetime(self):
        page = Page(preview.render(self.document))
        times = [attrs["datetime"] for tag, attrs in page.tags if tag == "time"]
        self.assertEqual(times, [self.document["asOf"]])

    def test_links_are_constructed_only_from_numeric_canonical_ids(self):
        self.document["items"][0]["nextAction"] = "javascript:alert(1) https://invalid.example/"
        page = Page(preview.render(self.document))
        links = [attrs["href"] for tag, attrs in page.tags if tag == "a"]
        self.assertEqual(links, ["https://work.haynesnetwork.com/work_packages/178"] * 4)
        for bad in ("javascript:alert(1)", "https://invalid.example/", "178?token=synthetic", -1, True, 2**31):
            with self.subTest(value=bad):
                self.document["items"][0]["canonicalWorkPackage"] = bad
                with self.assertRaises(ValueError):
                    preview.render(self.document)

    def test_extra_payload_and_secret_fields_are_rejected_without_echo(self):
        for field in ("prompt", "transcript", "accessToken", "refreshToken", "sourceUrl", "command", "role"):
            with self.subTest(field=field):
                doc = copy.deepcopy(self.document)
                doc["items"][0][field] = "SYNTHETIC-REJECTED-VALUE"
                with self.assertRaises(ValueError) as error:
                    preview.render(doc)
                self.assertNotIn("SYNTHETIC-REJECTED-VALUE", str(error.exception))
        self.document["credentials"] = {"synthetic": True}
        with self.assertRaises(ValueError):
            preview.render(self.document)

    def test_missing_ownership_and_action_are_visibly_unknown(self):
        row = self.document["items"][0]
        for field in ("owner", "nextActor", "nextAction", "sourceUpdatedAt", "verifiedAt"):
            row[field] = None
        page = preview.render(self.document)
        self.assertIn("Freshness unknown", page)
        self.assertGreaterEqual(page.count("<dd>Unknown</dd>"), 5)

    def test_source_age_and_last_verification_are_independent(self):
        row = self.document["items"][0]
        now = preview.timestamp(self.document["asOf"])
        self.assertEqual(preview.age(row["sourceUpdatedAt"], now), "10m")
        self.assertEqual(preview.age(row["verifiedAt"], now), "1m")
        self.assertEqual(preview.freshness(row["verifiedAt"], now)[0], "fresh")

    def test_older_verification_does_not_certify_a_newer_source_update(self):
        self.document["items"][0]["sourceUpdatedAt"] = "2026-10-23T14:18:00Z"
        self.assertIn("Verification predates source update", preview.render(self.document))

    def test_freshness_boundary_and_future_time(self):
        now = preview.timestamp(self.document["asOf"])
        for stamp, expected in ((None, "unknown"), ("2026-10-23T14:18:11Z", "unknown"),
                                ("2026-10-23T14:13:10Z", "fresh"), ("2026-10-23T14:13:09Z", "stale")):
            with self.subTest(stamp=stamp):
                self.assertEqual(preview.freshness(stamp, now)[0], expected)
        self.assertEqual(preview.age("2026-10-23T14:18:11Z", now), "Unknown (future timestamp)")

    def test_failed_read_is_never_verified_empty(self):
        self.document["items"] = []
        for state in ("denied", "unavailable"):
            self.document["readState"] = state
            page = preview.render(self.document)
            self.assertIn("Attention unavailable", page)
            self.assertIn("Current task state is Unknown", page)
            self.assertNotIn("Verified empty", page)
        self.document["readState"] = "verified"
        self.assertIn("Verified empty fixture", preview.render(self.document))

    def test_failed_read_cannot_supply_verified_rows(self):
        self.document["readState"] = "denied"
        with self.assertRaises(ValueError):
            preview.render(self.document)

    def test_row_text_and_input_byte_bounds(self):
        self.document["items"][0]["title"] = "x" * preview.MAX_TEXT
        preview.render(self.document)
        self.document["items"][0]["title"] += "x"
        with self.assertRaises(ValueError):
            preview.render(self.document)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "large.json"
            path.write_bytes(b" " * (preview.MAX_BYTES + 1))
            with self.assertRaisesRegex(ValueError, "byte bound"):
                preview.load(path)

    def test_row_count_bound_and_duplicate_id(self):
        row = self.document["items"][0]
        self.document["items"] = [dict(row, id=f"row-{i}") for i in range(preview.MAX_ROWS)]
        preview.render(self.document)
        self.document["items"].append(dict(row, id="overflow"))
        with self.assertRaises(ValueError):
            preview.render(self.document)
        self.document["items"] = [row, row]
        with self.assertRaisesRegex(ValueError, "duplicate"):
            preview.render(self.document)

    def test_duplicate_json_members_and_invalid_utf8(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "invalid.json"
            for content in (b'{"version":1,"version":2}', b'\xff'):
                path.write_bytes(content)
                with self.assertRaises(ValueError):
                    preview.load(path)

    def test_deep_json_fails_with_bounded_generic_error(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "nested.json"
            path.write_bytes(b"[" * 2000 + b"0" + b"]" * 2000)
            with self.assertRaisesRegex(ValueError, "^invalid synthetic JSON fixture$"):
                preview.load(path)

    def test_cli_recursion_failure_never_echoes_input_or_writes_output(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "preview.html"
            errors = io.StringIO()
            with mock.patch("sys.argv", ["preview.py", "--output", str(output)]), \
                    mock.patch.object(preview, "load", side_effect=RecursionError("SYNTHETIC-NOT-FOR-OUTPUT")), \
                    contextlib.redirect_stderr(errors):
                with self.assertRaises(SystemExit) as result:
                    preview.main()
            self.assertEqual(result.exception.code, 1)
            self.assertEqual(errors.getvalue(), "Invalid or unavailable synthetic fixture; no output written.\n")
            self.assertFalse(output.exists())

    def test_invalid_schema_fields_fail_closed(self):
        for field, value in (("group", "approved"), ("id", "\" onclick=alert(1)"),
                             ("sourceUpdatedAt", "2026-10-23"), ("verifiedAt", "2026-02-31T00:00:00Z"),
                             ("reviewHead", "main"), ("owner", {"admin": True})):
            with self.subTest(field=field):
                doc = copy.deepcopy(self.document)
                doc["items"][0][field] = value
                with self.assertRaises(ValueError):
                    preview.render(doc)
        for field, value in (("version", True), ("asOf", None), ("readState", "healthy"), ("items", {})):
            with self.subTest(field=field):
                doc = copy.deepcopy(self.document)
                doc[field] = value
                with self.assertRaises(ValueError):
                    preview.render(doc)

    def test_rendered_review_head_and_inert_evidence_badge_for_each_group(self):
        # Presentation only: no claim to detect actual review/release/authorization state.
        row = self.document["items"][2]
        self.document["items"] = [row]
        for group in preview.GROUPS:
            with self.subTest(group=group):
                row["group"] = group
                page = Page(preview.render(self.document))
                self.assertEqual(page.words.count("a" * 40), 1)
                self.assertFalse({"button", "form", "input"} & {tag for tag, _ in page.tags})
                badges = [attrs["class"] for tag, attrs in page.tags if tag == "span"]
                self.assertEqual(badges, ["freshness stale"])

    def test_inert_page_has_matching_style_hash_and_no_active_surface(self):
        page = Page(preview.render(self.document))
        self.assertFalse({"script", "iframe", "img", "form", "input", "button", "object", "embed", "link"} & {tag for tag, _ in page.tags})
        policies = [attrs["content"] for tag, attrs in page.tags if tag == "meta" and attrs.get("http-equiv") == "Content-Security-Policy"]
        digest = base64.b64encode(hashlib.sha256(preview.STYLE.encode()).digest()).decode()
        self.assertEqual(len(policies), 1)
        self.assertIn("default-src 'none'", policies[0])
        self.assertIn("style-src 'sha256-" + digest + "'", policies[0])
        self.assertIn("form-action 'none'", policies[0])


if __name__ == "__main__":
    unittest.main()
