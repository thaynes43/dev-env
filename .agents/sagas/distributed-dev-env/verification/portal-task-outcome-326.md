# Retained task outcome in the portal (#326)

Source contract, 2026-10-31 (OVERNIGHT-PORTALOUTCOME-326). This is the portal
slice that [#320](task-outcome-320.md) deferred. Tom can open a finished task in
the portal after its pod is reclaimed and read the outcome the task recorded at
its first exit: the summary line, pull request links and head SHAs. Process
exit stays a separate row. No HOME, transcript or live pod is read.

## What changed

- **Negotiation.** The portal sends `X-Dev-Env-Task-Outcome: v1` (the header
  #759 added for `agent-run`) on each `GET /v1/sessions?view=activity` page.
  The operator's portal reader then adds `activity.taskOutcome`. It does this only
  for a task whose `status.taskOutcome` names the same Session UID and passes
  `protocol.TaskOutcome.Validate`, through the same `sessionTaskOutcome` that
  `agent-run show` reads. Without the header there is no key. An older strict
  portal, which refuses unknown activity keys, therefore keeps reading. An older
  operator ignores the header; the newer portal then shows "No outcome recorded".
- **Strict reader.** `decodeTaskOutcome` accepts exactly `sessionUID`,
  `podUID` and `record` (`summary`, optional `prURLs` and `headSHAs`), with the
  operator's own validation. It also requires a `sessionUID` equal to the
  row's `uid`, and task mode. Anything else fails the whole read, like every
  other optional activity key.
- **View.** Every task's detail leads with a **Task outcome** panel:
  - **Recorded**, the summary, each pull request as `owner/repo#n`, linked, each
    head as 7 characters, and the original session and pod UIDs, shortened; or
  - **Unknown: No outcome recorded.**

  `taskResult` is labelled **Process exit** in the Activity panel ("Exited 0",
  "Failed", "Timed out", "Unknown"). The list badge for `Succeeded` reads
  **Exited 0**, not "Succeeded". The panel reads Session status alone, so it is
  the same in every phase. A reclaimed task is phase Suspended; HOME expiry later
  makes it Archived. The portal has no Reclaimed phase.
- **Links.** `ProductField.Href` links a detail value only when the renderer
  matches it against `protocol.TaskOutcomePRURLPattern` (the CRD's pattern,
  now exported). The link opens in a new tab with `noopener noreferrer`. Any
  other Href is dropped and the value shows as escaped text. No script was added
  and the CSP is unchanged; `views.css` gained two rules and no comments.
- **Read bound.** A page of 50 task sessions with every field at its bound was
  194,401 bytes without outcomes and 287,101 with them. That is over the
  portal's earlier, undocumented 256 KiB per-page read bound, so the whole read
  would have failed. The bound is now `apiv1.PortalSessionPageMaxBytes`
  (512 KiB). The operator's worst-case test and the portal's reader use the same
  constant. Real pages are far smaller.

Not included: claim and waiting evidence (#322 owns `internal/pages`,
`internal/taskclaim` and `internal/agentrun` tonight; joining them is a
follow-up), CRD changes, new access, retention, HOME or history deletion.

## Tests

New tests, each shown failing with its fix removed through
`go test -overlay=... -count=1` (the source was not changed for the run):

| Test | Fix removed | Observed failure |
| --- | --- | --- |
| `TestTaskOutcomeReaderRequestsAndAcceptsTheRecord` | `taskOutcome` not an accepted activity key | `response_contract` |
| same | request header not sent | `the read did not ask for the outcome: ""` |
| `TestTaskOutcomeInvalidIsRefused` | record validation skipped | invalid records accepted |
| same | row UID check removed | another session's record accepted |
| same | task-mode check removed | an interactive session's outcome accepted |
| `TestTaskOutcomeRendersEscapedTextAndAllowlistedLinks` | renderer link allowlist removed | `renderer linked a URL outside the allowlist` |
| `TestTaskOutcomeShownAfterReclaim`, `TestTaskExitZeroWithoutOutcomeIsUnknown` | outcome panel not added | `the outcome does not lead the detail` |
| same | old "Task result" label and "Succeeded" badge | `process exit ""` |
| `TestPortalReaderTaskOutcomeIsNegotiated` | header ignored, always sent | outcome sent without the header |
| same | never sent | `the opted-in read lost the outcome` |
| same | raw status sent without the binding and check | foreign, remote and invalid records sent |
| `TestPortalReaderWorstOutcomePageFitsThePortalReadBound` | bound back at 256 KiB | `287101 bytes, over the portal's 262144-byte read bound` |
| `TestTaskOutcomeStatePreviews` | outcome previews not written | `outcome-recorded.html: no such file` |

Escaping comes from `html/template` and cannot be removed without replacing
the renderer; its test is a regression guard. `TestSessionsPageStaysUnderTheResponseBound`
now includes a worst-case outcome on every row and still renders within 7/8 of
the 1 MiB response bound.

Runs used Go 1.27.3 from the official archive (SHA-256 matched go.dev) with
`GOTOOLCHAIN=local GOMAXPROCS=2 GOFLAGS=-p=1 nice -n 19`, one package at a time.

## Preview

`go run ./web/portal/preview -product <dir> -pages <new dir>` writes
`outcome-recorded.html` (Suspended after reclaim), `outcome-none.html` (Exited 0,
no record), `outcome-hostile.html` (script and image markup in the summary,
Archived), `outcome-hostile-links.html` (refused hrefs shown as text) and
`outcome-list.html`. The screenshots (1280x800 and 390x844, light and dark)
are in the PR.

## Delivery state

Source and tests only. Installed acceptance needs the 2.75.0 CRD and operator,
a portal image pin, task pods on agent 2.75 (the global-template hold applies),
and real before/after-reclaim UI evidence. Rollout order: either image may go
first. A new portal against an old operator shows "No outcome recorded"; an old
portal against a new operator never asks for the key. The lead,
dev-env-1029-132039, owns review, merge and rollout.
