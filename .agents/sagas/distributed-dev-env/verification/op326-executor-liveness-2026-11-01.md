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

This context could not run the Go tests: command execution was rejected.
The coordinator command is `go test -p 1 -count=1` on `./web/portal/session`
with `GOMAXPROCS=2`, `GOCACHE=/tmp/usage-040820-go-cache`, `nice -n 19`, and
`flock /tmp/usage-040820-test.lock`, using
`/home/dev/.local/toolchains/go/bin/go`. Targeted run first, then the package
if the grouping change needs it. No deployment follows from this note.

## Result receipt

The private report path
`/home/dev/.shared/handoffs/usage-reset-priority-20261101/OP326-102716-GROK-RESULT.md`
was rejected twice by the file writer, and Shell was rejected, so this
authorized note is the copy. No acceptance, merge, publish, or deploy.
Requested model grok-4.7-high. Served identity Unknown. No private quota
observation.

Head is still `1c5901b293c27f9bd2d06ea676e095c9e524474e` plus uncommitted edits
in `web/portal/session/product_activity.go`,
`web/portal/session/product_activity_test.go`, and this file.
`product_filter_test.go` is untouched. This context did not commit or push.
A fresh fetch and a competing-writer count were not performed.

`TestMissingExecutionStateStaysUnknown` expects this matrix. It was not run.

| Row | Phase | Mode | Agent state | Task result | Freshness | Group | Badge | Outcome |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| run-absent | Running | absent | absent | absent | observed | State Unknown | Unknown | none |
| run-task-unknown | Running | task | Unknown | Unknown | observed | State Unknown | Unknown | Unknown |
| run-remote-unknown | Running | remote | Unknown | absent | observed | State Unknown | Unknown | none |
| run-local-unknown | Running | local | Unknown | absent | observed | State Unknown | Unknown | none |
| pending-remote | Pending | remote | Unknown | absent | observed | State Unknown | Unknown | none |
| idle-phase-local | Idle | local | Unknown | absent | observed | State Unknown | Unknown | none |
| stale-unknown | Running | local | Unknown | absent | stale | State Unknown | Unknown | none |
| exited-no-result | Running | task | exited | absent | observed | Stopped | Exited | Unknown |
| exited-succeeded | Running | task | exited | Succeeded | observed | Stopped | Exited 0 muted | Unknown |
| exited-result-unknown | Running | task | exited | Unknown | observed | Stopped | Exited | Unknown |
| stale-busy | Running | remote | busy | absent | stale | In progress | Busy | none |
| suspended-remote | Suspended | remote | Unknown | absent | observed | Stopped | Suspended | none |
| archived-local | Archived | local | Unknown | absent | observed | Stopped | Archived | none |
| draining-unknown | Draining | remote | Unknown | absent | observed | Stopped | Draining | none |
| failed-unknown | Failed | remote | Unknown | absent | observed | Needs attention | Failed | none |

Expected counts: Needs attention 1, In progress 1, Stopped 6, State Unknown 7,
rail 15. Exit code and duration: unavailable.

`TestSessionsPageGroupsByAttention` now expects the activity-absent Running
row under State Unknown (`d-legacy` id s4, exited id s3).
`TestSessionLegacyRowRendersUnknownActivity` expects badge Unknown / muted.

Draining with no agent state stays Stopped / Draining. The producer’s task
result word Unknown is not shown as Exited 0, because that word is also the
default before any result exists. A known exited state still shows Exited.
