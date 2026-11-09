"""Offline tests for opwork_mcp.py. No tracker call, no kubectl, no token.

Run: python3 -I -B -m unittest discover -s images/agent/tools/openproject -p 'test_opwork_mcp.py' -v

opwork runs for real; only its request function is a fake in-memory tracker.
"""
import importlib.util
import io
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
SESSION = "test | worktree t | branch b"
EXPECTED_TOOLS = ["openproject_ping", "openproject_search", "openproject_show", "openproject_comment", "openproject_status",
                  "openproject_claim", "openproject_checkpoint", "openproject_create_followup"]


def load(name, file):
    spec = importlib.util.spec_from_file_location(name, os.path.join(HERE, file))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


mcp = load("opwork_mcp_under_test", "opwork_mcp.py")


class Down(Exception):
    """Stands in for agent-api's ApiUnavailable."""


def make_wp(number, status="New", owner="", subject="A story", description="", key="key", status_id=1, kind="Story"):
    return {"id": number, "lockVersion": 0, "subject": subject, "description": {"raw": description},
            "customField2": owner, "customField3": "thaynes43/dev-env", "customField4": {"raw": "accept"}, "customField5": {"raw": "constrain"},
            "customField6": key, "customField11": {"raw": ""},
            "_links": {"status": {"title": status, "href": "/api/v3/statuses/%d" % status_id}, "type": {"title": kind, "href": "/api/v3/types/3"},
                       "parent": {"href": "/api/v3/work_packages/95"}}}


class FakeTracker:
    """The few OpenProject endpoints opwork touches for these tests, with mutable state and a call log."""
    STATUSES = [("New", 1), ("In progress", 2), ("In review", 3), ("Paused", 4), ("Ungroomed", 5)]

    def __init__(self):
        self.calls, self.down, self.drop_notes = [], False, False
        self.items = {485: make_wp(485), 600: make_wp(600, owner="someone else | worktree x | branch y")}
        self.acts = {485: [], 600: []}

    def request(self, method, path, payload=None):
        self.calls.append((method, path, payload))
        if self.down:
            raise Down("tracker down")
        route = path.split("?")[0]
        if route == "/api/v3/users/me":
            return 200, {"id": 7, "login": "agent"}
        if route == "/api/v3/statuses":
            return 200, {"_embedded": {"elements": [{"name": n, "id": i} for n, i in self.STATUSES]}}
        if route == "/api/v3/types":
            return 200, {"_embedded": {"elements": [{"name": n, "id": i} for n, i in (("Saga", 1), ("Epic", 2), ("Story", 3))]}}
        if route == "/api/v3/relations":
            return 200, {"_embedded": {"elements": [{"type": "relates", "_links": {"from": {"href": "/api/v3/work_packages/485"}, "to": {"href": "/api/v3/work_packages/97"}}}]}}
        if route.endswith("/work_packages") and route.startswith("/api/v3/projects/"):
            rows = list(self.items.values())
            return 200, {"total": len(rows), "_embedded": {"elements": rows}}
        found = re.fullmatch(r"/api/v3/work_packages/(\d+)(/activities)?", route)
        if not found:
            return 404, {"message": "no route " + route}
        number = int(found.group(1))
        if found.group(2):
            if method == "POST":
                if not self.drop_notes:
                    self.acts[number].append({"id": 9000 + len(self.acts[number]) + 1, "createdAt": "2026-11-09T12:00:00Z",
                                              "comment": payload["comment"], "details": [], "_links": {"user": {"title": "agent"}}})
                return 201, {"id": 9001}
            return 200, {"_embedded": {"elements": self.acts[number]}}
        wp = self.items[number]
        if method == "PATCH":
            wp["lockVersion"] += 1
            if "customField2" in payload:
                wp["customField2"] = payload["customField2"]
            if "status" in payload.get("_links", {}):
                status_id = int(payload["_links"]["status"]["href"].rsplit("/", 1)[-1])
                wp["_links"]["status"] = {"title": next(n for n, i in self.STATUSES if i == status_id), "href": "/api/v3/statuses/%d" % status_id}
        return 200, wp


