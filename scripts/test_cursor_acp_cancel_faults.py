"""Pending-permission faults must fail closed when a late terminal is queued.

Coordinator EOF, a malformed permission decision, or cancellation while a
permission is pending must not become prompt success, and session/prompt is
sent once. Uses the real Session and run_transport with the same five-second
timeout. The helper-local queue seam reorders synthetic frames for both
queue.Queue and FrameQueue. publish_reader records the provider snapshot.
Answer drain is refused. This run's reader threads are joined before its
read descriptors are closed.
"""

import importlib.util
import json
import os
from pathlib import Path
import queue
import signal
import threading
import time
import unittest

SPEC = importlib.util.spec_from_file_location(
    "cursor_acp_session", Path(__file__).with_name("cursor-acp-session.py"))
ACP = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ACP)

INITIALIZE = {"jsonrpc": "2.0", "id": 1, "result": {"protocolVersion": 1}}
SESSION_NEW = {"jsonrpc": "2.0", "id": 2, "result": {"sessionId": "fixture-session"}}
PERMISSION = {
    "jsonrpc": "2.0",
    "id": 41,
    "method": "session/request_permission",
    "params": {
        "sessionId": "fixture-session",
        "toolCall": {"toolCallId": "fixture-call", "title": "Run fixture checks", "kind": "execute"},
        "options": [
            {"optionId": "allow", "kind": "allow_once", "name": "Allow"},
            {"optionId": "deny", "kind": "reject_once", "name": "Deny"},
        ],
    },
}
TERMINAL = {"jsonrpc": "2.0", "id": 3, "result": {"stopReason": "end_turn"}}
MALFORMED_DECISION = {"requestID": 41, "sessionID": "fixture-session", "kind": "permission"}
READER_JOIN_DEADLINE = 1


def kind_of(item):
    source, value = item
    if source == "decision-closed":
        return "eof"
    if source == "decision":
        return "decision"
    if source == "provider" and isinstance(value, dict):
        if value.get("method") == "session/request_permission":
            return "permission"
        if value.get("id") == 3 and "result" in value and "method" not in value:
            return "terminal"
        if value.get("id") == 1 and "result" in value:
            return "initialize"
        if value.get("id") == 2 and "result" in value:
            return "session-new"
    return "other"


class UnsupportedAnswerDrain(Exception):
    """Prompt-answer drain is outside these fault cases."""


class FaultQueue:
    """Release handshake, then the fault, only once the late terminal is held.

    This is the object installed for the helper's queue.Queue and FrameQueue
    constructors. It is not a FrameQueue implementation.
    """

    spec = None
    created = []

    def __init__(self, maxsize=128):
        spec = FaultQueue.spec
        self.maxsize = maxsize
        self.order = list(spec["order"])
        self.hold_before = spec["hold_before"]
        self.on_exhausted = spec["on_exhausted"]
        self.held = []
        self.released = []
        self.condition = threading.Condition(threading.RLock())
        self.idle = False
        self.partial = False
        self.buffered = b""
        self._generation = 0
        FaultQueue.created.append(self)

    def publish_reader(self, idle, partial, buffered):
        """Record one provider reader snapshot under the same lock as put/get."""
        with self.condition:
            self.idle = bool(idle)
            self.partial = bool(partial)
            self.buffered = buffered
            self._generation += 1
            self.condition.notify_all()

    def observe(self):
        raise UnsupportedAnswerDrain("answer-drain-unsupported")

    def wait_for_change(self, timeout):
        raise UnsupportedAnswerDrain("answer-drain-unsupported")

    def put(self, item, block=True, timeout=None):
        with self.condition:
            self.held.append(item)
            self.condition.notify_all()

    def get(self, block=True, timeout=None):
        deadline = None if timeout is None else time.monotonic() + timeout
        with self.condition:
            while True:
                item = self._take()
                if item is not None:
                    self.released.append(kind_of(item))
                    return item
                if not self.order and self.on_exhausted is not None and self._has("terminal"):
                    callback = self.on_exhausted
                    self.on_exhausted = None
                    callback()
                remaining = None if deadline is None else deadline - time.monotonic()
                if remaining is not None and remaining <= 0:
                    raise queue.Empty
                self.condition.wait(0.05 if remaining is None else min(remaining, 0.05))

    def _has(self, kind):
        return any(kind_of(item) == kind for item in self.held)

    def _take(self):
        if not self.order:
            return None
        want = self.order[0]
        if want == self.hold_before and not self._has("terminal"):
            return None
        for index, item in enumerate(self.held):
            if kind_of(item) == want:
                del self.order[0]
                return self.held.pop(index)
        return None


class PipePeer:
    def __init__(self, raw=b"", close_write=True):
        read_fd, write_fd = os.pipe()
        self.stdout = os.fdopen(read_fd, "rb", buffering=0)
        self.write_fd = write_fd
        if raw:
            os.write(write_fd, raw)
        if close_write:
            os.close(write_fd)
            self.write_fd = None

    def close_writer(self):
        if self.write_fd is not None:
            os.close(self.write_fd)
            self.write_fd = None

    def close_reader(self):
        self.stdout.close()


