#!/usr/bin/env python3
"""One fresh Cursor ACP conversation; no terminal capture or permission approval."""
import argparse
import collections
import hashlib
import json
import os
from pathlib import Path
import queue
import re
import select
import signal
import subprocess
import sys
import threading
import time
import unicodedata
import urllib.parse

# Wire-frame limits include the terminating newline.
MAX_FRAME = 65536
MAX_PROVIDER_FRAME = 4 * 1024 * 1024
# Count every object/array container, including the top-level object. Enforce
# this before decoding rather than relying on a Python build's recursion limit.
MAX_JSON_DEPTH = 64
MAX_PROMPT = 16384
LABEL = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
PERMISSION_KINDS = frozenset({"allow_once", "allow_always", "reject_once", "reject_always"})
# The provider child gets these names only when set, never the pod's tokens or keys.
# A Cursor auth variable needs its own separately approved change.
CHILD_ENVIRONMENT = ("PATH", "HOME", "LANG", "LC_ALL", "TERM", "TMPDIR", "XDG_RUNTIME_DIR")
SHUTDOWN_GRACE = 1.0


class Refusal(Exception):
    pass


# The receipt names which stream failed framing with a fixed value, never its text.
FRAME_SOURCES = {"provider": "provider", "decision": "coordinator"}


class FrameRefusal(Refusal):
    """Fixed framing category; receipt metadata never includes source text."""
    def __init__(self, detail, *, source, byte_count=None, limit=None):
        super().__init__("invalid-frame")
        self.metadata = {"detail": detail, "source": source}
        if byte_count is not None:
            self.metadata["byte_count"] = byte_count
        if limit is not None:
            self.metadata["limit"] = limit


class Arguments(argparse.ArgumentParser):
    """Bad arguments get a fixed-reason receipt, not usage text on stderr."""
    def error(self, message):
        raise Refusal("invalid-arguments")


def strict_object(raw, maximum=MAX_FRAME):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise Refusal("duplicate-field")
            result[key] = value
        return result
    if len(raw) > maximum:
        raise Refusal("frame-too-large")
    try:
        text = raw.decode("utf-8") if isinstance(raw, (bytes, bytearray)) else raw
        depth, quoted, escaped = 0, False, False
        for char in text:
            if quoted:
                if escaped:
                    escaped = False
                elif char == "\\":
                    escaped = True
                elif char == '"':
                    quoted = False
            elif char == '"':
                quoted = True
            elif char in "[{":
                depth += 1
                if depth > MAX_JSON_DEPTH:
                    raise Refusal("invalid-json")
            elif char in "]}":
                depth -= 1
        # The scan only bounds nesting. The decoder still validates all syntax,
        # duplicate keys, Unicode and non-finite numbers as before.
        value = json.loads(text, object_pairs_hook=pairs, parse_constant=lambda _: (_ for _ in ()).throw(ValueError()))
    except (ValueError, UnicodeError, RecursionError):
        raise Refusal("invalid-json") from None
    if not isinstance(value, dict):
        raise Refusal("object-required")
    return value


def label(value):
    return isinstance(value, str) and LABEL.fullmatch(value) is not None


MAX_TOOL_ID_BYTES = 1024


def tool_call_id(value):
    """ACP v1 ToolCallId is an opaque string, not a display/filename label.

    Keep exact equality in memory only (no trimming, normalization or output).
    A local UTF-8 byte bound limits cache keys; all other identifier policies stay
    unchanged. https://agentclientprotocol.com/protocol/v1/schema#toolcallid
    """
    if not isinstance(value, str) or len(value) > MAX_TOOL_ID_BYTES:
        return False
    try:
        return len(value.encode("utf-8")) <= MAX_TOOL_ID_BYTES
    except UnicodeError:
        return False


def request_id(value):
    return (type(value) is int and 0 <= value <= 2147483647) or label(value)


def bounded_text(value, maximum=2048):
    if not isinstance(value, str):
        return False
    try:
        size = len(value.encode("utf-8"))
    except UnicodeError:
        return False
    return 0 < size <= maximum and not any(ord(c) < 32 and c not in "\n\t" for c in value)


# Permission context is untrusted display data, never approval authority.
TOOL_KINDS = frozenset({"read", "edit", "delete", "move", "search", "execute", "think", "fetch", "switch_mode", "other"})
RAW_FIELDS = frozenset({"command", "args", "path", "paths", "query", "description"})
# Short names are unanchored substrings, so `_`-joined forms (DB_PW, API_KEY) match.
# `pass` alone needs no adjacent letter (DB_PASS, --pass, PASS1, not compass or
# bypass), or a camelCase capital after a lowercase letter (dbPass).
SECRET_NAME = re.compile(r"token|key|secret|passw(?:or)?d|passphrase|(?<![a-z])pass(?![a-z])|(?-i:(?<=[a-z])Pass)(?![a-z])"
                         r"|pwd|pw|auth|cookie|credential|bearer|session|signature|private|jwt", re.I)
SECRET_VALUE = re.compile(r"bearer\s|[A-Za-z0-9+/=_-]{32,}", re.I)
# URL userinfo: any `@` after `://` in the same token, so a password holding `/`,
# `?` or `#` still matches (a query `@` fails closed too); and bare user:password@.
# Possessive runs and the run-start lookbehind remove backtracking; a 4096-byte
# adversarial source stays far below the 100 ms budget recorded in the doc.
SECRET_USERINFO = re.compile(r"://[^\s@]*+@|(?<![^\s/:@])[^\s/:@]++:[^\s/@]*+@")
# -p/-P<value> or -p/-P <value>; -k, -K or -a then a spaced value that is not a
# flag (openssl enc, redis-cli); and -u/-U/--user/--proxy-user user:password or
# user%password. This deliberately also matches harmless forms such as
# `mkdir -p build`, `grep -P x` or `curl -k https://...`.
SECRET_FLAG = re.compile(r"(?:^|[\s\"'=])(?:-[pP]\s*\S|-[kKa]\s+[^\s-]|(?:-[uU]|--(?:proxy-)?user)(?:[=\s]+)?[\"']?[^\s:%\"']*[:%])")
SECRET_PATTERNS = (SECRET_NAME, SECRET_VALUE, SECRET_USERINFO, SECRET_FLAG)
# Fields concatenated with no separator rebuild a name or userinfo split across them.
# A base64/hex-like run is not checked there: gluing words and paths makes long runs.
GLUED_PATTERNS = (SECRET_NAME, SECRET_USERINFO, SECRET_FLAG)
# Displayed and checked text must be printable ASCII after NFKC. This rejects
# controls, separators, combining marks, homoglyphs (Cyrillic, Hangul fillers) and
# every other non-ASCII form the ASCII patterns above cannot see through.
PRINTABLE_ASCII = re.compile(r"[ -~]*")
REDACTED = "[REDACTED]"
MAX_CONTEXT = 2048
MAX_PROBE = 8192
PROBE_ROUNDS = 4
# In-memory correlation for this process only. Not a transcript and not approval.
MAX_TOOL_CACHE = 16
MAX_TOOL_CACHE_BYTES = 8192
MAX_TOOL_DROPPED = 16
TOOL_CACHE_TTL = 300.0
# Command and argv display refuse shell syntax. This does not parse or run a command.
SHELL_META = re.compile(r"[;&|<>`$(){}]")
_ARG_SEGMENT = r"(?:\.\.\.|[A-Za-z0-9_][A-Za-z0-9._-]*)"
RELATIVE_ARG = re.compile(rf"^(?:\.|{_ARG_SEGMENT})(?:/{_ARG_SEGMENT})+$")
TOOL_TRACKED = ("kind", "title", "locations", "rawInput")
TOOL_STATUSES = frozenset({"pending", "in_progress", "completed", "failed"})
TOOL_CACHE_REASONS = {
    "poison": "tool-call-unavailable",
    "conflict": "tool-call-conflict",
    "closed": "tool-call-closed",
    "expired": "tool-call-expired",
    "bound": "tool-cache-bound",
    "foreign": "no-correlated-tool-call",
}

