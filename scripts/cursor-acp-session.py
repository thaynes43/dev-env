#!/usr/bin/env python3
"""One fresh Cursor ACP conversation; no terminal capture or permission approval."""
import argparse
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
MAX_PROMPT = 16384
LABEL = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
PERMISSION_KINDS = frozenset({"allow_once", "allow_always", "reject_once", "reject_always"})
# The provider child gets these names only when set, never the pod's tokens or keys.
# A Cursor auth variable needs its own separately approved change.
CHILD_ENVIRONMENT = ("PATH", "HOME", "LANG", "LC_ALL", "TERM", "TMPDIR", "XDG_RUNTIME_DIR")
SHUTDOWN_GRACE = 1.0


class Refusal(Exception):
    pass


class FrameRefusal(Refusal):
    """Fixed framing category; receipt metadata never includes source text."""
    def __init__(self, detail, *, byte_count=None, limit=None):
        super().__init__("invalid-frame")
        self.metadata = {"detail": detail}
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
        value = json.loads(raw, object_pairs_hook=pairs, parse_constant=lambda _: (_ for _ in ()).throw(ValueError()))
    except (ValueError, UnicodeError, RecursionError):
        raise Refusal("invalid-json") from None
    if not isinstance(value, dict):
        raise Refusal("object-required")
    return value


def label(value):
    return isinstance(value, str) and LABEL.fullmatch(value) is not None


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


def context_string(value, path=False):
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
    def basename(text):
        name = text.replace("\\", "/").rstrip("/").rsplit("/", 1)[-1]
        if name in {"", ".", ".."}:
            raise ValueError
        return name
    return basename(display) if path or "/" in display or "\\" in display else display


def context_input(value, depth=0, path=False):
    if depth > 3:
        raise ValueError
    if isinstance(value, str):
        return context_string(value, path)
    if isinstance(value, list):
        if not 1 <= len(value) <= 8:
            raise ValueError
        return [context_input(item, depth + 1, path) for item in value]
    if isinstance(value, dict):
        if not 1 <= len(value) <= 8:
            raise ValueError
        # No arbitrary input key is copied. Even a secret-bearing KEY may itself
        # contain a credential, so it fails the whole context closed.
        if any(secret_text(key) for key in value):
            raise Redacted
        clean = {key: context_input(item, depth + 1, key in {"path", "paths"})
                 for key, item in value.items() if key in RAW_FIELDS}
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


def permission_context(tool):
    """Project ONLY this request's supported toolCall fields; never recover history."""
    insufficient = {"state": "insufficient-context"}
    try:
        if not isinstance(tool, dict):
            return insufficient
        # Bound the source as well as the output, including discarded fields.
        if len(json.dumps(tool, ensure_ascii=True, separators=(",", ":"))) > 4096:
            return insufficient
        if not label(tool.get("toolCallId")) or tool.get("kind") not in TOOL_KINDS:
            return insufficient
        title = context_string(tool.get("title"))
        clean = {"state": "available", "kind": tool["kind"], "title": title}
        locations = tool.get("locations")
        if locations is not None:
            if not isinstance(locations, list) or len(locations) > 8:
                return insufficient
            projected = []
            for location in locations:
                if not isinstance(location, dict):
                    return insufficient
                entry = {"path": context_string(location.get("path"), path=True)}
                line = location.get("line")
                if line is not None:
                    if type(line) is not int or not 0 <= line <= 2147483647:
                        return insufficient
                    entry["line"] = line
                projected.append(entry)
            clean["locations"] = projected
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


class Session:
    """State machine is independent of provider transport for offline qualification."""
    def __init__(self, send, emit, cwd, prompt, model):
        self.send, self.emit = send, emit
        self.cwd, self.prompt, self.model = cwd, prompt, model
        self.session_id = None
        self.expected = {1: "initialize"}
        self.pending = {}
        self.seen_requests = set()
        self.complete = False

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
                self.complete = True
                self.emit({"event": "prompt-complete", "sessionID": self.session_id, "stopReason": result["stopReason"]})
            return
        method, params = message["method"], message.get("params")
        if not isinstance(method, str) or not isinstance(params, dict):
            raise Refusal("invalid-method")
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
                # Deliberately do not emit any update content, tool data or text.
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
            self.emit({"event": "permission-required", "requestID": ident, "sessionID": self.session_id, "options": projected, "context": permission_context(params.get("toolCall"))})
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


def read_frames(stream, events, source):
    # Do not hold a BufferedReader lock on sys.stdin while the process exits.
    # A coordinator may leave its input pipe open after the total deadline.
    buffer = b""
    limit = MAX_PROVIDER_FRAME if source == "provider" else MAX_FRAME
    try:
        descriptor = stream.fileno()
        while True:
            readable, _, _ = select.select([descriptor], [], [], 0.25)
            if not readable:
                continue
            raw = os.read(descriptor, min(4096, limit + 1 - len(buffer)))
            if not raw:
                if buffer:
                    raise FrameRefusal("frame-truncated-eof", byte_count=len(buffer))
                events.put((source + "-closed", None))
                return
            buffer += raw
            while b"\n" in buffer:
                frame, buffer = buffer.split(b"\n", 1)
                if len(frame) + 1 > limit:
                    raise FrameRefusal("frame-too-large", byte_count=len(frame) + 1, limit=limit)
                events.put((source, strict_object(frame, maximum=limit)))
            if len(buffer) > limit:
                raise FrameRefusal("frame-too-large", byte_count=len(buffer), limit=limit)
    except FrameRefusal as error:
        events.put(("failure", error))
    except Refusal as error:
        detail = "frame-duplicate-key" if str(error) == "duplicate-field" else "frame-malformed-json"
        events.put(("failure", FrameRefusal(detail)))
    except (OSError, ValueError):
        events.put(("failure", "invalid-frame"))
    except Exception:
        events.put(("failure", "internal-error"))


def run_transport(process, coordinator_input, session, timeout, clock=time.monotonic, deadline=None):
    events = queue.Queue(maxsize=128)
    for stream, source in [(process.stdout, "provider"), (coordinator_input, "decision")]:
        threading.Thread(target=read_frames, args=(stream, events, source), daemon=True).start()
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
    deadline = None
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
        session = Session(send, emit, str(cwd.resolve()), prompt, args.model)
        run_transport(process, sys.stdin.buffer, session, args.timeout, deadline=deadline)
        return 0
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


if __name__ == "__main__":
    raise SystemExit(main())
