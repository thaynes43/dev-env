"""Offline tests for the tracker guard: body cap, scan bounds, and the priorities route.

Run: python3 -I -B -m unittest discover -s images/agent/tools/openproject -p 'test_opwork_guard.py' -v

No tracker call and no kubectl. opwork's real request() is used with a fake HTTP transport.
"""
import io
import json
import os
import tempfile
import unittest
from unittest import mock

from test_opwork_groom import load

opwork = load("opwork_guard_under_test", "opwork.py")


class FakeHTTP:
    def __init__(self, code, payload):
        self.code = code
        self.body = payload if isinstance(payload, bytes) else json.dumps(payload).encode()

    def read(self, n=-1):
        if n is None or n < 0:
            n = len(self.body)
        chunk, self.body = self.body[:n], self.body[n:]
        return chunk

    def close(self):
        return None


class BodyAndPriorities(unittest.TestCase):
    def setUp(self):
        self.api = opwork.load_api()
        self.api._token = None

    def opener(self, transport):
        opener = mock.Mock()
        opener.open.side_effect = transport
        return opener

    def test_oversized_body_is_rejected_before_json(self):
        marker = b"MARKER-NOT-A-TOKEN"
        payload = marker + b"Z" * (self.api.MAX_RESPONSE_BYTES + 1)
        seen = []

        def transport(req, timeout=None):
            seen.append(req.full_url)
            return FakeHTTP(200, payload)

        with mock.patch.object(self.api, "token", return_value="opapi-" + "ab" * 32), \
                mock.patch.object(self.api.urllib.request, "build_opener", return_value=self.opener(transport)):
            with self.assertRaises(self.api.ApiUnavailable) as caught:
                self.api.request("GET", "/api/v3/work_packages/1")
        self.assertEqual(str(caught.exception), "OpenProject response exceeds %d bytes" % self.api.MAX_RESPONSE_BYTES)
        self.assertNotIn("MARKER", str(caught.exception))
        self.assertEqual(len(seen), 1)

    def test_small_body_is_decoded_and_its_size_is_kept(self):
        raw = b'{"id":1,"ok":true}'
        self.assertLess(len(raw), self.api.MAX_RESPONSE_BYTES)

        def transport(req, timeout=None):
            return FakeHTTP(200, raw)

        with mock.patch.object(self.api, "token", return_value="opapi-" + "ab" * 32), \
                mock.patch.object(self.api.urllib.request, "build_opener", return_value=self.opener(transport)):
            code, data = self.api.request("GET", "/api/v3/work_packages/1")
        self.assertEqual(code, 200)
        self.assertEqual(data, {"id": 1, "ok": True})
        self.assertEqual(self.api.request.last_body_bytes, len(raw))

    def test_priority_item_path_is_refused_before_http(self):
        def boom(*args, **kwargs):
            raise AssertionError("http was reached")

        with mock.patch.object(self.api, "token", side_effect=boom), \
                mock.patch.object(self.api.urllib.request, "build_opener", side_effect=boom):
            for path in ("/api/v3/priorities/3", "/api/v3/prioritiesevil"):
                with self.assertRaises(AssertionError) as caught:
                    self.api.request("GET", path)
                self.assertIn("Unsupported API path", str(caught.exception))

    def test_order_uses_the_real_priorities_route(self):
        transport = OrderTransport()
        env = mock.patch.dict(os.environ, {"OPWORK_SESSION": "meatball"}, clear=False)
        env.start()
        self.addCleanup(env.stop)
        os.environ.pop("OPWORK_WRITE_ORIGIN", None)
        os.environ.pop("OPWORK_BASE_URL", None)
        with tempfile.TemporaryDirectory() as directory:
            os.environ["OPWORK_STATE_DIR"] = directory
            self.addCleanup(os.environ.pop, "OPWORK_STATE_DIR", None)
            tracker = opwork.Tracker(self.api.request, self.api.ApiUnavailable, "haynes-engineering")
            with mock.patch.object(self.api, "token", return_value="opapi-" + "ab" * 32), \
                    mock.patch.object(self.api.urllib.request, "build_opener", return_value=self.opener(transport.open)):
                code, out, err = invoke(tracker, ["order", "528", "--priority", "High"])
        self.assertEqual(code, 0, err)
        self.assertIn("priority: High", out)
        self.assertIn(("GET", "/api/v3/priorities"), transport.seen)
        self.assertFalse(any(path.startswith("/api/v3/priorities/") for _, path in transport.seen))
        self.assertEqual(transport.wp["_links"]["priority"]["href"], "/api/v3/priorities/3")