class Harness:
    """An Opwork runner wired to a FakeTracker, a private state dir and a fixed session."""

    def setUpHarness(self):
        self.fake = FakeTracker()
        self.state = tempfile.TemporaryDirectory()
        patch = mock.patch.dict(os.environ, {"OPWORK_SESSION": SESSION, "OPWORK_STATE_DIR": self.state.name})
        patch.start()
        self.addCleanup(patch.stop)
        self.addCleanup(self.state.cleanup)
        self.runner = mcp.Opwork()
        opwork = self.runner.load()
        self.runner.tracker = opwork.Tracker(self.fake.request, Down, "haynes-engineering")
        self.runner.unavailable = Down

    def call(self, name, **args):
        reply = mcp.handle({"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": name, "arguments": args}}, self.runner.run)
        return reply["result"]

    def posts(self):
        return [c for c in self.fake.calls if c[0] in ("POST", "PATCH")]

    def pending(self):
        folder = os.path.join(self.state.name, "pending")
        return sorted(os.listdir(folder)) if os.path.isdir(folder) else []


def text(result):
    return result["content"][0]["text"]


class Protocol(unittest.TestCase):
    def handle(self, message):
        return mcp.handle(message, lambda argv: self.fail("opwork must not run: %r" % argv))

    def test_initialize_negotiates_a_known_version(self):
        reply = self.handle({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-03-26"}})
        self.assertEqual(reply["result"]["protocolVersion"], "2025-03-26")
        self.assertEqual(reply["result"]["capabilities"], {"tools": {}})
        self.assertEqual(reply["result"]["serverInfo"]["name"], "opwork-mcp")

    def test_initialize_offers_the_latest_for_an_unknown_version(self):
        reply = self.handle({"jsonrpc": "2.0", "id": "a", "method": "initialize", "params": {"protocolVersion": "1999-01-01"}})
        self.assertEqual(reply["id"], "a")
        self.assertEqual(reply["result"]["protocolVersion"], mcp.PROTOCOL_VERSIONS[0])

    def test_notifications_get_no_reply(self):
        self.assertIsNone(self.handle({"jsonrpc": "2.0", "method": "notifications/initialized"}))
        self.assertIsNone(self.handle({"jsonrpc": "2.0", "method": "notifications/whatever"}))

    def test_unknown_method_is_an_error(self):
        reply = self.handle({"jsonrpc": "2.0", "id": 5, "method": "resources/list"})
        self.assertEqual(reply["id"], 5)
        self.assertEqual(reply["error"]["code"], -32601)
        self.assertNotIn("result", reply)

    def test_ping_replies_empty(self):
        self.assertEqual(self.handle({"jsonrpc": "2.0", "id": 2, "method": "ping"})["result"], {})

    def test_unknown_tool_is_a_protocol_error(self):
        reply = self.handle({"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "openproject_delete", "arguments": {}}})
        self.assertEqual(reply["error"]["code"], -32602)

    def test_invalid_requests(self):
        self.assertEqual(self.handle([1])["error"]["code"], -32600)
        self.assertEqual(self.handle({"jsonrpc": "1.0", "id": 1, "method": "ping"})["error"]["code"], -32600)
        self.assertEqual(self.handle({"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": []})["error"]["code"], -32602)

    def test_serve_replies_one_line_per_request_and_survives_garbage(self):
        stdin = io.StringIO('not json\n\n{"jsonrpc":"2.0","method":"notifications/initialized"}\n{"jsonrpc":"2.0","id":1,"method":"ping"}\n')
        stdout = io.StringIO()
        mcp.serve(stdin, stdout, lambda argv: self.fail("must not run"))
        lines = stdout.getvalue().splitlines()
        self.assertEqual(len(lines), 2)
        self.assertEqual(json.loads(lines[0])["error"]["code"], -32700)
        self.assertEqual(json.loads(lines[1]), {"jsonrpc": "2.0", "id": 1, "result": {}})


class ToolList(unittest.TestCase):
    def test_tools_and_schemas(self):
        reply = mcp.handle({"jsonrpc": "2.0", "id": 1, "method": "tools/list"}, None)
        tools = reply["result"]["tools"]
        self.assertEqual([t["name"] for t in tools], EXPECTED_TOOLS)
        for tool in tools:
            self.assertTrue(tool["description"], tool["name"])
            spec = tool["inputSchema"]
            self.assertEqual(spec["type"], "object")
            self.assertIs(spec["additionalProperties"], False)
            self.assertLessEqual(set(spec["required"]), set(spec["properties"]))
        by_name = {t["name"]: t["inputSchema"] for t in tools}
        self.assertEqual(by_name["openproject_search"]["required"], [])
        self.assertEqual(sorted(by_name["openproject_search"]["properties"]), ["limit", "parent", "status", "text", "type"])
        self.assertEqual(by_name["openproject_show"]["required"], ["id"])
        self.assertEqual(by_name["openproject_comment"]["required"], ["id", "text"])
        self.assertEqual(by_name["openproject_status"]["required"], ["id", "status"])
        self.assertEqual(by_name["openproject_create_followup"]["required"], ["epic", "subject", "source_key"])

    def test_no_override_or_destructive_tool_is_exposed(self):
        blob = json.dumps(mcp.TOOLS)
        for word in ("force", "complete", "flush", "delete"):
            self.assertNotIn('"%s"' % word, blob)
        self.assertEqual(len(mcp.TOOLS), 8)

    def test_json_round_trips(self):
        self.assertEqual(json.loads(json.dumps(mcp.TOOLS)), mcp.TOOLS)


class Argv(unittest.TestCase):
    def test_free_text_never_becomes_a_flag(self):
        self.assertEqual(mcp.build_argv("openproject_comment", {"id": 5, "text": "--force everything"}), ["comment", "5", "--", "--force everything"])
        self.assertEqual(mcp.build_argv("openproject_checkpoint", {"id": 5, "text": "-x"}), ["checkpoint", "5", "--", "-x"])
        self.assertEqual(mcp.build_argv("openproject_status", {"id": 5, "status": "In review", "note": "--force"}), ["status", "5", "--note=--force", "--", "In review"])
        self.assertEqual(mcp.build_argv("openproject_claim", {"id": 5}), ["claim", "5"])
        self.assertEqual(mcp.build_argv("openproject_search", {"text": "-x", "status": "New", "limit": 5, "parent": 9, "type": "Epic"}),
                         ["search", "--status=New", "--type=Epic", "--parent=9", "--limit=5", "--", "-x"])
        self.assertEqual(mcp.build_argv("openproject_show", {"id": 5, "history": True, "relations": False}), ["show", "5", "--history"])

    def test_followup_maps_every_field(self):
        argv = mcp.build_argv("openproject_create_followup", {"epic": 95, "subject": "-s", "source_key": "followup:1:x", "origin": 97, "description": "d",
                                                              "acceptance": "a", "constraints": "c", "confirm_new": True})
        self.assertEqual(argv, ["create", "--epic=95", "--subject=-s", "--source-key=followup:1:x", "--origin=97", "--description=d",
                                "--acceptance=a", "--constraints=c", "--confirm-new"])


class Calls(Harness, unittest.TestCase):
    def setUp(self):
        self.setUpHarness()

    def test_ping_is_attributed_to_the_session(self):
        result = self.call("openproject_ping")
        self.assertFalse(result["isError"])
        self.assertIn("ok: agent (user 7), project haynes-engineering, session <%s>" % SESSION, text(result))

    def test_search_maps_to_opwork_search_and_its_listing(self):
        result = self.call("openproject_search", text="story", status="New", type="Story", parent=95, limit=7)
        self.assertFalse(result["isError"])
        self.assertIn("#485 Story [New]", text(result))
        self.assertIn("-- 2 of 2 (page 1, 7 per page)", text(result))
        listing = next(c[1] for c in self.fake.calls if "/projects/haynes-engineering/work_packages" in c[1])
        self.assertIn("pageSize=7", listing)
        self.assertIn("subjectOrId", listing)
        self.assertEqual(self.posts(), [])

    def test_show_returns_opwork_text_and_history_and_relations_are_opt_in(self):
        self.fake.acts[485].append({"id": 1, "createdAt": "2026-11-09T11:00:00Z", "comment": {"raw": "hello there"}, "details": [], "_links": {"user": {"title": "tom"}}})
        plain = self.call("openproject_show", id=485)
        self.assertIn("source-key: key", text(plain))
        self.assertNotIn("hello there", text(plain))
        self.assertNotIn("relation:", text(plain))
        full = self.call("openproject_show", id=485, history=True, relations=True)
        self.assertIn("hello there", text(full))
        self.assertIn("relation: #485 relates #97", text(full))
        self.assertEqual(self.posts(), [])

    def test_comment_posts_attributed_and_reports_the_read_back(self):
        result = self.call("openproject_comment", id=485, text="progress note")
        self.assertFalse(result["isError"])
        self.assertEqual(text(result), "comment 9001 recorded on #485 (read back)")
        method, path, payload = self.posts()[0]
        self.assertEqual((method, path), ("POST", "/api/v3/work_packages/485/activities"))
        self.assertTrue(payload["comment"]["raw"].startswith("**[%s]** progress note\n\n(opwork:note=" % SESSION))
        self.assertEqual(self.pending(), [])

    def test_comment_that_is_not_read_back_is_unconfirmed_and_an_error(self):
        self.fake.drop_notes = True
        result = self.call("openproject_comment", id=485, text="lost?")
        self.assertTrue(result["isError"])
        self.assertIn("UNCONFIRMED", text(result))
        self.assertIn("[opwork exit 6:", text(result))
        self.assertEqual(len(self.pending()), 1)  # opwork's own local record, kept for flush
        self.assertEqual(len([c for c in self.posts() if c[0] == "POST"]), 1)  # never reposted

    def test_tracker_down_is_an_error_and_saves_the_note(self):
        self.fake.down = True
        result = self.call("openproject_comment", id=485, text="offline note")
        self.assertTrue(result["isError"])
        self.assertIn("TRACKER UNAVAILABLE", text(result))
        self.assertIn("[opwork exit 5:", text(result))
        self.assertEqual(len(self.pending()), 1)
        read = self.call("openproject_show", id=485)
        self.assertTrue(read["isError"])
        self.assertIn("TRACKER UNAVAILABLE", text(read))

    def test_claim_sets_owner_status_and_comments(self):
        result = self.call("openproject_claim", id=485, note="starting")
        self.assertFalse(result["isError"])
        self.assertIn("claimed (read back): #485 Story [In progress]", text(result))
        self.assertIn("owner=<%s>" % SESSION, text(result))
        self.assertEqual(self.fake.items[485]["customField2"], SESSION)
        self.assertTrue(self.fake.acts[485][0]["comment"]["raw"].startswith("**[%s]** Claimed. starting" % SESSION))

    def test_claim_of_another_sessions_item_is_refused_as_an_error(self):
        result = self.call("openproject_claim", id=600)
        self.assertTrue(result["isError"])
        self.assertIn("REFUSED", text(result))
        self.assertIn("[opwork exit 3:", text(result))
        self.assertEqual(self.posts(), [])

    def test_status_change_and_hold_refusal(self):
        result = self.call("openproject_status", id=485, status="In review", note="PR #1")
        self.assertFalse(result["isError"])
        self.assertIn("status set (read back): #485 Story [In review]", text(result))
        self.fake.items[485]["_links"]["status"] = {"title": "Paused", "href": "/api/v3/statuses/4"}
        before = len(self.posts())
        held = self.call("openproject_status", id=485, status="In progress")
        self.assertTrue(held["isError"])
        self.assertIn("explicit hold", text(held))
        self.assertEqual(len(self.posts()), before)

    def test_unknown_status_name_is_an_error(self):
        result = self.call("openproject_status", id=485, status="Nonsense")
        self.assertTrue(result["isError"])
        self.assertIn("Unknown status", text(result))

    def test_checkpoint_is_saved_locally_and_never_touches_the_tracker(self):
        result = self.call("openproject_checkpoint", id=485, text="halfway")
        self.assertFalse(result["isError"])
        self.assertIn("saved locally (NOT in OpenProject yet)", text(result))
        self.assertEqual(self.fake.calls, [])
        self.assertEqual(len(self.pending()), 1)

    def test_followup_duplicate_is_exit_4_and_creates_nothing(self):
        self.fake.items[95] = make_wp(95, kind="Epic", subject="Epic")
        self.fake.items[700] = make_wp(700, subject="OpenProject MCP server for agents", key="other")
        result = self.call("openproject_create_followup", epic=95, subject="OpenProject MCP server for agents", source_key="followup:1:new")
        self.assertTrue(result["isError"])
        self.assertIn("POSSIBLE DUPLICATE", text(result))
        self.assertIn("[opwork exit 4:", text(result))
        self.assertEqual([c for c in self.posts() if c[0] == "POST"], [])

    def test_followup_reuses_an_existing_source_key_with_no_write(self):
        self.fake.items[95] = make_wp(95, kind="Epic", subject="Epic")
        result = self.call("openproject_create_followup", epic=95, subject="A story", source_key="key")
        self.assertEqual(self.posts(), [])
        self.assertFalse(result["isError"])
        self.assertTrue(text(result).startswith("REUSED, not created"), text(result))

    def test_bad_arguments_are_errors_and_run_nothing(self):
        for name, args in (("openproject_show", {}), ("openproject_show", {"id": "485"}), ("openproject_show", {"id": True}), ("openproject_show", {"id": 0}),
                           ("openproject_comment", {"id": 1, "text": "   "}), ("openproject_comment", {"id": 1, "text": "-"}),
                           ("openproject_search", {"limit": 101}), ("openproject_ping", {"extra": 1}),
                           ("openproject_status", {"id": 1, "status": "x", "force": True}), ("openproject_comment", {"id": 1, "text": "x" * (mcp.MAX_TEXT + 1)})):
            result = self.call(name, **args)
            self.assertTrue(result["isError"], (name, args))
            self.assertIn("invalid arguments", text(result))
        self.assertEqual(self.fake.calls, [])
        self.assertEqual(self.pending(), [])

    def test_non_object_arguments_are_an_error(self):
        reply = mcp.handle({"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "openproject_show", "arguments": [485]}}, self.runner.run)
        self.assertTrue(reply["result"]["isError"])

    def test_long_output_is_truncated_with_a_marker(self):
        self.fake.items[485]["description"] = {"raw": "word " * 20000}
        result = self.call("openproject_show", id=485)
        self.assertFalse(result["isError"])
        self.assertIn("[truncated ", text(result))
        self.assertLess(len(text(result)), mcp.MAX_OUTPUT + 300)

    def test_a_token_shaped_string_never_leaves(self):
        secret = "opapi-" + "0123456789abcdef" * 4 + "0123456789abcd"
        self.fake.items[485]["subject"] = "leak " + secret
        result = self.call("openproject_show", id=485)
        self.assertNotIn(secret, text(result))
        self.assertIn("[redacted]", text(result))

    def test_opwork_never_reads_the_protocol_stdin(self):
        with mock.patch.object(sys, "stdin", io.StringIO("PROTOCOL STREAM")) as protocol:
            self.call("openproject_ping")
            self.assertEqual(protocol.read(), "PROTOCOL STREAM")

    def test_an_unexpected_exception_is_an_error_not_a_crash(self):
        def boom(method, path, payload=None):
            raise ValueError("boom")
        self.runner.tracker.request = boom
        result = self.call("openproject_ping")
        self.assertTrue(result["isError"])
        self.assertIn("internal error: ValueError", text(result))

    def test_argparse_refusal_is_an_error(self):
        code, out, err = self.runner.run(["show", "not-a-number"])
        self.assertNotEqual(code, 0)
        result = mcp.call_tool(lambda argv: (code, out, err), "openproject_ping", {})
        self.assertTrue(result["isError"])


class Process(unittest.TestCase):
    """The shipped entry point, as the image smoke test starts it: python3 -I, stdio only."""

    def test_initialize_and_tools_list_over_stdio(self):
        requests = "\n".join(json.dumps(m) for m in (
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "t", "version": "0"}}},
            {"jsonrpc": "2.0", "method": "notifications/initialized"},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
            {"jsonrpc": "2.0", "id": 3, "method": "nope"})) + "\n"
        done = subprocess.run([sys.executable, "-I", "-B", os.path.join(HERE, "opwork_mcp.py")], input=requests, capture_output=True, text=True, timeout=60)
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertEqual(done.stderr, "")
        replies = [json.loads(x) for x in done.stdout.splitlines()]
        self.assertEqual([r["id"] for r in replies], [1, 2, 3])
        self.assertEqual([t["name"] for t in replies[1]["result"]["tools"]], EXPECTED_TOOLS)
        self.assertEqual(replies[2]["error"]["code"], -32601)


if __name__ == "__main__":
    unittest.main()
