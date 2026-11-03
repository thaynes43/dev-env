# Consumer: a rejected-but-unexpired Claude login (2026-11-03)

Scope: the pod side only (agentd, agent-run). Source and synthetic tests. Nothing under
`internal/keeper`; no wire, CRD, operator or standby change. Not installed, not accepted.
Rebased on main after PR 891 (the Refreshing display) merged; the merged-state rules are below.

## The case

A provider revokes the keeper's Claude login while the applied access token has not expired.
Before this change no surface knew: `agent-run auth status`, agentd's login text and the status
message all read ready or logged in until the local expiry.

## What the pod can see

One signal: the most recent assistant turn in the session transcript is a 401 API-error entry (the
classifier the continue already uses; user and tool_result entries after it do not hide it). No refresh, CLI login, or credential read is added.

## The marker

`~/.agentd/claude-rejected.json`: `{"version":1,"generation":N,"observedAt":"<UTC>"}`; `observedAt` is the
time of the refused turn in the pod's transcript. No token, hash, error or transcript text. A sibling
`claude-rejected.lock` serialises writers (non-blocking flock; a busy lock skips that poll). Written
only by agentd's 5 s poll, only for a 401 turn dated after the current generation was applied (mtime of
`claude-access-generation.json`). First sight wins.

Design choices, not proved prevention (none was exercised against a real revocation or the real CLI):

- **Generation-only binding.** The marker for N says nothing about N+1. Before N+1 is re-observed every
  reader shows its pre-feature default (Ready, or "logged in"): not assumed unhealthy, not claimed proven.
- **Age never clears.** An unresolved same-generation rejection stays Rejected at 6 h, 7 h, 24 h; only the
  sentence changes after 10 minutes. The expired-token display takes over only when the access token
  itself has expired.
- **Cannot tell, not Ready.** A marker that exists but is unreadable, oversize, unknown-field, wrong
  version, generation 0, future-dated, not a regular file, or for a generation above the applied one
  reads `Unknown` (agent-run, exit 2) / `login-unverified:` (agentd). Ignored only when last written
  before the applied generation arrived, or when it parses and names an older generation. The writer
  never deletes a marker it cannot parse; it deletes only a parsed marker for an older generation, or a
  current one on explicit proof.
- **Explicit proof of acceptance.** An assistant turn that is not an API or auth error, not a sidechain,
  names a model other than `<synthetic>`, carries a non-empty `usage`, and is dated after the refusal.
  Nothing else clears it. The real CLI shape is Unknown; if it lacks these fields a marker clears only
  by a newer generation.

## Surfaces (text only, the state word never changes)

| Surface | Rejected | Cannot tell |
|---|---|---|
| `agent-run auth status` claude row | `Rejected`, exit 1 (an expired token stays `NeedsLogin`) | `Unknown`, exit 2 |
| agentd `login status`, heartbeat `status.agent.error` of a busy/idle/waiting projected Claude session | `login-rejected: ...` | `login-unverified: ...` |
| `agent-run rc start` | phase `provider_auth_ok` unknown, reason `login_rejected_pending` | reason `login_rejection_unverifiable` |

rc keeps its outcome, exit code and one-shot handoff; the text is information, never a gate.

## Fences unchanged

The marker neither triggers nor suppresses the continue. The continue receipt, once-per-generation
rule and the MAYBE_SPENT handling of every post-Deliver outcome are untouched; a test asserts
exactly one send across the gap and that observing a rejection leaves the receipt byte-identical.

## Revision 3 rules (review of 77ff879)

- Both readers use the same stable reference: agentd's generation receipt beside the marker (its
  generation, and its mtime for "last written before this generation arrived"). agent-run no longer
  uses the Secret volume's mtime, which a pod restart resets. A marker with no readable receipt is
  cannot-tell in both. A corrupt receipt with a marker present is cannot-tell, not none. agent-run
  refuses duplicate members like agentd's strict decoder.
- Clearing looks at the most recent assistant turn, past trailing user and tool_result entries. It
  clears only if that turn is Accepted (see above) and dated after the refusal. User or tool_result
  entries alone prove nothing.
- Attribution uses the request, not the failure: a 401 is charged to the current generation only if the
  nearest earlier user entry (the prompt or tool_result that triggered the call) is dated after the
  generation was applied. If the tail holds no such entry (cut by the 256 KiB tail, or none), the
  failure's own time is used and an in-flight old-token request failing just after the apply can be
  charged to the new generation. That is pinned by a test, not prevented.
