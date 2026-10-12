# ADR-004: Coordination record retention: archive, then delete

- **Status:** Accepted; implementation and acceptance open
- **Date:** 2026-10-12
- **Decider:** Tom Haynes
- **Ratified:** Tom, 2026-10-12, through the Claude native prompt (Q-29):
  "Archive, then delete (Recommended)"
- **Amends:** [DESIGN-010](../designs/010-atomic-task-claims.md)'s rule that no
  claim is released without a verified old-executor stop contract, **in the reap
  case only** (the owner Session's UID is absent). Also the lifetime project
  bounds of DESIGN-010, [DESIGN-013](../designs/013-published-stopped-context.md)
  and [DESIGN-014](../designs/014-durable-offline-inbox.md).
- **Retains:** [ADR-003](003-session-coordination-private-repositories.md) in
  full; DESIGN-008's private HOME retention; every other DESIGN-010 owner rule.
- **Design:** [DESIGN-024](../designs/024-coordination-record-retention.md)
- **Delivery:** [plan 11](../backlog/11-project-workspaces.md), "Coordination
  record retention"
- **Tracking:** [#175](https://github.com/thaynes43/dev-env/issues/175)

## Context

ADR-003 asks for durable task claims, durable messages and readable stopped
context. Three immutable kinds in the protected `coordination.dev-env.haynesops.com`
group carry them: TaskClaim, PublishedContext, and InboxMessage with its
InboxReadAck. Nothing deletes them. The operator's Role grants get, create and
list only.

Each kind has a per-Session and a per-project bound (PR #223). Since nothing is
deleted, both are lifetime bounds. A project that reaches its bound refuses every
new record of that kind for good. Recovery is a manual kubectl delete by Tom,
after an export. Plan 11 parked the question on 2026-10-12.

A claim also never ends. A Session that is reaped keeps its claims, so its task
can never be claimed again. DESIGN-010 allows release only under a future
verified stop contract, which does not exist yet.

## Q-29: the question as asked

Asked through the Claude Code native prompt (AskUserQuestion) by coordinator
session `task-dev-env-1012-105043`. Recorded 2026-10-12 17:13:14 UTC.

> Agent coordination records (task claims, shared notes, messages) are never
> deleted, so a busy project eventually stops accepting new ones (today: 2 of
> 1,024 used, so not urgent). Proposal: 30 days after the sessions involved are
> gone, copy each record to a private archive, then delete it. Archived notes
> stay readable, and a dead session's task becomes claimable again. Approve?

Options, recommended first, with the labels exactly as shown:

1. **Archive, then delete (Recommended).** The proposal above.
2. **Keep all, count live.**
3. **Manual cleanup only.**

The prompt carried no further text for options 2 and 3.

**Answer (Tom, 2026-10-12):** "Archive, then delete (Recommended)".

## Decision

### Release on reap, archive first

A record is released when the Sessions it depends on are absent:

| Kind | Released when |
|---|---|
| TaskClaim | its owner Session's UID is absent |
| PublishedContext | its source Session's UID is absent |
| InboxMessage | its sender's UID is absent, and its recipient has acknowledged it or its recipient's UID is absent |
| InboxReadAck | its message is released (the two go together) |

**Absent** means that an uncached read of the AgentSession proves that UID is
gone from the API server: the name is not found, or the name now holds a
different UID. Any other answer is Unknown, and Unknown releases nothing. A
stopped, suspended, deleting, Reaping or retained Session is present.

**Grace.** A record is deleted no sooner than 30 days after its release was
first observed. The operator re-checks absence immediately before the delete.

**Archive first.** Before any delete, the full object as the API server returned
it, with a manifest that holds its SHA-256, is written to a new ReadWriteOnce
volume. Only the shelf pod mounts that volume. It is not `dev-env-shared`. The
archive is kept indefinitely. Nothing removes an archive entry.

**One deleter.** A leader-only runnable in the operator does all of it. It marks
the first observation, waits out the grace, re-checks absence, verifies the
archive entry's hash under the archive's `.shelf.lock`, and deletes with UID and
resourceVersion preconditions. haynes-ops grants the operator, and only the
operator, a namespace-scoped delete on the four kinds. No agent, worker,
coordinator or API caller gets delete, and no API route deletes.

**Reads.** A read of a record that is no longer live falls back to the archive,
under the same scopes as a live read. The answer carries `archived: true`.
Without a shelf, such a read answers 503, never 404.

**Bounds.** Per-Session bounds stay lifetime bounds. A Session's records are
released only once it is absent, so a live Session's count never drops. Project
bounds count live records, so retention frees them. An alert fires when a
project's live count of a kind reaches 80% of its project bound.

### What this amends in DESIGN-010

DESIGN-010 says missing, stopped, deleting or reaped owners stay claimed, and
that no release happens without a verified old-executor stop contract. This ADR
adds one exception: the reap case, where the owner Session's UID is absent,
proved by an uncached read, and has been for 30 days since the first
observation. Then the claim is archived and deleted, and the task can be claimed
again by a new owner.

Absence is not stop proof in general. It is enough here for two reasons. First,
the operator releases a Session's finalizer only after an uncached read finds
its Pod and home volume gone (`releaseIfEmpty`), so an absent Session is a
finished reap, unless an admin lifted the finalizer by hand. Second, the 30 days
are a margin far beyond any Pod's termination. Every other DESIGN-010 case is
unchanged. A stopped, suspended, Reaping or retained owner keeps its claims. A
transfer from a present owner still needs the future stop contract. Release on
reap is not a handoff.

### Known gap: private Sessions are never absent

A Session with private history retention on (DESIGN-008, DESIGN-019) is never
released: `releaseIfEmpty` refuses it, so its AgentSession stays, Reaping, with
its HOME volume. Its UID is therefore never absent, and none of its records is
ever released. Its claims stay claimed, and its records keep counting toward
their project bounds.

The installed operator runs with `--enable-private-history-retention=true`.
With that flag, every Session without a shared workspace is retained this way
(`privateHistoryEnabled`), and shared workspaces are retired (ADR-003). So in
today's installation retention releases almost nothing. Only a record whose
Session left by another path qualifies, for example one whose finalizer an admin
lifted. The mechanism is still worth building now: it is what a HOME expiry
policy will feed, and the 80% alert works from day one.

DESIGN-008 already says that HOME expiry needs an explicit policy. Until one
exists, this gap stays. **It is not solved here.** Plan 11 carries it as a
follow-up that needs its own owner question. Canary originals are protected even
with the flag off (`privateHistoryProtected`), so their managed-output
publications stay live, which the fixed result reader needs.

### What does not change

- Records stay immutable. Nothing updates or patches them.
- SessionDirectory records, provider HOMEs, rescue bundles and their 30-day prune
  (D-67) are outside this decision.
- The manual recovery in DESIGN-010 stays, for Tom only. It bypasses the archive,
  so its export is the only copy.
- No worker grant, Secret read or broad RBAC is added.

## Consequences

| Id | Consequence |
|---|---|
| C-22 | Good: a busy project no longer stops accepting records for good. Records of finished Sessions free up 30 days after the Sessions are gone. |
| C-23 | Good: a reaped Session's task can be claimed again, without a stop contract, by the narrowest evidence available: the owner's UID is gone. |
| C-24 | Good: history survives deletion. Archived contexts and messages stay readable under the same scopes. |
| C-25 | Bad: the operator gains delete on four kinds. A guard test, preconditions and an archive check before every delete contain it. |
| C-26 | Bad: archived reads depend on one shelf pod. While it is down, they answer 503, and retention waits. Live reads and creates do not depend on it. |
| C-27 | Bad: the archive grows without limit. It needs a volume alert and, eventually, its own retention decision. |
| C-28 | Bad: records of private Sessions are never released until a HOME expiry policy exists (the known gap). With the installed flag that is nearly every Session, so the project bounds stay close to lifetime bounds until then. |
| C-29 | Neutral: an etcd restore could bring back an absent UID. The runnable never deletes while an owner is present, but after that Session's later deletion the earlier mark's grace has already run. The archive holds the record, so only the 30 days are skipped. |

## Delivery and acceptance

DESIGN-024 holds the contract. Plan 11 splits it into reviewable units. The 80%
alert can ship first, since it needs only today's metrics. Then, in order: the
agentd archive commands; the operator runnable with its guard test and envtest
proof; the read fallback; the haynes-ops volume, shelf mount and Role; then
enabling it. Everything stays default off until the enabling haynes-ops change.

Acceptance needs an envtest proof that a record is archived and verified before
it is deleted, and that the delete is refused when the archive entry is missing
or wrong, when the owner is present, and when absence is Unknown. Installed
acceptance follows on a disposable non-private Session.
