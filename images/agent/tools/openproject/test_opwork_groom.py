"""Offline tests for opwork groom and order. No tracker call, no kubectl, no token.

Run: python3 -I -B -m unittest discover -s images/agent/tools/openproject -p 'test_opwork_groom.py' -v

opwork runs for real. Only its request function is a fake in-memory tracker.
"""
import importlib.util
import io
import os
import re
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
SESSION = "meatball"


def load(name, file):
    spec = importlib.util.spec_from_file_location(name, os.path.join(HERE, file))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


opwork = load("opwork_groom_under_test", "opwork.py")


class Down(Exception):
    """Stands in for agent-api's ApiUnavailable."""


def make_wp(number):
    return {"id": number, "lockVersion": 4, "subject": "Front door",
            "description": {"raw": "old scope"}, "customField4": {"raw": "old acceptance"},
            "customField5": {"raw": "old constraints"}, "customField2": "",
            "_links": {"status": {"title": "Ungroomed", "href": "/api/v3/statuses/5"},
                       "type": {"title": "Story", "href": "/api/v3/types/3"},
                       "parent": {"href": "/api/v3/work_packages/516"},
                       "priority": {"title": "Normal", "href": "/api/v3/priorities/2"}}}


class FakeTracker:
    PRIORITIES = (("Low", 1), ("Normal", 2), ("High", 3), ("Immediate", 4))

    def __init__(self):
        self.calls = []
        self.items = {528: make_wp(528)}
        self.acts = {528: []}

    def request(self, method, path, payload=None):
        self.calls.append((method, path, payload))
        route = path.split("?")[0]
        if route == "/api/v3/priorities":
            return 200, {"_embedded": {"elements": [{"name": n, "id": i} for n, i in self.PRIORITIES]}}
        if route == "/api/v3/statuses":
            return 200, {"_embedded": {"elements": [{"name": "Ungroomed", "id": 5}]}}
        found = re.fullmatch(r"/api/v3/work_packages/(\d+)(/activities)?", route)
        if not found:
            return 404, {"message": "no route " + route}
        number = int(found.group(1))
        if found.group(2):
            if method == "POST":
                self.acts[number].append({"id": 1, "createdAt": "2026-11-10T06:00:00Z",
                                          "comment": payload["comment"], "details": [],
                                          "_links": {"user": {"title": "agent"}}})
                return 201, {"id": 1}
            return 200, {"_embedded": {"elements": self.acts[number]}, "total": len(self.acts[number])}
        wp = self.items[number]
        if method == "PATCH":
            if payload.get("lockVersion") != wp["lockVersion"]:
                return 409, {"message": "conflict"}
            wp["lockVersion"] += 1
            for key, value in payload.items():
                if key not in ("lockVersion", "_links"):
                    wp[key] = value
            links = payload.get("_links") or {}
            if "priority" in links:
                pid = int(links["priority"]["href"].rsplit("/", 1)[-1])
                title = next(n for n, i in self.PRIORITIES if i == pid)
                wp["_links"]["priority"] = {"title": title, "href": links["priority"]["href"]}
        return 200, wp


