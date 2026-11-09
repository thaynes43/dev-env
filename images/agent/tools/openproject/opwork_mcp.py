"""MCP stdio server over opwork, the shared OpenProject backlog CLI (haynes-engineering #485).

Newline-delimited JSON-RPC 2.0 on stdin/stdout. Standard library only. It adds no
credential, role or network path: every tool builds the same argv an agent would give
`opwork` and runs opwork's own `main()` in this process, with opwork's Tracker over
agent-api.py's `request`. So the token comes by the same kubectl exec and stays in this
process's memory, the comment and claim attribution is opwork's, a write is reported only
after opwork's read back, and the UNCONFIRMED / exit-6 text, the local pending records and
the exit codes are opwork's. A non-zero opwork exit is a tool error (`isError: true`).

Not exposed: opwork's --force overrides, `complete`, `relate`, `flush`, and description
changes. An agent that needs one runs `opwork` itself.

Tools: openproject_ping, _search, _show, _comment, _status, _claim, _checkpoint (saved
locally, like `opwork checkpoint`), _create_followup.
"""
import contextlib
import importlib.util
import io
import json
import os
import re
import sys

SERVER_NAME = "opwork-mcp"
SERVER_VERSION = "1.0.0"
PROTOCOL_VERSIONS = ("2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05")
MAX_OUTPUT = 24000  # characters of tool result text; a longer one is cut with a marker
MAX_TEXT = 50000  # characters of any one text argument
MAX_LIMIT = 100  # search page size

# opwork's exit codes (see its module doc), added to an error result so a model can act on it.
EXIT_MEANING = {
    1: "error",
    2: "invalid arguments",
    3: "conflict or refused; nothing was overwritten",
    4: "needs a decision (possible duplicate)",
    5: "tracker unavailable; the write may be saved locally and its outcome is UNCONFIRMED",
    6: "uncertain; verify with openproject_show before any retry",
    7: "evidence recorded but the state change was not allowed; read-back state reported",
}
TOKEN = re.compile(r"opapi-[0-9A-Za-z]{16,}")  # the API token's shape; never allowed into a result

ID = {"type": "integer", "minimum": 1, "description": "OpenProject work package id"}


def schema(properties, required=()):
    return {"type": "object", "properties": properties, "required": list(required), "additionalProperties": False}


def text_prop(description):
    return {"type": "string", "minLength": 1, "maxLength": MAX_TEXT, "description": description}


TOOLS = [
    {"name": "openproject_ping",
     "description": "Check the OpenProject tracker and show the session identity opwork attributes writes to. Read-only.",
     "inputSchema": schema({})},
    {"name": "openproject_search",
     "description": "Search haynes-engineering work items by text (subject or id). Open items unless a status is given. One page of results, ids ascending. Read-only.",
     "inputSchema": schema({
         "text": {"type": "string", "description": "words in the subject, or an id"},
         "status": {"type": "string", "description": "status name, or several separated by commas (for example 'In progress,In review'); closed items need this"},
         "type": {"type": "string", "description": "Saga, Epic or Story"},
         "parent": {"type": "integer", "minimum": 1, "description": "parent work package id"},
         "limit": {"type": "integer", "minimum": 1, "maximum": MAX_LIMIT, "description": "results to return (default 20)"}})},
    {"name": "openproject_show",
     "description": "Read one work item: status, owner, source key, repos, acceptance, constraints, description. Read-only.",
     "inputSchema": schema({"id": ID,
                            "history": {"type": "boolean", "description": "also the comments and field changes"},
                            "relations": {"type": "boolean", "description": "also the relations"}}, ["id"])},
    {"name": "openproject_comment",
     "description": "Add an attributed comment to a work item. Reported done only after it is read back; an UNCONFIRMED result means it may have landed, so do not repost.",
     "inputSchema": schema({"id": ID, "text": text_prop("the comment, Markdown")}, ["id", "text"])},
    {"name": "openproject_status",
     "description": "Set a work item's status, then optionally comment. Refused if the item is on an explicit hold or owned by another session. Read back.",
     "inputSchema": schema({"id": ID, "status": text_prop("status name, for example 'In review'"),
                            "note": text_prop("comment to add after the change")}, ["id", "status"])},
    {"name": "openproject_claim",
     "description": "Claim a work item for this session: sets the Execution owner and moves it to In progress. Refused if another session owns it or it is on hold. Read back.",
     "inputSchema": schema({"id": ID, "note": text_prop("comment to add after the claim")}, ["id"])},
    {"name": "openproject_checkpoint",
     "description": "Save a progress note LOCALLY for a work item. It is NOT in OpenProject until `opwork flush` runs; use openproject_comment to post to the tracker.",
     "inputSchema": schema({"id": ID, "text": text_prop("the note")}, ["id", "text"])},
    {"name": "openproject_create_followup",
     "description": "File a follow-up Story (or reuse the one with the same source key) under an Epic, status Ungroomed, with provenance. Searches first. A possible duplicate returns the matches and creates nothing; repeat with confirm_new only if the work is genuinely separate. Read back.",
     "inputSchema": schema({
         "epic": {"type": "integer", "minimum": 1, "description": "the Epic id the Story goes under"},
         "subject": text_prop("Story subject"),
         "source_key": text_prop("stable key, for example followup:97:topic; it is the identity for a re-run"),
         "origin": {"type": "integer", "minimum": 1, "description": "the Story this follows from; adds a relates link"},
         "description": text_prop("Markdown description"),
         "acceptance": text_prop("Markdown acceptance criteria"),
         "constraints": text_prop("Markdown constraints"),
         "confirm_new": {"type": "boolean", "description": "create despite a possible duplicate reported by an earlier call"}},
         ["epic", "subject", "source_key"])},
]
BY_NAME = {t["name"]: t for t in TOOLS}