# Opt-in, private structural evidence only. Never traverse unknown keys or copy
# source strings (including IDs, command text, models, content and extensions).
DIAGNOSTIC_KEYS = (
    "sessionId", "update", "sessionUpdate", "toolCall", "toolCallId", "kind",
    "status", "title", "content", "locations", "rawInput", "rawOutput",
    "options", "optionId", "name", "subject", "type", "id", "command",
    "args", "path", "paths", "query", "description", "line", "text",
    "modelId", "currentModelId", "models", "availableModels", "_meta",
)
DIAGNOSTIC_ENUMS = {
    "kind": TOOL_KINDS | PERMISSION_KINDS,
    "status": TOOL_STATUSES,
    "sessionUpdate": frozenset({"tool_call", "tool_call_update"}),
}
MAX_DIAGNOSTIC_RECORDS = 64
MAX_DIAGNOSTIC_BYTES = 96 * 1024
MAX_DIAGNOSTIC_RECORD = 8192


def structural_shape(value, depth=0, work=None):
    """Fixed schema key presence/types, enum tags and counts; bounded traversal.

    The public subject/type keys are observed structurally, never interpreted as
    a different protocol. Missing differs from present-null in the fields map.
    """
    work = [192] if work is None else work
    if work[0] <= 0:
        return {"truncated": True}
    work[0] -= 1
    types = {dict: "object", list: "array", str: "string", int: "integer",
             float: "number", bool: "boolean", type(None): "null"}
    result = {"type": types.get(type(value), "unsupported")}
    if isinstance(value, (dict, list)):
        result["count"] = len(value)
        if depth >= 5 or work[0] <= 0:
            result["truncated"] = True
        elif isinstance(value, dict):
            fields = {}
            for key in DIAGNOSTIC_KEYS:
                if key in value:
                    if work[0] <= 0:
                        result["truncated"] = True
                        break
                    # Payload/history/extension containers are opaque even when
                    # they happen to contain another familiar schema key.
                    child_depth = 5 if key in {"content", "rawOutput", "_meta"} else depth + 1
                    fields[key] = structural_shape(value[key], child_depth, work)
                    item = value[key]
                    if isinstance(item, str) and len(item) <= 32 and item in DIAGNOSTIC_ENUMS.get(key, ()):
                        fields[key]["tag"] = item
            result["fields"] = fields
            result["unknownCount"] = len(value) - sum(key in value for key in DIAGNOSTIC_KEYS)
        else:
            result["items"] = [structural_shape(item, depth + 1, work) for item in value[:8]]
            if len(value) > 8:
                result["truncated"] = True
    return result


def diagnostic_tool_update(update):
    """Skip streaming prose; preserve tool and subject/type structural evidence."""
    if not isinstance(update, dict):
        return False
    tag = update.get("sessionUpdate")
    return (isinstance(tag, str) and tag in {"tool_call", "tool_call_update"}) or "subject" in update or "type" in update


