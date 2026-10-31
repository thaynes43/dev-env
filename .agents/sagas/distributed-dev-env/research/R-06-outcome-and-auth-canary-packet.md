# R-06 Decision packet: auth canary and task-outcome acceptance (2026-10-31)

Draft for Tom, morning of 2026-10-31 (OpenProject #310, #320, #326). Research only. Nothing in this packet was
run, changed, merged or launched. Cluster reads are from 2026-10-31T05:01Z. The
selector-session and lifecycle reads are from 05:21Z.

## Bottom line

- **One combined canary cannot honestly prove both.** The two features live in
  different session modes. The auth recovery code runs only in a Claude
  remote/local TUI. The task outcome is written only by a task, and only tasks are
  reclaimed. One session cannot be both.
- A Claude task cannot be put on 2.75.0 today without the global template move.
  No existing selector matches a Claude task. The one task selector matches Codex
  tasks only.
- **Recommendation: approve one staged pair, run in order, on one signed image
  (dev-env:2.75.0).**
  - Stage A (outcome): one fresh Codex task on 2.75.0, through the existing task
    selector. It writes an outcome and is reclaimed. API and CLI evidence before
    and after reclaim. Tom's authenticated portal view after reclaim, at his
    convenience.
  - Stage B (auth): one fresh Claude remote canary on the same 2.75.0 image,
    through the existing generic selector. Its window is two natural keeper
    refreshes or 24 hours, whichever comes first. It ends with `agent-run
    suspend` and a selector revert.
- **An ordinary auth soak is not proof of the recovery branch.** It proves that
  branch only if a natural 401 happens during the window. There is no forced
  revocation and no refresh probe.
- Active sessions do not restart. The global templates are not touched. Both
  existing selector holdings stay as they are until Tom approves.

## 1. The held auth canary

**Reviewed scope** (OP310 journal 3716, 2026-10-31T00:10Z; the lead's narrower
adoption report is journal 3678):

- One fresh ordinary **Claude remote** session.
- Parent `dev-env-1029-132039`, idempotency key `auth310-2730-canary-20261031`.
- Selected by the existing generic selector `--pilot-agent-image` /
  `--pilot-agent-session`. It runs only after the usage-pilot owner releases that
  selector and Tom decides the held runtime step.
- Excluded: any global template change, active-session upgrade, new credentials,
  refresh or revocation probes, pairing copies and user-input inspection.
- The tracker text names no window and no end step.
- Its planning artifact (`/tmp/auth310-safe-adoption-proposal-20261031.md`, SHA-256
  `8aaf92ec…`) is not in this pod. I used the tracker text.

**Hold:** the OP310 description (journal 3850) says "AUTH CANARY REMAINS HELD: no
pilot-selector retarget, new canary, active restart, global pin or new login".
The OP85 REQ-AUDIT row "Persistent authentication recovery" says the same.

**Reviewed version:** `ghcr.io/thaynes43/dev-env:2.73.0@sha256:2e93a6c31cde7c56369b422b80c49757494cc5503498c65e767c082feed2ad66`.
It was signed and verified in publish-agent run 41133394393 (OP310 journal 3716).
It carries #741 (merged 96d4b428, 2026-10-30T20:57:28Z).

**Selector today** (live operator Deployment args, matching haynes-ops
`kubernetes/main/apps/dev-env-system/operator/app/helmrelease.yaml:181-182`):
`--pilot-agent-session=dev-env-1027-111645/usage-pilot-1` on 2.52.0@87e8ab93.
Custody and the open dependency are in section 5, "Custody from current data".

**Template image:** `haynes-ops .../templates/app/templates.yaml:17` =
2.71.0@a9421cd1. The live ConfigMap `dev-env-templates` matches. **2.71.0 lacks
#741.** Any pod that falls back to the template runs without the recovery fix.

**What a canary can prove about auth recovery:**

- A signed #741 agentd boots in a fresh remote pod. The pod image digest shows
  the image.
- agentd applies the keeper's access projection. Evidence: the ABSENCE of its two
  fixed warning lines ("Claude authentication requires a current keeper
  projection" and "pod holds its own login") in the pod's container log
  (`kubectl logs`, a permitted read), plus an answered turn. The line "Claude
  projection applied" is logged only when a failed sync recovers, so a healthy
  boot does not print it (`internal/agentd/daemon.go:540-559`).
- When a natural keeper refresh happens. Evidence: the keeper's fixed-word state
  log "claude auth state" with `state=refreshed`
  (`internal/keeper/claude_worker.go:236`). It carries no token values.
- Ordinary turns get answers after each natural refresh. Evidence: the reply to
  each turn (the lead reads the canary's answer). The Session status
  `conversationUsage.turns` count is only weak corroboration: it counts provider
  calls (`api/v1alpha1/agentsession_types.go:1379-1420`), and how an error entry is
  counted was not verified.
- Only if a **natural** old-token 401 lands in the transcript after a refresh:
  agentd sends one `continue` and logs "Claude session continued after the login
  was renewed" (`internal/agentd/claude_continue.go:264`). The next turn then
  succeeds.

**What it cannot prove:**

- **The recovery branch itself, unless a natural 401 happens.** An ordinary soak
  shows no regression. It does not show recovery. There is no forced 401 and no
  revocation or refresh probe. An idle TUI makes no requests, so it sees no 401.
- That the provider's revocation window or the Secret propagation window is
  gone. #741 does not claim that (PR #741 body; backlog
  `03-remote-control.md` diff).
- Which token failed. The transcript cannot identify it, and one later error
  on the new token can use up that generation's single recovery
  (`claude_continue.go:33-41`).
- Keeper sole-refresher. That is a design property: pods get an access-only
  projection (`internal/claudeauth`). The canary can only observe one keeper
  Lease holder. It does not prove there is no other refresher.
- Adoption by active sessions. `dev-env-1029-132039` and `dev-env-1030-182804`
  stay on 2.45.0. Remote sessions never auto-drain (`internal/controller/drain.go:61-65`).
- Anything about Codex auth.
- The canary's per-generation receipt (`~/.agentd/claude-continue.json`) is not
  read. Reading it needs exec into the pod, which agents do not have, and it
  would be private auth inspection.
- No login and no auth command is part of it.
- The natural refresh cadence is Unknown. The keeper refreshes once per
  access-token life, at two-thirds of it (`internal/keeper/claude_worker.go:108-117`).
  The life comes from the provider and was not inspected. A 24-hour window may
  see fewer than two refreshes.

## 2. What outcome acceptance needs

| Need | State at 05:01Z | Evidence |
|---|---|---|
| Agent ≥2.75 in the task pod (agentd writes the outcome at first task exit) | **Missing.** Every task pod runs 2.71.0 or 2.45.0. | `kubectl get pods -n dev-agents`. `runagent.go:463-465` reads the outcome file. `status.go:63` reports it for task mode only. |
| Operator ≥2.75 (records it) | **Live.** `sha-fac30c8@sha256:f73bb749…`, generation 142, 2/2 ready. | Live Deployment. OP326 journal 3880. `internal/apiserver/task_outcome.go:18-57` |
| agent-run ≥2.75 for `show` | **Missing in this pod.** `/usr/local/bin/agent-run` is v2.45.0. The shared copy `~/.shared/handoffs/agent-run-v2.74.0` is 2.74.0. Neither sends `X-Dev-Env-Task-Outcome: v1` (`internal/agentrun/client.go:199`, new in 2.75), so the API strips the field (`task_outcome.go:71-72`). | `agent-run version` |
| CRD with `status.taskOutcome` | **Live.** CRD generation 21, Established, field present. Immutable (`agentsessions.yaml:2709`) and cannot be erased (`:3248-3250`). | `kubectl get crd`. OP326 journal 3870 |
| Portal display (OP326) | **In progress.** Worker `dev-env-1031-043708` has uncommitted code (journal 3878). No PR yet. The change touches the **operator's** portal reader as well as the portal. So it needs a release, **another operator pin** and a portal pin. The v2.75.0 portal (haynes-ops #4485, merged 04:59:50Z) does not show outcomes. | OP326 journals 3861, 3878 |
| Real before- and after-reclaim UI and API evidence | **None exists.** 0 of 326 AgentSessions had a `taskOutcome` at 05:01Z (snapshot). | `kubectl get agentsessions -n dev-agents -o json` |

Reclaim has been observed in production on four Codex tasks as of 05:01Z:
`dev-env-1031-033520` and three others show `TaskComputeReclaimed`. A Claude task, `haynes-ops-1031-045309`,
was in `Grace` until 05:09:30Z.

## 3. Version compatibility

**Target:** `ghcr.io/thaynes43/dev-env:2.75.0@sha256:15dc76a6a51f253aa1decd61f5be83ab5c44441c65b9b9b852717267c698fbe9`.

- Release v2.75.0 is on `fac30c804b8fccda7530bce61c49df0910543741`, published
  04:37:44Z.
- publish-agent run 41169349678, job "Publish (agent)" 124443724051: smoke test,
  push, sign and verify all succeeded.
- Log: push at 04:42:14Z. Then `Verification for ghcr.io/thaynes43/dev-env@sha256:15dc76a6…`.
  It was verified as signed by `publish-agent.yml@refs/tags/v2.75.0` at 04:42:21Z.

**Can a 2.75.0 task pod run under the current templates and operator? Yes:**

- The operator is the same release. `sha-fac30c8` is the v2.75.0 tag commit.
- From 2.71.0 to 2.75.0, agentd changed in two commits only: #741 and #759
  (`git log v2.71.0..v2.75.0 -- internal/agentd`).
- `images/agent` is unchanged from 2.71.0 to 2.75.0. The Claude and Codex CLIs
  are the same as in the template image.
- `internal/templates` and the pod build are unchanged. The only controller
  commits are #738 and #732, both operator-side.
- A pilot image changes only the agent container's image. Env, mounts, profile
  and the revision label stay the template's (`docs/portal-session-runtime.md:283-330`).
- Precedent: `dev-env-1029-014357`, a Codex task selected onto 2.63.0 by the task
  selector, ran under the template and was reclaimed (`TaskComputeReclaimed`).

**Same image for Stage B:** `git diff v2.73.0 v2.75.0 --
internal/agentd/claude_continue.go` is empty. Since 2.73.0 the only agentd commit
is #759 (`git log v2.73.0..v2.75.0 -- internal/agentd`). So 2.75.0 has the same
recovery code as the reviewed 2.73.0, plus the outcome writer.

**Limit:** the only task selector accepts Codex tasks. A Claude task on 2.75.0
needs either the global template move or new source.

## 4. The pilot selector mechanism

Four default-off selectors exist in v2.75.0 (`cmd/dev-env-operator/main.go:257-262`,
`internal/controller/pod.go`):

| Selector | Matches | Live value |
|---|---|---|
| `--pilot-agent-*` | Claude remote/local, or Codex remote; repo dev-env (`pod.go:129-142`) | `dev-env-1027-111645/usage-pilot-1` → 2.52.0 |
| `--task-pilot-agent-*` | **Codex task only**; repo dev-env; not budget-bound (`pod.go:160-169`) | `dev-env-1019-205125/provider-child-cap-20261029-01` → 2.63.0 |
| `--remote-pilot-agent-*` | Codex remote only (`pod.go:175-182`) | `dev/dev-env\|v2-codex-host-1019-01` → 2.70.0 |
| Codex RC image | one named Codex remote session | not set |

All four exclude sessions with a workspace, a project canary or a private pilot.

**Does a pilot apply only to new pods of matching sessions? Yes, with these
conditions:**

- The selector is checked only in `sessionPod()`, when a pod is built
  (`pod.go:186-199`). Existing pods are never compared or replaced.
- It needs an exact parent **and** key. The API sets the parent from the
  caller's authenticated identity.
- It matches every session with that tuple, for life, at each new pod. "One
  session" is an operating rule: use a fresh key once (`docs/portal-session-runtime.md:314-324`).
- Changing a selector is an operator-args edit in haynes-ops. The operator
  Deployment rolls. Session pods are not touched: at the last rollout, 25 pods
  kept the same UIDs and restarts (OP326 journal 3880).
- The template revision does not change, so no drain is triggered. Drain is
  revision-based and never applies to remote or private-history sessions
  (`drain.go:61-71`).
- **Stale comment:** `helmrelease.yaml:200` says "no current Session has this
  key". In fact `dev-env-1029-014357` used it, and that session is now
  Reclaimed. The same comment block (`:205-206`) says to remove both lines when
  that worker is complete.

## 5. Why one canary cannot prove both, and the staged proposal

### Why not one session

1. **Recovery is TUI-only.** `continueAfterRenewal` returns at once in task
   mode and needs a TUI launch (`claude_continue.go:198-199, :222`). The #741
   doc says: "task mode or pod-owned mode never sends."
2. **A Claude task authenticates with the static token.** The operator gives
   Claude task and local pods `CLAUDE_CODE_OAUTH_TOKEN` (`pod.go:485-488`, set at
   `:300`). That takes precedence over the keeper's access projection, which
   full-profile pods also sync (`templates.yaml:117`; `daemon.go:536-545` gates on
   agent, not mode). Either way, the recovery code above is gated off in task
   mode, so a task cannot exercise it.
3. **Only a task writes an outcome and is reclaimed.** The API records an outcome
   only for `mode: task` (`internal/apiserver/task_outcome.go:27`). Reclaim scope
   is task-only (`internal/controller/terminal_task_reclaim.go:195-198`). Remote
   sessions are never reclaimed.
4. **There is no Claude task selector** (section 4).

Each auth limit in section 1 also applies to any task-based design. Running a
TUI next to a task inside one session is not a supported shape.

### Custody from current data

**Task selector `dev-env-1019-205125/provider-child-cap-20261029-01` (Root).**
No dependency remains.

- haynes-ops #4395 (merged 2026-10-29T01:40Z) set this key for one authorized
  OP235 source worker.
- That worker is `dev-env-1029-014357` (OP235 journal 2249). It ended with exit 0
  (journal 2253).
- OP235 is **Accepted**. Journal 2293 says "no remaining unit action".
- At 05:21Z the session was Suspended since 2026-10-30T22:24:44Z, with conditions
  `TaskComputeReclaimed`. It has no pod. Its PVC `home-dev-env-1029-014357` is
  Bound and retained, because it is private-history protected.
- Effect of a retarget: if that session were resumed, its new pod would get the
  template's 2.71.0 instead of 2.63.0. A resumed task never runs its prompt
  again (`agent-run help resume`).

**Usage pilot `dev-env-1027-111645/usage-pilot-1`.** One narrow dependency
remains.

- At 05:21Z, `dev-env-1028-031951` (Claude local) was Running. Pod UID
  59856934-fb8d-466a-b749-4668e589cd10, started 2026-10-30T19:08:43Z, on
  2.52.0@87e8ab93, 0 restarts.
- Its portal send and history targets are bound by Session UID
  (`helmrelease.yaml:197-198`), not by image. A selector change does not touch
  them.
- OP310 journal 3718: 111645 released custody for planning. The Codex usage
  coordinator holds the usage acceptance handoff (`CODEX-USAGE-HANDOFF.md`),
  including Tom's portal check of the conversation usage panel.
- OP310 journal 3729: there is no technical reason to keep 2.52. The usage code
  is identical from 2.52.0 to 2.71.0.
- No later record. OP310's last journal is 3850 (a description edit).
- **What remains:** the pilot pod is rebuilt only by preemption, a node drain or
  an explicit restart or resume. If that happens while Stage B holds the
  selector (at most 24 hours), the pod gets 2.71.0. After Stage B's revert, a
  rebuild gets 2.52.0 again.
- **Who must answer:** the Codex usage coordinator (OP310 journal 3718), if its
  acceptance compares against a 2.52 image. Otherwise Tom accepts this narrow
  exposure as part of the single decision in section 6. The coordinator's
  session name is not recorded in OP.

### Stage A: the outcome canary

One fresh Codex task on 2.75.0. It writes one outcome file, is reclaimed, and is
read back.

**Prerequisites. None of these changes runtime:**

- agent-run 2.75.0 is extracted from the signed image, with recorded provenance.
  Same method as the 2.74.0 copy (crane, checked layer digest, binary sha256).
- Baseline snapshot: every session pod's UID, image and restarts; the operator
  args; the template image.
- Zero sessions carry the new key.

Stage A does not wait for OP326. The record is immutable, so Tom's later portal
view still reads the same record.

**Steps, with the evidence each one gives:**

| Step | Action | Evidence | Stop if |
|---|---|---|---|
| A1 | One haynes-ops PR. It changes exactly two operator args: `--task-pilot-agent-image=ghcr.io/thaynes43/dev-env:2.75.0@sha256:15dc76a6…` and `--task-pilot-agent-session=dev-env-1029-132039/outcome-canary-2750-20261031-01`. Final PR review, guard, merge. Flux rolls the operator. | Args diff is 2 lines. Deployment generation goes up and is 2/2 ready. Every session pod UID and restart count is unchanged from the baseline. | Anything else differs, or any session pod changed. |
| A2 | The lead runs exactly one task: `agent-run --project dev-env --agent codex --model gpt-6.1-sol --effort low --size S --idempotency-key outcome-canary-2750-20261031-01 -p "<fixed prompt>"`. The prompt says: write `~/work/<session>.outcome.json` atomically with `prURLs` [dev-env PR 759], `headSHAs` [`942ed6b808f6619f70c543e7afda24f90fd5d4bc`] and a fixed summary such as "Outcome canary: retained record test, no code change". Change no repository. Open no PR. | Session name and UID. Pod UID. Pod image and imageID are `2.75.0@sha256:15dc76a6…`. | The image is not that digest. Record it; launch nothing more. |
| A3 | The task exits, in minutes. Read during the 10-minute grace. | **API:** `kubectl get agentsession <n> -o jsonpath='{.status.taskOutcome}'` shows the record, with sessionUID and podUID equal to the Session and Pod. **CLI:** `agent-run show <n>` (2.75.0) prints "Task outcome", the PR and the head (`internal/agentrun/sessions.go:284-291`). `agent-run show` (2.45.0) is expected to print none, which would confirm the negotiation. The condition is `TerminalTaskPodReclaim=Grace` with `dueAt`. **Browser proof before reclaim: unverified** (see the optional extra below). | No outcome appears within 2 minutes of exit. Record "No outcome recorded". Do **not** relaunch with the same key, because a second session would match too. |
| A4 | The operator reclaims at `dueAt`. | Conditions `TaskComputeReclaimed` / `Reclaimed`. The pod is NotFound. The PVC is kept. | No reclaim by `dueAt` + 5 minutes. Record the reason (for example `ProofUnknown`). Delete nothing by hand. |
| A5 | Read again after reclaim. | **API:** the sha256 of the `taskOutcome` JSON is identical before and after. **CLI:** `agent-run show` (2.75.0) prints it in the Reclaimed phase. | The record changed. That would be a defect: stop and report. |
| A6 | End. Revert the A1 PR, which restores Root's tuple exactly. Record the evidence on OP320 and OP326. | The haynes-ops diff equals the pre-A1 args. Session pods are unchanged. | Not applicable. |
| A7 | At Tom's convenience, once OP326's release, operator pin and portal pin are live: Tom opens the canary's session detail in the portal. | Tom's authenticated view of the outcome after reclaim. | The portal does not show it. Record it on OP326. |

**Optional extra (not in the minimal proposal):** a live before-reclaim portal
view. It needs OP326 installed before A2, and Tom at the portal during the
10-minute grace. Without it, before-reclaim browser proof stays unverified.

**Budget:** one Codex task (small; Codex allowance per OP85 is 51% used against a
75% ceiling). About 45 minutes elapsed for A1 to A6. One session at a time. Two
operator rollouts (A1 and its revert).

**Rollback and stop boundary:**

- Revert the A1 PR. That rolls the operator only. No session pod changes.
- **Rollback floor:** once 2.75 records a `taskOutcome`, an operator below 2.75
  cannot update that session's status. A full-status write from an older
  operator drops the field, and the CRD refuses it with "original task outcome
  cannot be erased" (`agentsessions.yaml:3248-3250`; also
  `helmrelease.yaml:131-132`). This adds to the existing floor of 2.71 that
  reclaim records already set (`helmrelease.yaml:251`).
- The canary session stays Suspended/Reclaimed. The archive timer cannot delete
  its HOME while the private-history latch holds and the DESIGN-025 HOME-expiry
  flags stay off, as they are now (`internal/controller/guard.go:314-320`). No
  reap is needed.
- If the canary is resumed after the revert, its new pod gets 2.71.0. A resumed
  task never runs its prompt again.

**What Stage A does not prove:** that a Claude task writes an outcome in
production. The writer is provider-independent (`runagent.go:441-475`). But a
Claude task on 2.75 waits for the template move.

### Stage B: the auth canary (proposed form)

**What differs from the reviewed scope (journal 3716):**

| Item | Reviewed | Proposed |
|---|---|---|
| Image | 2.73.0@2e93a6c3 | 2.75.0@15dc76a6 (same recovery code, section 3) |
| Session, parent, key, selector | Claude remote, `dev-env-1029-132039`, `auth310-2730-canary-20261031`, `--pilot-agent-*` | Same. The key name still says 2730. |
| Window | not stated | two natural keeper refreshes or 24 hours, whichever comes first |
| Turns | not stated | one short ordinary turn per hour |
| End | not stated | `agent-run suspend`, then revert the selector to `usage-pilot-1` |
| HOME retention | not stated | created with `--archive-after 720h` |
| Exclusions | as listed in section 1 | Same: no template change, no active-session upgrade, no credentials, no login, no refresh or revocation probe, no pairing copy, no user-input inspection |

**Steps, with the evidence each one gives:**

| Step | Action | Evidence | Stop if |
|---|---|---|---|
| B0 | Run after Stage A, because both stages edit `helmrelease.yaml`. Take a baseline snapshot. Check that zero sessions carry the key. | The baseline. | The usage coordinator objects (see custody). |
| B1 | One haynes-ops PR. It changes exactly two operator args: `--pilot-agent-image=ghcr.io/thaynes43/dev-env:2.75.0@sha256:15dc76a6…` and `--pilot-agent-session=dev-env-1029-132039/auth310-2730-canary-20261031`. Final PR review, guard, merge. | Args diff is 2 lines. The operator is 2/2 ready. Every session pod is unchanged, including `dev-env-1028-031951` (same UID, still 2.52.0). | Anything else differs, or any session pod changed. |
| B2 | The lead creates one ordinary Claude remote session (`agent-run --interactive`) with that key and `--archive-after 720h`. | Session name and UID. Pod image and imageID are `2.75.0@sha256:15dc76a6…`. Neither projection warning line appears, and the first turn is answered. | Wrong image. Or "Claude authentication requires a current keeper projection" or "pod holds its own login" persists. Then suspend (B5) and stop. |
| B3 | One short ordinary turn per hour, through `agent-run msg`. | The lead reads each reply. Keeper `state=refreshed` log lines give each natural refresh time. The first turn after each refresh is answered. `conversationUsage.turns` is weak corroboration only. | A turn gets no answer and no recovery follows. Suspend (B5) and stop. |
| B4 | The window ends: two natural refreshes after B2, or 24 hours, whichever comes first. Save only the fixed agentd lines from the container log (the two projection warnings, `projection applied` if a recovery happened, `continued after the login was renewed`, `continue ... was not confirmed`). Also save the Session `conversationUsage`. | A receipt with those lines and counts. These container log lines are lost when the pod stops, so this step comes first. Loki may also hold them; its retention is Unknown. | Not applicable. |
| B5 | `agent-run suspend <canary>`. | Phase Suspended, `suspendedAt` set, rescue result recorded, PVC Bound. | The rescue fails. The volume is then kept for a human; record it. |
| B6 | Revert the B1 PR. That restores `usage-pilot-1` on 2.52.0 exactly. | The args equal the pre-B1 baseline. Session pods are unchanged. | Not applicable. |

**Budget:** at most 24 hours elapsed. About 24 short Claude turns (Max usage
Unknown, but small). One canary session. Two operator rollouts (B1 and its
revert).

**What the end step keeps, and the timers that delete it** (verified in source):

- **Suspend keeps the HOME volume:** the worktree, the conversation, gitignored
  output and agentd's state in `~/.agentd`, including the continue receipt
  (`agent-run help suspend`; `internal/agentd/settings.go:54, 146`).
- **Archive timer:** a suspended session is archived at `suspendedAt` +
  `archiveAfter`. The templates' default is 168 hours (`templates.yaml:49`
  `lifecycle.archiveAfter`). A session can override it at creation with
  `agent-run --archive-after` (`internal/controller/reconciler.go:844-879`). This
  proposal uses 720 hours, so the HOME stays 30 days after B5.
