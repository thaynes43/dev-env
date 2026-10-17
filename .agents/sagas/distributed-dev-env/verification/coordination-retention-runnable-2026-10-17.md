# Coordination retention runnable, 2026-10-17

Source only. The section 5 leader runnable is in `internal/coordretention` and
stays default off. This record does not accept R3, installed retention, or MVP.

## What landed

The operator gains `--enable-coordination-retention` (default false) and
`--coordination-retention-grace` (default 720h). Retention on with
`--enable-coordination-archive-reads` off is refused at process start. A grace
under 720h is refused at process start. `--enable-coordination-archive-reads`
is unchanged and still default false.

When the retention flag is false, the runnable is not added. `Start` on a
disabled runnable returns without a pass, a shelf call, or a metric. When the
flag is true, a leader-elected runnable waits 10 minutes, then runs a pass
every 6 hours. Each pass uses a 30-minute deadline.

A pass finds a Running Ready shelf, probes the archive with one read-only
lookup, lists paged summaries, evaluates the existing rules, and looks up
identities in batches of 1024. Marks and releases walk in kind, project, name,
UID order from the in-memory resume key and wrap. Caps are 256 marks and 256
releases. `release` re-reads, archives, verifies with `get`, then
`deleteRecord` with UID and resourceVersion preconditions, then confirms the
record is gone. Unknown and owner-present do not delete. Shelf execs take one
slot from the existing limit of 2 and release it before the next exec. Rescue
and prune execs are unchanged and still take no slot.

Metrics use the section 5 names. The leader registers every pass result at 0
before the first pass and unregisters them when leadership ends.

## Tests

One package at a time, `nice -n 19`, `GOMAXPROCS=2`, `GOFLAGS=-p=1`, `-count=1`:

- `go test -count=1 ./internal/coordretention/` — pass, about 0.4s
- `go test -count=1 ./cmd/dev-env-operator/` — pass, about 0.3s
- `go vet ./internal/coordretention/ ./cmd/dev-env-operator/` — pass

Covered: flag off does no pass; retention on with reads off refuses start;
grace under 720h refuses start; unknown does not delete; owner present does
not delete; precondition conflict does not delete; not found counts done;
forbidden does not delete; a verified record is deleted only after get; a
capped pass resumes at the next record; the runnable holds one exec slot; the
first wait is 10 minutes, the interval is 6 hours, and the pass deadline is
30 minutes.

## Safer readings

Each sentence is from DESIGN-024 section 5. The implementation chooses the
refusal that does not delete.

- "Without it, or when it answers `no_archive`, the pass ends as `shelf_unavailable`." Any error finding a Running Ready shelf, and any archive probe error other than the deadline or a busy exec slot, ends `shelf_unavailable` and deletes nothing. A busy slot ends `failed`. A cancelled deadline ends `partial`.

- "At most 256 marks and 256 releases in one pass." Present-event grace resets are not counted in that 256. They run before marks and releases. A failed reset is remembered by that leader and blocks a later release of that record until a reset event clears grace. This follows "The grace restarts at the next observed absence" and "A record is never deleted sooner than 30 days after the latest absence began".

- "batched in one `mark` exec per project." A project with more than 1024 kept records is split at 1024 because the shelf mark command rejects a larger batch. The sentence does not say what to do above 1024.

- "The ack keeps its own mark, made with the message's." The ack's absent event uses rule `message-released` and observation `message-released`, with the message mark's time. That is the only absent event the protocol accepts for an ack. The message is still live then. The event is the structural companion, not a claim the message was already deleted.

- "while the message is live the ack is handled only through it." An ack whose message UID is still in the live summaries is not marked, reset, or released on its own.

- "Unknown: nothing. Count it." The metrics table has no unknown series. The pass log counts unknown summaries. `refused_total{reason="unknown"}` counts only a release recheck that returns Unknown.

- "NotFound counts as done." A delete that returns NotFound increments `deleted_total` after confirm also sees the record gone. A recheck that finds the record already gone does not increment `deleted_total`.

- "A pass at 30 days minus a minute deletes none. A pass after 30 days deletes them." Exactly 720h counts as grace over, because that is not sooner than 30 days.

- A message and its ack count as one mark and one release toward the caps. The per-kind metrics still increment for each object.

- The pending gauge on a capped pass uses the full classification, including records the mutation window has not reached.

- Slot exhaustion is not one of the named pass results. It ends `failed` and deletes nothing.

- The design does not say to refuse retention when the API address is `0`. Start refuses only retention on with reads off, plus a grace under 720h.

## Still absent

- Envtest E1, E2, E4, E6, and E7 are not in this change. The tests use a fake clock, fake shelf, and fake client.
- The controller guard scanner was not moved. `internal/controller` was not edited. `TestOnlyRetentionDeletesCoordinationRecords` scans `internal/coordretention` and the operator flag gate. It does not type-check every other package. Function literals are a known gap.
- The `internal/shelf` package comment was not changed.
- `dev_env_coordination_records*` help text was already updated by earlier source and was not edited here.
- No haynes-ops Role, deployment flag, or alert was added. The retention flag is not set in any deployment.
- The inbox message-and-ack unit is implemented and was not exercised by the focused TaskClaim tests.
- A new leader starts with an empty resume key. A failed present reset clears the old grace in the archive, and a release still refuses unless the journal tail is the absent event that opened `GraceStart`. Memory of the failure is not required.
