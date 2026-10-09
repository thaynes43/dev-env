# 11: shared projects, workspace freshness and remote pods

**Status:** R1–R7 and ADR-002 accepted; two feasibility attempts and the subsequent
[cost diagnostic](../../../../docs/trials/2026-10-09-cephfs-cost-result.md) are
incomplete and cleaned up. Shared Git/storage acceptance is unproved.
[Issue #130](https://github.com/thaynes43/dev-env/issues/130) holds the slow peer
Git check, evidence and next bounded diagnostic proposal. Workspace/client
implementation and normal rollout remain gated. The separate v2 fresh-start bug fix shipped in agent
2.9.1 (#124/#123, haynes-ops #3636); it does not implement this project contract.
The [Accepted ADR-002](../adrs/002-shared-project-workspaces.md) draws the concrete
topology and storage choice, with trial and normal-rollout gates.
**Depends on:** plan 04 revision for Codex remote hosts/refresh; ratified ADR-002
superseding ADR-001's Storage/C-09 decisions and explicit D-15/D-22 revision for
shared Git/task files. R2 supersedes Q-20; there is no
remaining app-choice gate.
**Delivery gate:** prove the supported user journeys before advertising them.
No v1 or active session restart is authorized here.

## Goal and authoritative requirements

The [owner requirement](../requirements/2026-10-09-project-roots.md) defines one
project concept for both Claude Code and Codex. Stable roots live outside the
swept task directory. Multiple Codex remote links span pods and reach the same
workspace. Keep one task writer and one rotating refresh owner; common files do
not require common agent runtime/auth state.

| Requirement | Outcome |
|---|---|
| R1 | GitOps catalog declares project/repo/default/rules; boot/sync makes missing roots; removal reports rather than deletes; add is one user operation |
| R2 | Both agents open the same project/repo map; declared Codex trust and Claude multi-repo scope; launcher accepts `--project` |
| R3 | One project-rule source renders to both providers |
| R4 | Project rules reach task worktrees alongside repository rules; actual session loading is tested |
| R5 | Roots persist and refresh cleanly at boot/daily; task sweep stays under `~/work` |
| R6 | Canonical health is checked; automatic repair requires proof that nothing is lost |
| R7 | Multiple independently addressable Codex pod links share project/workspace files |

Read the [workflow guide](../../../../docs/workflow-guide.md#start-here-how-you-would-use-it)
for the owner journeys and rendered diagrams. D-74 recorded the target change;
Q-21/D-75 now accept ADR-002 and the bounded existing-CephFS trial. The primary
management UI and credential migration remain unselected.

## Verified baseline, 2026-10-09

- Manual detached anchors exist under `/home/dev/codex`, including the three-repo
  sigo-alumni project. Canonical common Git directories are under `/home/dev/repos`;
  task worktrees are under `/home/dev/work`.
- These paths and the one Codex daemon state currently share v1's ext4 RWO home
  PVC. Sharing only anchor checkouts would omit linked Git dependencies.
- Haynes-ops #3633 is open/unmerged; its mounted instructions are not a catalog,
  sync command, trust renderer or health checker. Mounted-resource changes follow
  the existing held-draft/natural-break rule.
- Current v1 and v2 launchers take `--repo`, not `--project`; the built API accepts
  `repo` and rejects unknown `project` fields. Codex v2 creation remains disabled.
- Project rules are not bound to both agents or propagated to flat task worktrees;
  these anchors have no generated Codex trust entries.
- The sweeper deletes eligible directories only under `~/work`; it also runs
  repository-wide `git worktree prune`. Protecting root directories alone does
  not protect registration if an anchor is temporarily missing/unmounted.
- Local `/proc`/PID checks cannot prove another pod's session is dead. A clean
  anchor can have active cwd users; cleanliness is not an ownership signal.
- The supplied stale-index symptoms were historical, not reproduced in the two
  named repos at this audit. Behind/detached/index failures remain required tests.
  Comparisons using cached origin refs are not fresh source evidence.

## Design to complete before implementation

1. Draw and review project open, task start, second remote host, resume/transfer,
   phone question, status/link discovery, finish/suspend and maintenance journeys.
   Keep operator API/controller distinct from a coordinator agent role. D-37's
   console is planned; no additional requester-agent service exists today.
2. **D-79 contract defined; implementation pending:** see the
   [catalog and task-rule contract](../../../../docs/shared-project-catalog.md).
   Implement catalog reconciliation and `project add` through the normal GitOps
   branch/PR/Flux workflow, with one authoritative list and no undeclared-root
   deletion. Catalog updates must not restart active hosts to reload declarations.
3. **Decision complete: Accepted ADR-002** supersedes ADR-001's affected Storage/
   C-09 and D-15/D-22 assumptions. Preserve both accepted records. Deliver shared
   project/reference/task mounts with identical absolute Git paths in every pod,
   private provider homes, enrollment/socket state and appropriate cache isolation.
   The bounded storage trial is a feasibility check; normal rollout still needs
   acceptance. Today's per-session RWO homes are not the shared workspace.
4. Implement D-80's [two persistent coordinator hosts](../../../../docs/codex-coordinator-hosts.md),
   each with private enrollment surviving idle replacement and read-only shared
   files. Use a dedicated child-scoped API identity to request managed Claude/
   Codex executors. Native app threads remain host-local; S-4 forwarding is later
   work. Preserve one keeper refresh owner and prove the pinned Linux CLI route.
5. Bind both providers to each project: generated Codex trust, Claude repo scope,
   one rule source, and a reliable task rule propagation mechanism. R4's nested
   `.work` option conflicts with R5's explicit sweep boundary; prefer a project
   pointer/composed provider documents or verified launch injection with flat
   `~/work` tasks. Nested directory ancestry alone cannot prove Git-root rules
   loading. Preserve repository instructions and record the project-rule revision.
6. Serialize reference mutations by common Git directory across pods: sync,
   initialization, fetch/ref resolution, anchor update, worktree creation and prune.
   Fetch successfully and pin the base SHA before a new implementation prompt.
   Protect managed anchor registration during missing mounts; test cross-node
   locking rather than assuming local `flock`/PID behavior is sufficient.
7. Define task writer ownership/fencing and explicit host handoff. Existing
   worktree/index/WIP and conversation identity are preserved on resume. Sharing
   files does not automatically share conversations or authorize simultaneous
   edits. All app/remote/CLI starts use preflight or remain coordinator-only.
8. Implement boot/daily canonical health and conservative R6 repair: fresh pinned
   target; canonical identity; no conflicts/in-progress operations; index matches
   a commit reachable from fetched remote history; files match index; no untracked
   files at all, including ignored files; local-only HEAD/default commits and
   other worktrees' branches remain recoverable. Recheck under ownership/lock and
   retain a repair receipt. Unsafe states report and preserve, not broad hard reset.
9. Implement root refresh/removal reporting, task-only rescue/sweep and protection
   of live sessions on other pods. Before integration, fetch the target and inspect
   the actual diff; relevant review/CI belongs to the current PR head.
10. Verify native owner-question delivery and answer round-trip on each phone path.
    A question recorded in DESIGN or a status stream is not delivery. Ask one
    verified, concrete decision at a time; keep privileged approvals under Q-16.
11. Update quick start and HANDOFF with actual acceptance/deployment evidence.
    V1 mounted-resource changes remain held drafts because they bounce the pod.

## Acceptance

### Source delivery units

D-77 divides the implementation while runtime acceptance remains gated:

1. **Source merged #135 (`316c03c`):** disabled shared mounts/identity, common Git locks, durable writer records and
   owned-task rescue. Old private sessions retain their behavior. This initial
   core refuses post-launch shared rescue until the next unit supplies stop proof.
2. **Source merged #140 (`7b3ce4c`):** shared-only supervisor stop/preserve and a distinct bounded hold-rescue pod,
   with controller proof of the exact old executor's genuine termination before
   any cleanup. Missing/deleted/partitioned owners remain refused.
3. D-81's [retained-private-home receipt and detach](../../../../docs/shared-private-home-retention.md)
   after verified rescue and both-pod absence, before Session finalization. This
   keeps private provider state without enabling automatic home destruction.
4. Catalog/API/CLI, fresh source and actual both-provider project/repo rule loading.
5. **Auth source merged #138 (`bcb2599`), runtime off:** keeper-owned fresh Codex
   login/refresh/reload, D-80's scoped coordinator class,
   managed Codex launch/resume and two retained remote hosts;
   explicit transfer follows the proven stop contract.
6. Complete storage, client, phone, lifecycle and cleanup acceptance for the
   [owner test milestone](../../../handoffs/2026-10-09-testable-v2.md).

The template/session feature stays off until its relevant source, deployment and
acceptance gates pass. Source units do not close the checks below by themselves.

- [x] ADR-002 explicitly supersedes ADR-001's affected storage/cloning decisions;
      Tom ratified it through structured Q-21 before workspace implementation.
- [ ] Empty-PVC boot produces every declared project with the correct repositories;
      sync is idempotent; undeclared roots are reported and never deleted.
- [ ] `project add` declares and materializes through GitOps as one user workflow;
      catalog updates preserve all active hosts/sessions.
- [ ] Claude and Codex open the same multi-repo project, see all intended repos,
      trust/scope settings, and identical project rules.
- [ ] Both real agents load project plus repo rules from a task worktree; prove
      actual instruction loading, not only file presence. Flat sweep paths remain.
- [ ] A root idle thirty days survives task cleanup and refreshes to the declared
      default when safe; a dirty root is reported and left intact.
- [ ] Canonical wrong branch, detached HEAD, stale index, dirty files and behind
      states are reported. Safe fixtures repair; local-only commits, ignored-file
      collisions, conflicts and active owners are refused/preserved.
- [ ] Two simultaneous Codex remote links in different pods reach the same roots,
      common Git metadata and task files, with distinct enrollment/daemon state.
- [ ] Keeper refresh/reload and one-host replacement preserve both links without
      competing refresh calls or live credential copying.
- [ ] Cross-pod concurrent sync/task starts serialize by Git common directory and
      record immutable start SHAs, fetch times, rules revisions and task owners.
- [ ] Failed fresh fetch launches no task; explicit historical restore stays
      distinguishable from fresh source. Offline resume preserves WIP/Git state.
- [ ] Task transfer fences the previous writer; peer cleanup never reaps live work;
      missing shared mounts cannot prune permanent anchor registration.
- [ ] App/remote/CLI/delegation starts each use verified preflight or a documented
      coordinator-only route; cached primary project HEAD cannot select new source.
- [ ] Native phone decision prompt and answer round-trip works for the supported
      Claude/Codex paths; logs/doc questions do not satisfy this check.
- [ ] Current-source integration, rescue/restore, cleanup and no-active-restart
      behavior pass before this workflow or v2 cutover is called complete.

## Checks and sources

No CPU burners, stress tools, busy loops or wide/looped tests on shared nodes.
Use bounded Git/fake-provider fixtures. Go checks use `nice -n 19`, GOMAXPROCS=2,
`-p 2`, one suite at a time, at most two compiling agents. Every cluster fixture
container, including injected init containers, must have CPU limits.

- [Owner requirements R1–R7](../requirements/2026-10-09-project-roots.md)
- [Workflow guide](../../../../docs/workflow-guide.md)
- [DESIGN-001 6.3/6.6, D-74](../designs/001-dev-env-v2.md#63-codex)
- [Plan 04](04-rolling-updates-codex.md)
- V1 GitOps launcher: haynes-ops `kubernetes/main/apps/dev/dev-env/app/resources/agent-run.sh`
- V2: `internal/agentd/clone.go`, `internal/agentd/render.go`,
  `internal/apiserver/apiv1/types.go`, `internal/apiserver/validate.go`
- [Official instruction discovery](https://learn.chatgpt.com/docs/agent-configuration/agents-md),
  [project behavior](https://learn.chatgpt.com/docs/projects),
  [remote connections](https://learn.chatgpt.com/docs/remote-connections)