- **What archive deletes:** the HOME volume, after a verified rescue
  (`reconciler.go:885-960`; `guard.go:314-340`). The rescue bundle holds git refs
  and tracked or untracked worktree files only. Gitignored files, the
  conversation and `~/.agentd` are not in it. The Session stays as an Archived
  record. An archived session cannot be resumed, only restored from its bundle.
- **What survives archive:** the Archived Session record (its status, including
  `conversationUsage`), the rescue bundle, and `~/.shared/logs/<name>.log`. The
  log holds only TUI start and exit lines. The shelf pruner keeps a session's
  bundles and logs while its Session object exists. It removes them 720 hours
  after the object is gone (`internal/shelf/pruner.go:16-103`).
- **No history retention or export for remote sessions.** Only tasks are marked
  for private-history retention. Remote and local sessions are never marked
  (`internal/controller/private_history.go:40-45`). The DESIGN-025 history
  archive (`--enable-home-history-archive`) is for private Sessions only, and it
  is off in the live operator args.
- **Reap is not used.** `agent-run reap` archives the volume right after the
  rescue (`agent-run help reap`).
- **Resume after the revert:** `agent-run resume` builds a new pod. With the
  selector reverted, that pod gets the template's 2.71.0, which lacks #741. The
  conversation resumes, but the canary no longer runs the fix. Do not resume it;
  a resume is a new decision.
