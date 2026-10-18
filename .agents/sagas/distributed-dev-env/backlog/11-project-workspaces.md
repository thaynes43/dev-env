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

## Central cross-repository work requirement

The [2026-10-16 owner requirement](../requirements/2026-10-16-central-work-coordination.md)
adds canonical OpenProject Saga/Epic/Story discovery, claims, history and linked repo
execution artifacts across harnesses. Agents search and update existing central work;
Tom refines and green-lights it centrally. Agent CRUD is required. Approval statuses
are coordination metadata; a technical status firewall is not an acceptance gate.
This is a recorded requirement, not an enabled integration or a change to Chapter 7.

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
*Update, Q-28, 2026-10-12: Claude canary 1012 returned both markers too; see the
[Claude canary 1012 record](../verification/claude-canary-1012-2026-10-12.md).
Genuine private GitHub authorization and worktrees, and a fresh task pin, remain
open for #173, with the parent items above.*
*Update, Q-32, 2026-10-13 UTC: the Sigo rerun canary
`rule-canary-claude-sigo-1012b` (after the 2.25.0 deploy) holds all seven
DESIGN-011 items. Root acceptance `a3aa0b7d` accepts private GitHub
authorization and a fresh pinned task worktree on a private repository: the
task fetch ran after Create, equals the pre-Create tip and is recorded as the
task source (Claude only). The Q-30 run on 2026-10-12 had shown both but failed
bootstrap. See the [Sigo rerun record](../verification/sigo-rerun-2026-10-13.md).
Still open for #173: two bounded pods compared for independent Git directories
and worktrees, a failed fetch blocking fresh work, and preserved dirty, WIP and
reference state in an installed run.*

### Next #173 slice: private catalog startup and refresh

