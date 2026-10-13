"""Adapter for the unchanged Mac-owned transport-r2 protocol; no HTTP/auth code."""

import base64
import datetime
import re

import collector as C
from private_pipe import strict_json

TRANSPORT_ERRORS = {
    "ConfigUnavailable", "ConfigRefused", "ContextRefused", "AuthShapeRefused", "ServerShapeRefused",
    "RequestShapeRefused", "ListQueryRequired", "RequestBudgetExhausted", "RequestRefused", "RequestTimeout",
    "TransportRefused", "HTTPStatusRefused", "EncodingRefused", "DeclaredResponseBudgetExhausted",
    "ResponseBudgetExhausted", "ResponseReadRefused", "MalformedJSONResponse", "MalformedRequest", "InputBudgetExhausted",
}
NANOSECONDS = 1_000_000_000
EXCHANGE_BUDGET_NS = 6 * NANOSECONDS


def rfc3339nano_ns(value):
    """Exact UTC calendar parsing on Python3.9+, with no float or truncation."""
    C.require(isinstance(value, str), "MalformedTransportTimestamp")
    match = re.fullmatch(r"([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,9}))?Z", value)
    C.require(match is not None, "MalformedTransportTimestamp")
    try:
        year, month, day, hour, minute, second = (int(part) for part in match.groups()[:6])
        stamp = datetime.datetime(year, month, day, hour, minute, second, tzinfo=datetime.timezone.utc)
    except ValueError:
        raise C.Refused("MalformedTransportTimestamp") from None
    days = stamp.toordinal() - datetime.date(1970, 1, 1).toordinal()
    seconds = days * 86400 + hour * 3600 + minute * 60 + second
    fraction = int((match.group(7) or "").ljust(9, "0"))
    return seconds * NANOSECONDS + fraction