- **No rollback floor from Stage B.** A remote session records no `taskOutcome`.

## 6. The decision for Tom

**Approve the staged pair as scoped in section 5?**

- Stage A: one fresh Codex task on signed dev-env:2.75.0 through the existing
  task selector, retargeted from Root's completed key. API and CLI evidence
  before and after reclaim. Tom's portal view after reclaim, at his convenience.
- Stage B, after Stage A: one fresh Claude remote canary on the same signed
  2.75.0 image through the existing generic selector. The window is two natural
  keeper refreshes or 24 hours, whichever comes first. One short turn per hour.
  It ends with suspend and a selector revert. The HOME is kept 720 hours after
  the suspend.

Options:

- **Yes, both stages as scoped. Recommended.** It gives #741 its first
  runtime run seen in this cluster (an absence claim, per OP310 journals 3678 and
  3716 and the live images) on a remote session and gives the outcome record its first
  installed run. A yes also accepts the narrow 031951 exposure (section 5,
  "Custody from current data"), unless the usage coordinator objects before B1.
- **Narrower: Stage A only.** Stage B stays held. Cost: #741 stays without any
  production evidence on a remote session.
- **No.** Both stay held.

Notes on a yes:

- Each stage's version and selector change is still a haynes-ops PR with final
  review, the guard and a merge. This decision does not replace that review.