class StructuralDiagnostic:
    """One exclusive 0600 regular file directly in the selected worktree.

    At most 64 records/96 KiB, 8 KiB per record, 192 shape nodes, depth 5,
    eight array items and 64 in-memory ID ordinals. Exhaustion disables capture;
    it cannot change permission decisions or normal receipts. No file is opened
    and no extra receipt is emitted unless --diagnostic-file is explicitly set.
    A bounded daemon queue owns disk writes: stalled storage cannot hold up the
    session. A missing capture-end marker means incomplete diagnostic evidence.
    """
    def __init__(self, cwd, filename, deadline):
        if not label(filename) or filename in {".", ".."}:
            raise Refusal("invalid-diagnostic-target")
        directory = os.open(cwd, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            self.fd = os.open(filename, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                              0o600, dir_fd=directory)
        finally:
            os.close(directory)
        self.deadline = deadline
        self.records = self.bytes = 0
        self.ordinals = {}
        self.disabled = False
        self.closed = False
        self.writes_failed = False
        self.output = queue.Queue(maxsize=MAX_DIAGNOSTIC_RECORDS + 1)
        self.writer = threading.Thread(target=self._write_records, daemon=True)
        try:
            self.writer.start()
        except Exception:
            os.close(self.fd)
            self.fd = None
            raise

    def _write_records(self):
        try:
            while True:
                raw = self.output.get()
                try:
                    if raw is None:
                        return
                    if not self.writes_failed:
                        # One write per bounded record. Regular-file writes may
                        # stall; only this daemon waits, never the session thread.
                        try:
                            if os.write(self.fd, raw) != len(raw):
                                self.writes_failed = self.disabled = True
                        except OSError:
                            self.writes_failed = self.disabled = True
                finally:
                    self.output.task_done()
        finally:
            os.close(self.fd)
            self.fd = None

    def close(self):
        if self.closed:
            return
        self.closed = True
        # record() reserves room for both a capture-bound and this final marker.
        raw = b'{"diagnostic":"acp-structure-v1","stage":"capture-end"}\n'
        self.output.put_nowait(raw)
        self.output.put_nowait(None)
        self.writer.join(timeout=max(0, min(0.05, self.deadline - time.monotonic())))
        self.ordinals.clear()

    def ordinal(self, tool):
        ident = tool.get("toolCallId") if isinstance(tool, dict) else None
        if not tool_call_id(ident):
            return None
        if ident not in self.ordinals and len(self.ordinals) < 64:
            self.ordinals[ident] = len(self.ordinals) + 1
        return self.ordinals.get(ident)

    def cache(self, session, tool):
        ident = tool.get("toolCallId") if isinstance(tool, dict) else None
        entry = session.tool_cache.get(ident) if tool_call_id(ident) else None
        states = {"open", "poison", "conflict", "closed", "expired", "foreign", "bound"}
        state = entry["state"] if entry and entry["state"] in states else "absent"
        return {"state": state, "fields": structural_shape(entry["fields"] if entry else {}),
                "sameSession": bool(entry and entry.get("session") == session.session_id),
                "dropped": bool(tool_call_id(ident) and ident in session.tool_dropped),
                "cacheClosed": session.tool_cache_closed,
                "untrackedClosed": session.tool_untracked_closed,
                "cacheCount": len(session.tool_cache), "cacheBytes": session.tool_cache_bytes}

    def record(self, stage, session, tool=None, params=None, before=None, context=None):
        if self.disabled or self.closed or self.fd is None:
            return
        # These arguments come only from the fixed call sites below. No arbitrary
        # event/metadata dictionary, source reason, key or value is serialised.
        if stage not in {"permission-ingress", "update-ingress", "update-result", "projection"}:
            return
        record = {"diagnostic": "acp-structure-v1", "stage": stage,
                  "ordinal": self.ordinal(tool), "tool": structural_shape(tool),
                  "cache": self.cache(session, tool)}
        ident = tool.get("toolCallId") if isinstance(tool, dict) else None
        record["toolIdAccepted"] = tool_call_id(ident)
        record["toolIdLabelAccepted"] = label(ident)
        raw_input = tool.get("rawInput") if isinstance(tool, dict) else None
        record["expectedSyntheticCommand"] = bool(isinstance(raw_input, dict) and (
            raw_input.get("command") == "touch OP318-DIAG-SENTINEL-2017" or (
                raw_input.get("command") == "touch" and
                raw_input.get("args") == ["OP318-DIAG-SENTINEL-2017"])))
        if params is not None:
            record["params"] = structural_shape(params)
            record["sameSession"] = bool(session.session_id and params.get("sessionId") == session.session_id)
        if before is not None:
            record["before"] = before
        if context is not None:
            record["projection"] = structural_shape(context)
            record["available"] = context.get("state") == "available"
            record["redacted"] = context.get("title") == REDACTED
            record["reasons"] = {reason: context.get("reason") == reason
                                 for reason in TOOL_CACHE_REASONS.values()}
        raw = json.dumps(record, ensure_ascii=True, separators=(",", ":")).encode() + b"\n"
        if (self.records >= MAX_DIAGNOSTIC_RECORDS - 2 or
                self.bytes + len(raw) > MAX_DIAGNOSTIC_BYTES - 256 or
                len(raw) > MAX_DIAGNOSTIC_RECORD):
            raw = b'{"diagnostic":"acp-structure-v1","stage":"capture-bound","truncated":true}\n'
            self.disabled = True
        self.output.put_nowait(raw)
        self.records += 1
        self.bytes += len(raw)


class Redacted(Exception):
    """A secret-like value anywhere fails the whole permission context closed."""


def printable_ascii(value):
    return PRINTABLE_ASCII.fullmatch(value) is not None


def secret_probes(value):
    """The value plus its percent-decoded and NFKC forms, closed under both, bounded.

    Text still changing after the last round has deeper layers left undecoded, so
    it fails closed (ValueError) instead of being checked only in part.
    """
    found, frontier = {value}, [value]
    for _ in range(PROBE_ROUNDS):
        following = []
        for text in frontier:
            for probe in (urllib.parse.unquote(text), unicodedata.normalize("NFKC", text)):
                if len(probe) > MAX_PROBE:
                    raise ValueError
                if probe not in found:
                    found.add(probe)
                    following.append(probe)
        if not following:
            return found
        frontier = following
    raise ValueError


def secret_text(value, patterns=SECRET_PATTERNS):
    probes = secret_probes(value)
    # A decoded form that is not printable ASCII after NFKC, such as percent-encoded
    # Cyrillic, cannot be checked by the ASCII patterns, so it fails closed.
    if not all(printable_ascii(unicodedata.normalize("NFKC", probe)) for probe in probes):
        raise ValueError
    return any(pattern.search(probe) for probe in probes for pattern in patterns)


def context_string(value, path=False, *, argv=False, command=False):
    if not isinstance(value, str) or not value or len(value) > 256:
        raise ValueError
    # Reject control bytes, DEL, bidi/format controls, surrogate code points and
    # line/paragraph separators. Display uses the NFKC form, so fullwidth or
    # compatibility separators cannot hide parent fragments from path reduction.
    if any(unicodedata.category(c).startswith(("C", "Zl", "Zp")) for c in value):
        raise ValueError
    if len(value.encode("utf-8")) > 256:
        raise ValueError
    # Non-ASCII context fails closed by design: the NFKC display form must be
    # printable ASCII, so homoglyphs and combining marks cannot disguise a name.
    display = unicodedata.normalize("NFKC", value)
    if not display or not printable_ascii(display) or len(display) > 256:
        raise ValueError
    # Fail the whole context closed before reducing paths, so a secret in a parent
    # directory, URL userinfo, command flag or prose prefix cannot survive as a fragment.
    if secret_text(value):
        raise Redacted
    # Free-form command/title/argument text cannot distinguish a spaced path
    # from prose or multiple arguments. Refuse rather than leak parent fragments.
    # Explicit path/paths fields can safely reduce the entire value.
    if not path and ("/" in display or "\\" in display) and any(c.isspace() or c in "\"'<>" for c in display):
        raise ValueError
    # Structured command/argv only. A title or prose string is never split into a command.
    # Fold "\" before the parent-segment check. Basename already treats it as "/",
    # so ".." written with backslashes would otherwise reduce to a visible name.
    if argv or command:
        folded = display.replace("\\", "/")
        if SHELL_META.search(display) or "~" in display or ".." in folded.split("/"):
            raise ValueError
    # The relative grammar is forward-slash only, and only for argv, not path fields.
    if argv and not path and RELATIVE_ARG.fullmatch(display):
        # Relative package paths such as ./... stay intact. Absolute paths still reduce below.
        return display
    def basename(text):
        name = text.replace("\\", "/").rstrip("/").rsplit("/", 1)[-1]
        if name in {"", ".", ".."}:
            raise ValueError
        return name
    return basename(display) if path or "/" in display or "\\" in display else display


def context_input(value, depth=0, path=False, *, argv=False, command=False):
    if depth > 3:
        raise ValueError
    if isinstance(value, str):
        return context_string(value, path, argv=argv, command=command)
    if isinstance(value, list):
        if not 1 <= len(value) <= 8:
            raise ValueError
        return [context_input(item, depth + 1, path, argv=argv, command=command) for item in value]
    if isinstance(value, dict):
        if not 1 <= len(value) <= 8:
            raise ValueError
        # No arbitrary input key is copied. Even a secret-bearing KEY may itself
        # contain a credential, so it fails the whole context closed.
        if any(secret_text(key) for key in value):
            raise Redacted
        clean = {}
        for key, item in value.items():
            if key not in RAW_FIELDS:
                continue
            # path/paths stay basename reduction. Inherited args/command checks
            # stay in force beneath them, so a nested path cannot skip shell or "..".
            child_path = key in {"path", "paths"}
            child_argv = argv or key == "args"
            child_command = command or key == "command"
            clean[key] = context_input(item, depth + 1, child_path, argv=child_argv, command=child_command)
        if not clean:
            raise ValueError
        return clean
    # ACP permits arbitrary JSON; this display subset intentionally does not.
    raise ValueError


def source_strings(value):
    """Every source string value, in order, for one joined check across fields.

    Keys are left out so they cannot separate a flag from its value; each key is
    checked on its own in context_input.
    """
    if isinstance(value, str):
        yield value
    elif isinstance(value, list):
        for item in value:
            yield from source_strings(item)
    elif isinstance(value, dict):
        for item in value.values():
            yield from source_strings(item)


def project_locations(locations):
    if not isinstance(locations, list) or len(locations) > 8:
        raise ValueError
    projected = []
    for location in locations:
        if not isinstance(location, dict):
            raise ValueError
        entry = {"path": context_string(location.get("path"), path=True)}
        line = location.get("line")
        if line is not None:
            if type(line) is not int or not 0 <= line <= 2147483647:
                raise ValueError
            entry["line"] = line
        projected.append(entry)
    return projected


def permission_context(tool):
    """Project one toolCall's supported fields. Callers may supply same-session cache fields.

    This function has no memory. It does not read transcripts, content or raw output.
    """
    insufficient = {"state": "insufficient-context"}
    try:
        if not isinstance(tool, dict):
            return insufficient
        # Bound the source as well as the output, including discarded fields.
        if len(json.dumps(tool, ensure_ascii=True, separators=(",", ":"))) > 4096:
            return insufficient
        if not tool_call_id(tool.get("toolCallId")) or tool.get("kind") not in TOOL_KINDS:
            return insufficient
        title = context_string(tool.get("title"))
        clean = {"state": "available", "kind": tool["kind"], "title": title}
        locations = tool.get("locations")
        if locations is not None:
            clean["locations"] = project_locations(locations)
        if tool.get("rawInput") is not None:
            clean["rawInput"] = context_input(tool["rawInput"])
        # A flag and its value may sit in separate strings, list items or fields
        # (`["-u", "user:password"]`), so the joined source text is checked too:
        # title, then every raw input value in source order, then location paths.
        # Concatenated without a separator, it also catches a name or userinfo
        # split across fields (`["--pa", "ssword=value"]`).
        joined = [tool["title"], *source_strings(tool.get("rawInput")), *(x["path"] for x in locations or ())]
        if secret_text(" ".join(joined)) or secret_text("".join(joined), GLUED_PATTERNS):
            raise Redacted
        if len(json.dumps(clean, ensure_ascii=True, separators=(",", ":"))) > MAX_CONTEXT:
            return insufficient
        return clean
    except Redacted:
        # Raised only after toolCallId and kind passed. Any redaction fails the
        # whole context closed: no projected title, location or input survives.
        redacted = {"state": "insufficient-context", "kind": tool["kind"], "title": REDACTED}
        if tool.get("rawInput") is not None:
            redacted["rawInput"] = REDACTED
        return redacted
    except (ValueError, TypeError, UnicodeError, RecursionError):
        return insufficient


def tracked_update_fields(update):
    """Sanitized metadata fields from one tool_call update. Content and output are ignored."""
    fields = {}
    if "kind" in update and update["kind"] is not None:
        if update["kind"] not in TOOL_KINDS:
            raise ValueError
        fields["kind"] = update["kind"]
    if "title" in update and update["title"] is not None:
        fields["title"] = context_string(update["title"])
    if "locations" in update and update["locations"] is not None:
        fields["locations"] = project_locations(update["locations"])
    if "rawInput" in update and update["rawInput"] is not None:
        encoded = json.dumps(update["rawInput"], ensure_ascii=True, separators=(",", ":"))
        if len(encoded) > 4096:
            raise ValueError
        fields["rawInput"] = context_input(update["rawInput"])
    joined = []
    if isinstance(update.get("title"), str):
        joined.append(update["title"])
    if update.get("rawInput") is not None:
        joined.extend(source_strings(update["rawInput"]))
    locations = update.get("locations")
    if isinstance(locations, list):
        for location in locations:
            if isinstance(location, dict) and isinstance(location.get("path"), str):
                joined.append(location["path"])
    if joined and (secret_text(" ".join(joined)) or secret_text("".join(joined), GLUED_PATTERNS)):
        raise Redacted
    return fields


class Session:
    """State machine is independent of provider transport for offline qualification."""
    def __init__(self, send, emit, cwd, prompt, model, clock=None, diagnostic=None):
        self.send, self.emit = send, emit
        self.cwd, self.prompt, self.model = cwd, prompt, model
        self.session_id = None
        self.expected = {1: "initialize"}
        self.pending = {}
        self.seen_requests = set()
        self.complete = False
        self.clock = time.monotonic if clock is None else clock
        self.tool_cache = {}
        self.tool_dropped = set()
        self.tool_cache_bytes = 0
        self.tool_cache_closed = False
        self.tool_untracked_closed = False
        self.diagnostic = diagnostic

    def start(self):
        self.emit({"event": "starting", "modelRequested": self.model, "servedModel": "Unknown", "promptSHA256": hashlib.sha256(self.prompt.encode()).hexdigest()})
        self.call(1, "initialize", {"protocolVersion": 1, "clientCapabilities": {"fs": {"readTextFile": False, "writeTextFile": False}, "terminal": False}, "clientInfo": {"name": "dev-env-cursor-acp-source", "version": "1"}})

    def call(self, ident, method, params):
        self.send({"jsonrpc": "2.0", "id": ident, "method": method, "params": params})

    def response(self, ident, result):
        self.send({"jsonrpc": "2.0", "id": ident, "result": result})

    def provider(self, message):
        if message.get("jsonrpc") != "2.0":
            raise Refusal("invalid-jsonrpc")
        if "method" not in message:
            ident = message.get("id")
            if type(ident) is not int or ident not in self.expected or ("result" in message) == ("error" in message):
                raise Refusal("unexpected-response")
            method = self.expected.pop(ident)
            if "error" in message:
                raise Refusal("provider-error-" + method)
            result = message["result"]
            if not isinstance(result, dict):
                raise Refusal("invalid-result")
            if method == "initialize":
                if type(result.get("protocolVersion")) is not int or result["protocolVersion"] != 1:
                    raise Refusal("unsupported-protocol")
                self.expected[2] = "session-new"
                self.call(2, "session/new", {"cwd": self.cwd, "mcpServers": []})
            elif method == "session-new":
                if not label(result.get("sessionId")):
                    raise Refusal("invalid-session")
                self.session_id = result["sessionId"]
                self.emit({"event": "session-created", "sessionID": self.session_id})
                self.expected[3] = "prompt"
                self.call(3, "session/prompt", {"sessionId": self.session_id, "prompt": [{"type": "text", "text": self.prompt}]})
            else:
                if not label(result.get("stopReason")) or self.pending:
                    raise Refusal("invalid-prompt-result")
                self.close_tool_cache()
                self.complete = True
                self.emit({"event": "prompt-complete", "sessionID": self.session_id, "stopReason": result["stopReason"]})
            return
        method, params = message["method"], message.get("params")
        if not isinstance(method, str) or not isinstance(params, dict):
            raise Refusal("invalid-method")
        if self.diagnostic:
            if method == "session/request_permission":
                self.diagnostic.record("permission-ingress", self, params.get("toolCall"), params=params)
            elif method == "session/update" and (diagnostic_tool_update(params.get("update")) or "subject" in params or "type" in params):
                self.diagnostic.record("update-ingress", self, params.get("update"), params=params)
        if "id" not in message:
            if method == "session/update":
                # Fail closed: before session/new answers, no session id exists to correlate.
                if not self.session_id:
                    raise Refusal("early-session-update")
                if params.get("sessionId") != self.session_id:
                    raise Refusal("cross-session-update")
                update = params.get("update", {})
                if not isinstance(update, dict) or not label(update.get("sessionUpdate")):
                    raise Refusal("invalid-update")
                # Keep sanitized tool metadata only. Never emit content, output or text.
                self.observe_tool_update(update)
            return
        ident = message["id"]
        if not request_id(ident):
            raise Refusal("invalid-request-id")
        if (type(ident), ident) in self.seen_requests:
            raise Refusal("duplicate-request")
        if len(self.seen_requests) >= 256 or len(self.pending) >= 16:
            raise Refusal("request-bound-exceeded")
        self.seen_requests.add((type(ident), ident))
        if not self.session_id or 3 not in self.expected:
            raise Refusal("request-outside-prompt")
        if method == "session/request_permission":
            if params.get("sessionId") != self.session_id:
                raise Refusal("cross-session-request")
            options = params.get("options")
            if not isinstance(options, list) or not 1 <= len(options) <= 16:
                raise Refusal("invalid-permission-options")
            projected = []
            for item in options:
                if not isinstance(item, dict) or not label(item.get("optionId")) or not isinstance(item.get("kind"), str) or item["kind"] not in PERMISSION_KINDS:
                    raise Refusal("invalid-permission-options")
                if item["optionId"] in [x["optionID"] for x in projected]:
                    raise Refusal("duplicate-permission-option")
                projected.append({"optionID": item["optionId"], "kind": item["kind"]})
            self.pending[(type(ident), ident)] = ("permission", projected)
            self.emit({"event": "permission-required", "requestID": ident, "sessionID": self.session_id, "options": projected, "context": self.observed_permission_context(params.get("toolCall"))})
        elif method == "cursor/ask_question":
            if "sessionId" in params and params["sessionId"] != self.session_id:
                raise Refusal("cross-session-question")
            questions = params.get("questions")
            if not isinstance(questions, list) or not 1 <= len(questions) <= 8:
                raise Refusal("invalid-questions")
            clean, seen = [], set()
            for question in questions:
                if not isinstance(question, dict) or not label(question.get("id")) or question["id"] in seen or not bounded_text(question.get("prompt")) or type(question.get("allowMultiple", False)) is not bool:
                    raise Refusal("invalid-question")
                seen.add(question["id"])
                options = question.get("options")
                if not isinstance(options, list) or not 1 <= len(options) <= 16:
                    raise Refusal("invalid-question-options")
                choices, option_ids = [], set()
                for option in options:
                    if not isinstance(option, dict) or not label(option.get("id")) or option["id"] in option_ids or not bounded_text(option.get("label"), 512):
                        raise Refusal("invalid-question-option")
                    option_ids.add(option["id"])
                    choices.append({"id": option["id"], "label": option["label"]})
                clean.append({"id": question["id"], "prompt": question["prompt"], "options": choices, "allowMultiple": question.get("allowMultiple", False)})
            self.pending[(type(ident), ident)] = ("question", clean)
            self.emit({"event": "question-required", "requestID": ident, "sessionID": self.session_id, "questions": clean})
        else:
            # No plan acceptance, FS/terminal callback, or arbitrary extension execution.
            self.send({"jsonrpc": "2.0", "id": ident, "error": {"code": -32601, "message": "Unsupported client operation"}})
            self.emit({"event": "unsupported-request", "sessionID": self.session_id})

    def decision(self, value):
        ident = value.get("requestID")
        key = (type(ident), ident) if request_id(ident) else None
        if key not in self.pending or value.get("sessionID") != self.session_id:
            raise Refusal("stale-or-cross-session-decision")
        kind, expected = self.pending[key]
        if value.get("kind") != kind:
            raise Refusal("decision-kind-mismatch")
        if kind == "permission":
            if set(value) != {"requestID", "sessionID", "kind", "optionID"}:
                raise Refusal("invalid-permission-decision")
            option = next((x for x in expected if x["optionID"] == value.get("optionID")), None)
            if not option or option["kind"] != "reject_once":
                raise Refusal("permission-approval-not-supported")
            result = {"outcome": {"outcome": "selected", "optionId": option["optionID"]}}
        else:
            if set(value) != {"requestID", "sessionID", "kind", "answers"} or not isinstance(value.get("answers"), list):
                raise Refusal("invalid-question-decision")
            answers = value["answers"]
            if len(answers) != len(expected):
                raise Refusal("incomplete-question-decision")
            seen = set()
            for answer in answers:
                if not isinstance(answer, dict) or set(answer) != {"questionId", "selectedOptionIds"}:
                    raise Refusal("invalid-answer")
                question = next((x for x in expected if x["id"] == answer["questionId"]), None)
                selected = answer["selectedOptionIds"]
                if not question or answer["questionId"] in seen or not isinstance(selected, list) or not selected or any(not isinstance(x, str) for x in selected) or len(set(selected)) != len(selected) or (not question["allowMultiple"] and len(selected) != 1) or not set(selected) <= {x["id"] for x in question["options"]}:
                    raise Refusal("invalid-answer")
                seen.add(answer["questionId"])
            result = {"outcome": {"outcome": "answered", "answers": answers}}
        del self.pending[key]  # A transport failure never makes this request retryable.
        self.response(ident, result)
        self.emit({"event": "decision-sent", "requestID": ident, "sessionID": self.session_id, "kind": kind})

    def close_tool_cache(self):
        """Drop correlated tool metadata at prompt completion, failure or process exit."""
        self.tool_cache.clear()
        self.tool_dropped.clear()
        self.tool_cache_bytes = 0
        self.tool_cache_closed = True
        self.tool_untracked_closed = False

    def _drop_tool(self, tool_id):
        """Remember a bounded set of ids that did not fit. Past that, latch.

        Evicting an id, or storing every later one, would forget a poison or grow
        without a bound. The latch fails every id that is not already cached.
        """
        if tool_id in self.tool_dropped:
            return
        if len(self.tool_dropped) < MAX_TOOL_DROPPED:
            self.tool_dropped.add(tool_id)
            return
        self.tool_untracked_closed = True

    def _install_tool(self, tool_id, state, fields):
        if self.tool_cache_closed or not tool_call_id(tool_id):
            return
        if self.tool_untracked_closed and tool_id not in self.tool_cache:
            return
        current = self.tool_cache.get(tool_id)
        if current and current["state"] != "open":
            return
        if state != "open":
            fields = {}
        try:
            size = len(json.dumps(fields, ensure_ascii=True, separators=(",", ":")))
        except (TypeError, ValueError, UnicodeError):
            state, fields, size = "poison", {}, 2
        if state == "open" and size > MAX_CONTEXT:
            state, fields, size = "poison", {}, 2
        if current is None and len(self.tool_cache) >= MAX_TOOL_CACHE:
            self._drop_tool(tool_id)
            return
        old_bytes = current["bytes"] if current else 0
        if state == "open" and self.tool_cache_bytes - old_bytes + size > MAX_TOOL_CACHE_BYTES:
            if current is None:
                self._drop_tool(tool_id)
                return
            state, fields, size = "poison", {}, 2
        self.tool_cache_bytes += size - old_bytes
        self.tool_cache[tool_id] = {"state": state, "fields": fields, "at": self.clock(), "bytes": size, "session": self.session_id}

    def _cached_tool(self, tool_id):
        entry = self.tool_cache.get(tool_id)
        if entry:
            if entry.get("session") != self.session_id:
                return {"state": "foreign", "fields": {}}
            if entry["state"] == "open" and self.clock() - entry["at"] > TOOL_CACHE_TTL:
                self._install_tool(tool_id, "expired", {})
                entry = self.tool_cache.get(tool_id) or {"state": "expired", "fields": {}}
            return entry
        if tool_id in self.tool_dropped or self.tool_untracked_closed:
            return {"state": "bound", "fields": {}}
        return None

    def observe_tool_update(self, update):
        if not self.diagnostic or not diagnostic_tool_update(update):
            return self._observe_tool_update(update)
        before = self.diagnostic.cache(self, update)
        try:
            return self._observe_tool_update(update)
        finally:
            self.diagnostic.record("update-result", self, update, before=before)

    def _observe_tool_update(self, update):
        """Remember sanitized tool_call metadata for this session id only."""
        if self.tool_cache_closed or not self.session_id or not isinstance(update, dict):
            return
        if update.get("sessionUpdate") not in {"tool_call", "tool_call_update"}:
            return
        tool_id = update.get("toolCallId")
        if not tool_call_id(tool_id):
            return
        if tool_id not in self.tool_cache and (tool_id in self.tool_dropped or self.tool_untracked_closed):
            return
        status = update.get("status")
        # Any present invalid status, including null, tombstones the id.
        # Set membership hashes the value, so validate its type first.
        if "status" in update and (not isinstance(status, str) or status not in TOOL_STATUSES):
            self._install_tool(tool_id, "poison", {})
            return
        if status in {"completed", "failed"}:
            self._install_tool(tool_id, "closed", {})
            return
        current = self._cached_tool(tool_id)
        if current and current["state"] != "open":
            return
        try:
            incoming = tracked_update_fields(update)
        except Redacted:
            self._install_tool(tool_id, "poison", {})
            return
        except (ValueError, TypeError, UnicodeError, RecursionError):
            self._install_tool(tool_id, "poison", {})
            return
        if not incoming:
            return
        base = dict(current["fields"]) if current else {}
        if any(name in incoming and name in base and incoming[name] != base[name] for name in ("kind", "title")):
            self._install_tool(tool_id, "conflict", {})
            return
        combined = dict(base)
        combined.update(incoming)
        if "kind" in combined and "title" in combined:
            projected = permission_context({"toolCallId": tool_id, **combined})
            if projected.get("state") != "available":
                self._install_tool(tool_id, "poison", {})
                return
            stored = {key: projected[key] for key in TOOL_TRACKED if key in projected}
        else:
            stored = combined
        self._install_tool(tool_id, "open", stored)

    def _redacted_permission(self, tool, entry):
        kind = tool.get("kind") if isinstance(tool, dict) else None
        if kind not in TOOL_KINDS and entry:
            kind = entry.get("fields", {}).get("kind")
        if kind not in TOOL_KINDS:
            return {"state": "insufficient-context", "reason": "tool-call-unavailable"}
        result = {"state": "insufficient-context", "kind": kind, "title": REDACTED}
        cached_raw = bool(entry and "rawInput" in entry.get("fields", {}))
        if (isinstance(tool, dict) and tool.get("rawInput") is not None) or cached_raw:
            result["rawInput"] = REDACTED
        return result

    def observed_permission_context(self, tool):
        if not self.diagnostic:
            return self._observed_permission_context(tool)
        before = self.diagnostic.cache(self, tool)
        context = self._observed_permission_context(tool)
        self.diagnostic.record("projection", self, tool, before=before, context=context)
        return context

    def _observed_permission_context(self, tool):
        """Merge a partial permission toolCall with the same-session sanitized cache.

        Explicit kind or title that disagrees with the cache is a conflict. A poisoned,
        expired, closed or oversized id cannot fall back to an older benign snapshot.
        """
        if not isinstance(tool, dict):
            return permission_context(tool)
        tool_id = tool.get("toolCallId")
        explicit = [name for name in TOOL_TRACKED if tool.get(name) is not None]
        if not tool_call_id(tool_id):
            return permission_context(tool)
        if self.tool_cache_closed:
            if not explicit:
                return {"state": "insufficient-context", "reason": "no-correlated-tool-call"}
            return permission_context(tool)
        entry = self._cached_tool(tool_id)
        if entry is None:
            if not explicit:
                return {"state": "insufficient-context", "reason": "no-correlated-tool-call"}
            return permission_context(tool)
        if entry["state"] != "open":
            return {"state": "insufficient-context", "reason": TOOL_CACHE_REASONS.get(entry["state"], "tool-call-unavailable")}
        try:
            sanitized = {}
            for name in explicit:
                if name == "kind":
                    if tool["kind"] not in TOOL_KINDS:
                        raise ValueError
                    sanitized[name] = tool["kind"]
                elif name == "title":
                    sanitized[name] = context_string(tool["title"])
                elif name == "locations":
                    sanitized[name] = project_locations(tool["locations"])
                else:
                    encoded = json.dumps(tool["rawInput"], ensure_ascii=True, separators=(",", ":"))
                    if len(encoded) > 4096:
                        raise ValueError
                    sanitized[name] = context_input(tool["rawInput"])
        except Redacted:
            self._install_tool(tool_id, "poison", {})
            return self._redacted_permission(tool, entry)
        except (ValueError, TypeError, UnicodeError, RecursionError):
            self._install_tool(tool_id, "poison", {})
            return {"state": "insufficient-context", "reason": "tool-call-unavailable"}
        if any(name in sanitized and name in entry["fields"] and sanitized[name] != entry["fields"][name] for name in ("kind", "title")):
            self._install_tool(tool_id, "conflict", {})
            return {"state": "insufficient-context", "reason": "tool-call-conflict"}
        merged = {"toolCallId": tool_id}
        for name in TOOL_TRACKED:
            if name in explicit:
                merged[name] = tool[name]
            elif name in entry["fields"]:
                merged[name] = entry["fields"][name]
        if "kind" not in merged or "title" not in merged:
            return {"state": "insufficient-context", "reason": "no-correlated-tool-call"}
        context = permission_context(merged)
        if context.get("state") != "available":
            self._install_tool(tool_id, "poison", {})
            return context
        if any(name in sanitized and entry["fields"].get(name) != sanitized[name] for name in ("rawInput", "locations")):
            stored = {key: context[key] for key in TOOL_TRACKED if key in context}
            self._install_tool(tool_id, "open", stored)
        return context


def write_bounded(descriptor, raw, deadline, clock=time.monotonic):
    """Neither a stalled provider nor an unread coordinator pipe can extend ownership."""
    previous = os.get_blocking(descriptor)
    os.set_blocking(descriptor, False)
    try:
        remaining = memoryview(raw)
        while remaining:
            budget = deadline - clock()
            if budget <= 0:
                raise Refusal("deadline-exceeded")
            _, writable, _ = select.select([], [descriptor], [], budget)
            if not writable:
                raise Refusal("deadline-exceeded")
            try:
                count = os.write(descriptor, remaining)
            except BlockingIOError:
                continue
            if count <= 0:
                raise Refusal("transport-write-failed")
            remaining = remaining[count:]
    except OSError:
        raise Refusal("transport-write-failed") from None
    finally:
        os.set_blocking(descriptor, previous)


class ByteBudget:
    """Wire bytes of provider frames from parse start until handled, released FIFO.

    A reader waiting here stops reading its pipe, so the provider blocks on its
    own writes. Nothing outstanding always admits one frame, and no admitted frame
    exceeds the limit, so a single frame at the limit never waits forever.
    """
    def __init__(self, limit):
        self.limit = limit
        self.used = 0
        self.waiting = 0
        self.sizes = collections.deque()
        self.condition = threading.Condition()

    def acquire(self, count):
        with self.condition:
            self.waiting += 1
            try:
                while self.used and self.used + count > self.limit:
                    self.condition.wait()
            finally:
                self.waiting -= 1
            self.used += count
            self.sizes.append(count)

    def release_next(self):
        with self.condition:
            self.used -= self.sizes.popleft()
            self.condition.notify_all()


def read_frames(stream, events, source, budget=None):
    # Do not hold a BufferedReader lock on sys.stdin while the process exits.
    # A coordinator may leave its input pipe open after the total deadline.
    buffer = b""
    limit = MAX_PROVIDER_FRAME if source == "provider" else MAX_FRAME
    origin = FRAME_SOURCES[source]
    try:
        descriptor = stream.fileno()
        while True:
            readable, _, _ = select.select([descriptor], [], [], 0.25)
            if not readable:
                continue
            raw = os.read(descriptor, min(4096, limit + 1 - len(buffer)))
            if not raw:
                if buffer:
                    raise FrameRefusal("frame-truncated-eof", source=origin, byte_count=len(buffer))
                events.put((source + "-closed", None))
                return
            buffer += raw
            while b"\n" in buffer:
                frame, buffer = buffer.split(b"\n", 1)
                if len(frame) + 1 > limit:
                    raise FrameRefusal("frame-too-large", source=origin, byte_count=len(frame) + 1, limit=limit)
                if budget is not None:
                    # Before parsing: parsed objects can be many times the wire size.
                    budget.acquire(len(frame) + 1)
                events.put((source, strict_object(frame, maximum=limit)))
            if len(buffer) > limit:
                raise FrameRefusal("frame-too-large", source=origin, byte_count=len(buffer), limit=limit)
    except FrameRefusal as error:
        events.put(("failure", error))
    except Refusal as error:
        detail = "frame-duplicate-key" if str(error) == "duplicate-field" else "frame-malformed-json"
        events.put(("failure", FrameRefusal(detail, source=origin)))
    except (OSError, ValueError):
        events.put(("failure", "invalid-frame"))
    except Exception:
        events.put(("failure", "internal-error"))


def run_transport(process, coordinator_input, session, timeout, clock=time.monotonic, deadline=None):
    events = queue.Queue(maxsize=128)
    # Only provider frames take the byte budget; coordinator frames keep the 64 KiB bound.
    budget = ByteBudget(MAX_PROVIDER_FRAME)
    for stream, source, frames in [(process.stdout, "provider", budget), (coordinator_input, "decision", None)]:
        threading.Thread(target=read_frames, args=(stream, events, source, frames), daemon=True).start()
    deadline = clock() + timeout if deadline is None else deadline
    session.start()
    input_closed = False
    while not session.complete:
        remaining = deadline - clock()
        if remaining <= 0:
            raise Refusal("deadline-exceeded")
        try:
            source, value = events.get(timeout=min(remaining, 0.25))
        except queue.Empty:
            continue
        if source == "provider":
            session.provider(value)
            value = None  # Drop the parsed frame before its bytes return to the budget.
            budget.release_next()
        elif source == "decision":
            session.decision(value)
        elif source == "decision-closed":
            input_closed = True
        elif source == "provider-closed":
            raise Refusal("provider-ended-before-result")
        else:
            if isinstance(value, Refusal):
                raise value
            raise Refusal(value)
        if input_closed and session.pending:
            raise Refusal("decision-input-closed")


def child_environment(source=None):
    """Only allowlisted names reach the third-party binary; nothing else is inherited."""
    source = os.environ if source is None else source
    return {name: source[name] for name in CHILD_ENVIRONMENT if name in source}


def signal_group(group, number):
    try:
        os.killpg(group, number)
    except OSError:
        pass  # Already gone (ProcessLookupError); cleanup never raises.


def leader_exited(process):
    if not hasattr(os, "waitid"):
        return process.poll() is not None
    try:
        # WNOWAIT leaves the leader unreaped, so its id cannot name another group yet.
        return os.waitid(os.P_PID, process.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT) is not None
    except OSError:
        return True  # Unknown leader state: stop waiting and send the SIGKILL.


def stop_group(process, grace=SHUTDOWN_GRACE, clock=time.monotonic):
    """SIGTERM the owned group, allow a short grace, then always SIGKILL the group.

    A child or grandchild that ignores SIGTERM must not outlive the client, so
    SIGKILL goes to the whole group even when the leader exited in time.
    """
    group = process.pid  # start_new_session=True makes the leader's pid the group id.
    try:
        signal_group(group, signal.SIGTERM)
        end = clock() + grace
        while clock() < end and not leader_exited(process):
            time.sleep(0.02)
    finally:
        # Always, even if the grace wait is interrupted.
        signal_group(group, signal.SIGKILL)
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            pass  # The group is already SIGKILLed; reaping must not raise past cleanup.


def exit_on_signal(signum, frame):
    """Turn SIGTERM/SIGHUP to the client into SystemExit so cleanup stops the child group."""
    raise SystemExit(128 + signum)


def main(argv=None):
    process = None
    session = None
    deadline = None
    diagnostic = None
    def emit(value, best_effort=False):
        raw = json.dumps(value, ensure_ascii=True, separators=(",", ":"))
        if len(raw.encode("utf-8")) + 1 > MAX_FRAME:
            raise Refusal("receipt-too-large")
        data = raw.encode("utf-8") + b"\n"
        if not best_effort:
            write_bounded(sys.stdout.fileno(), data, deadline)
            return
        descriptor = sys.stdout.fileno()
        previous = os.get_blocking(descriptor)
        os.set_blocking(descriptor, False)
        try:
            os.write(descriptor, data)  # Failure receipt is best effort; never wait.
        finally:
            os.set_blocking(descriptor, previous)
    try:
        parser = Arguments(description=__doc__)
        parser.add_argument("command", choices=["run"])
        parser.add_argument("--client", required=True)
        parser.add_argument("--worktree", required=True)
        parser.add_argument("--model", required=True)
        parser.add_argument("--prompt-file", required=True)
        parser.add_argument("--timeout", type=int, default=600)
        parser.add_argument("--diagnostic-file", help="Opt-in structural JSONL; exclusive private basename in --worktree")
        args = parser.parse_args(argv)
        deadline = time.monotonic() + max(1, min(args.timeout, 3600))
        client, cwd, prompt_path = Path(args.client), Path(args.worktree), Path(args.prompt_file)
        if not client.is_absolute() or not client.is_file() or not os.access(client, os.X_OK) or not cwd.is_absolute() or not cwd.is_dir() or not prompt_path.is_absolute() or not prompt_path.is_file() or not label(args.model) or not 1 <= args.timeout <= 3600:
            raise Refusal("invalid-input")
        with prompt_path.open("rb") as handle:
            raw = handle.read(MAX_PROMPT + 1)
        if not 1 <= len(raw) <= MAX_PROMPT:
            raise Refusal("invalid-prompt-size")
        try:
            prompt = raw.decode("utf-8")
        except UnicodeError:
            raise Refusal("invalid-prompt-encoding") from None
        if args.diagnostic_file is not None:
            diagnostic = StructuralDiagnostic(str(cwd.resolve()), args.diagnostic_file, deadline)
        # A signal to the client must still reach the finally that stops the child
        # group. While Popen runs, the child exists but is not owned yet, so only
        # record the signal there and replay it once `process` is assigned.
        pending = []
        signal.signal(signal.SIGTERM, lambda signum, frame: pending.append(signum))
        signal.signal(signal.SIGHUP, lambda signum, frame: pending.append(signum))
        process = subprocess.Popen([str(client.resolve()), "--model", args.model, "acp"], cwd=str(cwd.resolve()), env=child_environment(), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True)
        signal.signal(signal.SIGTERM, exit_on_signal)
        signal.signal(signal.SIGHUP, exit_on_signal)
        if pending:
            raise SystemExit(128 + pending[0])
        def send(value):
            raw = json.dumps(value, ensure_ascii=True, separators=(",", ":")).encode() + b"\n"
            if len(raw) > MAX_FRAME:
                raise Refusal("outbound-frame-too-large")
            write_bounded(process.stdin.fileno(), raw, deadline)
        session = Session(send, emit, str(cwd.resolve()), prompt, args.model, diagnostic=diagnostic)
        try:
            run_transport(process, sys.stdin.buffer, session, args.timeout, deadline=deadline)
            return 0
        finally:
            session.close_tool_cache()
    except Exception as error:
        # Reason codes only: never an exception message, provider text or traceback.
        reason = str(error) if isinstance(error, Refusal) else "process-io-failed" if isinstance(error, OSError) else "internal-error"
        try:
            receipt = {"event": "failed", "reason": reason}
            if isinstance(error, FrameRefusal):
                receipt.update(error.metadata)
            emit(receipt, best_effort=True)
        except Exception:
            pass  # A blocked coordinator output must never postpone child cleanup.
        return 2
    finally:
        if process is not None:
            stop_group(process)
        if diagnostic is not None:
            diagnostic.close()


if __name__ == "__main__":
    raise SystemExit(main())