class TransportReader:
    def __init__(self, pipe, expected):
        C.require(C.digest(expected) == C.EXPECTED_BUNDLE_SHA256, "UnexpectedSourceBundle")
        self.pipe, self.requests, self.bytes, self.get_calls, self.failed = pipe, 0, 0, 0, False
        self.receipts = []
        self.lists = {C.api_path(v, r, C.NAMESPACE) for v, r, _ in C.KINDS}
        self.paths = set(self.lists) | {C.api_path(p["apiVersion"], p["resource"], name=p["metadata"]["name"])
                                      for p in expected["policies"]}
        self.paths |= {C.api_path("v1", "namespaces", name=C.NAMESPACE), C.api_path("admissionregistration.k8s.io/v1",
                       "validatingwebhookconfigurations", name="kyverno-resource-validating-webhook-cfg")}
        resources = {kind: (v, r) for v, r, kind in C.KINDS}
        self.paths |= {C.api_path(*resources[key.split("/")[0]], C.NAMESPACE, key.split("/")[1])
                       for key in expected["lineage"]}

    def get(self, path, query=None):
        try:
            C.require(not self.failed, "TransportAlreadyFailed")
            C.require(path in self.paths and self.get_calls < C.MAX_REQUESTS, "RequestShapeRefused")
            if path in self.lists:
                C.require(isinstance(query, dict) and set(query) <= {"limit", "continue"} and
                          type(query.get("limit")) is int and query["limit"] == C.PAGE_SIZE, "ListQueryRequired")
                token = query.get("continue", "")
                C.require(isinstance(token, str) and len(token.encode("utf-8")) <= 8192, "RequestShapeRefused")
            else:
                C.require(query is None, "RequestShapeRefused")
            self.get_calls += 1
            request = {"op": "get", "path": path, **({"query": query} if query is not None else {})}
            reply = self.pipe.exchange(request)
            C.require(isinstance(reply, dict) and set(reply) <= {"receipt", "bodyBase64"}, "MalformedTransportReply")
            rec = reply.get("receipt")
            C.require(isinstance(rec, dict) and set(rec) <= {"method", "path", "startedAt", "finishedAt", "httpStatus",
                      "responseBytes", "attempt", "totalResponseBytes", "error"}, "MalformedTransportReceipt")
            error = rec.get("error")
            C.require(error is None or error in TRANSPORT_ERRORS, "MalformedTransportError")
            for field in ("responseBytes", "attempt", "totalResponseBytes"):
                C.require(type(rec.get(field)) is int and rec[field] >= 0, "MalformedTransportAccounting")
            attempt, size, total = rec["attempt"], rec["responseBytes"], rec["totalResponseBytes"]
            previous_attempt = self.requests
            C.require(attempt in (self.requests, self.requests + 1) and attempt <= C.MAX_REQUESTS and
                      size <= C.MAX_RESPONSE_BYTES + 1 and total == self.bytes + size and
                      total <= C.MAX_TOTAL_BYTES + 1, "MalformedTransportAccounting")
            status = rec.get("httpStatus", 0)
            C.require(type(status) is int and 0 <= status <= 599, "MalformedTransportStatus")
            startup = error in {"ConfigUnavailable", "ConfigRefused", "ContextRefused", "AuthShapeRefused", "ServerShapeRefused",
                               "MalformedRequest", "InputBudgetExhausted"} and attempt == 0 and total == 0
            # A future helper allowlist drift can refuse a locally allowed path.
            # Preserve only this exact pre-dispatch refusal, never a success.
            redacted = error == "RequestShapeRefused" and rec.get("path") == "" and rec.get("method") == "GET" and \
                attempt == previous_attempt and size == 0 and total == self.bytes and \
                "httpStatus" not in rec and "bodyBase64" not in reply
            C.require(rec.get("path") == path and rec.get("method") == "GET" or
                      startup and rec.get("path", "") == "" and rec.get("method", "") == "" or redacted, "MalformedTransportPath")
            timestamps = []
            for field in ("startedAt", "finishedAt"):
                stamp = rec.get(field, "")
                timestamps.append(None if startup and stamp == "" else rfc3339nano_ns(stamp))
            C.require(all(stamp is None for stamp in timestamps) or all(stamp is not None for stamp in timestamps),
                      "MalformedTransportTimestamp")
            if timestamps[0] is not None:
                # Span includes pre-context validation and post-request cleanup;
                # the separately enforced Go HTTP context remains five seconds.
                C.require(0 <= timestamps[1] - timestamps[0] <= EXCHANGE_BUDGET_NS, "MalformedTransportTimestamp")
            self.requests, self.bytes = attempt, total
            self.receipts.append({key: rec[key] for key in rec})
            if error:
                C.require("bodyBase64" not in reply, "UnexpectedErrorBody")
                raise C.Refused(error, status if status else None)
            C.require(attempt == previous_attempt + 1 and status == 200 and
                      size <= C.MAX_RESPONSE_BYTES and total <= C.MAX_TOTAL_BYTES, "MalformedTransportSuccess")
            encoded = reply.get("bodyBase64")
            C.require(isinstance(encoded, str) and len(encoded) <= 4 * ((C.MAX_RESPONSE_BYTES + 2) // 3), "MalformedTransportBody")
            body = base64.b64decode(encoded, validate=True)
            C.require(len(body) == size, "MalformedTransportAccounting")
            return strict_json(body)
        except C.Refused:
            self.failed = True
            self.pipe.close()
            raise
        except (ValueError, TypeError, UnicodeError, RecursionError, KeyError):
            self.failed = True
            self.pipe.close()
            raise C.Refused("MalformedTransportReply") from None


def capture_report(reader, expected, cel_checker, *, kyverno_version):
    report = C.new_report(expected)
    try:
        C.audit(reader, expected, report, cel_checker, kyverno_version=kyverno_version)
    except C.Refused as error:
        report["failure"] = {"code": error.code}
        if error.http_status is not None:
            report["failure"]["httpStatus"] = error.http_status
    except Exception:
        report["failure"] = {"code": "MalformedOrUnexpectedInput"}
    report["finishedAt"] = C.timestamp()
    report["transport"] = {"revision": "transport-r2", "actualHTTPAttempts": reader.requests,
                           "responseBodyBytes": reader.bytes, "getInvocations": reader.get_calls,
                           "receipts": reader.receipts, "profile": "GET-only; no redirects/replay/proxies/HTTP2; private IPC"}
    return report