- An ordinary auth soak is **not** proof of the recovery branch unless a natural
  401 happens. There is no forced revocation and no refresh probe. If no 401
  happens, Stage B reports boot, projection and answered turns across natural
  refreshes, and nothing more.

**Kept explicit:**

- **Selector holdings:** both existing holdings stay unchanged until this
  approval. Each stage changes one selector, then reverts it exactly.
- **Active sessions and templates:** no active session restarts or drains. The
  global templates and their revision are not touched.
- **Rollback floor:** after Stage A, the operator must stay at 2.75 or later for
  the canary task's Session (section 5, Stage A).
- **Old-pilot rebuild fallback:**
  - During Stage A, a resume of `dev-env-1029-014357` would get 2.71.0, not
    2.63.0.
  - During Stage B, a rebuild of `dev-env-1028-031951` would get 2.71.0, not
    2.52.0. After the revert it gets 2.52.0 again.
  - After Stage B, a resume of the canary gets 2.71.0, without #741.

## 7. Kept distinct

- **Authenticated browser proof.** This is Tom's own Authentik-authenticated
  portal view. Agents cannot log in. Preview-harness screenshots (OP326) and
  agent Playwright runs do not replace it. The minimal proposal has Tom's view
  after reclaim only. **Browser proof before reclaim stays unverified** unless
  the optional live window is used. Related browser acceptance is OP239.
