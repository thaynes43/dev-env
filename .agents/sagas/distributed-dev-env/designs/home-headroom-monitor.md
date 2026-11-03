# DESIGN: Bounded HOME bytes and inodes headroom monitoring (source, #386)

Date 2026-11-03. Source design only: no deploy,
no CRD change, no operator change, no live default change. Branch agent/dev-env-home-headroom
from origin/main 84fa61c. Information, not authorization.

## Problem
agentd samples only /tmp, bytes only, and only logs (tmp_usage.go, DESIGN-035 item 3). HOME is the
persistent volume where Go caches, clones and worktrees live; it filled on 2026-10-30 (16G of
build cache) and nothing reports its bytes or inodes. A volume can run out of inodes while bytes
are free. Nothing exposes the figure to the operator, `agentd ctl status` or a person.

## Decision
Add one statfs-based sample of the filesystem holding $HOME (Settings.Home) and report it as
`homeHeadroom` on the existing agentd Status (heartbeat body and `agentd ctl status`). No new
route, no new metric endpoint, no directory scan.

Reported: measured numbers (bytes total/available, inodes total/free) and a closed state word.
Never an action, never a path, never an error string, never file names or contents.

## Files
- NEW internal/agentd/protocol/home_headroom.go: wire type, closed enums, Valid().
- NEW internal/agentd/home_headroom.go: monitor (cadence, statfs timeout, classification, logging).
- NEW tests beside both.
- protocol/status.go: one field, `HomeHeadroom *HomeHeadroom json:"homeHeadroom,omitempty"`.
- daemon.go: two lines (a monitor field beside tmpUsage near line 68; one assignment in beat(),
  near line 651).
- cmd/agentd/main.go: one line in `ctl status` (stateless one-shot sample).
- NEW docs: this design as .agents/sagas/distributed-dev-env/designs/home-headroom-monitor.md.

Not touched, by design: tmp_usage.go and DESIGN-035 (both rewritten by held candidate 1c5a537),
CRDs, operator, keeper, haynes-ops. (internal/agentd/protocol/status.go gains the one optional `homeHeadroom` field; see above.)

