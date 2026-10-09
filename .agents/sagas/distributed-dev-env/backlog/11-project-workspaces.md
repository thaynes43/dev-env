# 11: project homes and repository freshness

**Status:** design/implementation backlog; v2 fresh-branch fetch failure fixed
in the workflow-guide change, broader client integration not built.
**Depends on:** Q-20's client clarification for registration; plan 04 for v2
Codex hub/executor integration. V1 protections can be prepared independently.
**Delivery gate:** verify the paths introduced here before treating the Codex
workflow as complete. This is not permission to restart v1 or active sessions.

## Goal

Give projects a stable entry under `/work/codex` in v1. Every implementation
task gets its own branch/worktree from verified source. A persistent reference
clone, an app-created worktree or an old chat must not silently supply stale
source for a new task.

Tom requested these folders, session management and stale-repository protection
on 2026-10-09 America/New_York. The audit found `/work` and `/work/codex` absent.
Current v1 launchers use `/home/dev/repos` and `/home/dev/work`; phone Codex chats
bypass their new-task setup. Q-20 clarifies the intended app registration.

## Contract to implement

- Stable, managed Git-backed project/coordinator homes under
  `/work/codex/<project>`, on persistent storage. Implementation stays in
  separate task worktrees. Preserve the existing reference/task locations until
  an explicit migration changes them.
- New task: verify repository identity, successfully fetch origin, resolve the
  selected remote base to a full commit SHA, and create `agent/<task>` from that
  SHA. Stop before agent launch if source cannot be verified. An explicitly
  selected historical feature/rescue base is recorded as such.
- Shared v1 references: one lock per Git common directory across initialization,
  fetch, base resolution and worktree registration. Cooperating launchers and
  project refreshers use the same lock; timeout errors are visible.
- Durable task provenance: repository, selected base ref, immutable start SHA,
  successful fetch time, task branch/worktree and chat/session owner. Store it
  outside tracked source and expose safe metadata in session inspection.
- Resume: verify the intended repo/common directory/branch and preserve all
  local work. Offline recovery is explicit. Never reset or rebase an active
  worktree automatically. Missing workspace and branch for a saved launch must
  require restoration rather than creating unrelated source for an old chat.
- Project homes: show revision/freshness and use current rules/handoff for new
  work. Refresh only a clean, quiescent home. New tasks do not rely on its cached
  HEAD. A folder symlink or background daily fetch is insufficient.
- Before integration: fetch the target, inspect divergence and the actual diff,
  deliberately resolve conflicts, then run affected validation on the current
  PR head. Preserve agents' existing self-merge workflow.

These are accidental-staleness protections. V1 agents share filesystem access
and can bypass a wrapper. Do not call that a security boundary. V2 private
session homes retain the accepted storage/isolation architecture (D-15/D-22).

## Steps

1. Resolve Q-20 and document the exact supported client entry points, including
   phone-created chats, local CLI, app-managed worktrees and native delegation.
2. Define persistent `/work/codex` mapping and project registration/ownership.
   Preserve existing chats and coordinator directories; do not copy auth state.
3. Add the shared reference transaction and provenance to the managed launcher.
   Verify remote URL/base identity before using any cached checkout.
4. Integrate every supported new-task client with the preflight. Where the
   pinned client lacks an enforceable launch seam, keep that entry coordinator-only
   and dispatch implementation through the verified launcher; instructions alone
   do not satisfy an enforced fresh-start claim.
5. Wire safe resume and current-source warnings without modifying active WIP.
6. Integrate the contract with plan 04's hub and session executor. Test the
   actual pinned CLI/client instead of assuming app worktree creation fetches.
7. Add current-target/diff checks before PR publication and integration.
8. Update the quick start, HANDOFF and runtime evidence. V1 pod configuration
   changes under haynes-ops `dev-env/app/resources/**` are held drafts because
   they bounce this pod; Tom merges at a natural break.

## Acceptance

- [ ] `/work/codex/<project>` survives a pod replacement and registration points
      to the intended persistent project home; existing task/chats are preserved.
- [ ] Two concurrent tasks for one project have separate branches/worktrees and
      a serialized reference transaction with immutable start SHAs.
- [ ] An old local main/project checkout cannot determine a new default-base task;
      the task starts at the successfully fetched remote tip.
- [ ] Failed fetch/auth/network on new-task setup launches no implementation
      prompt and leaves existing work intact.
- [ ] Default-branch changes and explicit historical bases are handled deliberately;
      the recorded base identity matches the commit used for worktree creation.
- [ ] Phone/app/CLI/native-delegation entry points each use preflight or are
      explicitly coordinator-only; bypass paths are not advertised as protected.
- [ ] Offline resume preserves staged, unstaged and untracked work; foreign
      repo/path or broken HEAD is refused without modification. Changed branch
      and detached/in-progress Git state warn and remain intact.
- [ ] A saved chat with both branch and worktree missing does not resume against
      a freshly manufactured branch. Verified rescue/restore remains usable.
- [ ] Main advancing after task creation produces a visible integration check;
      relevant CI/review evidence belongs to the current PR head.
- [ ] Current-source instructions are discoverable from each registered primary
      project folder; stale coordinator snapshots do not silently supply policy.
- [ ] Cleanup/reaping recognizes active owners and preserves unfinished/rescued
      branches, with no v1 or active session restart for rollout.

## Checks and sources

No CPU burners, stress tools, busy loops or wide/looped tests on shared nodes.
Use bounded Git fixtures or fake providers; Go checks run under `nice -n 19`
with `GOMAXPROCS=2`, `-p 2`, one suite at a time, and at most two compiling agents.
Any cluster fixture gets a CPU limit. CI builds images.

- [Workflow guide](../../../../docs/workflow-guide.md#6-protection-from-stale-repositories)
- [DESIGN-001 6.3/6.6](../designs/001-dev-env-v2.md#63-codex)
- [Plan 04](04-rolling-updates-codex.md)
- V1 GitOps launcher: haynes-ops
  `kubernetes/main/apps/dev/dev-env/app/resources/agent-run.sh`
- V2: `internal/agentd/clone.go`, `internal/agentd/launch.go`,
  `internal/agentd/protocol/status.go`, `internal/apiserver/validate.go`
- [Official project context](https://learn.chatgpt.com/docs/projects) and
  [worktree behavior](https://learn.chatgpt.com/docs/environments/git-worktrees)
