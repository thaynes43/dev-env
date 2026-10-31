#!/usr/bin/env python3
"""Offline checker for one cursor-acp-session.py receipt stream.

The verdict names a few stages. It does not launch a provider, send a decision,
approve a permission, infer a served model or a cost, or copy assistant, question,
or tool text. A decision-sent line is a client report of a transport send.
"""
import io
import json
import re
import sys
import unicodedata

MAX_FRAME = 65536
MAX_EVENTS = 515
MAX_SEEN = 256
MAX_PENDING = 16
MAX_UNSUPPORTED = 256
LABEL = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")
REASON = re.compile(r"^[a-z0-9][a-z0-9-]{0,63}$")
# Framing detail on an "invalid-frame" failure: fixed kinds and byte counts only.
FRAME_DETAILS = frozenset({"frame-too-large", "frame-malformed-json", "frame-duplicate-key", "frame-truncated-eof"})
MAX_FRAME_COUNT = 1 << 31
PERMISSION_KINDS = frozenset({"allow_once", "allow_always", "reject_once", "reject_always"})
DECISION_KINDS = frozenset({"question", "permission"})
# Mirrors cursor-acp-session.py permission_context(); a test pins these to its constants.
TOOL_KINDS = frozenset({"read", "edit", "delete", "move", "search", "execute", "think", "fetch", "switch_mode", "other"})
RAW_FIELDS = frozenset({"command", "args", "path", "paths", "query", "description"})
CONTEXT_STRING_MAX = 256
CONTEXT_ENTRIES_MAX = 8
CONTEXT_DEPTH_MAX = 3
CONTEXT_MAX = 2048
REDACTED = "[REDACTED]"
PRINTABLE_ASCII = re.compile(r"[ -~]*")


class Refusal(Exception):
    def __init__(self, reason):
        self.reason = reason
        super().__init__(reason)


def safe_reason(error):
    reason = getattr(error, "reason", "")
    if isinstance(reason, str) and REASON.fullmatch(reason):
        return reason
    return "internal-error"


def label_ok(value):
    return isinstance(value, str) and LABEL.fullmatch(value) is not None


def request_id_ok(value):
    return (type(value) is int and 0 <= value <= 2147483647) or label_ok(value)


def bounded_text(value, maximum):
    if not isinstance(value, str):
        return False
    if any(ord(char) < 32 and char not in "\n\t" for char in value):
        return False
    try:
        size = len(value.encode("utf-8"))
    except UnicodeError:
        return False
    return 0 < size <= maximum


def frame_detail(event):
    """Validate an invalid-frame detail: a fixed kind plus bounded byte counts, never text."""
    detail = event["detail"]
    if detail not in FRAME_DETAILS:
        raise Refusal("invalid-failure-detail")
    counted = detail in ("frame-too-large", "frame-truncated-eof")
    if ("byte_count" in event) != counted or ("limit" in event) != (detail == "frame-too-large"):
        raise Refusal("invalid-failure-detail")
    out = {"detail": detail}
    for key, name in (("byte_count", "byteCount"), ("limit", "limit")):
        if key in event:
            value = event[key]
            if type(value) is not int or not 0 < value <= MAX_FRAME_COUNT:
                raise Refusal("invalid-failure-detail")
            out[name] = value
    if detail == "frame-too-large" and out["byteCount"] <= out["limit"]:
        raise Refusal("invalid-failure-detail")
    return out


def exact(event, keys):
    present = set(event)
    required = set(keys)
    if present - required:
        raise Refusal("unexpected-field")
    if required - present:
        raise Refusal("missing-field")


