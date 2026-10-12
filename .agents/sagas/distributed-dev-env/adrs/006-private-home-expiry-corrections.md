# ADR-006: Corrections to ADR-005 after its review

- **Status:** Accepted 2026-10-12 (agent decision D-87). It narrows ADR-005 and
  corrects statements in it. It adds no authority and asks nothing new of Tom.
- **Date:** 2026-10-12
- **Decider:** the agent that finished the review findings of PR #233, within
  Tom's Q-31 ruling ("Archive, then delete (Recommended)")
- **Amends:** [ADR-005](005-private-home-expiry.md): "Stopping a reaped private
  executor", "The archive volume is a sibling of ADR-004's", "What this changes"
  (DESIGN-008, DESIGN-019, D-45, D-62), "What does not change" and C-35 and C-37.
  [ADR-004](004-coordination-record-retention.md): "Agents cannot read it".
- **Retains:** Q-31 as asked and answered; everything else in ADR-005 and
  ADR-004.
- **Design:** [DESIGN-025](../designs/025-private-home-expiry.md), corrected in
  the same change; a dated note in
  [DESIGN-024](../designs/024-coordination-record-retention.md) section 3
- **Delivery:** [plan 11](../backlog/11-project-workspaces.md), "Private HOME
  expiry", units H1 to H7, and follow-up F3
- **Tracking:** [#174](https://github.com/thaynes43/dev-env/issues/174)

## Context

PR #233 merged ADR-005 and DESIGN-025. An independent review then read both
against the source and against haynes-ops RBAC on `main`. It found 13 defects.
DESIGN-025 fixes them all. Five change what ADR-005 itself says, and an Accepted
ADR is never edited, so this ADR amends it.

## Decision

### 1. The operator stops a private executor only at a reap

ADR-005 stopped a private executor at the reap and at the suspended-archive
deadline (D-62). Q-31 did not ask about stopping executors. It asked about
deleting a HOME 30 days after a proven stop. The stop at the deadline went
further than that:

- The idle timer suspends an interactive Session after 72 hours idle, and the
  archive deadline falls 168 hours later. So a private interactive Session left
  alone for about ten days would have had its executor killed, with no human
  asking.
- `activeDeadlineSeconds` cannot be raised, and the executor latch (DESIGN-008)
  refuses a replacement executor. That Session could never run again. Today it
  keeps its Pod and resumes.
- The owner hold did not prevent the stop.

So, replacing ADR-005's section "Stopping a reaped private executor":

- **The stop is for a Reaping Session only.** A reap is an explicit request to
  end a Session, and it already ends the executor of every non-private Session.
- **An owner hold blocks the stop.** The operator first records a stop intent in
  the Session's status. That write carries the resourceVersion of the uncached
  read that found the Session Reaping with no hold, and it is confirmed by a
  re-read. Only then does it patch the Pod. A hold written before the intent
  makes the intent's write conflict, so the hold wins. A hold written after it
  comes too late, and the stop goes ahead.
- **A suspended private Session is never stopped.** Q-31's policy still triggers
  expiry for one past its archive deadline, but only if its executor ended on its
  own (an eviction, for example) and that end was recorded. A suspended private
  Session whose executor still runs keeps its HOME until it is reaped.
- **A stop at the suspended deadline is not planned.** It would end a live
  Session that no human asked to end, so it needs its own owner question before
  any work. Plan 11 follow-up F3 records that.

C-39 below replaces ADR-005's C-37. The D-62 line of ADR-005's "What this
changes" now reads: a suspended private Session past its archive deadline is not
stopped; if its recorded executor has ended, it expires like a reaped one.

### 2. Who can read the archives

ADR-005 called the history archive "shelf-only", with "the same rules" as
ADR-004's archive. DESIGN-025 said that only Tom can read a history with no
project. ADR-004 says agents cannot read its archive. All three rest on
`dev-env-identity-guard` and the Kyverno `dev-env-exec-guard`, and both of those
match only `dev-agents` identities. So the statements hold for v2 agents and for
API callers, and not for these:

- **v1's OPERATOR tier** (`dev/dev-env`, which every agent in the v1 pod uses,
  and `upgrade-agent/dev-env-ops`) holds `pods/exec` and Job `create` across the
  cluster. It can exec into the shelf, or run a Job on the shelf's node that
  mounts an archive claim, and then read, rewrite or delete any entry.
- **`dev-env-broker` and `dev-env-keeper`** hold `pods/exec` in `dev-agents`
  with no name limit.
- **cluster-admin identities**, and node, storage and hypervisor administrators,
  reach every volume in the cluster.

Once a HOME is deleted, the archive is the only copy of its history. So:

- The docs name the real readers (DESIGN-025 section 7, DESIGN-024's note).
- **H7 adds an admission guard for both archives**, in the reviewed haynes-ops
  change. For every ServiceAccount except `dev-env-operator`, it refuses exec,
  attach and port-forward into the shelf pod, and ephemeral containers on it. It
  refuses any Pod or workload that mounts either archive claim, except the
  shelf's own Deployment and the ReplicaSet and Pod made from it. It refuses any
  change to the shelf Deployment's pod template by anyone but Flux, except one
  that changes only the `kubectl.kubernetes.io/restartedAt` annotation
  (`kubectl rollout restart`), which leaves the pod spec as it was. Human users
  keep their access.
- **It narrows v1 at the shelf only.** On 2026-10-12 no runbook in haynes-ops or
  in this repository execs into the shelf. H7 checks that again before the guard
  ships. If a v1 workflow needs that access, it becomes a question for Tom first,
  because no v2 guard may cut a capability that agents use today.
- **Outside the guard:** cluster-admin, node, storage and hypervisor access, as
  for every volume in the cluster. If Tom's phone path acts as a ServiceAccount
  (DESIGN-025 section 9's check), the guard names that account, as the override
  policy does.

### 3. The terminal record sits beside the executor record

ADR-005 put the terminal observation on the executor record,
`status.privateHistory.lastExecutor`. The operator rebuilds that record on every
reconcile and carries only the conversation id forward
(`internal/controller/private_history.go`). A rebuild would drop the
observation, the immutability rule would refuse the write, and the Session's
status would freeze. So the observation is a sibling field,
`status.privateHistory.executorTerminal`, bound to `lastExecutor.podUID`.
Nothing else about it changes.

A second sibling, `executorMissed`, records once that the recorded executor was
found gone with no terminal record. The `missed` count and its warning then fire
once per Session, not again after every operator restart.

### 4. A fifth guard exception: the expiry's hold pod

`podRemovalAllowed` refuses every Pod of a protected private Session before it
reaches the hold-pod rule. The hold-pod rule also keeps a Running, un-rescued
hold pod while the Session wants its Pod gone. So the expiry's own hold pod could
never be removed after a cancel, an owner hold or archive being turned off. D-45
gains a fifth named exception, `expiryHoldRemovalAllowed`. `TestOnlyTheGuardDeletes`
lists it beside ADR-005's four.

### 5. What the history export leaves out

ADR-005 says no credential leaves a HOME. The export enforces that by file name.
Claude's `file-history/` keeps snapshots of edited files under hashed names, so a
snapshot of an edited `.env`, kubeconfig or key file would pass the name check.
`paste-cache/` is the same. So a subtree that keeps copies of user or workspace
files under names that do not say what they are is not exported: Claude
`file-history/`, `paste-cache/` and `uploads/`, and Codex `attachments/`. The
manifest lists them by name and size, so nothing is dropped silently. Code goes
through the Git rescue, and the conversation through the transcripts.

agentd's own entries are classified by name, like the providers'. Its
question-and-answer records and its captured assistant text are history and are
exported. An unclassified name refuses the export.

## Consequences

| Id | Consequence |
|---|---|
| C-39 | Bad: a reap of a private Session now ends its running executor, as a reap does for every other Session. Any API caller may reap any Session, as in v1, so a mistaken reap of a live private Session ends that agent. Its HOME, transcripts and Pod object stay, but the executor latch refuses a replacement, so it cannot run again. A hold set before the reap prevents the stop. Replaces ADR-005 C-37. |
| C-40 | Good: no executor is stopped unless someone reaps its Session. Suspended private Sessions keep running, as today. |
| C-41 | Bad: a suspended private Session whose executor still runs never expires until it is reaped. Its HOME and its coordination records stay. |
| C-42 | Good: the archives are guarded against v1's cluster-wide exec and Jobs, and against the broker's and keeper's exec. Bad: v1 loses exec into the shelf, which no workflow uses today; cluster-admin, node, storage and hypervisor access still reach the archives. Amends ADR-005 C-35. |
| C-43 | Bad: edit snapshots, pasted-text caches and attachments are not archived. A file that lived only there goes with the HOME. |

## Delivery

DESIGN-025 holds the corrected contract. Plan 11's units do not change in order.
H1 adds `executorTerminal` and `executorMissed`. H3 is the stop for Reaping only,
with its intent and the hold. H4 classifies agentd's entries and leaves out the
subtrees above. H5 adds `expiryHoldRemovalAllowed`. H7 adds the archive guard
after its parity check. Follow-up F3 records the stop at the suspended deadline
as not planned.