class BadArguments(Exception):
    pass


def validate(tool, args):
    """Check args against the tool's schema (the small subset used above); raise BadArguments."""
    spec = tool["inputSchema"]
    if not isinstance(args, dict):
        raise BadArguments("arguments must be an object")
    for key in args:
        if key not in spec["properties"]:
            raise BadArguments("unknown argument %r" % key)
    for key in spec["required"]:
        if key not in args:
            raise BadArguments("missing argument %r" % key)
    for key, value in args.items():
        prop = spec["properties"][key]
        kind = prop["type"]
        if kind == "integer":
            if type(value) is not int:
                raise BadArguments("%s must be an integer" % key)
            if value < prop.get("minimum", value) or value > prop.get("maximum", value):
                raise BadArguments("%s out of range" % key)
        elif kind == "boolean":
            if type(value) is not bool:
                raise BadArguments("%s must be a boolean" % key)
        elif kind == "string":
            if type(value) is not str:
                raise BadArguments("%s must be a string" % key)
            if len(value) > prop.get("maxLength", MAX_TEXT):
                raise BadArguments("%s is too long" % key)
            if len(value) < prop.get("minLength", 0) or ("minLength" in prop and not value.strip()):
                raise BadArguments("%s must not be empty" % key)


def option(flag, value):
    return ["%s=%s" % (flag, value)]  # one argv item: a value that starts with '-' is not read as a flag


def build_argv(name, a):
    """The opwork argv for a tool call. Free text goes after `--`, or after `=`, never as a bare word."""
    if name == "openproject_ping":
        return ["ping"]
    if name == "openproject_search":
        argv = ["search"]
        if "status" in a:
            argv += option("--status", a["status"])
        if "type" in a:
            argv += option("--type", a["type"])
        if "parent" in a:
            argv += option("--parent", a["parent"])
        if "limit" in a:
            argv += option("--limit", a["limit"])
        return argv + (["--", a["text"]] if a.get("text", "").strip() else [])
    if name == "openproject_show":
        return ["show", str(a["id"])] + (["--history"] if a.get("history") else []) + (["--relations"] if a.get("relations") else [])
    if name == "openproject_comment":
        return ["comment", str(a["id"]), "--", a["text"]]
    if name == "openproject_status":
        return ["status", str(a["id"])] + (option("--note", a["note"]) if "note" in a else []) + ["--", a["status"]]
    if name == "openproject_claim":
        return ["claim", str(a["id"])] + (option("--note", a["note"]) if "note" in a else [])
    if name == "openproject_checkpoint":
        return ["checkpoint", str(a["id"]), "--", a["text"]]
    if name == "openproject_create_followup":
        argv = ["create"] + option("--epic", a["epic"]) + option("--subject", a["subject"]) + option("--source-key", a["source_key"])
        for key, flag in (("origin", "--origin"), ("description", "--description"), ("acceptance", "--acceptance"), ("constraints", "--constraints")):
            if key in a:
                argv += option(flag, a[key])
        return argv + (["--confirm-new"] if a.get("confirm_new") else [])
    raise KeyError(name)


def scrub(text):
    return TOKEN.sub("[redacted]", text)


def clip(text, limit=MAX_OUTPUT):
    if len(text) <= limit:
        return text
    return "%s\n[truncated %d characters of %d; narrow the request, for example without history or relations]" % (text[:limit], len(text) - limit, len(text))


