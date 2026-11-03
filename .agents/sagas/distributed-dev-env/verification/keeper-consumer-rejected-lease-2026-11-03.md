# Consumer: a rejected-but-unexpired Claude login (2026-11-03)

Scope: the pod side only (agentd, agent-run). Source and synthetic tests. Nothing under
`internal/keeper`; no wire, CRD, operator or standby change. Not installed, not accepted.
Builds on the display fix in PR 891 (expired token shown as refreshing); this branch is from
main and does not contain it.

## The case

A provider revokes the keeper's Claude login while the applied access token has not expired.
Before this change no surface knew: `agent-run auth status`, agentd's login text and the status
message all read ready or logged in until the local expiry.

## What the pod can see

One signal: the session transcript's last turn is a 401 API-error entry (the classifier the
continue already uses). No refresh, CLI login, or credential read is added.

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

## Exit code meaning (scripts)

`agent-run auth status` exits 1 for a Rejected row. Exit 1 was documented as expiring or expired. A
script that treats 1 as "renew the login" would misfire: the reason says no sign-in is needed while a
newer generation can still arrive. docs/v2-quickstart.md section 6 and the auth help now say so.

## RED evidence (names and fixed-word failures)

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
