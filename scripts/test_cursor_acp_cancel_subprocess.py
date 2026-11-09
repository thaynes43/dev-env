"""Pending-permission cancellation of the real helper and one synthetic child.

SOURCE ONLY until Root reviews this file and the ownership plan and clears
execution. Importing this module does not start a process. There is no
self-execution entry.

The prepared terminal is withheld from the provider pipe. That is zero
evidence about the helper queue. One helper and one fake child. No killpg.

os.kill(pid, 0) is not used. Absent, zombie, reused, and alive stay distinct.
Nonce, starttime, ppid, and pidfd are ownership. S and R are the only ready
states, and only after permission-required. The ready poll uses the remainder
of the permission deadline. Readiness is recorded before cleanup. After the
finalizer, the same recorded pid and starttime are classified again. A
finalizer signal is a test failure. A failed gate may still run that finalizer. The helper is an unreaped child or a pidfd
taken at launch. The child is signaled only through a pidfd opened while its
parent was still the helper. The synthetic child arms its own alarm before
any handshake or fifo wait.
"""

import json
import os
from pathlib import Path
import select
import signal
import subprocess
import sys
import tempfile
import time
import unittest

HELPER = Path(__file__).with_name("cursor-acp-session.py")
IDENTITY_DEADLINE = 5
PERMISSION_DEADLINE = 10
HELPER_EXIT_DEADLINE = 8
DRAIN_DEADLINE = 2
HELPER_GRACE_WAIT = 4
FALLBACK_WAIT = 2
CHILD_SELF_TIMEOUT = 40
MAX_EVENT_LINE = 8192
ALIVE_STATES = {"S", "R"}  # Ready runstates. D stays unreadiness and is not signaled.
TERMINAL = b'{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}\n'
PERMISSION = (
    b'{"jsonrpc":"2.0","id":41,"method":"session/request_permission","params":'
    b'{"sessionId":"fixture-session","toolCall":{"toolCallId":"fixture-call",'
    b'"title":"Run fixture checks","kind":"execute"},"options":['
    b'{"optionId":"allow","kind":"allow_once","name":"Allow"},'
    b'{"optionId":"deny","kind":"reject_once","name":"Deny"}]}}\n'
)


def child_script():
    """Synthetic ACP peer. One definition of the frames, inserted as literals."""
    return f"""import json, os, signal, sys
from pathlib import Path

def atomic(name, payload):
    temporary = Path(name + ".tmp")
    temporary.write_bytes(payload)
    fd = os.open(temporary, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)
    os.rename(temporary, name)

def log(method):
    with Path("inbound-methods.jsonl").open("a", encoding="utf-8") as handle:
        handle.write(method + "\\n")
        handle.flush()
        os.fsync(handle.fileno())

def emit(raw):
    sys.stdout.buffer.write(raw)
    sys.stdout.buffer.flush()

def expire(_signum, _frame):
    signal.signal(signal.SIGALRM, signal.SIG_DFL)
    try:
        Path("child-self-timeout").write_bytes(b"alarm\\n")
    except OSError:
        pass
    os._exit(3)
signal.signal(signal.SIGALRM, expire)
signal.alarm({CHILD_SELF_TIMEOUT})

nonce = Path("nonce.txt").read_text(encoding="utf-8").strip()
stat = Path("/proc/self/stat").read_text(encoding="utf-8")
fields = stat[stat.rfind(")") + 2:].split()
atomic("ownership.json", json.dumps({{
    "nonce": nonce,
    "pid": os.getpid(),
    "ppid": os.getppid(),
    "pgrp": os.getpgrp(),
    "sid": os.getsid(0),
    "starttime": int(fields[19]),
}}, separators=(",", ":")).encode("ascii") + b"\\n")
if sys.argv[1:] != ["--model", "fixture-model", "acp"]:
    sys.exit(2)
terminal = {TERMINAL!r}
permission = {PERMISSION!r}
for raw_line in sys.stdin.buffer:
    message = json.loads(raw_line)
    method = message.get("method")
    if not isinstance(method, str):
        break
    log(method)
    if method == "initialize" and message.get("id") == 1:
        emit(b'{{"jsonrpc":"2.0","id":1,"result":{{"protocolVersion":1}}}}\\n')
    elif method == "session/new" and message.get("id") == 2:
        emit(b'{{"jsonrpc":"2.0","id":2,"result":{{"sessionId":"fixture-session"}}}}\\n')
    elif method == "session/prompt" and message.get("id") == 3:
        atomic("late-terminal.frame", terminal)
        emit(permission)
        release = os.open("terminal-release.fifo", os.O_RDONLY)
        try:
            if os.read(release, 1):
                atomic("terminal-sent", terminal)
                emit(terminal)
        finally:
            os.close(release)
        hold = os.open("terminal-release.fifo", os.O_RDONLY)
        try:
            os.read(hold, 1)
        finally:
            os.close(hold)
    else:
        break
"""