class OrderTransport:
    def __init__(self):
        self.seen = []
        self.notes = []
        self.wp = {
            "id": 528, "lockVersion": 4, "subject": "Front door",
            "description": {"raw": "old scope"}, "customField2": "",
            "customField4": {"raw": ""}, "customField5": {"raw": ""},
            "_links": {
                "status": {"title": "Ungroomed", "href": "/api/v3/statuses/5"},
                "type": {"title": "Story", "href": "/api/v3/types/3"},
                "parent": {"href": "/api/v3/work_packages/516"},
                "project": {"href": "/api/v3/projects/1", "title": "haynes-engineering"},
                "priority": {"title": "Normal", "href": "/api/v3/priorities/2"},
            },
        }

    def open(self, req, timeout=None):
        path = req.full_url.split(self_origin(), 1)[-1]
        base = path.split("?", 1)[0]
        self.seen.append((req.get_method(), base))
        method = req.get_method()
        if method == "GET" and base == "/api/v3/projects/haynes-engineering":
            return FakeHTTP(200, {"id": 1, "identifier": "haynes-engineering"})
        if method == "GET" and base == "/api/v3/priorities":
            return FakeHTTP(200, {"_embedded": {"elements": [
                {"name": "Low", "id": 1}, {"name": "Normal", "id": 2},
                {"name": "High", "id": 3}, {"name": "Immediate", "id": 4}]}})
        if method == "GET" and base == "/api/v3/work_packages/528/activities":
            return FakeHTTP(200, {"_embedded": {"elements": list(self.notes)}, "total": len(self.notes)})
        if base == "/api/v3/work_packages/528" and method == "GET":
            return FakeHTTP(200, self.wp)
        if base == "/api/v3/work_packages/528" and method == "PATCH":
            payload = json.loads(req.data.decode())
            href = payload["_links"]["priority"]["href"]
            self.wp["lockVersion"] += 1
            self.wp["_links"]["priority"] = {"title": "High", "href": href}
            return FakeHTTP(200, self.wp)
        if method == "POST" and base == "/api/v3/work_packages/528/activities":
            payload = json.loads(req.data.decode())
            self.notes.append({"id": 1, "createdAt": "2026-11-10T06:00:00Z", "comment": payload["comment"],
                               "details": [], "_links": {"user": {"title": "agent"}}})
            return FakeHTTP(201, {"id": 1})
        raise AssertionError("unexpected %s %s" % (method, path))


def self_origin():
    return opwork.load_api().ORIGIN