- **Source fixtures.** #759 tests and red/green controls
  (`.agents/sagas/distributed-dev-env/verification/task-outcome-320.md`). OP326
  preview scenarios. #741's synthetic recovery tests. All of these are source
  and tested evidence, not installed or usable evidence.
- **Operator readiness.** Operator sha-fac30c8 live. CRD generation 21 with
  `taskOutcome`. Portal v2.75.0 pinned. These are readiness, not acceptance: no
  session has an outcome yet.

## Limits found

- The full planning artifact for the earlier proposal is not in this pod. Only
  the tracker summary (OP310 journal 3716) was read. Its end step, if it had
  one, is Unknown.
- `opwork search` matches subjects only. No item has "canary" in its subject.
- The provider's access-token life, and so the refresh cadence, is Unknown. It
  was not inspected by design. A 24-hour window may see fewer than two refreshes.
- Every supported end of a remote session deletes its HOME on a timer: suspend
  at `suspendedAt` + `archiveAfter`, reap right after the rescue. Remote sessions
  get no history retention or export.
- agentd's container log lines are lost when the pod stops. Loki retention is
  Unknown. Evidence must be saved before the suspend.
- agentd logs "Claude projection applied" only when a failed sync recovers, so a
  healthy boot shows only the absence of the warnings. Per-generation proof inside
  the pod is in a HOME receipt that is not read.