def frame_bytes(*messages):
    return b"".join(json.dumps(message, separators=(",", ":")).encode() + b"\n" for message in messages)


def refuse_launch(*args, **kwargs):
    raise AssertionError("provider launch")


class HelperModuleProxy:
    """Stand-in for one attribute of the imported helper. The stdlib module stays put."""

    def __init__(self, module):
        object.__setattr__(self, "_module", module)

    def __getattr__(self, name):
        return getattr(object.__getattribute__(self, "_module"), name)


class QueueProxy(HelperModuleProxy):
    def __getattr__(self, name):
        if name == "Queue":
            return FaultQueue
        return super().__getattr__(name)


class SubprocessProxy(HelperModuleProxy):
    def __getattr__(self, name):
        if name == "Popen":
            return refuse_launch
        return super().__getattr__(name)


class ThreadProxy(HelperModuleProxy):
    """Record Thread objects this run starts. Do not enumerate the process."""

    def __init__(self, module):
        super().__init__(module)
        object.__setattr__(self, "created", [])

    def __getattr__(self, name):
        if name == "Thread":
            return self._start
        return super().__getattr__(name)

    def _start(self, *args, **kwargs):
        thread = object.__getattribute__(self, "_module").Thread(*args, **kwargs)
        object.__getattribute__(self, "created").append(thread)
        return thread