def proc_fields(pid):
    """Parse /proc/pid/stat. None when the pid is absent. Never signals."""
    try:
        text = Path(f"/proc/{pid}/stat").read_text(encoding="utf-8")
    except FileNotFoundError:
        return None
    cut = text.rfind(")")
    if cut < 0:
        return None
    fields = text[cut + 2:].split()
    return {
        "state": fields[0],
        "ppid": int(fields[1]),
        "pgrp": int(fields[2]),
        "sid": int(fields[3]),
        "starttime": int(fields[19]),
    }


def observe(pid, starttime):
    """Classify one recorded starttime. os.kill(pid, 0) is not consulted."""
    info = proc_fields(pid)
    if info is None:
        return "absent", None
    if info["starttime"] != starttime:
        return "reused", info["state"]
    if info["state"] in {"Z", "X"}:
        return "zombie", info["state"]
    return "alive", info["state"]


def status_ppid(pid):
    for line in Path(f"/proc/{pid}/status").read_text(encoding="utf-8").splitlines():
        if line.startswith("PPid:"):
            return int(line.split()[1])
    raise AssertionError("ppid-missing")


def cmdline(pid):
    raw = Path(f"/proc/{pid}/cmdline").read_bytes()
    return [part.decode("utf-8", "replace") for part in raw.split(b"\0") if part]


def child_ids(pid):
    path = Path(f"/proc/{pid}/task/{pid}/children")
    try:
        listed = path.read_text(encoding="utf-8").split()
    except FileNotFoundError as error:
        raise AssertionError("children-file-missing") from error
    return [int(item) for item in listed]


def read_bounded(path, limit):
    data = Path(path).read_bytes()
    if len(data) > limit:
        raise AssertionError("record-too-large")
    return data


def pidfd_owned(pid, starttime, ppid):
    """Open the pid, then keep the fd only if that same starttime is still there.

    A reused pid fails the second read and is not signaled. The fd, once kept,
    stays bound to that process after the numeric pid can be reused.
    """
    if not isinstance(pid, int) or pid <= 1 or pid == os.getpid() or pid == os.getpgrp():
        return None
    try:
        fd = os.pidfd_open(pid)
    except OSError:
        return None
    info = proc_fields(pid)
    if info is None or info["starttime"] != starttime or info["ppid"] != ppid or info["pgrp"] == os.getpgrp():
        os.close(fd)
        return None
    return fd


def pidfd_send(fd, signum):
    """Signal one already-owned pidfd. False means that process is already gone."""
    try:
        signal.pidfd_send_signal(fd, signum)
    except ProcessLookupError:
        return False
    return True


def adopt_child_fd(root, nonce, helper_pid):
    """One bounded read of the handshake file. No scan and no group signal."""
    path = root / "ownership.json"
    try:
        if not path.is_file():
            return None
        record = json.loads(read_bounded(path, 1024).decode("ascii"))
        if record.get("nonce") != nonce:
            return None
        return pidfd_owned(record.get("pid"), record.get("starttime"), helper_pid)
    except (OSError, ValueError, TypeError, UnicodeError):
        return None


def next_line(stream, pending, deadline):
    while b"\n" not in pending:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise AssertionError("deadline")
        readable, _, _ = select.select([stream], [], [], min(0.25, remaining))
        if not readable:
            continue
        chunk = os.read(stream.fileno(), 4096)
        if not chunk:
            raise AssertionError("stdout-eof")
        pending.extend(chunk)
        if len(pending) > MAX_EVENT_LINE and b"\n" not in pending:
            raise AssertionError("event-line-too-large")
    line, _, rest = bytes(pending).partition(b"\n")
    pending[:] = rest
    if len(line) > MAX_EVENT_LINE:
        raise AssertionError("event-line-too-large")
    return line


