"""Bounded private IPC only; never capture stderr or print raw helper replies."""

import hashlib
import json
import os
from pathlib import Path
import re
import selectors
import subprocess
import time

from collector import Refused, require


def strict_json(data):
    def pairs(rows):
        result = {}
        for key, value in rows:
            if key in result:
                raise ValueError()
            result[key] = value
        return result
    return json.loads(data, object_pairs_hook=pairs,
                      parse_constant=lambda _: (_ for _ in ()).throw(ValueError()))


class BinaryPipe:
    def __init__(self, binary, sha256, *, max_input, max_reply):
        require(isinstance(sha256, str) and re.fullmatch(r"[0-9a-f]{64}", sha256), "UnverifiedHelperBinary")
        path = Path(binary).resolve(strict=True)
        require(path.is_file() and path.stat().st_size <= 32 * 1024 * 1024, "UnverifiedHelperBinary")
        checksum = hashlib.sha256()
        with path.open("rb") as source:
            while block := source.read(65536):
                checksum.update(block)
        require(checksum.hexdigest() == sha256, "UnverifiedHelperBinary")
        self.sha256, self.max_input, self.max_reply = sha256, max_input, max_reply
        self.started, self.closed = time.monotonic(), False
        # Credentials stay inside the separately reviewed Mac transport. Neither
        # helper inherits provider, HOME, proxy, auth-plugin or TLS override env.
        self.process = subprocess.Popen([str(path)], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=subprocess.DEVNULL, env={}, close_fds=True, start_new_session=True)
        os.set_blocking(self.process.stdin.fileno(), False)
        os.set_blocking(self.process.stdout.fileno(), False)

    def exchange(self, value):
        require(not self.closed, "HelperAlreadyFailed")
        try:
            data = json.dumps(value, ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode("utf-8") + b"\n"
            require(len(data) <= self.max_input, "HelperInputBudgetExceeded")
            deadline = min(self.started + 120, time.monotonic() + 6)
            sent, reply = 0, bytearray()
            with selectors.DefaultSelector() as selector:
                selector.register(self.process.stdin, selectors.EVENT_WRITE)
                selector.register(self.process.stdout, selectors.EVENT_READ)
                while True:
                    remaining = deadline - time.monotonic()
                    require(remaining > 0, "HelperDeadlineExceeded")
                    events = selector.select(remaining)
                    require(bool(events), "HelperDeadlineExceeded")
                    for key, mask in events:
                        if key.fileobj is self.process.stdin:
                            try:
                                sent += os.write(key.fd, data[sent:])
                            except BlockingIOError:
                                continue
                            if sent == len(data):
                                selector.unregister(self.process.stdin)
                        else:
                            try:
                                block = os.read(key.fd, min(65536, self.max_reply + 1 - len(reply)))
                            except BlockingIOError:
                                continue
                            require(bool(block), "HelperEOF")
                            reply.extend(block)
                            require(len(reply) <= self.max_reply, "HelperReplyBudgetExceeded")
                            if b"\n" in reply:
                                require(sent == len(data) and reply.count(b"\n") == 1 and reply.endswith(b"\n"),
                                        "MalformedHelperReply")
                                return strict_json(bytes(reply[:-1]))
        except Refused:
            self.close()
            raise
        except (OSError, ValueError, UnicodeError, RecursionError):
            self.close()
            raise Refused("HelperPipeRefused") from None

    def close(self):
        if self.closed:
            return
        self.closed = True
        for stream in (self.process.stdin, self.process.stdout):
            stream.close()
        if self.process.poll() is None:
            self.process.terminate()
        try:
            self.process.wait(timeout=1)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=1)


class CELChecker:
    def __init__(self, pipe):
        self.pipe, self.calls = pipe, 0
        reply = self.pipe.exchange({"op": "hello"})
        require(reply == {"ok": True, "profile": "cel-go0.30.0"}, "UnsupportedCELHelper")

    def equal(self, actual, expected):
        try:
            require(isinstance(actual, str) and isinstance(expected, str), "MalformedCELExpression")
            require(len(actual.encode("utf-8")) <= 65536 and len(expected.encode("utf-8")) <= 65536,
                    "CELExpressionBudgetExceeded")
            require(self.calls < 255, "CELRequestBudgetExceeded")
            self.calls += 1
            reply = self.pipe.exchange({"op": "compare", "actual": actual, "expected": expected})
            require(isinstance(reply, dict) and reply.get("profile") == "cel-go0.30.0", "UnsupportedCELHelper")
            if reply.get("ok") is not True:
                require(set(reply) == {"ok", "profile", "code"} and reply.get("code") in {
                    "InvalidCEL", "CELFailure", "CELBudgetExceeded", "MalformedCELRequest", "CELInputRefused"},
                    "MalformedCELHelperReply")
                raise Refused(reply["code"])
            require(set(reply) == {"ok", "profile", "equal"} and type(reply["equal"]) is bool, "MalformedCELHelperReply")
            return reply["equal"]
        except Refused:
            self.pipe.close()
            raise
        except (UnicodeError, ValueError, TypeError):
            self.pipe.close()
            raise Refused("MalformedCELExpression") from None
