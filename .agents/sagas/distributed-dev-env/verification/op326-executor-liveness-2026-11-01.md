# OP326 connected executor-state display — 2026-11-01

Source-only note for the Sessions list. It does not claim a deploy, a release,
or acceptance.

Base `1c5901b293c27f9bd2d06ea676e095c9e524474e` on
`agent/op326-liveness-grok-1101-102716`. The connected read is
`LoadProduction` → `SessionSource.read` → `GET /v1/sessions?view=activity` →
`decodeSessionPage` → `sessionsPage` → `sessionRow`. `portalActivity` already
copies only a known agent state and otherwise leaves `Unknown`. It sets a task
result only when the session mode is `task`. `decodeSessionActivity` refuses a
task result, task outcome, or measured usage on any other mode. This change
does not alter that producer or decoder.

## What was wrong

After a task result and an explicit Failed, Suspended, or Archived phase,
`sessionGroup` used a known agent state. If that state was missing or
`Unknown`, phase `Running`, `Pending`, or `Idle` still filed the row in
In progress. `sessionStatus` then showed the phase, with `Running` in the
`ok` tone. A pod phase was being read as a live executor.

## What already held

A known `exited` state is Stopped / Exited while the phase is still Running.
`Succeeded` is Stopped / Exited 0 in the muted tone. The task outcome panel
stays Unknown when no outcome record is present, including after Exited 0.
Those cases were not the bug. Suspended and Archived stay Stopped with their
phase words. Failed stays Failed and uses the existing attention window.
Known busy, waiting, idle, pending, exited, failed, and interrupted states
are unchanged. A stale freshness flag does not drop a known state and does
not create one. Draining with no execution state stays Stopped / Draining.

## Display rule

Missing or `Unknown` execution state stays group State Unknown and badge
Unknown. Running, Pending, and Idle do not fill that gap. An interactive
task result never reaches this page: the decoder rejects it. No outcome is
invented from exit 0.

## Checks

`TestMissingExecutionStateStaysUnknown` decodes one activity page, builds
`sessionsPage`, and checks `portal.RenderProduct` output: group headings,
counts, badges, the In progress exclusion, Exited 0 beside outcome Unknown,
remote and local Unknown, Pending, Suspended, Archived, Draining, Failed,
and stale busy versus stale Unknown. `TestSessionsPageGroupsByAttention`
now expects the activity-absent Running row under State Unknown.
`TestSessionLegacyRowRendersUnknownActivity` expects badge Unknown.

The Grok context could not execute commands. The parent integration driver ran
`./web/portal/session` with Go 1.27.3, `GOMAXPROCS=2`, `go test -p 1`,
`nice -n 19`, the shared test lock and cache. The final suite passed (package
1.108 s; elapsed 2.22 s). Product and test SHA-256 hashes matched before and
after that run and matched the imported source. No unchanged suite was rerun
by the receiving coordinator.

## Custody and limits

Grok implementation was committed as
`8ce7c8c8ddfb1f81f7b68067ef05a63bd2a9da29` and imported into the dedicated
coordinator branch. Requested model: `grok-4.7-high`; served identity: Unknown.
The retained controller ended before integration; no active implementer remained.
OP326 comments 4756 and 4759 record the separate driver test and transfer receipts.

The original Running-plus-exited case already grouped as Stopped. This repair
addresses only missing execution data. It changes no API, access permission,
lifecycle, retention, usage/reset card or chart. Session inventory counts still
include retained sessions; they are not counts of executing agents. Production
browser acceptance is not established by these synthetic tests. Publication,
deployment and installed usability require separate evidence in OP326.