def strict_object(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            if type(key) is not str or key in result:
                raise Refusal("duplicate-field")
            result[key] = value
        return result

    try:
        value = json.loads(
            raw,
            object_pairs_hook=pairs,
            parse_constant=lambda _constant: (_ for _ in ()).throw(ValueError()),
        )
    except Refusal:
        raise
    except (ValueError, UnicodeError, RecursionError):
        raise Refusal("invalid-json") from None
    if type(value) is not dict:
        raise Refusal("object-required")
    return value


def decode_line(raw):
    if isinstance(raw, str):
        try:
            raw = raw.encode("utf-8")
        except UnicodeError:
            raise Refusal("invalid-json") from None
    if not isinstance(raw, (bytes, bytearray)):
        raise Refusal("invalid-json")
    if raw.endswith(b"\n"):
        raw = raw[:-1]
    if raw.endswith(b"\r"):
        raw = raw[:-1]
    if len(raw) + 1 > MAX_FRAME:
        raise Refusal("frame-too-large")
    if not raw.strip():
        raise Refusal("invalid-json")
    return strict_object(bytes(raw))


def each_line(source):
    if isinstance(source, str):
        try:
            source = source.encode("utf-8")
        except UnicodeError:
            raise Refusal("invalid-json") from None
    if isinstance(source, (bytes, bytearray)):
        source = io.BytesIO(bytes(source))
    if hasattr(source, "readline"):
        while True:
            raw = source.readline(MAX_FRAME + 1)
            if not raw:
                return
            if isinstance(raw, str):
                try:
                    encoded = raw.encode("utf-8")
                except UnicodeError:
                    raise Refusal("invalid-json") from None
            else:
                encoded = raw
            if len(encoded) > MAX_FRAME:
                raise Refusal("frame-too-large")
            yield encoded
        return
    for raw in source:
        yield raw


def validate_questions(questions):
    """Check the structured question shape, then drop the text."""
    if not isinstance(questions, list) or not 1 <= len(questions) <= 8:
        raise Refusal("invalid-question")
    seen_questions = set()
    for question in questions:
        if type(question) is not dict or set(question) != {"id", "prompt", "options", "allowMultiple"}:
            raise Refusal("invalid-question")
        if (
            not label_ok(question["id"])
            or question["id"] in seen_questions
            or not bounded_text(question["prompt"], 2048)
            or type(question["allowMultiple"]) is not bool
        ):
            raise Refusal("invalid-question")
        seen_questions.add(question["id"])
        options = question["options"]
        if not isinstance(options, list) or not 1 <= len(options) <= 16:
            raise Refusal("invalid-question")
        seen_options = set()
        for option in options:
            if type(option) is not dict or set(option) != {"id", "label"}:
                raise Refusal("invalid-question")
            if not label_ok(option["id"]) or option["id"] in seen_options or not bounded_text(option["label"], 512):
                raise Refusal("invalid-question")
            seen_options.add(option["id"])


def validate_permission_options(options):
    if not isinstance(options, list) or not 1 <= len(options) <= 16:
        raise Refusal("invalid-permission")
    seen = set()
    for option in options:
        if type(option) is not dict or set(option) != {"optionID", "kind"}:
            raise Refusal("invalid-permission")
        kind = option["kind"]
        if (
            not label_ok(option["optionID"])
            or option["optionID"] in seen
            or not isinstance(kind, str)
            or kind not in PERMISSION_KINDS
        ):
            raise Refusal("invalid-permission")
        seen.add(option["optionID"])


def context_string_ok(value, path=False):
    """One receipt string: the session's display form, so ASCII with no path separator."""
    if not isinstance(value, str) or not value or len(value) > CONTEXT_STRING_MAX:
        return False
    if any(unicodedata.category(char).startswith(("C", "Zl", "Zp")) for char in value):
        return False
    try:
        if len(value.encode("utf-8")) > CONTEXT_STRING_MAX:
            return False
    except UnicodeError:
        return False
    if PRINTABLE_ASCII.fullmatch(unicodedata.normalize("NFKC", value)) is None or PRINTABLE_ASCII.fullmatch(value) is None:
        return False
    # The session reduces every path-like string to its basename before emitting it,
    # and refuses "." and ".." only in path fields, so a non-path "." (for example
    # an argument) is valid output.
    return "/" not in value and "\\" not in value and not (path and value in {".", ".."})


def context_input_ok(value, depth=0, path=False):
    if depth > CONTEXT_DEPTH_MAX:
        return False
    if isinstance(value, str):
        return context_string_ok(value, path)
    if isinstance(value, list):
        return 1 <= len(value) <= CONTEXT_ENTRIES_MAX and all(context_input_ok(item, depth + 1, path) for item in value)
    if type(value) is dict:
        return (
            1 <= len(value) <= CONTEXT_ENTRIES_MAX
            and set(value) <= RAW_FIELDS
            and all(context_input_ok(item, depth + 1, key in {"path", "paths"}) for key, item in value.items())
        )
    return False


def context_locations_ok(locations):
    if not isinstance(locations, list) or len(locations) > CONTEXT_ENTRIES_MAX:
        return False
    for location in locations:
        if type(location) is not dict or "path" not in location or set(location) - {"path", "line"}:
            return False
        if not context_string_ok(location["path"], True):
            return False
        if "line" in location and not (type(location["line"]) is int and 0 <= location["line"] <= 2147483647):
            return False
    return True


def validate_permission_context(context):
    """Check #758's bounded permission context, then drop every string.

    Returns "available" or "insufficient". Anything the session would not emit
    raises Refusal("invalid-permission-context"). The text is never returned.
    """
    if type(context) is not dict:
        raise Refusal("invalid-permission-context")
    state = context.get("state")
    keys = set(context)
    ok = False
    if state == "insufficient-context":
        # Bare, or the fail-closed redaction shape: kind kept, text replaced.
        if keys == {"state"}:
            ok = True
        elif keys in ({"state", "kind", "title"}, {"state", "kind", "title", "rawInput"}):
            ok = (
                isinstance(context["kind"], str)
                and context["kind"] in TOOL_KINDS
                and context["title"] == REDACTED
                and context.get("rawInput", REDACTED) == REDACTED
            )
        result = "insufficient"
    elif state == "available":
        result = "available"
        if {"state", "kind", "title"} <= keys <= {"state", "kind", "title", "locations", "rawInput"}:
            ok = (
                isinstance(context["kind"], str)
                and context["kind"] in TOOL_KINDS
                and context_string_ok(context["title"])
                and ("locations" not in context or context_locations_ok(context["locations"]))
                and ("rawInput" not in context or context_input_ok(context["rawInput"]))
            )
    else:
        result = None
    if not ok:
        raise Refusal("invalid-permission-context")
    try:
        size = len(json.dumps(context, ensure_ascii=True, separators=(",", ":")))
    except (TypeError, ValueError):
        raise Refusal("invalid-permission-context") from None
    if size > CONTEXT_MAX:
        raise Refusal("invalid-permission-context")
    return result


class Checker:
    """One pass over client receipts. Ids and kinds only; no transcript store."""

    def __init__(self):
        self.count = 0
        self.started = False
        self.terminal = False
        self.session_id = None
        self.pending = {}
        self.seen = set()
        self.questions_seen = 0
        self.decisions_seen = 0
        self.question_decisions = []
        self.permission_decisions = []
        self.permission_contexts = []
        self.unsupported = 0
        self.stop_reason = None
        self.helper_failed = None
        self.helper_failure_detail = None
        self.prompt_after = False
        self.prompt_without = False

    def add(self, event):
        if self.terminal:
            raise Refusal("event-after-terminal")
        if type(event) is not dict:
            raise Refusal("object-required")
        self.count += 1
        name = event.get("event")
        if name == "starting":
            self.on_starting(event)
        elif name == "session-created":
            self.on_session_created(event)
        elif name == "question-required":
            self.on_question(event)
        elif name == "permission-required":
            self.on_permission(event)
        elif name == "decision-sent":
            self.on_decision(event)
        elif name == "unsupported-request":
            self.on_unsupported(event)
        elif name == "prompt-complete":
            self.on_prompt(event)
        elif name == "failed":
            self.on_failed(event)
        else:
            raise Refusal("unknown-event")

    def remember(self, ident, kind):
        key = (type(ident), ident)
        if key in self.seen:
            raise Refusal("duplicate-request")
        if len(self.seen) >= MAX_SEEN or len(self.pending) >= MAX_PENDING:
            raise Refusal("event-bound-exceeded")
        self.seen.add(key)
        self.pending[key] = kind

    def require_session(self, value):
        if self.session_id is None:
            raise Refusal("session-not-established")
        if not label_ok(value):
            raise Refusal("invalid-session")
        if value != self.session_id:
            raise Refusal("cross-session")

    def on_starting(self, event):
        if self.started:
            raise Refusal("duplicate-event")
        if self.count != 1:
            raise Refusal("invalid-order")
        exact(event, ("event", "modelRequested", "servedModel", "promptSHA256"))
        digest = event["promptSHA256"]
        if not label_ok(event["modelRequested"]) or not isinstance(digest, str) or SHA256.fullmatch(digest) is None:
            raise Refusal("invalid-starting")
        if event["servedModel"] != "Unknown":
            raise Refusal("served-model-claim")
        self.started = True

    def on_session_created(self, event):
        exact(event, ("event", "sessionID"))
        if not self.started:
            raise Refusal("invalid-order")
        if self.session_id is not None:
            raise Refusal("duplicate-event")
        if not label_ok(event["sessionID"]):
            raise Refusal("invalid-session")
        self.session_id = event["sessionID"]

    def on_question(self, event):
        exact(event, ("event", "requestID", "sessionID", "questions"))
        self.require_session(event["sessionID"])
        ident = event["requestID"]
        if not request_id_ok(ident):
            raise Refusal("invalid-request-id")
        validate_questions(event["questions"])
        self.remember(ident, "question")
        self.questions_seen += 1

    def on_permission(self, event):
        # `context` is optional: receipts from before dev-env #758 carry none.
        if "context" in event:
            exact(event, ("event", "requestID", "sessionID", "options", "context"))
        else:
            exact(event, ("event", "requestID", "sessionID", "options"))
        self.require_session(event["sessionID"])
        ident = event["requestID"]
        if not request_id_ok(ident):
            raise Refusal("invalid-request-id")
        validate_permission_options(event["options"])
        state = validate_permission_context(event["context"]) if "context" in event else "absent"
        self.remember(ident, "permission")
        self.permission_contexts.append({"requestID": ident, "permissionContext": state})

    def on_decision(self, event):
        exact(event, ("event", "requestID", "sessionID", "kind"))
        self.require_session(event["sessionID"])
        ident = event["requestID"]
        if not request_id_ok(ident):
            raise Refusal("invalid-request-id")
        kind = event["kind"]
        if not isinstance(kind, str) or kind not in DECISION_KINDS:
            raise Refusal("invalid-decision")
        key = (type(ident), ident)
        if key not in self.pending:
            raise Refusal("stale-decision")
        if self.pending[key] != kind:
            raise Refusal("decision-kind-mismatch")
        del self.pending[key]
        self.decisions_seen += 1
        if kind == "question":
            self.question_decisions.append(ident)
        else:
            self.permission_decisions.append(ident)

    def on_unsupported(self, event):
        exact(event, ("event", "sessionID"))
        self.require_session(event["sessionID"])
        if self.unsupported >= MAX_UNSUPPORTED:
            raise Refusal("event-bound-exceeded")
        self.unsupported += 1

    def on_prompt(self, event):
        exact(event, ("event", "sessionID", "stopReason"))
        self.require_session(event["sessionID"])
        stop = event["stopReason"]
        if not label_ok(stop):
            raise Refusal("invalid-stop-reason")
        if self.pending:
            raise Refusal("prompt-complete-with-pending")
        self.stop_reason = stop
        self.terminal = True
        if self.questions_seen:
            self.prompt_after = True
        else:
            self.prompt_without = True

    def on_failed(self, event):
        keys = ("event", "reason")
        if event.get("reason") == "invalid-frame" and "detail" in event:
            keys += ("detail",) + tuple(k for k in ("byte_count", "limit") if k in event)
        exact(event, keys)
        reason = event["reason"]
        if not isinstance(reason, str) or REASON.fullmatch(reason) is None:
            raise Refusal("invalid-reason")
        if "detail" in event:
            self.helper_failure_detail = frame_detail(event)
        self.helper_failed = reason
        self.terminal = True

    def stages(self):
        names = []
        if self.session_id is not None:
            names.append("fresh-session-transport-established")
        if self.question_decisions:
            names.append("correlated-question-decision-sent")
        if self.permission_decisions:
            names.append("permission-decision-sent")
        if self.prompt_after:
            names.append("prompt-complete-after-decision")
        if self.prompt_without:
            names.append("prompt-complete-without-question")
        if self.helper_failed is not None:
            names.append("helper-failed")
        return names

    def verdict(self):
        if self.question_decisions:
            reply_reason = "decision-sent-is-transport-only"
        else:
            reply_reason = "no-correlated-question-decision"
        out = {
            "stages": self.stages(),
            "transportEstablished": self.session_id is not None,
            "questionDecisionSent": bool(self.question_decisions),
            "promptCompleteAfterDecision": self.prompt_after,
            "promptCompleteWithoutQuestion": self.prompt_without,
            "permissionDecisionSent": bool(self.permission_decisions),
            "pendingRequests": len(self.pending),
            "autonomousQuestionReply": False,
            "autonomousQuestionReplyReason": reply_reason,
            "receiptSender": "client",
            "servedModel": "not-inferred",
            "permissionApproval": "not-inferred",
            "cost": "not-inferred",
            "providerUnderstanding": "not-inferred",
        }
        if self.session_id is not None:
            out["sessionID"] = self.session_id
        if self.stop_reason is not None:
            out["stopReason"] = self.stop_reason
        if self.helper_failed is not None:
            out["helperFailed"] = self.helper_failed
        if self.helper_failure_detail is not None:
            out["helperFailureDetail"] = self.helper_failure_detail
        if self.questions_seen:
            out["questionOrigin"] = "provider-request"
        if self.decisions_seen:
            out["decisionOrigin"] = "coordinator-decision"
            out["decisionMeaning"] = "transport-send-only"
        if self.question_decisions:
            out["questionRequestIDs"] = list(self.question_decisions)
        if self.permission_decisions:
            out["permissionRequestIDs"] = list(self.permission_decisions)
            # The decision-sent receipt carries no option id. The client sends only
            # reject_once and refuses any other kind; that is its contract, not an
            # observed option. It is never a question reply and never an approval.
            out["permissionDecisionMeaning"] = "client-reject-once-only-not-approval"
            out["permissionCountedAsQuestionReply"] = False
        if self.permission_contexts:
            # State only. No title, path, location or raw input is ever copied.
            out["permissionContexts"] = list(self.permission_contexts)
        return out


def classify(source):
    checker = Checker()
    count = 0
    for raw in each_line(source):
        count += 1
        if count > MAX_EVENTS:
            raise Refusal("event-bound-exceeded")
        checker.add(decode_line(raw))
    if count == 0:
        raise Refusal("empty-input")
    return checker.verdict()


def write(value):
    sys.stdout.write(json.dumps(value, ensure_ascii=True, separators=(",", ":")) + "\n")


def main(argv=None):
    argv = list(sys.argv[1:] if argv is None else argv)
    handle = None
    try:
        if len(argv) != 1:
            raise Refusal("invalid-arguments")
        if argv[0] == "-":
            verdict = classify(sys.stdin.buffer)
        else:
            handle = open(argv[0], "rb")
            verdict = classify(handle)
        write(verdict)
        return 0
    except Refusal as error:
        write({"event": "refused", "reason": safe_reason(error)})
        return 2
    except OSError:
        write({"event": "refused", "reason": "unreadable-input"})
        return 2
    finally:
        if handle is not None:
            handle.close()


if __name__ == "__main__":
    raise SystemExit(main())