class PendingPermissionLateTerminalTests(unittest.TestCase):
    def run_fault(self, order, hold_before, decision=b"", close_decision=True, on_exhausted=None):
        provider = PipePeer(frame_bytes(INITIALIZE, SESSION_NEW, PERMISSION, TERMINAL))
        coordinator = PipePeer(decision, close_write=close_decision)
        sent, events = [], []
        session = ACP.Session(
            sent.append, events.append, "/synthetic/project", "Synthetic offline task", "fixture-model")
        FaultQueue.spec = {"order": order, "hold_before": hold_before, "on_exhausted": on_exhausted}
        FaultQueue.created = []
        self.observed = None
        self.cleanup_record = {"readers": 0, "alive": ["unrun"], "read_closed": False, "errors": ["not-released"]}
        saved_queue, saved_subprocess, saved_threading = ACP.queue, ACP.subprocess, ACP.threading
        frame_defined = "FrameQueue" in vars(ACP)
        saved_frame_queue = vars(ACP).get("FrameQueue")
        tracker = ThreadProxy(ACP.threading)
        ACP.queue = QueueProxy(ACP.queue)
        ACP.subprocess = SubprocessProxy(ACP.subprocess)
        ACP.threading = tracker
        ACP.FrameQueue = FaultQueue
        body_error = None
        try:
            try:
                ACP.run_transport(provider, coordinator.stdout, session, 5)
            except BaseException as error:
                body_error = error
            self._release_transport(provider, coordinator, session, sent, events, tracker)
        finally:
            ACP.queue, ACP.subprocess, ACP.threading = saved_queue, saved_subprocess, saved_threading
            if frame_defined:
                ACP.FrameQueue = saved_frame_queue
            else:
                del ACP.FrameQueue
        if body_error is not None:
            body_error.add_note(json.dumps(self.cleanup_record, sort_keys=True))
            raise body_error
        record = self.cleanup_record
        if record["alive"] or record["errors"] or record["readers"] != 2 or not record["read_closed"]:
            self.fail(json.dumps(record, sort_keys=True))

    def _release_transport(self, provider, coordinator, session, sent, events, tracker):
        """Close writers, join this run's readers, and only then close read fds."""
        errors = []
        alive = []
        read_closed = False
        try:
            for peer in (provider, coordinator):
                try:
                    peer.close_writer()
                except OSError as error:
                    errors.append("writer-" + type(error).__name__)
            for thread in list(tracker.created):
                thread.join(READER_JOIN_DEADLINE)
                if thread.is_alive():
                    alive.append(thread.name)
            if not alive:
                read_closed = True
                for peer in (provider, coordinator):
                    try:
                        peer.close_reader()
                    except OSError as error:
                        errors.append("reader-" + type(error).__name__)
                        read_closed = False
        except Exception as error:
            errors.append("release-" + type(error).__name__)
            read_closed = False
        self.cleanup_record = {
            "readers": len(tracker.created),
            "alive": alive,
            "read_closed": read_closed,
            "errors": errors,
        }
        try:
            fault_queue = FaultQueue.created[0]
            self.observed = (
                session,
                list(sent),
                list(events),
                list(fault_queue.released),
                [item[1] for item in fault_queue.held if kind_of(item) == "terminal"],
            )
        except (IndexError, AttributeError) as error:
            self.cleanup_record["errors"].append("observed-" + type(error).__name__)
            self.observed = None

    def assert_reader_cleanup(self, primary):
        record = dict(self.cleanup_record)
        record["primary"] = str(primary)
        self.assertEqual(self.cleanup_record["readers"], 2, record)
        self.assertEqual(self.cleanup_record["alive"], [], record)
        self.assertEqual(self.cleanup_record["errors"], [], record)
        self.assertTrue(self.cleanup_record["read_closed"], record)

    def _check(self, method, *args):
        """Keep this run's cleanup record on a later oracle failure."""
        note = json.dumps(self.cleanup_record, sort_keys=True)
        try:
            method(*args, note)
        except AssertionError as error:
            error.add_note(note)
            raise

    def assert_terminal_cannot_succeed(self, session, sent, events, held_terminal):
        self._check(self.assertFalse, session.complete)
        self._check(self.assertEqual, session.expected.get(3), "prompt")
        self._check(self.assertIn, (int, 41), session.pending)
        self._check(self.assertEqual, [item.get("method") for item in sent], ["initialize", "session/new", "session/prompt"])
        self._check(self.assertFalse, any("result" in item for item in sent))
        self._check(self.assertEqual, held_terminal, [TERMINAL])
        self._check(
            self.assertEqual,
            [item["event"] for item in events if item["event"] in {"permission-required", "prompt-complete", "decision-sent"}],
            ["permission-required"])
        self._check(self.assertNotIn, "prompt-complete", json.dumps(events))

    def test_decision_eof_with_late_terminal_fails_closed_without_second_prompt(self):
        """Permission, coordinator EOF, then a valid terminal already queued."""
        with self.assertRaises(ACP.Refusal) as caught:
            self.run_fault(["initialize", "session-new", "permission", "eof"], "eof")
        self.assert_reader_cleanup(caught.exception)
        session, sent, events, released, held_terminal = self.observed
        self._check(self.assertEqual, str(caught.exception), "decision-input-closed")
        self._check(self.assertEqual, released, ["initialize", "session-new", "permission", "eof"])
        self.assert_terminal_cannot_succeed(session, sent, events, held_terminal)

    def test_malformed_permission_decision_with_late_terminal_fails_closed(self):
        """A decision missing optionID cannot be followed into prompt success."""
        with self.assertRaises(ACP.Refusal) as caught:
            self.run_fault(
                ["initialize", "session-new", "permission", "decision"],
                "decision",
                decision=frame_bytes(MALFORMED_DECISION))
        self.assert_reader_cleanup(caught.exception)
        session, sent, events, released, held_terminal = self.observed
        self._check(self.assertEqual, str(caught.exception), "invalid-permission-decision")
        self._check(self.assertEqual, released, ["initialize", "session-new", "permission", "decision"])
        self.assert_terminal_cannot_succeed(session, sent, events, held_terminal)

    def test_cancellation_with_late_terminal_does_not_resurrect_success(self):
        """SIGTERM through the real handler, with the terminal already queued."""
        previous = signal.signal(signal.SIGTERM, ACP.exit_on_signal)
        try:
            with self.assertRaises(SystemExit) as caught:
                self.run_fault(
                    ["initialize", "session-new", "permission"],
                    None,
                    close_decision=False,
                    on_exhausted=lambda: os.kill(os.getpid(), signal.SIGTERM))
        finally:
            signal.signal(signal.SIGTERM, previous)
        self.assert_reader_cleanup(caught.exception)
        session, sent, events, released, held_terminal = self.observed
        self._check(self.assertEqual, caught.exception.code, 128 + signal.SIGTERM)
        self._check(self.assertEqual, released, ["initialize", "session-new", "permission"])
        self.assert_terminal_cannot_succeed(session, sent, events, held_terminal)

    def test_reader_snapshot_is_recorded_and_answer_drain_is_refused(self):
        """Both constructors use FaultQueue. Drain methods refuse instead of completing."""
        self.assertIs(QueueProxy(queue).Queue, FaultQueue)
        FaultQueue.spec = {"order": [], "hold_before": None, "on_exhausted": None}
        FaultQueue.created = []
        events = FaultQueue(maxsize=128)
        self.assertIs(events, FaultQueue.created[0])
        events.publish_reader(True, False, b"")
        self.assertTrue(events.idle)
        self.assertFalse(events.partial)
        self.assertEqual(events.buffered, b"")
        self.assertEqual(events._generation, 1)
        events.publish_reader(False, True, b"partial-frame")
        self.assertFalse(events.idle)
        self.assertTrue(events.partial)
        self.assertEqual(events.buffered, b"partial-frame")
        self.assertEqual(events._generation, 2)
        with self.assertRaises(UnsupportedAnswerDrain) as observed:
            events.observe()
        self.assertEqual(str(observed.exception), "answer-drain-unsupported")
        with self.assertRaises(UnsupportedAnswerDrain) as waited:
            events.wait_for_change(0)
        self.assertEqual(str(waited.exception), "answer-drain-unsupported")
        self.assertEqual(events.held, [])
        self.assertEqual(events.released, [])


if __name__ == "__main__":
    unittest.main()