- Write, remove and lock failures each log one fixed-word line when the class begins
  ("Claude rejection record could not be written/removed/locked"; no path, no error text) and again only
  after it clears. A rejection that could not be written is kept in memory and added to the heartbeat
  message, so a full volume does not read as logged in there. agent-run reads files only, so it still
  says Ready until the write succeeds.
- The agentd text names the fixed observation time, not an age, so `status.agent.error` does not change
  on every heartbeat; it changes once when the ten-minute window passes. agent-run computes its relative
  age on read.

## Merge with the pending-refresh display (PR 891, merged)

Precedence, tested at the boundaries in both readers (unexpired, expired 1 s, just inside 3 min, exactly
3 min, lapsed login, no marker): a recorded rejection for the current generation while the token is
unexpired is Rejected / `login-rejected:`. Once the token has expired the expiry state stands
(Refreshing for the 3 minute window, then NeedsLogin / `login-required:`, or NeedsLogin when the login
lapsed) with the exit code it had, and the rejection is appended to the Reason / text, so it is never
hidden by Refreshing and the row is never Ready. A cannot-tell marker is appended the same way.

rc: `login-rejected:` keeps the phase-reason-only behaviour and is not an owner action (a refused login
is waited out; the keeper's next generation and the existing one-time continue are the recovery); it ends
as `agent_start_timeout` if it outlasts agent start. `refreshing:` still maps to `auth_refresh_pending`
and `login-required:` to `auth_projection_unavailable`, with the rejection appended.

## Revision 4 rules (re-review of a151204)

- The running-session heartbeat (`status.agent.error`) shows the rejection at every boundary of the
  access token, through the status path: `login-rejected:` while unexpired, then `refreshing:` (3
  minutes) or `login-required:` with the rejection appended. A rejection that could not be written
  (memory only) follows the same rules.
- The wording states only what is known: the provider rejected the applied token while it was
  unexpired; a newer generation may or may not arrive; this tool requests no sign-in; check the keeper
  if none arrives. A 401 cannot tell a keeper delay from a revoked login, so neither is claimed.
- One binding for both readers: agentd's applied receipt generation. If apply is stuck at N with N+1
  published, both say Rejected for a marker for N (agent-run adds "generation N+1 is published but this
  pod has applied N"). A marker above the receipt generation is cannot-tell in both.
- Recording uses the most recent assistant turn, as clearing does: a user or tool_result entry inside
  the poll window no longer loses the 401; a later accepted turn supersedes it.
- A flock failure other than "busy" is logged (fixed words, no errno text); busy stays silent.
- agent-run does not apply the pod's marker to a `--claude-file` that is not the pod's projection, and
  says so on stderr; with HOME unset and no `--rejected-file` it says the record was not checked.

## Exit code meaning (scripts)

`agent-run auth status` exits 1 for a Rejected row, and exit 1 was documented as expiring or expired.
Exit 1 here does not mean "renew the login": a script that treats 1 as "renew" would misfire. The
quickstart, the help (including its Exit codes paragraph) and the reason text say so.

## RED evidence (names and fixed-word failures)

Honesty note: the 91a2c03 and 77ff879 runs below were made against earlier heads of this branch; those
commits were squashed and rebased, so those RED runs cannot be reproduced from the branch history. The
revision 4 RED run (below) was made in place on a151204 before the changes and is the only one a
reviewer can reproduce, by checking out a151204 and applying the new tests.

Method: for the 91a2c03 run, a scratch worktree of that head with the new test file and a one-line shim
for a new constant. For the 77ff879 run, the new tests were run in place against that head's code before
the fix. The failing assertion was read from the output each time. Not reproducible on main (the feature
does not exist there).

Against 91a2c03 (revision 2 tests): agentd `NeverAgesIntoReady` ("logged in on the keeper's projection"
at 6h, 7h, 24h), `YieldsToTheExpiredTokenDisplay...`, `ClearsOnlyOnExplicitProofOfAcceptance/{empty_usage,
no_usage, plain_text_turn_with_no_model_or_usage, synthetic_model}`, `UnbelievableMarkerIsCannotTell...`
(11 variants: want login-unverified, got logged in), `...OlderThanTheAppliedGeneration...`,
`FreshObservationReplaces...`, `BusyLockMakesThePollDoNothing` ("a poll wrote while another writer held
the lock"); agent-run `NeverAgesIntoReady` (6h, 7h, 24h) and `UnbelievableMarkerIsCannotTell` (11
variants: exit 0, want 2). Guards that passed before (not RED): generation rebinding, stale delete,
restart, looping writers.

Against 77ff879 (this revision's tests): agentd `ClearsOnAnAcceptedAssistantTurnEvenWithUserEntriesAfterIt`
(3 subtests: marker still present), `AttributesByTheRequestNotTheFailure` (2 subtests: marker written for
an in-flight old-token 401), `UnreadableReceiptWithAMarkerIsCannotTell` (3 subtests: text "logged in",
want login-unverified), `WriteFailureIsLoggedOnceAndStillReachesTheHeartbeat`, `RemoveFailureAndLock
FailureAreLogged` (2 subtests: no log line), `HeartbeatTextIsStableWhileUnresolved`, and the date-form
change in `NeverAgesIntoReady`; agent-run `...CannotTell` subtests `corrupt marker, projection mtime
newer than the marker` (exit 0, want 2), `newer generation published but not yet applied` (exit 2, want
0), `marker with no receipt` (exit 1, want 2), `valid marker, corrupt receipt` (exit 1, want 2),
`duplicate members` (exit 0, want 2). Guard, passed before: `SecondWriterIsExcludedWhile...` (the
flock already excluded; it now holds a writer inside its critical section through a test seam).
`LoopingWritersLeaveAWholeMarker` proves atomic replace and convergence only, not exclusion.

Against a151204 (revision 4 tests): agentd `RunningSessionHeartbeatAtEveryBoundary` (wording at the
unexpired row; blank message at the expired rows is the finding itself, the rows asserting
`refreshing:` / `login-required:` plus the rejection), `...a rejection that could not be written
survives the expiry`, `RecordedEvenWhenUserEntriesFollowTheRefusal` (2 subtests: no marker),
`FlockErrorIsLoggedAndBusyIsNot` (no log line), and the wording expectations
(`RecordedWhileLocalTokenUnexpired`, `NeverAgesIntoReady`, `MergedWithTheExpiredTokenDisplay`); agent-run
`BindsToTheAppliedGeneration` (2 subtests), `SaysWhenItDidNotCheckTheMarker` (2 subtests), the corrupt
marker with a newer generation published, and the wording and merged-state tests. Guards that passed
before (pinned behaviour, not RED): `PinnedAttributionLimits` (4 subtests), agentd's reader already
bound to the receipt.

## Limits

- Detection needs a 401 as the last assistant turn at a poll (every 5 s). A 401 followed by a retry that
  completes inside one poll window is never seen; retry-recovered rejections are invisible.
- The real CLI's 401 and response transcript shapes are the existing Unknown; tests use the synthetic
  shape. If real responses lack model and usage, a marker clears only by a newer generation.
- Write failures: agent-run and `agentd ctl status` read files, so a rejection that could not be
  written is visible only in the daemon's own heartbeat (in memory) until a write succeeds.
- mtime and clock trust: a marker whose observedAt is more than a minute ahead of the pod's clock (a
  backward clock step) reads as cannot-tell; "last written before this generation" trusts file mtimes.
- Request attribution needs the request's own transcript entry in the 256 KiB tail.
- `agent-run auth status` exits 1 on Rejected (see above).
- Standby metric: `internal/standby/metrics.go` still reads a rejected running session as
  `registered`. Showing it needs a wire decision (a new agent state word or status field, apiserver
  view, CRD copy to haynes-ops, standby mapping). Not done.
- The Secret volume delay (about a minute) is platform behaviour, not shown by source.
- The marker needs a TUI session with a transcript. A headless task has none and shows no marker.
- Every rule here (generation-only binding, no age-out, cannot-tell fallback, acceptance proof) is a
  design choice, not proved prevention.
- Attribution (pinned by `PinnedAttributionLimits`): (a) the receipt is written before the credentials,
  so a 401 for an old-token request, failing after the receipt and before a failed credentials write is
  retried, is charged to the new generation; attribution cannot see which token a request used. A
  credentials-file mtime would be a conservative guard but the CLI also touches that file, which would
  lose real rejections (fail open), so it is not used. (b) A queued user entry dated after the apply and
  ahead of an old-token 401 makes the request look new. (c) If the nearest user entry has no timestamp,
  the failure's own time decides. (d) A backward clock step (the 401 stamped before the apply time) loses
  the observation. (e) Clearing trusts the response time while attribution uses the request time.
- A 401 followed by a retry inside one 5 s poll window is never seen; a 401 more than 256 KiB from the
  end of the transcript is not read.
- The heartbeat overlay for a rejection that could not be written is in memory: an agentd restart loses
  it, and `agent-run auth status` and `agentd ctl status` read files only.
- agent-run reads the receipt and marker at different moments from agentd's clear: a proof-clear race can
  show Unknown for a moment (it fails closed), as can the few seconds while a newly published generation
  is not yet applied and the marker is unreadable.
- I could not map "limits 1, 4, 5 and 9" of the review literally (that numbering was not in the message
  I received); every limit raised in the re-review is covered above.
