# ADR-005: Private session HOME expiry: archive, then delete

- **Status:** Accepted; implementation and acceptance open
- **Date:** 2026-10-12
- **Decider:** Tom Haynes
- **Ratified:** Tom, 2026-10-12, through the Claude native prompt (Q-31):
  "Archive, then delete (Recommended)"
- **Closes:** follow-up F1 of [ADR-004](004-coordination-record-retention.md),
  its "Known gap: private Sessions are never absent".
- **Amends:** [DESIGN-008](../designs/008-private-provider-history-retention.md)
  (HOME expiry now has a policy, and a private executor gets a durable terminal
  record); [DESIGN-019](../designs/019-private-pod-retention-guard.md) (a reaped
  private executor is stopped, and its Pod may go once its termination is
  recorded); [DESIGN-001](../designs/001-dev-env-v2.md) D-45 (the guarded deletes
  gain one expiry path), D-55 (a hold pod for a private Session, bound to its
  HOME UID), D-62 (a suspended private Session past its archive deadline) and
  D-67 (the shelf gains a second archive volume);
  [DESIGN-013](../designs/013-published-stopped-context.md) (its payload-read
  scope also covers archived provider history).
- **Retains:** [ADR-003](003-session-coordination-private-repositories.md) and
  ADR-004 in full; DESIGN-010's stop contract for claim transfer;
  `status.privateHistory.stopState: Unknown`; every guard for canary originals.
- **Design:** [DESIGN-025](../designs/025-private-home-expiry.md)
- **Delivery:** [plan 11](../backlog/11-project-workspaces.md), "Private HOME
  expiry"
