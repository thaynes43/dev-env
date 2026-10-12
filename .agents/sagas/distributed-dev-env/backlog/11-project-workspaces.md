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

Claude, Codex and future local-model agents on different pods load common rules, discover each
other's work, communicate, avoid duplicate task claims and read authorized live
or stopped-session context. Each implements in its own fresh task worktree.

## Reusable source and gaps

- Private clone/worktree preparation fetches and pins a base commit. Trusted
  catalog bootstrap covers managed-daemon startup and explicit private refresh.
  Owned private roots are released; installed model-free checks cover one public
  repository. Private native preflight is released, with API/profile/native
  instruction loading still unaccepted.
- Private declared-project admission/preparation and provider rule composition
  are built independently of shared storage. Admission remains disabled and
  actual provider rule loading remains unaccepted.
- CLI/API list, show, live TUI messages and log tails exist. These do not provide
  a complete native-thread directory, durable offline messages or stopped-history
  reading.
- Claude shared memory support exists. Provider transcript indexing/export and
  authorized peer-context access remain missing.
- The independent private-history retention guard is installed. It retains an
  original Session/HOME/executor identity across uncertainty and upgrades;
  pre-guard operator downgrade is not established safe once latches exist.
  Actual history export/directory/stopped reads remain unaccepted. Explicit
  published-context source is under review and does not prove native history.

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
*Update, 2026-10-12: the Codex original's result returned both rule markers; see
the [Codex original result record](../verification/codex-original-result-2026-10-12.md).
Claude loading remains unproven.*

### Next #173 slice: private catalog startup and refresh

[DESIGN-006](../designs/006-private-catalog-bootstrap.md) defines catalog-wide
private bootstrap and safe refresh through a trusted named catalog read. Startup
and explicit refresh record revisions/outcomes and preserve unsafe reference
state. Neither path replaces selected-task fetch/pinning or resume provenance.
Promotion changes new-session settings only and retains the private-task gate.
Real-provider loading, project roots and other clients remain separate acceptance.

### Private permanent roots

[DESIGN-007](../designs/007-private-project-roots.md) adds default-off managed
Git-backed project roots and detached private repository anchors. Ownership,
index/files, ignored entries, local commits, operation state and catalog/source
revisions must be proved before safe fast-forward. Existing tasks keep their
saved rules and WIP. Native-host conversion and actual provider loading remain
separate acceptance; the pending Q-24 routing choice is not silently decided.

### Private native startup guard

[DESIGN-009](../designs/009-private-native-codex-preflight.md) adds default-off
trusted private-root/configuration preparation. Durable reservation, repeated
absence/authority checks and original input identity prevent a live or uncertain
host from being refreshed or replaced. Native review and focused tests passed;
private API/profile and actual native instruction loading remain separate.
The deployed model-free roots check failed before verifier acceptance; preserve
attempt 1 and its Unknown internal cause. Offline umask mismatch proof supports
a separate small source correction and fresh named check, without a retry here.

### First explicit claims source unit

[DESIGN-010](../designs/010-atomic-task-claims.md) adds immutable full-identity
TaskClaim ownership in a separate protected coordination group. Atomic Create
and uncached confirmation preserve one original owner across parents, retries
and stopped/deleting/absent/replaced Session observations. A separate project
ACL exposes bounded metadata without inheriting broad legacy mutation rights.
Focused/schema/lint/codegen checks and native review passed. Protected schema,
namespaced operator get/create/list Role and signed API promotion are delivery
gates; installed contention, inbox/acknowledgements and budget/handoff linkage
remain separate. Initial fetch pin and executor stop are Unknown.

### Parent issues

[DESIGN-012](../designs/012-owned-root-wrapper-permissions.md) corrects generated
wrapper modes independently of a private umask, only after ownership/authority
proof. Focused checks and native review passed; the first installed roots failure
is retained with Unknown internal cause. A signed image and new reviewed named
check are required; source checks are not operational retry or roots acceptance.

The first #174 source guard is defined by
[DESIGN-008](../designs/008-private-provider-history-retention.md): default-off
private HOME/Session retention, immutable provenance and a one-way executor
attempt. Retention survives flag disable; missing executors remain Unknown.
Source review/tests do not prove installed GC/storage behavior. Deploy the
operator/schema, then deliver separate provider-context adapters and scoped
stopped reads. No shared-Git TTL or broad legacy peer mutation right is adopted.

- [#173](https://github.com/thaynes43/dev-env/issues/173): independent catalog/rules and local repo startup.
- [#174](https://github.com/thaynes43/dev-env/issues/174): retained session directory and authorized live/stopped context.
- [#175](https://github.com/thaynes43/dev-env/issues/175): cross-parent task claims and durable messages.
- [#176](https://github.com/thaynes43/dev-env/issues/176): cost Unknown/provenance and model attribution.
- [#154](https://github.com/thaynes43/dev-env/issues/154) and [#160](https://github.com/thaynes43/dev-env/issues/160): task budgets and interrupted native recovery.

[Issue #91](https://github.com/thaynes43/dev-env/issues/91) remains the resume index.
Each parent issue must be delivered through finite slices with its own checkpoint;
its size does not authorize an unbounded investigation.