### Exact overlaps
- Held candidate 1c5a537 (tracker #370): tracked.patch hunk headers show daemon.go `@@ -488,6`
  (supervise area), tmp_usage.go, DESIGN-035, controller/pod.go, images. My daemon.go edits sit at
  ~68 and ~651: no hunk overlap; textual merge should be clean. The held candidate's
  tmp_usage.go is untouched by me.
- PR #891: daemon.go `@@ -398` (waitForLogin). No overlap.
- Semantic overlap to flag for the held candidate's resolution: DESIGN-035 item 3 says "No status
  protocol, API or CRD field is added" for /tmp. This design adds a status field for HOME only; it
  does not change that /tmp statement.

## Cadence and cost
- One statfs(2) per sample on a single path. No readdir, no stat of entries, no walk.
- Minimum interval 30 s between real samples (`homeHeadroomEvery`); the heartbeat is 60 s, so in
  practice one per beat. Extra callers in this process (boot beats) never stack; `agentd ctl status` runs in another process with its own guard, so a second statfs can run beside the daemon's.
- The statfs runs in a goroutine with a 3 s wait. A hung mount (NFS/Ceph stall) yields
  `unreadable/timeout` after 3 s and the heartbeat continues. At most one statfs is ever outstanding;
  while it is stuck no second one is started and the sample stays `unreadable/timeout`.
- Clock: a sample at `now < last sample` (clock stepped back) is taken again; a sample is
  reused only when 0 <= now-last < 30 s. ObservedAt is the injected/caller clock, UTC.

## Output (protocol.HomeHeadroom, version 1)
```
homeHeadroom: {
  version: 1,
  state: ok | low | exhausted | unknown | unreadable,
  reason: (only for unknown/unreadable) closed enum,
  observedAt: <UTC>,
  bytes:  { state: ok|low|exhausted, total, available }   // omitted when not measurable
  inodes: { state: ok|low|exhausted, total, available }   // omitted when not measurable
}
```
- Absent `homeHeadroom` means not measured (old agentd, or none taken yet). It is never zeros.
- `unreadable` = the statfs call failed. reason: permission | not-found | io | timeout | other
  (mapped from errno; the error text is dropped because it carries the path).
- `unknown` = statfs succeeded but a figure is not usable: zero-size filesystem (Blocks==0 or
  Files==0, e.g. filesystems that report no inodes), counts that contradict each other
  (free > total, avail > free), or a size that overflows uint64. reason: bytes-invalid |
  inodes-invalid | both-invalid. The usable half is still reported.
- Overall state = worst of: exhausted > low > unknown (a missing half) > ok. So "inodes
  exhausted while bytes are free" is `exhausted`, with bytes.state ok and inodes.state exhausted.
- Bytes use fragment size (Frsize, else Bsize) and Bavail (what the agent's uid can use), as df.
  Inodes use Files/Ffree.

## Thresholds (source constants, not a sizing claim)
| | low | exhausted |
|---|---|---|
| bytes available / total | < 15% | < 3% (0 included) |
| inodes free / total | < 10% | < 2% (0 included) |
Integer 128-bit comparison (math/bits), no float, no overflow. They report state; they trigger
nothing. A later consumer decides actions.

## Logging
One line on a state change only (not every sample): Warn when entering low, exhausted, unknown or
unreadable, Info on return to ok. Fields: state, reason, and the four numbers. No path, no err text.
First sample at ok logs nothing.

## Test plan (RED first, fake statfs, injected clock; nothing fills a disk)
bytes low; inodes exhausted with bytes free; both fine; reserved blocks (Bavail < Bfree);
Frsize vs Bsize; unreadable (EACCES, ENOENT, EIO, other, wrapped PathError with a secret path
that must not appear in JSON or log); statfs hang (timeout, single outstanding call, recovery
after it returns); zero-size (Blocks==0, Files==0); contradictory counts; uint64 overflow;
cadence and cache; clock step back and big forward; log only on transition; vanishing HOME dir
and entry churn on a real t.TempDir (no scan, so churn cannot change the result); Valid() rejects
bad enums; JSON round trip; Status JSON carries/omits the field; heartbeat decode by the operator
(non-strict) tolerates it.

## Safe-default review (scope item 3: proposal, docs only)
Measured samples, one pod, 2026-11-03, `df`: HOME 20G volume, 9.9G used (51%), 1,310,720 inodes,
184,323 used (15%). These are one sample of one session, not a workload PASS and not universal.
Incident reference: 2026-10-30 build cache 16G filled a HOME (session note). Reading:
- 20Gi HOME with a Go cache of 5-16G leaves 15% (3Gi) low line close to a normal busy state;
  the 15% low threshold is deliberately a "look" signal, 3% a "stop starting heavy work" signal.
- Inode use was 15% at 9.9G: inodes are not the first limit for this workload mix, but a
  node_modules/Playwright-heavy session could be; hence they are watched separately.
- DESIGN-035 sizes /tmp (8Gi default, 1-64Gi bounds) and moves Go work dirs to HOME via
  GOTMPDIR, which shifts pressure from /tmp to HOME. This monitor is the missing HOME-side gauge.
Proposed, not applied: keep /tmp default 8Gi; do not raise HOME default on one sample; collect
homeHeadroom across sessions (portal/operator follow-up) for two weeks, then re-derive defaults
from the p95 used fraction per size class. Grounding for any resize belongs to #349, not here.

## Follow-ups (new bounded stories, not done here)
1. Operator: copy homeHeadroom into AgentSession status (CRD field, haynes-ops CRD copy first) and
   an operator metric/portal pane. Needs the CRD-copy-before-pin sequence.
2. Consumer policy (what to do at exhausted): owner decision, not source here.
3. Fold HOME state into storage-run preflight when the held candidate resolves.

## Limits
Statfs describes the whole volume, not one directory; another writer is not attributed. An
EmptyDir limit is not a statfs quota (see DESIGN-035). A sample is a point in time, not a
reservation. Unverified: behavior on a real hung mount (only a fake hang is tested).

Note on reach: the boot-time heartbeat (daemon.go, the `CollectStatus` call before the supervise
loop) does not carry the field; the first supervised beat does, then every 60 s. `agentd ctl status`
takes its own one-shot sample. The operator ignores the field today (non-strict heartbeat decode,
covered by TestHeartbeatBodyAcceptsUnknownFieldsFromANewerAgent and, with a real body, TestHeartbeatBodyAcceptsTheHomeHeadroomField); persisting it is follow-up 1.

## Review notes (added after independent review of 6f446ee)

- A cold hung HOME blocks the beat that calls the sampler for up to the 3 s bound (and
  holds its mutex); the supervise loop is delayed by the same amount once per hang, and a
  final beat on shutdown has that much less of its 5 s budget. Later samples return
  `timeout` immediately because at most one statfs is outstanding.
- A filesystem that reports zero inodes can never reach `ok`: bytes decide `low` or the
  state is `unknown` with an inode reason. When bytes are low and inodes are unmeasurable the
  overall state is `low` with no reason, so a consumer cannot tell the inode half was invalid.
- With a monotonic production clock a wall-clock step backwards does not trigger the
  resample branch; that branch is exercised with injected clocks only.
- There is no hysteresis: a value hovering at a threshold logs on every transition.
- `Valid()` is a contract for tests and consumers; the closed set holds by construction in
  the sampler, not by a check at the send boundary.