class ScanBounds(unittest.TestCase):
    def setUp(self):
        self.saved = (opwork.SCAN_MAX_PAGES, opwork.SCAN_MAX_ROWS, opwork.SCAN_MAX_BYTES)
        self.addCleanup(self.restore)
        self.pager = Pager()
        self.tracker = opwork.Tracker(self.pager.request, RuntimeError, "haynes-engineering")

    def restore(self):
        opwork.SCAN_MAX_PAGES, opwork.SCAN_MAX_ROWS, opwork.SCAN_MAX_BYTES = self.saved

    def test_scan_returns_a_page_inside_the_bounds(self):
        opwork.SCAN_MAX_PAGES, opwork.SCAN_MAX_ROWS, opwork.SCAN_MAX_BYTES = 2, 10, 1000
        self.pager.elements, self.pager.total, self.pager.page_bytes = 1, 1, 20
        rows = self.tracker.scan([{"status": {"operator": "*", "values": []}}])
        self.assertEqual([row["id"] for row in rows], [9])
        self.assertEqual(rows[0]["subject"], "SECRET-ROW")

    def test_scan_refuses_past_pages_rows_and_bytes(self):
        opwork.SCAN_MAX_PAGES, opwork.SCAN_MAX_ROWS, opwork.SCAN_MAX_BYTES = 1, 100, 100000
        self.pager.elements, self.pager.total, self.pager.page_bytes = 1, 5, 20
        with self.assertRaises(opwork.Fail) as caught:
            self.tracker.scan([])
        self.assertEqual(caught.exception.code, 3)
        self.assertIn("scan stopped", str(caught.exception))
        self.assertNotIn("SECRET-ROW", str(caught.exception))
        self.assertEqual(self.pager.listings, 1)

        self.pager.listings = 0
        opwork.SCAN_MAX_PAGES, opwork.SCAN_MAX_ROWS = 5, 1
        self.pager.elements, self.pager.total = 2, 2
        with self.assertRaises(opwork.Fail) as caught:
            self.tracker.scan([])
        self.assertIn("scan stopped", str(caught.exception))
        self.assertEqual(self.pager.listings, 1)

        self.pager.listings = 0
        # The page's JSON is under this cap. The recorded body size is over it,
        # so the bound has to be the bytes the client kept, not a second encoding.
        opwork.SCAN_MAX_ROWS, opwork.SCAN_MAX_BYTES = 100, 100000
        self.pager.elements, self.pager.total, self.pager.page_bytes = 1, 1, 100001
        with self.assertRaises(opwork.Fail) as caught:
            self.tracker.scan([])
        self.assertIn("scan stopped", str(caught.exception))
        self.assertNotIn("SECRET-ROW", str(caught.exception))
        self.assertEqual(self.pager.listings, 1)

    def test_search_and_create_refuse_a_bounded_scan_without_a_write(self):
        opwork.SCAN_MAX_PAGES, opwork.SCAN_MAX_ROWS, opwork.SCAN_MAX_BYTES = 1, 100, 100000
        self.pager.elements, self.pager.total, self.pager.page_bytes = 1, 5, 20
        code, out, err = invoke(self.tracker, ["search", "--source-key", "missing"])
        self.assertEqual(code, 3, err)
        self.assertIn("scan stopped", err)
        self.assertNotIn("SECRET-ROW", out + err)
        self.assertEqual([c for c in self.pager.calls if c[0] == "POST"], [])

        self.pager.calls.clear()
        code, out, err = invoke(self.tracker, ["create", "--epic", "1", "--subject", "A story", "--source-key", "fresh:1", "--confirm-new"])
        self.assertEqual(code, 3, err)
        self.assertIn("scan stopped", err)
        self.assertNotIn("SECRET-ROW", out + err)
        self.assertEqual([c for c in self.pager.calls if c[0] == "POST"], [])


class Pager:
    def __init__(self):
        self.calls = []
        self.listings = 0
        self.elements = 1
        self.total = 1
        self.page_bytes = 20

    def request(self, method, path, payload=None):
        base = path.split("?", 1)[0]
        self.calls.append((method, base))
        # Bound methods expose attributes of the function. scan reads this size.
        Pager.request.last_body_bytes = self.page_bytes
        if base == "/api/v3/projects/haynes-engineering":
            return 200, {"id": 1, "identifier": "haynes-engineering"}
        if base == "/api/v3/work_packages/1":
            return 200, {"id": 1, "lockVersion": 1, "subject": "Epic",
                         "_links": {"project": {"href": "/api/v3/projects/1"},
                                    "type": {"title": "Epic", "href": "/api/v3/types/2"},
                                    "status": {"title": "New", "href": "/api/v3/statuses/1"}}}
        if base.startswith("/api/v3/projects/") and base.endswith("/work_packages"):
            self.listings += 1
            row = {"id": 9, "subject": "SECRET-ROW", "lockVersion": 1, "customField6": "other",
                   "_links": {"type": {"title": "Story"}, "status": {"title": "New"}, "parent": {}}}
            return 200, {"total": self.total, "_embedded": {"elements": [dict(row) for _ in range(self.elements)]}}
        return 404, {"message": "no route"}


def invoke(tracker, argv):
    out, err = io.StringIO(), io.StringIO()
    from contextlib import redirect_stderr, redirect_stdout
    with redirect_stdout(out), redirect_stderr(err):
        code = opwork.main(list(argv), tracker, getattr(tracker, "unavailable", RuntimeError))
    return code, out.getvalue(), err.getvalue()


if __name__ == "__main__":
    unittest.main()