class GroomOrder(unittest.TestCase):
    def setUp(self):
        self.fake = FakeTracker()
        self.env = mock.patch.dict(os.environ, {"OPWORK_SESSION": SESSION}, clear=False)
        self.env.start()
        os.environ.pop("OPWORK_WRITE_ORIGIN", None)
        os.environ.pop("OPWORK_BASE_URL", None)
        self.addCleanup(self.env.stop)

    def invoke(self, argv):
        out, err = io.StringIO(), io.StringIO()
        tracker = opwork.Tracker(self.fake.request, Down, "haynes-engineering")
        from contextlib import redirect_stderr, redirect_stdout
        with redirect_stdout(out), redirect_stderr(err):
            code = opwork.main(list(argv), tracker, Down)
        return code, out.getvalue(), err.getvalue()

    def writes(self):
        return [c for c in self.fake.calls if c[0] in ("POST", "PATCH")]

    def test_groom_from_files_patches_with_lock_and_reads_back(self):
        with tempfile.TemporaryDirectory() as directory:
            scope = os.path.join(directory, "scope.md")
            accept = os.path.join(directory, "accept.md")
            with open(scope, "w", encoding="utf-8") as handle:
                handle.write("new scope")
            with open(accept, "w", encoding="utf-8") as handle:
                handle.write("new acceptance")
            code, out, err = self.invoke(["groom", "528", "--description", scope, "--acceptance", accept, "--expect-lock", "4"])
        self.assertEqual(err, "", err)
        self.assertEqual(code, 0, out)
        self.assertIn("groomed (read back): #528", out)
        self.assertIn("new scope", out)
        self.assertIn("new acceptance", out)
        self.assertIn("old constraints", out)
        patch = next(c for c in self.fake.calls if c[0] == "PATCH")
        self.assertEqual(patch[2]["lockVersion"], 4)
        self.assertEqual(patch[2]["description"], {"raw": "new scope"})
        self.assertEqual(patch[2]["customField4"], {"raw": "new acceptance"})
        self.assertNotIn("customField5", patch[2])
        self.assertNotIn("_links", patch[2])
        note = self.fake.acts[528][0]["comment"]["raw"]
        self.assertIn("**[%s]**" % SESSION, note)
        self.assertIn("Groomed description, acceptance.", note)

    def test_groom_stdin_and_origin_line(self):
        os.environ["OPWORK_WRITE_ORIGIN"] = "meatball"
        old = sys.stdin
        sys.stdin = io.StringIO("from stdin")
        try:
            code, out, err = self.invoke(["groom", "528", "--constraints", "-"])
        finally:
            sys.stdin = old
        self.assertEqual(code, 0, err)
        self.assertIn("from stdin", out)
        note = self.fake.acts[528][0]["comment"]["raw"]
        self.assertIn("Origin: meatball", note)
        self.assertIn("**[meatball]**", note)

    def test_empty_field_is_refused_before_a_write(self):
        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "empty.md")
            open(path, "w", encoding="utf-8").close()
            code, out, err = self.invoke(["groom", "528", "--description", path])
        self.assertEqual(code, 3)
        self.assertIn("REFUSED", err)
        self.assertEqual(self.writes(), [])

    def test_two_stdin_fields_are_refused_before_a_request(self):
        code, _, err = self.invoke(["groom", "528", "--description", "-", "--acceptance", "-"])
        self.assertEqual(code, 3)
        self.assertIn("stdin", err)
        self.assertEqual(self.fake.calls, [])

    def test_order_sets_priority_and_reads_it_back(self):
        os.environ["OPWORK_WRITE_ORIGIN"] = "meatball"
        code, out, err = self.invoke(["order", "528", "--priority", "High"])
        self.assertEqual(code, 0, err)
        self.assertIn("priority: High", out)
        self.assertIn("ordered (read back): #528", out)
        patch = next(c for c in self.fake.calls if c[0] == "PATCH")
        self.assertEqual(patch[2]["_links"]["priority"]["href"], "/api/v3/priorities/3")
        self.assertEqual(patch[2]["lockVersion"], 4)
        self.assertIn("Origin: meatball", self.fake.acts[528][0]["comment"]["raw"])
        self.assertEqual(self.fake.items[528]["_links"]["priority"]["title"], "High")

    def test_order_same_priority_does_not_write(self):
        code, out, err = self.invoke(["order", "528", "--priority", "Normal"])
        self.assertEqual(code, 0, err)
        self.assertIn("priority: Normal", out)
        self.assertEqual(self.writes(), [])

    def test_unknown_priority_writes_nothing(self):
        code, _, err = self.invoke(["order", "528", "--priority", "Urgent"])
        self.assertEqual(code, 1)
        self.assertIn("Unknown", err)
        self.assertEqual(self.writes(), [])

    def test_never_writes_as_tom(self):
        os.environ["OPWORK_SESSION"] = "Tom"
        code, _, err = self.invoke(["groom", "528", "--description", "-"])
        self.assertEqual(code, 3)
        self.assertIn("never writes as Tom", err)
        self.assertEqual(self.fake.calls, [])
        os.environ["OPWORK_SESSION"] = "Tom | worktree x | branch y"
        code, _, err = self.invoke(["order", "528", "--priority", "High"])
        self.assertEqual(code, 3)
        self.assertEqual(self.fake.calls, [])

    def test_bad_origin_is_refused_before_a_request(self):
        os.environ["OPWORK_WRITE_ORIGIN"] = "Meatball"
        code, _, err = self.invoke(["order", "528", "--priority", "High"])
        self.assertEqual(code, 3)
        self.assertIn("OPWORK_WRITE_ORIGIN", err)
        self.assertEqual(self.fake.calls, [])

    def test_invalid_token_file_is_not_echoed(self):
        secret = "not-a-real-token-value"
        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "credential")
            with open(path, "w", encoding="utf-8") as handle:
                handle.write(secret + "\n")
            env = os.environ.copy()
            env["OPWORK_TOKEN_FILE"] = path
            env["OPWORK_SESSION"] = "meatball"
            env.pop("OPWORK_WRITE_ORIGIN", None)
            proc = subprocess.run([sys.executable, "-I", "-B", os.path.join(HERE, "opwork.py"), "show", "528"],
                                  capture_output=True, text=True, env=env, timeout=30, check=False)
        combined = proc.stdout + proc.stderr
        self.assertNotEqual(proc.returncode, 0)
        self.assertNotIn(secret, combined)
        self.assertNotIn(path, combined)


if __name__ == "__main__":
    unittest.main()