class RunstateNotReady(Exception):
    """Owned processes stayed the same and left S/R. Identity failures are not this."""

    def __init__(self, helper_kind, helper_state, child_kind, child_state):
        self.helper_kind = helper_kind
        self.helper_state = helper_state
        self.child_kind = child_kind
        self.child_state = child_state
        super().__init__(f"helper={helper_kind}:{helper_state} child={child_kind}:{child_state}")


@unittest.skipUnless(
    os.environ.get("DEV_ENV_RUN_HELD_CURSOR_SUBPROCESS") == "1",
    "held: source only until Root clears execution (Studio custody 2026-11-09)",
)
class PendingPermissionSubprocessTests(unittest.TestCase):
    def test_cancel_while_permission_pending_reaps_owned_child(self):
        """SIGTERM the helper after permission-required. Do not dispatch the terminal."""
        self.assertLess(len(TERMINAL), 256)
        self.assertLess(len(PERMISSION), 1024)
        self.assertTrue(TERMINAL.endswith(b"\n") and PERMISSION.endswith(b"\n"))
        sigterm = signal.getsignal(signal.SIGTERM)
        sighup = signal.getsignal(signal.SIGHUP)
        fallback = {"helper": False, "child": False}
        helper = None
        helper_fd = None
        helper_start = None
        child_fd = None
        child_pid = None
        child_start = None
        root = None
        nonce = None
        measured = None
        stderr = None
        body_error = None
        self.stage = "spawn"
        self.owned_observation = None
        self.cleanup_record = None
        with tempfile.TemporaryDirectory(prefix="acp-cancel-subprocess-") as directory:
            try:
                root = Path(directory)
                nonce = os.urandom(16).hex()
                (root / "nonce.txt").write_text(nonce + "\n", encoding="utf-8")
                os.mkfifo(root / "terminal-release.fifo", 0o600)
                (root / "prompt.txt").write_text("Synthetic offline task\n", encoding="utf-8")
                fake = root / "fake-cursor"
                fake.write_text("#!" + sys.executable + " -I\n" + child_script(), encoding="utf-8")
                fake.chmod(0o700)
                stderr = (root / "helper-stderr.bin").open("wb")
                helper = subprocess.Popen(
                    [sys.executable, "-I", "-B", str(HELPER), "run", "--client", str(fake),
                     "--worktree", str(root), "--model", "fixture-model",
                     "--prompt-file", str(root / "prompt.txt"), "--timeout", "30"],
                    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=stderr,
                    bufsize=0, start_new_session=False)
                try:
                    helper_fd = os.pidfd_open(helper.pid)
                except OSError:
                    helper_fd = None
                helper_info = proc_fields(helper.pid)
                self.assertIsNotNone(helper_info, "helper absent at spawn")
                helper_start = helper_info["starttime"]
                self.assertEqual(helper_info["ppid"], os.getpid())
                self.assertEqual(os.getpgid(helper.pid), os.getpgrp())
                self.stage = "ownership"
                deadline = time.monotonic() + IDENTITY_DEADLINE
                while not (root / "ownership.json").exists():
                    if helper.poll() is not None:
                        self.fail(f"helper exited {helper.returncode} before ownership")
                    if time.monotonic() >= deadline:
                        self.fail("ownership handshake missed its deadline")
                    time.sleep(0.02)
                record = json.loads(read_bounded(root / "ownership.json", 1024).decode("ascii"))
                self.assertEqual(record["nonce"], nonce)
                child_pid = record["pid"]
                child_start = record["starttime"]
                self.assertIsNone(helper.poll(), "helper exited before the child handle was opened")
                child_fd = pidfd_owned(child_pid, child_start, helper.pid)
                self._require_provenance(helper, helper_start, record, child_fd)
                pending = bytearray()
                events = []
                self.stage = "permission"
                permission_deadline = time.monotonic() + PERMISSION_DEADLINE
                while True:
                    item = json.loads(next_line(helper.stdout, pending, permission_deadline).decode("utf-8"))
                    events.append(item["event"])
                    if item["event"] == "permission-required":
                        self.assertEqual(item["requestID"], 41)
                        self.assertEqual(item["sessionID"], "fixture-session")
                        break
                    self.assertLess(len(events), 8, "too many events before permission")
                self.assertEqual(events, ["starting", "session-created", "permission-required"])
                self.assertEqual(read_bounded(root / "late-terminal.frame", 256), TERMINAL)
                self.assertFalse((root / "terminal-sent").exists(), "terminal was dispatched before cancel")
                self.assertEqual((root / "inbound-methods.jsonl").read_text(encoding="utf-8").splitlines(),
                                 ["initialize", "session/new", "session/prompt"])
                self._wait_until_ready(helper.pid, helper_start, record, fake, permission_deadline)
                self.assertIsNotNone(child_fd, "verified child pidfd required before cancellation")
                self.stage = "signaled"
                try:
                    if helper_fd is None:
                        if helper.poll() is not None:
                            self.fail("helper exited before SIGTERM; not retried")
                        os.kill(helper.pid, signal.SIGTERM)
                    else:
                        signal.pidfd_send_signal(helper_fd, signal.SIGTERM)
                except ProcessLookupError:
                    self.fail("owned helper was gone at SIGTERM; not retried")
                try:
                    helper.wait(timeout=HELPER_EXIT_DEADLINE)
                except subprocess.TimeoutExpired:
                    self.fail("helper still alive after SIGTERM deadline")
                self.stage = "exited"
                self.assertEqual(helper.returncode, 128 + signal.SIGTERM)
                self.assertFalse((root / "child-self-timeout").exists(), "child safety timeout is not helper cleanup")
                self.assertIsNotNone(child_fd, "child ownership unknown")
                kind, state = observe(child_pid, child_start)
                self.assertEqual(kind, "absent", f"owned child after helper exit is {kind}:{state}")
                measured = "absent"
                self.assertFalse((root / "terminal-sent").exists(), "terminal was dispatched after the fault")
                self.assertEqual(read_bounded(root / "late-terminal.frame", 256), TERMINAL)
                self.assertEqual((root / "inbound-methods.jsonl").read_text(encoding="utf-8").splitlines(),
                                 ["initialize", "session/new", "session/prompt"])
                self.stage = "drained"
                drain_deadline = time.monotonic() + DRAIN_DEADLINE
                while True:
                    remaining = drain_deadline - time.monotonic()
                    if remaining <= 0:
                        self.fail("helper stdout drain missed its deadline")
                    readable, _, _ = select.select([helper.stdout], [], [], min(0.25, remaining))
                    if not readable:
                        continue
                    chunk = os.read(helper.stdout.fileno(), 4096)
                    if not chunk:
                        break
                    pending.extend(chunk)
                if pending and b"\n" not in bytes(pending):
                    self.fail("trailing helper stdout")
                while b"\n" in bytes(pending):
                    line, _, rest = bytes(pending).partition(b"\n")
                    pending = bytearray(rest)
                    events.append(json.loads(line.decode("utf-8"))["event"])
                self.assertNotIn("prompt-complete", events)
                self.assertNotIn("decision-sent", events)
            except BaseException as error:
                body_error = error
            finish_error = None
            stream_errors = []
            try:
                self._finish(helper, helper_fd, child_fd, root, nonce, measured, fallback)
            except BaseException as error:
                finish_error = error
            finally:
                stream_errors = self._close_owned_streams(helper, stderr)
            try:
                after_finalizer, observe_errors = self._classify_after_finalizer(
                    helper, helper_start, child_pid, child_start)
            except Exception as error:
                after_finalizer = {
                    "helper": {"kind": "error", "state": None},
                    "child": {"kind": "error", "state": None},
                }
                observe_errors = ["after-finalizer-" + type(error).__name__]
            self.cleanup_record = self._cleanup_record(
                root, measured, fallback, child_fd, body_error, finish_error, stream_errors,
                after_finalizer, observe_errors)
            if body_error is not None:
                body_error.add_note(json.dumps(self.cleanup_record, sort_keys=True))
                if finish_error is not None:
                    raise body_error from finish_error
                raise body_error
            if finish_error is not None:
                finish_error.add_note(json.dumps(self.cleanup_record, sort_keys=True))
                raise finish_error
        diagnostic = json.dumps(self.cleanup_record, sort_keys=True)
        self.assertEqual(self.cleanup_record["stream_errors"], [], diagnostic)
        self.assertEqual(self.cleanup_record["observe_errors"], [], diagnostic)
        self.assertFalse(fallback["helper"] or fallback["child"], diagnostic)
        self.assertIs(signal.getsignal(signal.SIGTERM), sigterm)
        self.assertIs(signal.getsignal(signal.SIGHUP), sighup)

    def _record_owned(self, stage, helper_pid, helper_start, child_pid, child_start):
        """Remember this test's helper and child only. No process scan."""
        if helper_pid is None or helper_start is None:
            helper_kind, helper_state = "unknown", None
        else:
            helper_kind, helper_state = observe(helper_pid, helper_start)
        if not isinstance(child_pid, int) or child_start is None:
            child_kind, child_state = "unknown", None
        else:
            child_kind, child_state = observe(child_pid, child_start)
        self.stage = stage
        self.owned_observation = {
            "stage": stage,
            "helper_kind": helper_kind,
            "helper_state": helper_state,
            "child_kind": child_kind,
            "child_state": child_state,
        }
        return helper_kind, helper_state, child_kind, child_state

    def _require_provenance(self, helper, helper_start, record, child_fd):
        """Nonce, starttime, ppid, and pidfd. Runstate is not a reason to signal."""
        helper_kind, helper_state, child_kind, child_state = self._record_owned(
            "provenance", helper.pid, helper_start, record.get("pid"), record.get("starttime"))
        self.assertIsNone(helper.poll(), "helper reaped before ownership was held")
        self.assertEqual(helper_kind, "alive", f"helper provenance {helper_kind}:{helper_state}")
        self.assertEqual(status_ppid(helper.pid), os.getpid())
        helper_live = proc_fields(helper.pid)
        self.assertIsNotNone(helper_live, f"helper provenance {helper_kind}:{helper_state}")
        self.assertEqual(helper_live["ppid"], os.getpid())
        self.assertEqual(helper_live["starttime"], helper_start)
        self.assertEqual(os.getpgid(helper.pid), os.getpgrp())
        self.assertIsNotNone(child_fd, "child pidfd was not held while the helper was its parent")
        self.assertEqual(child_kind, "alive", f"child provenance {child_kind}:{child_state}")
        child_live = proc_fields(record["pid"])
        self.assertIsNotNone(child_live, f"child provenance {child_kind}:{child_state}")
        self.assertEqual(child_live["ppid"], helper.pid)
        self.assertEqual(status_ppid(record["pid"]), helper.pid)
        self.assertEqual(child_live["starttime"], record["starttime"])

    def _wait_until_ready(self, helper_pid, helper_start, record, fake, deadline):
        """Every pass rechecks identity. Only RunstateNotReady waits out the same deadline."""
        while True:
            self._record_owned(
                "ready", helper_pid, helper_start, record.get("pid"), record.get("starttime"))
            try:
                self._require_cancel_target(helper_pid, helper_start, record, fake)
                return
            except RunstateNotReady as drifted:
                self.owned_observation = {
                    "stage": "ready",
                    "helper_kind": drifted.helper_kind,
                    "helper_state": drifted.helper_state,
                    "child_kind": drifted.child_kind,
                    "child_state": drifted.child_state,
                }
                helper_kind = drifted.helper_kind
                helper_state = drifted.helper_state
                child_kind = drifted.child_kind
                child_state = drifted.child_state
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                self.fail(
                    "readiness stage=ready "
                    f"helper={helper_kind}:{helper_state} child={child_kind}:{child_state}")
            time.sleep(min(0.02, remaining))

    def _require_cancel_target(self, helper_pid, helper_start, record, fake):
        """Raise before any signal unless this exact child is still the owned leader."""
        helper_kind, helper_state = observe(helper_pid, helper_start)
        self.assertEqual(helper_kind, "alive")
        self.assertEqual(status_ppid(helper_pid), os.getpid())
        self.assertEqual(proc_fields(helper_pid)["ppid"], os.getpid())
        self.assertEqual(os.getpgid(helper_pid), os.getpgrp())
        child_pid = record["pid"]
        child_kind, child_state = observe(child_pid, record["starttime"])
        self.assertEqual(child_kind, "alive", f"child not alive before signal:{child_state}")
        live = proc_fields(child_pid)
        self.assertEqual(live["ppid"], helper_pid)
        self.assertEqual(status_ppid(child_pid), helper_pid)
        self.assertEqual(live["pgrp"], child_pid)
        self.assertEqual(live["sid"], child_pid)
        self.assertEqual(live["starttime"], record["starttime"])
        self.assertEqual(record["ppid"], helper_pid)
        self.assertEqual(record["pgrp"], child_pid)
        self.assertEqual(record["sid"], child_pid)
        self.assertEqual(os.getpgid(child_pid), child_pid)
        self.assertEqual(os.getsid(child_pid), child_pid)
        self.assertNotEqual(child_pid, os.getpgrp())
        self.assertEqual(child_ids(helper_pid), [child_pid])
        self.assertIn(str(fake), " ".join(cmdline(child_pid)))
        self.assertIn(str(HELPER), " ".join(cmdline(helper_pid)))
        if helper_state not in ALIVE_STATES or child_state not in ALIVE_STATES:
            raise RunstateNotReady(helper_kind, helper_state, child_kind, child_state)

    def _close_owned_streams(self, helper, stderr):
        """Close handles even when finalization raised. Return close-error types."""
        handles = [stderr]
        errors = []
        if helper is not None:
            handles.extend((helper.stdin, helper.stdout))
        for handle in handles:
            if handle is None:
                continue
            try:
                handle.close()
            except OSError as error:
                errors.append(type(error).__name__)
        return errors

    def _established(self, pid, starttime):
        return (isinstance(pid, int) and not isinstance(pid, bool)
                and isinstance(starttime, int) and not isinstance(starttime, bool))

    def _classify_recorded(self, pid, starttime, errors, label):
        """One metadata read of an already recorded pid and starttime. No signal."""
        if not self._established(pid, starttime):
            return {"kind": "Unknown", "state": None}
        try:
            kind, state = observe(pid, starttime)
        except Exception as error:
            errors.append(label + "-" + type(error).__name__)
            return {"kind": "error", "state": None}
        if kind not in {"absent", "reused", "zombie", "alive"}:
            errors.append(label + "-unclassified")
            return {"kind": "error", "state": None}
        return {"kind": kind, "state": state}

    def _classify_after_finalizer(self, helper, helper_start, child_pid, child_start):
        """Post-finalizer classes for the same recorded identities. Readiness is separate."""
        errors = []
        helper_pid = None if helper is None else helper.pid
        return {
            "helper": self._classify_recorded(helper_pid, helper_start, errors, "helper"),
            "child": self._classify_recorded(child_pid, child_start, errors, "child"),
        }, errors

    def _cleanup_record(self, root, measured, fallback, child_fd, body_error, finish_error, stream_errors,
                        after_finalizer, observe_errors):
        """Keep readiness, post-finalizer class, and secondary errors beside a primary failure."""
        self_timeout = None
        if root is not None:
            self_timeout = (root / "child-self-timeout").exists()
        return {
            "stage": self.stage,
            "measured": measured,
            "fallback_helper": fallback["helper"],
            "fallback_child": fallback["child"],
            "child_fd_held": child_fd is not None,
            "self_timeout": self_timeout,
            "readiness": self.owned_observation,
            "after_finalizer": after_finalizer,
            "observe_errors": observe_errors,
            "primary_error": None if body_error is None else type(body_error).__name__,
            "finish_error": None if finish_error is None else type(finish_error).__name__,
            "stream_errors": stream_errors,
        }

    def _finish(self, helper, helper_fd, child_fd, root, nonce, measured, fallback):
        """Graceful owned-helper exit first. Child signals use a held pidfd only."""
        adopted = None
        try:
            if (child_fd is None and helper is not None and helper.poll() is None
                    and root is not None and nonce is not None):
                adopted = adopt_child_fd(root, nonce, helper.pid)
            if helper is not None and helper.poll() is None:
                if self._signal_helper(helper, helper_fd, signal.SIGTERM):
                    fallback["helper"] = True
                    try:
                        helper.wait(timeout=HELPER_GRACE_WAIT)
                    except subprocess.TimeoutExpired:
                        self._signal_helper(helper, helper_fd, signal.SIGKILL)
                        try:
                            helper.wait(timeout=FALLBACK_WAIT)
                        except subprocess.TimeoutExpired:
                            pass
            if measured == "absent":
                return
            fd = child_fd if child_fd is not None else adopted
            if fd is not None and pidfd_send(fd, signal.SIGKILL):
                fallback["child"] = True
        finally:
            for fd in (helper_fd, child_fd, adopted):
                if fd is not None:
                    os.close(fd)

    def _signal_helper(self, helper, helper_fd, signum):
        """SIGTERM or SIGKILL the unreaped helper. Never its process group."""
        if helper.poll() is not None:
            return False
        if helper_fd is not None:
            return pidfd_send(helper_fd, signum)
        try:
            os.kill(helper.pid, signum)
        except ProcessLookupError:
            return False
        return True
