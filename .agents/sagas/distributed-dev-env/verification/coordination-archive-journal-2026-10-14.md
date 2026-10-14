# Coordination archive journal/grace source fixture — 2026-10-14

Scope: DESIGN-024 R2 append-only event journal and truthful marked lookup after
source #287, merge `42cd82da46646c26eee6aa0d57008fa71c09abd5`.
No broader retention system is redesigned or activated.

`put --mark` requires the manifest/object request plus an absent event. The shelf
verifies and durably publishes the revision and indices under the exclusive lock
before appending absent. Plain put now returns actual recorded grace. `mark`
accepts one JSON array of 1–1024 identity/event items with present/reset only,
and skips unmarked UIDs. Lookup reconstructs grace under the shared lock while
reading metadata only; exact get remains the object verification step.

Events retain the accepted format, sequence names and observations. Structural
validation bounds roles, identities, observation enums, rule, timestamp and by.
These are supplied observations, not authenticated Session absence or permission
to release. Reset can record lost archive data without a new Session observation.
No Kubernetes API read, release evaluation, retention runnable or deletion is added.

The reader scans event names in 128-entry batches, then validates one canonical
event per contiguous eight-digit sequence using constant journal memory. Unknown
or malformed state, gaps, duplicate sequences, symlinks, excess links, events
over 8 KiB and backward timestamps are corrupt, with no partial lookup result.
Missing/empty journals mean unmarked. First absent after present/reset starts
grace; repeated absent preserves it. Present/reset clears grace.

Append uses private fsynced staging and an exclusive hard link, never overwrite.
A second link may remain in private incoming staging until the already-planned
cleanup. Existing events are never removed. Retry after a visible link followed
by failed parent sync repairs durability even when the transition is skipped.
Mark preflights the batch before writes; a publication failure can leave a
prefix. Retry skips/resyncs already-cleared UIDs, with no duplicate event.
The existing 1 MiB + 64 KiB stdin and 4 MiB metadata output bounds remain.

The synthetic main-dispatcher fixture uses existing filesystems with the real
device check, temporary data and no subprocess clients. Its command scenario:

```text
stdin put --mark -> lookup: graceStart=2026-10-14T11:00:00Z
repeat marked put/plain put: original grace preserved
stdin mark present -> lookup: graceStart=null
stdin mark reset -> lookup: graceStart=null
re-mark -> lookup: graceStart=2026-10-14T16:00:00Z
corrupt journal -> lookup: exit1 corrupt, no grace/result
```

Run the new scenario in a configured source checkout:

```sh
GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
  flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 \
  /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go \
  test -p 1 -count=1 -v -run '^TestCoordArchiveCLIMarkedLookupGraceTransitions$' ./cmd/agentd
```

The focused coordination suite passed all 41 top-level methods (108 passing
nodes including finite subcases), with no skips: cmd 0.096s, agentd 1.177s,
protocol 0.054s, cached Go 1.27.2 under the same lock/CPU bounds and no downloads.
It covers all four kinds, preserving old event bytes, unknown/malformed/linked
journals, gaps/duplicates, clock rollback, invalid marking before writes,
1024-item mark bounds, unverified-object refusal, revision/index failure before
marking, and failed event publication sync followed by idempotent repair.
Changed put/lookup wiring warranted the existing focused regressions; no separate
repeat of the plain demo was run.

The first run's sole failure exposed a writer error-class regression: journal
preflight saw a linked archive parent before the existing no_archive guard.
A bounded no-follow parent check preserves that distinction. The corrected
focused suite above passed; the original failure log is preserved privately.
Historical budgets/counters and the three-failure/60-minute stall rule are not reset.

General latest get, list, old incoming cleanup and shelf startup wiring remain
R2 work. Installed consumers, H7/H8, full parity and pilot acceptance are separate.
Live impersonated dry-runs await separate original Tom approval; consumed inquiry
permission is not reused. Release/deployment, archive mount/activation/deletion,
v1 changes and cutover stay held.

Hosted lint then identified an unused initial budget assignment and two command
branches that should be tagged switches. Those were corrected, and stale command
help was updated per advisory review. The final focused suite above passed after
that small control-flow delta; native read-only delta review found no blockers.