class Opwork:
    """Runs opwork.main in this process. The module and the Tracker (with its in-memory token) are made once."""

    def __init__(self):
        self.module = None
        self.tracker = None
        self.unavailable = None
        self.api = None  # agent-api.py's module, whose `_token` is this process's cached token

    def load(self):
        if self.module is None:
            here = os.path.dirname(os.path.abspath(__file__))
            spec = importlib.util.spec_from_file_location("opwork", os.path.join(here, "opwork.py"))
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            self.module = module
        return self.module

    def run(self, argv):
        """(exit code, stdout, stderr) of `opwork <argv>`."""
        opwork = self.load()
        out, err = io.StringIO(), io.StringIO()
        # stdin is the protocol stream: opwork must never read it (`-` text is refused earlier too).
        saved_stdin, sys.stdin = sys.stdin, io.StringIO("")
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                project = os.environ.get("OPWORK_PROJECT", opwork.PROJECT)
                if self.tracker is None and argv[0] not in ("checkpoint", "pending"):
                    api = self.api = opwork.load_api()
                    self.unavailable = api.ApiUnavailable
                    self.tracker = opwork.Tracker(api.request, api.ApiUnavailable, project)
                code = opwork.main(["--project", project, *argv], self.tracker, self.unavailable)
            except SystemExit as stop:  # argparse refusing the argv
                code = stop.code if isinstance(stop.code, int) else 2
            except Exception as error:  # opwork reports its own failures; this is a bug or a missing file
                print("opwork-mcp internal error: %s: %s" % (type(error).__name__, error), file=err)
                code = 1
            finally:
                sys.stdin = saved_stdin
        out, err = out.getvalue(), err.getvalue()
        if code and re.search(r"HTTP 401\b", out + err):
            # The helper may have issued a new token. opwork reads it once per CLI run; this
            # process is long-lived, so drop the cached one. Nothing is replayed: the agent retries.
            if self.api is not None:
                self.api._token = None
            err += "\nThe cached API token was dropped; the next call reads it again."
        return code, out, err


def call_tool(runner, name, args):
    """The MCP tools/call result for one tool."""
    tool = BY_NAME[name]
    try:
        validate(tool, args)
        if name in ("openproject_comment", "openproject_checkpoint") and args["text"].strip() == "-":
            raise BadArguments("text must not be a single '-'")  # opwork would read it as 'text from stdin'
        argv = build_argv(name, args)
    except BadArguments as error:
        return {"content": [{"type": "text", "text": "invalid arguments: %s" % error}], "isError": True}
    code, out, err = runner(argv)
    text = out.rstrip("\n")
    if err.strip():
        text = "%s\n%s" % (text, err.rstrip("\n")) if text else err.rstrip("\n")
    if code:
        text = "%s\n[opwork exit %s: %s]" % (text, code, EXIT_MEANING.get(code, "failed"))
    return {"content": [{"type": "text", "text": clip(scrub(text))}], "isError": code != 0}


def error_reply(request_id, code, message):
    return {"jsonrpc": "2.0", "id": request_id, "error": {"code": code, "message": message}}


def handle(message, runner):
    """The reply to one decoded message, or None for a notification."""
    if not isinstance(message, dict) or message.get("jsonrpc") != "2.0" or not isinstance(message.get("method"), str):
        return error_reply(message.get("id") if isinstance(message, dict) else None, -32600, "invalid request")
    method, params = message["method"], message.get("params", {})
    if "id" not in message:  # a notification (notifications/initialized, notifications/cancelled, ...): no reply
        return None
    request_id = message["id"]
    if type(request_id) not in (int, str):
        return error_reply(None, -32600, "invalid request id")
    if not isinstance(params, dict):
        return error_reply(request_id, -32602, "params must be an object")
    if method == "initialize":
        asked = params.get("protocolVersion")
        version = asked if asked in PROTOCOL_VERSIONS else PROTOCOL_VERSIONS[0]
        return {"jsonrpc": "2.0", "id": request_id, "result": {
            "protocolVersion": version, "capabilities": {"tools": {}},
            "serverInfo": {"name": SERVER_NAME, "version": SERVER_VERSION},
            "instructions": "OpenProject backlog (project haynes-engineering) through opwork. Writes are attributed to this session and reported after a read back; an UNCONFIRMED or uncertain result means check with openproject_show before retrying."}}
    if method == "ping":
        return {"jsonrpc": "2.0", "id": request_id, "result": {}}
    if method == "tools/list":
        return {"jsonrpc": "2.0", "id": request_id, "result": {"tools": TOOLS}}
    if method == "tools/call":
        name = params.get("name")
        if name not in BY_NAME:
            return error_reply(request_id, -32602, "unknown tool: %s" % (name if isinstance(name, str) else "?"))
        arguments = params.get("arguments")
        return {"jsonrpc": "2.0", "id": request_id, "result": call_tool(runner, name, {} if arguments is None else arguments)}
    return error_reply(request_id, -32601, "method not found: %s" % method)


def serve(stdin, stdout, runner):
    for raw in stdin:
        raw = raw.strip()
        if not raw:
            continue
        try:
            message = json.loads(raw)
        except ValueError:
            reply = error_reply(None, -32700, "parse error")
        else:
            reply = handle(message, runner)
        if reply is not None:
            stdout.write(json.dumps(reply, separators=(",", ":")) + "\n")  # ASCII-escaped: one line
            stdout.flush()


def main():
    for stream in (sys.stdin, sys.stdout):  # text in and out is UTF-8 whatever the locale
        with contextlib.suppress(AttributeError, ValueError):
            stream.reconfigure(encoding="utf-8")
    serve(sys.stdin, sys.stdout, Opwork().run)
    return 0


if __name__ == "__main__":
    sys.exit(main())