- **Tracking:** [#174](https://github.com/thaynes43/dev-env/issues/174)

## Context

A private Session is one with no shared workspace. Its provider HOME is a
ReadWriteOnce volume, `home-<session>`, that holds the provider's chat history,
the repositories and the task worktrees. DESIGN-008 and DESIGN-019 keep that
volume, the Session and the original executor Pod when the Session is suspended
or reaped. Nothing deletes them. DESIGN-008 says that HOME expiry needs an
explicit policy. There was none.

So finished private Sessions pile up. Their task claims never free up either:
ADR-004 releases a record only once its Session is absent, and a private Session
never becomes absent (ADR-004 "Known gap", plan 11 follow-up F1). On 2026-10-12
three such HOMEs exist. All three are test canaries, about 30 MB each, so the
question is not urgent.

The same day showed what a stop proof may rest on. An undeclared reboot of node
talosw02, from about 18:14 to 18:19 UTC, removed the two retained 1011 canary
Pods, whose executors had already ended. The operator correctly refused to
recreate them and marked their preservation Unknown. The ended Pods had been the
only evidence that those executors had stopped, and that evidence was gone.

## Q-31: the question as asked

Asked through the Claude Code native prompt (AskUserQuestion) by coordinator
session `task-dev-env-1012-105043`. Recorded 2026-10-12 18:45:12 UTC.

> Private agent home folders are never deleted today, so finished sessions pile
> up and their task claims never free up (3 exist now, all test canaries, about
> 30 MB each, so not urgent). Proposal: 30 days after a session is proven
> stopped, save its code and chat history (no credentials) to a private archive,
> then delete the folder. Test canaries are never auto-deleted. Approve?

Options, recommended first, with the labels exactly as shown:

1. **Archive, then delete (Recommended).** The proposal above.
2. **Keep homes, free claims.**
3. **Keep all, manual cleanup.**

The prompt carried no further text for options 2 and 3.

**Answer (Tom, 2026-10-12):** "Archive, then delete (Recommended)".

## Decision

### Which HOMEs expire

A HOME expires only when all of these hold:

- Its Session is private: no `spec.workspace`, no `spec.projectCanary`, and a
  `status.privateHistory` record whose HOME UID is bound.
- The Session is Reaping, or it is Suspended and past its suspended-archive
  deadline (D-62's timer).
- The operator holds a durable stop proof for the Session's recorded executor
  (below).
- At least 30 days have passed since the expiry's grace started, or Tom asked
  for release-now.
- Tom has not put a hold on it.

**Canary originals never expire automatically.** Release-now does not reach them
either: the fixed canary result reader (DESIGN-022) reads their records live.

### The durable stop proof

**A retained Pod is not a durable stop proof.** A node can lose it at any time,
as talosw02 did. The proof is the operator's own recorded observation that the
executor Pod with the recorded UID terminated. The operator writes that
observation into the Session's status, and confirms the write, before the Pod can
disappear. Two facts make it a proof: Pod UIDs are never reused, and a Pod in a
terminal phase never runs again.

The observation counts only when an uncached read shows all of this:

- the Pod's UID is the Session's recorded executor UID
  (`status.privateHistory.lastExecutor.podUID`), and the Session controls it;
- its phase is `Succeeded` or `Failed`, and the kubelet reports every container
  terminated;
- its node is Ready and holds a fresh lease, so a live kubelet wrote that status.
  A `Failed` phase that the Pod garbage collector set for a lost node does not
  count.

**Absence alone never proves stop.** If the Pod vanishes before the observation
is recorded, the stop stays Unknown and that HOME never expires automatically.
If the Pod vanishes after it, expiry goes on: the record is the proof, not the
Pod.

The record is immutable and bound to that one executor UID. It is used by HOME
expiry only. It does not change `stopState`, which stays `Unknown`, and it does
not authorize a claim transfer, a resume or a replacement executor. Those keep
their own future contract (DESIGN-010).

### Grace, cancel, hold and release-now

The grace is 30 days. It starts at the first recorded stop proof, in a new
record, `status.privateHistory.expiry`. A resume of the Session, or a new Pod for
it, clears that record. A later expiry then starts a new 30 days.

Two overrides are Tom's alone:

- **hold:** nothing more happens to that HOME until he removes the hold;
- **release-now:** skips the grace and nothing else. The stop proof, the rescue,
  the export, the archive check and every re-check still apply.

An admission policy refuses both to every ServiceAccount, so no agent, worker,
coordinator or operator code path can set them. (If Tom's own phone tool acts as
a ServiceAccount, the policy names that one account; DESIGN-025 section 9.)

### Before a HOME is deleted, in order

1. **Git rescue.** A D-55 hold pod is bound to the HOME's UID, and the usual
   rescue runs in it. It must pass.
2. **History export.** The provider's chat history is exported from an
   allowlist of paths. Credentials are never exported. If any file with a
   credential name (such as Codex `auth.json`) sits inside the exported tree,
   the export refuses and the HOME stays.
3. **Archive.** The export and the rescue's Git bundle go into a ReadWriteOnce
   archive volume that only the shelf pod mounts, with a SHA-256 manifest. Its
   PersistentVolume is set to `Retain`. The archive is kept indefinitely. Nothing
   removes an entry.
4. **Readable.** Archived history is readable through the API under the same
   payload-read scope as DESIGN-013's published context, and every answer says
   `archived: true`.
5. **Delete.** Only the operator's leader deletes, through the guarded delete
   path (D-45). Immediately before the delete it re-verifies the archive's hash
   under the archive's `.shelf.lock`, and it re-reads the Session, HOME and Pod
   UIDs uncached. The delete carries UID and resourceVersion preconditions.
   `releaseIfEmpty` and `releaseSession` gain one exception: a Session whose
   expiry record confirms its HOME is gone.

**Deletion is off by default** and stays off until the archived-history read is
accepted on the installed system (#174). Until then the steps run up to the
archive and stop there.

Then, for a reaped Session, ADR-004 takes over: once the Session is released, its
UID is absent, and its coordination records are archived and deleted 30 days
later.

### The archive volume is a sibling of ADR-004's

The ruling names a shelf-only RWO archive volume with a Retain PV. ADR-004
already has one, `dev-env-coordination-archive`. HOME history goes into a
sibling volume, `dev-env-history-archive`, with the same rules: shelf-only,
`Retain`, never pruned, never off the cluster. Three reasons:

- **Size.** A coordination record is kilobytes; an exported HOME is megabytes to
  gigabytes. A full history archive must not stop coordination retention, nor the
  other way round. Each volume gets its own fill alert and grows on its own.
- **Shape.** DESIGN-024's archive stores single objects of at most 1 MiB. A HOME
  export is a tree of files with a streamed write, which needs its own layout and
  commands.
- **Content.** Full transcripts are more sensitive than coordination records. A
  later backup or retention decision for one should not have to cover the other
  (ADR-004 C-27).

The cost is a second manual `Retain` step for Tom, and a second volume that the
shelf must reattach after a node loss. DESIGN-024's recovery steps cover both.

### Stopping a reaped private executor

A stop proof needs an executor that stops. An ordinary private executor
(`RestartPolicy: Always`) never ends on its own: agentd keeps running after the
agent exits. DESIGN-019 keeps its Pod through suspend and reap, and that design
deliberately added no stop request. It named a finite stop request as the next
contract.

This ADR adds it, at the two points where today's lifecycle ends a non-private
Session for good: the reap, and the suspended-archive deadline (D-62), after
which a Session can never be resumed. So a private Session is stopped when it is
Reaping, or when it is suspended and passes that deadline. The operator sets the running
executor's `spec.activeDeadlineSeconds`, with the Pod's UID and resourceVersion
as preconditions. The kubelet then stops the containers, with the usual grace,
and marks the Pod `Failed`. The Pod object stays, so its termination can be
recorded. No Pod is deleted, no HOME byte changes, and a suspended Session
before its deadline is not stopped. Like the rest, it is default off.

### What this changes

- **DESIGN-008.** HOME expiry has a policy. The executor record gains an
  immutable terminal observation. "This slice adds no private typed termination
  proof" stays true of `stopState`, which keeps the value `Unknown`.
- **DESIGN-019.** The original executor Pod gets a stop request at reap and at
  the archive deadline. Once its termination is recorded, it may be removed, but
  only by the expiry at its rescue step, with UID and resourceVersion
  preconditions, so that the D-55 hold pod can take its name. A Pod whose
  termination is not recorded stays protected as before.
- **D-45.** `podRemovalAllowed`, `volumeRemovalAllowed`, `releaseIfEmpty` and
  `releaseSession` each gain one exception for a confirmed expiry record.
  `TestOnlyTheGuardDeletes` names them, and a new guard test pins the one Pod
  patch.
- **D-55.** A hold pod is also made for a private Session, by the expiry only,
  and only after the stop proof. It is bound to the HOME's UID: the operator
  checks the claim's UID before it creates the hold pod, again once the pod
  runs, and before each later step.
- **D-62.** A suspended private Session past its archive deadline is stopped and
  then expires like a reaped one. Before its HOME is deleted, the operator records
  `status.archivedAt`, so a resume is refused from then on. The Session stays
  phase `Archived` until it is reaped.
- **D-67.** The shelf mounts the history archive too, and copies the rescue
  bundles of an expiring Session from the shared volume into it. D-67's 30-day
  prune of the shared copies does not change.
- **DESIGN-013.** Its payload-read scope also authorizes reads of archived
  provider history (new routes, DESIGN-025 section 8). Publication does not
  change.
- **ADR-004.** Its known gap closes. A private Session becomes absent once its
  HOME has expired and the Session is released. Canary originals stay present,
  and so do their records.

### What does not change

- A Session whose executor was never observed has no stop proof and never
  expires: one never started, one with only a known non-attempt, or one with
  `admission: Unknown`. Neither does a Session whose executor vanished before its
  termination was recorded, such as the two 1011 canaries. Their HOMEs stay until
  Tom removes them by hand.
- No credential leaves a HOME. Content inside transcripts is not scanned.
- No worker, agent or coordinator gains a delete, a Secret read or broad RBAC.
  No API route deletes or sets an override.
- Shared-workspace Sessions keep their own lifecycle.

## Consequences

| Id | Consequence |
|---|---|
| C-30 | Good: finished private Sessions no longer pile up. Their HOMEs go 30 days after a proved stop, and their task claims and records then follow ADR-004. |
| C-31 | Good: a stop proof no longer depends on a Pod object surviving. A recorded termination outlives node reboots, Pod loss and operator restarts. |
| C-32 | Good: code and chat history survive the HOME, readable under the existing payload scope, with Git bundles kept past D-67's prune. |
| C-33 | Bad: the operator gains a pod patch (the stop request), a delete of a private executor Pod and a delete of private HOMEs. Guard tests, preconditions, re-checks at every step and an archive check before every delete contain them. |
| C-34 | Bad: provider history layouts change with CLI versions. An unclassified name in a provider directory refuses the export, so a new layout blocks expiry until a source change classifies it. History is never dropped silently. |
| C-35 | Bad: the history archive grows without limit, holds full transcripts (which may contain secrets an agent printed) and depends on the shelf pod and its identity guard, like ADR-004's archive. |
| C-36 | Bad: a private Session whose executor was never observed, or vanished before its termination was recorded, never expires. Those HOMEs need Tom. |
| C-37 | Neutral: a reaped private Session's executor is now stopped at reap. Today it keeps running. Nothing is lost: the HOME and the Pod object stay. |
| C-38 | Neutral: once an operator has written a terminal record, rolling the operator back below that release leaves those Sessions' status frozen, because the schema refuses a write that drops the record. It never deletes anything. |

## Delivery and acceptance

DESIGN-025 holds the contract. Plan 11 splits it into units H0 to H10. The
terminal record ships first and on its own, because each day without it can lose
a proof. Then the stop request, the agentd export and archive commands, the
operator's expiry up to the archive, and the reads. Deletion comes last, after
an installed read of a real archived history is accepted (#174).

Acceptance needs envtest proof that a HOME is archived and verified before it is
deleted; that the delete is refused when the archive is missing or changed, when
a credential sits in the exported tree, and when any read is Unknown; that a
canary never expires; that a Pod lost after its recorded termination still
expires; and that a Pod lost without one never does. Installed acceptance records
the archived read (H8), then the first deletion and the ADR-004 release that
follows it (H10).