- How `conversationUsage` counts an error entry was not verified.
- The Codex usage coordinator's session name is not recorded in OP.
- OP326's release version and timing are Unknown. Its operator change means one
  more operator pin before Tom's portal view.
- `helmrelease.yaml:200` has a stale comment (section 4).

## Evidence index

- Tracker: OP310 journals 3624, 3678, 3716, 3718, 3729, 3850. OP320 journals
  3789, 3823. OP326 journals 3834, 3861, 3870, 3878, 3880. OP235 journals 2249,
  2253, 2286, 2293 (Accepted). OP85 REQ-AUDIT rows "Persistent authentication
  recovery" and "Compute reclaim and retained task outcomes". OP286 (task and
  remote selectors).
- haynes-ops `origin/main` 823b2cd7 (re-read at a036fda0 at 05:40Z; the cited lines still hold): `operator/app/helmrelease.yaml:130-134,
  165-214, 245-259`; `templates/app/templates.yaml:17, 43-51, 89-117`;
  `crds/app/agentsessions.yaml:2664-2709, 3248-3250`. haynes-ops #4395 (task
  selector key for OP235). Templates unchanged at main d20f3971 (05:07Z).
- dev-env v2.75.0 (`fac30c80`): `internal/controller/pod.go:93-199, 482-488`;
  `drain.go:56-71`; `terminal_task_reclaim.go:89-215`;
  `private_history.go:34-45`; `guard.go:300-345`; `reconciler.go:500-530,
  844-960`; `internal/shelf/pruner.go:16-103`;
  `internal/agentd/claude_continue.go:19-41, 198-271`; `daemon.go:536-559`;
  `settings.go:54, 146`; `runagent.go:441-475`; `status.go:63`;
  `internal/keeper/claude_worker.go:108-117, 236`;
  `internal/apiserver/task_outcome.go:18-78`;
  `internal/agentrun/client.go:199`; `sessions.go:284-291`;
  `help.go:31-33, 330-372, 615-623`;
  `api/v1alpha1/agentsession_types.go:465-484, 1379-1420`;
  `docs/portal-session-runtime.md:283-330, 571`;
  `docs/workflow-guide.md:683-706`.
- Cluster reads at 05:01Z (a snapshot; the fact-check later counted 327): 326
  AgentSessions, 0 with a `taskOutcome`. 25 pods in
  dev-agents. Operator image `sha-fac30c8@f73bb749…`, 46 args. ConfigMap
  `dev-env-templates` image 2.71.0@a9421cd1.
- Cluster reads at 05:21Z: `dev-env-1029-014357` and `dev-env-1028-031951`
  sessions, pods and PVCs. Operator args have no `history` or `expiry` flag
  besides `--enable-private-history-retention=true` and the portal history
  target. `kubectl auth can-i get pods/log -n dev-agents` is yes.
- Signing: `gh release view v2.75.0`. publish-agent run 41169349678, job
  124443724051, log lines for push (04:42:14Z) and verification (04:42:21Z).
