# Task outcome retention (#320)

Source contract, 2026-10-31 (OVERNIGHT-OUTCOME-320): at the first managed task's
exit, agentd reads exactly `~/work/<session>.outcome.json`, at most 2048 bytes.
The JSON has only `prURLs` (up to three GitHub thaynes43 pull-request URLs,
256 bytes each), `headSHAs` (up to three 40-hex heads), and `summary` (one
nonempty printable Unicode line, at most 200 characters). Unknown fields,
trailing data, symlinks, non-regular files, missing files and any invalid field
produce no outcome. No partial text, prompt or transcript is harvested.

The task result retains the validated snapshot with the initial invocation's
Session and Pod UIDs. Only that pod reports it via the authenticated heartbeat.
The API accepts it for a finished task from the matching original executor,
using the existing usage/exit provenance and private executor binding. A
separate optimistic status patch records the first value; replay, replacement,
invalid payload and omission never change it. CRD validation also prevents
mutation or erasure. CLI reads use Session status in every phase, including
Suspended and Reclaimed; no HOME read or pod revival is involved.

A new `status.taskOutcome` schema field is unavoidable for retained structured
status. Run `make generate`; haynes-ops must copy the generated AgentSession
CRD before an operator pin. Source/test delivery does not establish installation
or runtime acceptance. Lead dev-env-1029-132039 owns review, merge and rollout.

Validation: bounded-file, payload, authenticated heartbeat identity/first-write,
CRD immutability and Reclaimed CLI fixtures. Each behavioral test has a recorded
red/green receipt in the PR and canonical OpenProject story #320.

## Test and review receipts

The five focused suites passed: `internal/agentd/protocol`, `internal/agentd`,
`internal/agentrun`, `internal/apiserver`, and `api/v1alpha1` (real envtest API).
Runs use `GOTOOLCHAIN=local GOMAXPROCS=2 GOFLAGS=-p=1 nice -n 19 go test`,
one package at a time. Go 1.27.3 was installed from the official archive after
its SHA-256 matched the go.dev release metadata.

Finite negative controls used `go test -overlay=... -count=1`; they did not
mutate the reviewed source or launch additional suites concurrently:

| New test | Fix removed for the negative control | Observed failure |
| --- | --- | --- |
| `TestTaskOutcomeRecordValidation`, `TestTaskOutcomePrintableUnicode` | Payload validator returns nil | Off-org/invalid URL, bad SHA, item/summary limits, controls/newlines and 201 Unicode characters accepted |
| `TestTaskOutcomeFileAndExitSnapshot` | Exit capture assignment removed | Valid file produced no retained snapshot |
| `TestShowRetainedTaskOutcome` | CLI display removed | Missing outcome in Reclaimed, Suspended and Running output |
| `TestTaskOutcomeHeartbeat`, `TestTaskOutcomeHeartbeatRefusals` | API validation/identity/first-write guards removed | Invalid payload, wrong pod/session/executor, unbound exit and second write accepted |
| `TestTaskOutcomeConcurrentFirstWrite` | API optimistic lock removed with the guards above | Concurrent first snapshot overwritten |
| `TestTaskOutcomeNegotiatedRead` | Old-reader suppression removed | Old response unexpectedly contained `taskOutcome` |
| `TestTaskOutcomeSchema` | Baseline CRD without the field | Invalid candidates were pruned/accepted; valid candidate pruned |
| `TestTaskOutcomeSchema` | New field retained, transition/erasure guards removed | Second write, field removal, whole-status removal and null status accepted |

Two additional regressions were observed failing against the actual preceding
implementation before correction: invalid raw UTF-8 was normalized by
`encoding/json` and accepted; and a same-pod exit carrying a different attempt ID
was accepted. The reader now rejects invalid UTF-8 before decoding, and the API
requires the complete exit source to equal the retained initial usage source.

Independent native review (`outcome_review`, GPT-6.1 Sol, xhigh) found those two
issues and confirmed both closed after the corrections. Final disposition: no
remaining actionable findings; review was read-only and ran no tests.

The task outcome is source/tested only. This work does not merge, publish, copy
CRDs, pin images, deploy or establish installed/usable acceptance. The lead owns
those steps. The portal is deliberately deferred because its strict, separately
versioned activity reader needs coordinated API/portal delivery; the requested
CLI path is implemented. New CLI requests opt into the optional outcome field
with `X-Dev-Env-Task-Outcome: v1`, preserving older strict Session/list readers.

## Broader package validation

Full package suites passed for `internal/agentd/protocol`, `internal/agentrun`
and `api/v1alpha1`. Full `internal/agentd` (614 tests, 514 seconds) and
`internal/apiserver` (63 seconds) exposed four unchanged temporary-path fixtures:

- `TestDecisionActualHeadlessToOwnedTTYJourney`: tmux teardown unconfirmed.
- `TestHistoryExportRefusesUnexpectedEntries/a_socket_in_projects`: a rename
  from `/tmp` into the configured HOME-backed `GOTMPDIR` crossed filesystems.
- `TestEnvtestOwnedAuthorityLatencyComposition` and
  `TestEnvtestPrivateBudgetDispatchReserveStopPreserve`: the HOME-backed test
  directory made the Unix socket pathname exceed the platform limit.

Go 1.27's `testing.TempDir` uses `GOTMPDIR`; `os.MkdirTemp("", ...)` uses
`TMPDIR`. The pod config selects a longer HOME-backed Go temp path. No source
or fixture change was made: all four affected tests passed in finite, serial,
focused reruns with `GOTMPDIR=/tmp TMPDIR=/tmp` (agentd 0.923 seconds, apiserver
8.779 seconds). The relevant fixture files are unchanged from the pinned base.
The full broad runs remain recorded as failed in this environment; they are
not relabelled green. The new outcome tests passed in those runs, and their
focused green/negative-control evidence is recorded above. Required make checks
and CI are reported separately in the PR/tracker handoff.

Final required local checks passed: `make GO_PARALLELISM=1 lint check-layout
check-generated` under the same local-toolchain/low-priority environment.
Lint reported **0 issues**; layout and generated-file verification were clean.
`make generate` ran explicitly and again through `check-generated`.
