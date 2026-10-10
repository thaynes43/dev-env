# Plan 11: common rules, session coordination and fresh per-pod repositories

**Status:** scope corrected by the owner on 2026-10-10, America/New_York;
implementation and real-client acceptance remain open.
**Governing decision:** [ADR-003](../adrs/003-session-coordination-private-repositories.md).
**Requirements:** [owner's five outcomes](../requirements/2026-10-10-owner-scope.md).

This plan replaces the mandatory shared-Git/RWX plan. Reuse useful catalog,
rule, freshness and retention code; do not enable old workspace opt-ins merely
to obtain those capabilities. Prior storage trials and their stopped routes
remain historical. No new backend selection or storage benchmark is a prerequisite.

## Goal

Claude and Codex agents on different pods load common rules, discover each
other's work, communicate, avoid duplicate task claims and read authorized live
or stopped-session context. Each implements in its own fresh task worktree.

## Reusable source and gaps

- Private clone/worktree preparation already fetches and pins a base commit.
  Full catalog preparation at each pod's startup is not delivered.
- Project catalog and provider rule composition are built, but their current
  admission/preparation depends on the old shared workspace identity. Decouple it.
- CLI/API list, show, live TUI messages and log tails exist. These do not provide
  a complete native-thread directory, durable offline messages or stopped-history
  reading.
- Claude shared memory support exists. Provider transcript indexing/export and
  authorized peer-context access remain missing.
- Normal private-session reap deletes the home after Git-only rescue. The newer
  retained-home receipt is gated on shared tasks. Preserve provider history
  independently of repository topology before advertising archive/history parity.

The [coordination contract](../../../../docs/session-coordination.md) records
exact source evidence and the new read/mutation boundaries.

## Delivery units

1. **Per-pod catalog and rules.** Clone missing declared repos, refresh clean
   reference views, materialize permanent local project roots and compose both
   providers' instructions in task worktrees. Preserve dirty/undeclared roots.
   Record catalog/rule revisions; apply updates without rolling busy sessions.
2. **Session identity and retained history.** Index managed sessions and provider
   conversations with actual execution state. Preserve transcript/memory/handoff
   locators when the executor stops and when a session is archived. No home
   deletion until the required retained context is verified.
3. **Discovery and context reads.** Provide authorized fleet-wide metadata and
   bounded history/memory reads. Reading a stopped session must work without a
   Ready source pod or new provider/model turn. Do not export credentials or
   enrollment state. Keep mutation rights separate from peer read rights.
4. **Task claims and durable messages.** Atomically claim an explicit logical
   task; expose its repo/issue/branch, owner, current activity and related sessions.
   Attribute messages and preserve an offline inbox and acknowledgements. Permit
   assistance/delegation without creating a second task owner.
5. **Handoff and recovery.** Verify the old executor stopped before ownership
   transfer. Restore commits or rescued WIP into the recipient's local worktree
   and give it the retained transcript/handoff context. Preserve uncertainty;
   a stale heartbeat is not stop proof. Exact native conversation import is a
   separate supported-provider test.
6. **Owner workflow.** Demonstrate both providers on distinct limited pods with
   current source/rules, peer discovery/message, stopped-history read, controlled
   transfer and preservation through operator upgrade. Publish actual commands
   and limits before claiming the pilot usable.

## Acceptance

- Two pods have independent Git administrative directories and task worktrees.
  Each new task records a successful fetch and exact immutable base commit.
- Both providers load the same declared global/project rules plus native repo
  instructions at project root and task path; file presence alone is not proof.
- A second agent sees an existing claim and assists or takes different work.
  Concurrent claims for the same explicit task return the existing owner.
- A message to a stopped session is durably recorded and acknowledged on a
  supported later delivery/read; it is not silently injected as a duplicate prompt.
- An authorized peer reads a stopped session's transcript, memory and handoff
  without resuming it. Unrelated or privileged state is refused.
- Archive retains those artifacts and a discoverable session record. Git-only
  rescue and a deleted PVC are insufficient evidence.
- Handoff preserves WIP and budget history, proves old-writer stop and starts a
  fresh recipient worktree with explicit source/rescue provenance.
- Operator updates preserve active pod identities. Agent replacement occurs at
  a safe boundary with retained state, not as a consequence of rule refresh.
- Missing provider usage/cost remains Unknown. The 60-minute stall/three-failure
  default carries across pods and children.

Each implementation unit gets an outcome, deadline, checkpoint and proof. No
CPU burners, load tools or wide test loops; bounded tests under `nice -n 19`.
Every admitted cluster container, including init containers, needs resource limits.

## Historical code and migration

Shared workspace/stop/private-home source (#135/#140/#147), catalog and sync
(#145/#150/#153), auth/scoped tasks (#138/#143/#148/#152), child decisions (#155)
and budget/native source (#162/#163/#166/#167/#168/#170) are inventory, not proof
of this corrected workflow. Keep auth, ownership and preservation controls;
adapt useful pieces and retire unneeded gates through reviewed changes.

V1 remains available. This plan authorizes no v1 bounce, cutover, credential
copying or storage retry. Eventual frontend and local LLM execution remain
visible in the owner outcome map, not prerequisites for every private Git task.

## Durable implementation tracking

### First #173 slice: private declared-project tasks

[DESIGN-005](../designs/005-private-project-tasks.md) defines the first bounded
source contract. Project admission supplies a catalog snapshot without shared
storage. Selected-repo preparation validates identity, fetches the declared
remote branch and pins a private task worktree; resume preserves WIP and saved
rules. New budget-bound private dispatch remains refused until its separate
budget integration is delivered. The legacy shared paths stay guarded.
The separate private-project admission flag defaults off; source promotion
preserves the existing fixture/coordinator/budget flag contract.

Source checks and image/GitOps delivery have separate receipts on #173. Actual
provider loading is still unaccepted. The next finite source slice is local
catalog-wide startup bootstrap/refresh, preserving dirty and ambiguous roots.
Permanent project roots, remaining clients and both-provider/two-pod acceptance
remain on the parent issue. No retired storage route is reopened.

### Parent issues

- [#173](https://github.com/thaynes43/dev-env/issues/173): independent catalog/rules and local repo startup.
- [#174](https://github.com/thaynes43/dev-env/issues/174): retained session directory and authorized live/stopped context.
- [#175](https://github.com/thaynes43/dev-env/issues/175): cross-parent task claims and durable messages.
- [#176](https://github.com/thaynes43/dev-env/issues/176): cost Unknown/provenance and model attribution.
- [#154](https://github.com/thaynes43/dev-env/issues/154) and [#160](https://github.com/thaynes43/dev-env/issues/160): task budgets and interrupted native recovery.

[Issue #91](https://github.com/thaynes43/dev-env/issues/91) remains the resume index.
Each parent issue must be delivered through finite slices with its own checkpoint;
its size does not authorize an unbounded investigation.