[DESIGN-006](../designs/006-private-catalog-bootstrap.md) defines catalog-wide
private bootstrap and safe refresh through a trusted named catalog read. Startup
and explicit refresh record revisions/outcomes and preserve unsafe reference
state. Neither path replaces selected-task fetch/pinning or resume provenance.
Promotion changes new-session settings only and retains the private-task gate.
Real-provider loading, project roots and other clients remain separate acceptance.
*Update, Q-32, 2026-10-13 UTC: installed startup bootstrap reached `Ready` on a
private multi-repository catalog project (`sigo-alumni`: two private
repositories, plus `dev-env`) in a new HOME. The boot step and
`status.agent.projectBootstrap` (#239) both report every repository and project
root `Ready`, with no reason code; under 2.24.1 the same catalog ended
`Partial`. That is consistent with #237's fix, but neither run recorded the tip
of `sigoalumni-org`, so no receipt shows it still tracks the symlink. With
everything `Ready`, no non-`Ready` #239 reason code was exercised. Missing-repo
bootstrap in an existing HOME, and
preservation of dirty or ambiguous reference state in an installed run, are not
shown yet. See the [Sigo rerun record](../verification/sigo-rerun-2026-10-13.md).*

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

### Coordination record retention

Parked 2026-10-12 from the review of PR #223 (#175). **Ruled the same day
(Q-29).**

TaskClaims, published contexts and inbox messages are immutable and were never
deleted. Their per-Session and per-project bounds
([DESIGN-010](../designs/010-atomic-task-claims.md) "At the bound, recovery and
rollback") were therefore lifetime bounds. A project that reaches one refuses
every new record of that kind with 429 `limit_exceeded`, including managed
canary output capture. The defaults are, per Session and per project:
claims 512 and 4096, publications 128 and 1024, messages 512 and 4096. The
counts are exported as `dev_env_coordination_records*` metrics. Until the units
below are enabled, the only recovery is a manual kubectl delete by Tom, after an
export, with the preservation duty in DESIGN-010.

**Ruling, 2026-10-12 (Q-29).** Tom answered through the Claude native prompt:
"Archive, then delete (Recommended)". This replaces the three open decisions
parked here (which history survives; archive and who deletes; how bounds count):

1. History survives in an archive. A record is released 30 days after the
   Sessions it depends on are first proved absent: a claim's owner, a
   publication's source, a message's sender plus its recipient's ack or absence.
2. It is archived first (full object and SHA-256 manifest, on a new RWO volume
   that only the shelf pod mounts, kept indefinitely). Only a leader-only
   operator runnable deletes, with UID and resourceVersion preconditions, after
   it verifies the archive. Reads fall back to the archive.
3. Per-Session bounds stay lifetime bounds; project bounds count live records.
   The defaults stay. An alert fires at 80%.

[ADR-004](../adrs/004-coordination-record-retention.md) records the decision,
including its one amendment of DESIGN-010 (claim release in the reap case only).
[DESIGN-024](../designs/024-coordination-record-retention.md) is the contract.
Everything is default off until R6.

**Delivery units, each its own reviewed PR, in this order:**

| Unit | Where | What | Done when |
|---|---|---|---|
| R0 | dev-env docs | ADR-004, DESIGN-024 and these amendments | merged |
| R1 | haynes-ops | `DevEnvCoordinationRecordsNearLimit` (80%, existing metrics only) | merged and the rule loaded; can go any time |
| R2 | dev-env source | `agentd ctl coord-archive put\|mark\|lookup\|get\|list`, the size bound, the time-ordered indexes, the archive mount check, `agentd shelf --coordination-archive`, protocol types, `TestArchiveNeverRemoves`, a measurement of a worst-case `get`'s peak memory under the shelf's limits | merged and in a released agent image |
| R3 | dev-env source | `internal/coordretention` runnable (default off), shelf client methods and the per-replica exec limit, the bulk lookups and resume key, retention metrics, the shared guard scanner, `TestOnlyRetentionDeletesCoordinationRecords`, envtest E1, E2, E4, E6, E7, and the `internal/shelf` package comment (the shelf no longer mounts only the shared volume) | merged 2026-10-17 as `be6581fc`; not accepted or installed. *2026-10-17:* E6's replaced-owner decision is `TestEnvtestReplacedOwnerReleases` on a real API server. The delete after grace stays in the fake-client tests. E7's second pass is `TestSecondPassDoesNotDeleteTheSameRevision` on the fake client: it does not delete or archive the revision again. A concurrent pair of passes remains. E4's delete right is `TestEnvtestRetentionDeleteRight` on a real API server. The identity-guard exec refusal remains. E2's altered-bytes refusal is `TestArchiveMismatchLeavesTheRecord` on the fake client: the record stays and the stored bytes are not overwritten. E2's missing verify read is `TestArchiveMissingLeavesTheRecord` on the fake client: the record stays and the runnable writes no replacement. The reset, the new grace, and the E2 envtest remain. `TestShelfUnavailableDoesNotDelete` counts archive calls: a missing shelf pod makes none, and `no_archive` makes one probe lookup and no put or mark. Archived reads answering 503 remain. E2's oversize refusal is `TestOversizeRecordIsNotMarked` on the fake client: the record stays and is not marked. The E2 envtest remains. E1's one-claim archive-before-delete is `TestEnvtestArchiveBeforeDelete` on a real API server and the real coordination archive. The stored bytes equal the canonical claim read before the delete. `TestEnvtestArchiveBeforeDeleteContext` archives one published context whose source session is absent, then deletes it after the grace. `TestEnvtestArchiveBeforeDeleteMessage` archives one inbox message whose sender and recipient are both absent, then deletes it after the grace. `TestEnvtestArchiveBeforeDeleteAck` archives a message and its acknowledgement, then deletes the message before the ack. A process exec and the archived-read API remain. Put, mark, and lookup cross the ctl JSON boundary. E7's two passes at once are `TestEnvtestTwoPassesDeleteOnce` on a real API server and the real coordination archive: exactly one delete succeeds and the archive keeps one revision. |
| R4 | dev-env source | read fallback (default off), the archived item shape and `archived=only` lists, the ack-retry fallback, client tolerance tests, bound wording, the templates check that refuses the archive claim, envtest E3 and E5 | merged and in a released operator image. *2026-10-17:* E5's bound effect is in `TestEnvtestBoundsAndSameWorkUnderOperatorRole`: after one live claim is deleted, the remaining owner is refused while the project is under its bound, and another owner can then create. The operator role still cannot delete. E3's message-before-ack delete order is in `TestInboxReleaseDeletesTheMessageBeforeTheAck`. An ack whose delete conflicts is removed on the next pass by `TestInboxAckDeletesOnTheNextPassAfterAConflict`. `TestEnvtestInboxRule` runs the keep decisions on a real API server. Delete order remains the fake-client tests. The operator image pin remains. |
| R5 | haynes-ops | the archive PVC, shelf `Recreate` strategy, mount, flag, image pin, header comment and (if R2 needs it) memory limit; the archive guard (ADR-007/Q-35, preserving v1 after its parity check, unless H7 shipped it); `DevEnvShelfUnavailable` and `DevEnvCoordinationArchiveFilling`; then Tom sets the bound PV to `Retain` | the shelf Ready with the archive mounted, and its PV `Retain` |
| R6 | haynes-ops | Role `dev-env-coordination-retention` (delete, operator only), the operator pin, then reads on, then retention on, and `DevEnvCoordinationRetentionStalled`. The shelf must already run an agent image that returns the lookup `latestState` and `latestAt` fields; an older shelf refuses every release as `archive_outdated` | a complete retention pass on the installed operator |
| R7 | dev-env docs | installed verification record (DESIGN-024 section 11) | merged |

**Follow-up F1, needs an owner question: HOME expiry.** With the installed
`--enable-private-history-retention=true`, every Session without a shared
workspace is retained and never absent, so retention releases almost nothing
today (ADR-004 "Known gap"). DESIGN-008 already requires an explicit policy for
HOME expiry. That policy, when Tom rules on it, is what lets retention release
these records. Ask it as its own question, with its own premise checked; it is
not decided here.
*Decided 2026-10-12 (Q-31): see "Private HOME expiry" below.*

### Private HOME expiry

Follow-up F1 of the retention section above. **Ruled 2026-10-12 (Q-31).** Tom
answered through the Claude native prompt: "Archive, then delete
(Recommended)".

1. A private Session (no shared workspace, not a canary) that is Reaping, with
   no owner hold, is stopped. One Suspended past its archive deadline is never
   stopped; it expires only if its executor ended on its own (ADR-006). The
   executor's termination is recorded in the Session's status before the Pod can
   vanish; a retained Pod is not a durable proof, and absence alone never proves
   stop.
2. 30 days after that record, a Git rescue runs in a hold pod bound to the HOME's
   UID, the provider history (an allowlist, never credentials) and the Git bundle
   go to an archive volume on the shelf with a `Retain` PV, kept indefinitely and
   guarded from H7 against untrusted v2 actors; v1 retains its existing
   administrator access under ADR-007/Q-35. Archived history is readable
   under D-92's [trusted shared-history requirement](../requirements/2026-10-13-shared-agent-history.md).
   The history-only source correction includes all existing trusted callers
   and projectless history; installed retrieval remains to be accepted.
   Denied reads between Tom's trusted agents are not a gate.
3. Only then, with deletion enabled, the operator's leader deletes the HOME
   through the guarded path and releases the Session. ADR-004 then releases its
   records. Canary originals never expire. Tom alone may hold an expiry or
   release it now (which skips only the grace).

[ADR-005](../adrs/005-private-home-expiry.md) records the decision, and
[ADR-006](../adrs/006-private-home-expiry-corrections.md) (D-87) corrects it after
its review. [ADR-007](../adrs/007-preserve-v1-archive-trust-boundary.md)
(Q-35/D-89, relayed) preserves v1 unchanged and supersedes only the proposed
v1 archive narrowing. [DESIGN-025](../designs/025-private-home-expiry.md) is the contract. Everything
is default off; deletion waits for the installed archived-history read (#174).

**Delivery units, each its own reviewed PR, in this order:**

| Unit | Where | What | Done when |
|---|---|---|---|
| H0 | dev-env docs | ADR-005, DESIGN-025 and the amendments; ADR-006's corrections | merged |
| H1 | dev-env source | the terminal record (`executorTerminal` and `executorMissed` beside `lastExecutor`, CRD rules, the shared verifier refactored, the confirmed write, `ExecutorEnded`, metrics, flag). In source in #236 (DESIGN-025 3.6), released in 2.25.0 | merged and in a released operator image |
| H2 | haynes-ops | CRD copy, node and lease `get` for the operator, operator pin, record on | records appear for ended private executors; ship it early, each day without it can lose a proof. Done 2026-10-12 (haynes-ops #3852, #3854, 2.25.0): two records and two missed markers stored |
| H3 | dev-env source | the stop request for Reaping Sessions only (`stopRequest` intent, hold check, `activeDeadlineSeconds`) and its guard test. In source in #244 (DESIGN-025 4.1), default off. Installed in 2.26.0, off (haynes-ops #3878, 2026-10-13) | merged |
| H4 | dev-env source | agentd `history-export` and `history-archive` commands, the layout tables (agentd by name, copies of user files excluded) and fixtures, guard tests, the resource measurement. In source in #246 (DESIGN-025 7.1); the in-cluster measurement moves to H7. Released in agent 2.26.0 and 2.27.0; the shelf still runs 2.9.1 until H7 | merged and in a released agent image |
| H5 | dev-env source | the expiry record and steps up to `Archived`, the private hold pod and `expiryHoldRemovalAllowed`, the relay, overrides, metrics. In source in #251 (DESIGN-025 5.4), default off (`--enable-home-history-archive`); deletes no HOME. The reference copy of `dev-env-home-expiry-guard` for H7 is in `internal/controller/testdata/haynes-ops/`. Installed in 2.27.0, off (haynes-ops #3885, #3886, 2026-10-13) | merged with required CI and independent review |
| H6 | dev-env source | the `/v1/history` reads and `agent-run history`, the delete and release steps (off), `TestExpiryDeleteNeedsVerify`. In source in #254 (DESIGN-025 6.7 and 8.1): the reads behind `--enable-history-archive-reads`, the delete and release behind `--enable-home-expiry-delete`, which the operator refuses without the archive and the reads; both default off, and the delete stays off until H8. Installed in 2.27.0, both off (haynes-ops #3886, #3887, 2026-10-13) | merged and in a released operator image |
| H7 | haynes-ops | history archive PVC, shelf mount, override policy, the archive guard (ADR-007/Q-35, preserving v1 after its parity check, unless R5 shipped it), alerts; history-PV Retain verified under the one-time, consumed Q-37/D-91 delegation; stop, archive and reads on | a private Session reaches `Archived` with its HOME intact |
| H8 | dev-env docs | installed record of the archived read (DESIGN-025 section 15) | merged; this is the #174 read acceptance |
| H9 | haynes-ops | delete on, `DevEnvHomeExpiryStalled` | merged |
| H10 | dev-env docs | installed record of the first expiry and of the ADR-004 release 30 days later | merged |

**Follow-up F2, no owner question needed: never-started private Sessions.** A
private Session with `admission: NeverStarted`, no executor attempt and no HOME
is never released either: `releaseIfEmpty` refuses every private Session, and
DESIGN-025 needs a terminal record. Such a Session has nothing to archive. A
narrow release (uncached absence of Pod and HOME, a confirmed `NeverStarted`
admission, no attempt) would let ADR-004 release its records. It needs its own
design note and guard test; it is not part of Q-31.

**Follow-up F3, not planned: a stop at the suspended-archive deadline.** ADR-005
stopped a private executor at that deadline too; ADR-006 removed it, because the
idle timer suspends an interactive Session after 72 hours idle, so about ten
days alone would have killed a live Session for good with nobody asking. As a
result a suspended private Session whose executor still runs keeps its HOME
until it is reaped. Do not build that stop without a new owner question, asked
with that premise: it ends a live Session no human asked to end.

**Follow-up F4, needs evidence first: the rest of Claude Code's config names.**
Claude Code 2.1.296 lists 49 subdirectories of its config directory. H4
classified the ones a real HOME holds (DESIGN-025 6.3, H4's confirmation). The
other 34 have no class: `agent-memory`, `agent-memory-local`,
`agent-memory-project`, `api-dumps`, `chrome`, `cowork_plugins`, `debug`,
`downloads`, `dump-prompts`, `feedback-bundles`, `file-transfers`, `ide`,
`image-cache`, `local`, `local-settings`, `logs`, `mcp-discovery-cache`,
`mcp-skill-archives`, `plans`, `project-settings`, `remote`, `remote-control`,
`scratch`, `seed-admin`, `shares`, `startup-perf`, `statsig`, `storage-v2`,
`systemd`, `tasks`, `teams`, `todos`, `traces` and `usage-data`. A HOME that
holds one refuses its export (`unknown_entry`), and the expiry stops with
`DevEnvHomeExpiryNeedsOwner`. That is safe: the HOME stays. To classify a name,
look at a HOME that has it, names only, and read what the client writes there.
`plans`, `todos`, `tasks` and `agent-memory*` may be history; `downloads`,
`file-transfers`, `image-cache`, `scratch` and `shares` may hold copies of user
files (ADR-006). Each name goes in `claudeLayout`
(`internal/agentd/historyexport_layout.go`) with a fixture in
`historyexport_test.go`, in the change that classifies it. To find the list
again, search the pinned `claude` binary for the array that holds
`"dump-prompts"`. Do F4 before H7 turns the archive on: Claude Code may write
`remote-control/` once v2 Claude sessions run in remote mode (plan 03), and the
first HOME that holds it refuses until it has a class.

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

### Bounded pilot rendezvous correction — 2026-10-16

C7-PILOT-RENDEZVOUS-OFFLINE-1016 follows the closed PARTIAL two-actor pilot #353.
[DESIGN-027](../designs/027-pilot-rendezvous.md) removes the demonstrated 48-second
coordinator/reporting handoff from A's verified claim to B invocation and separates
startup/ACK/terminal deadlines. The small default-off existing-CLI harness and
opt-in create/log `--once` remain source/offline only; normal CLI defaults keep
their retry behavior. Exact path claim, original clocks and historical failures
are retained in the story orders. Independent Mac review and required CI gate
source acceptance; new actors/window/activation remain unauthorized. Follow the
[concrete acceptance procedure](../verification/pilot-rendezvous-2026-10-16.md).
OpenProject integration is a separate readiness task, not a new strict status gate.
