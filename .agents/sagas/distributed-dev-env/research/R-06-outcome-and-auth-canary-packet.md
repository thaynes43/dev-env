# R-06 Decision packet: auth canary and task-outcome acceptance (2026-10-31)

Draft for Tom, morning of 2026-10-31 (OpenProject #310, #320, #326). Research only. Nothing in this packet was
run, changed, merged or launched. Cluster reads are from 2026-10-31T05:01Z.

## Bottom line

- **One combined canary cannot honestly prove both.** The two features live in
  different session modes. The auth recovery code runs only in a Claude
  remote/local TUI. The task outcome is written only by a task, and only tasks are
  reclaimed. One session cannot be both.
- A Claude task cannot be put on 2.75.0 today without the global template move.
  No existing selector matches a Claude task. The one task selector matches Codex
  tasks only.
- **Recommendation: a staged pair, run in order, one image (dev-env:2.75.0).**
  - Stage A (outcome): one fresh Codex task on 2.75.0, via the existing task
    selector. It writes an outcome, is reclaimed, and is read back by API, CLI and
    portal. Run it once OP326's portal display is installed, so one run gives
    before-and-after evidence on all three surfaces.
  - Stage B (auth): the held auth canary keeps its reviewed scope and stays held.
    If Tom approves it later, run it on 2.75.0 rather than 2.73.0. Its recovery
    code is byte-identical.
- No active session restarts. The templates are not touched.

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
- Its planning artifact (`/tmp/auth310-safe-adoption-proposal-20261031.md`, SHA-256
  `8aaf92ec…`) is not in this pod. I used the tracker text.

**Hold:** the OP310 description (journal 3850) says "AUTH CANARY REMAINS HELD: no
pilot-selector retarget, new canary, active restart, global pin or new login".
The OP85 REQ-AUDIT row "Persistent authentication recovery" says the same.

**Version:** `ghcr.io/thaynes43/dev-env:2.73.0@sha256:2e93a6c31cde7c56369b422b80c49757494cc5503498c65e767c082feed2ad66`.
It was signed and verified in publish-agent run 41133394393 (OP310 journal 3716).
It carries #741 (merged 96d4b428, 2026-10-30T20:57:28Z).

**Selector today** (live operator Deployment args, matching haynes-ops
`kubernetes/main/apps/dev-env-system/operator/app/helmrelease.yaml:181-182`):
`--pilot-agent-session=dev-env-1027-111645/usage-pilot-1` on 2.52.0@87e8ab93. The
selected session `dev-env-1028-031951` (Claude local) runs pod UID 59856934…
on 2.52.0, with 0 restarts.

- Custody: 111645 released it for planning only (OP310 journal 3718).
- One dependency is still open: the Codex usage coordinator holds the usage
  acceptance handoff. Whether it needs 2.52 kept across a rebuild is unverified
  (journals 3718 and 3729).

**Fallback:**

- If the selector is retargeted, the running usage pilot pod keeps 2.52.0. On
  its next pod build it gets the template image, 2.71.0 (journal 3718).
- If the selector is later removed, the canary's own next pod also falls back
  to 2.71.0. **2.71.0 lacks #741.** So the canary must end before the selector
  is reverted.
- Template image: `haynes-ops .../templates/app/templates.yaml:17` =
  2.71.0@a9421cd1. The live ConfigMap `dev-env-templates` matches.

**What it can prove about auth recovery:**

- A signed #741 agentd boots in a fresh remote pod on the keeper's access
  projection. The pod image digest is the evidence.
- agentd follows each natural keeper generation. Evidence: the projection-sync
  log lines through `agent-run log`. Known-target agentd logs are the permitted
  evidence (OP310).
- Ordinary turns get authenticated replies after each natural rotation. This is
  a soak: no regression.
- Only if a **natural** old-token 401 lands in the transcript after a rotation:
  agentd sends one `continue`, logs "Claude session continued after the login
  was renewed" (`internal/agentd/claude_continue.go:264` at v2.75.0), and
  the next turn succeeds.

**What it cannot prove:**

- The recovery path itself, unless a natural 401 happens. No forced 401 and no
  revocation probe are allowed. An idle TUI makes no requests, so it would see
  no 401 at all.
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
- No login and no auth command is part of it.
- The natural rotation cadence is Unknown. The keeper refreshes once per
  access-token life, at two-thirds of it (`internal/keeper/claude_worker.go:108-117`).
  The life comes from the provider and was not inspected.

## 2. What outcome acceptance needs

| Need | State at 05:01Z | Evidence |
|---|---|---|
| Agent ≥2.75 in the task pod (agentd writes the outcome at first task exit) | **Missing.** Every task pod runs 2.71.0 or 2.45.0. | `kubectl get pods -n dev-agents`. `runagent.go:463-465` reads the outcome file. `status.go:63` reports it for task mode only. |
| Operator ≥2.75 (records it) | **Live.** `sha-fac30c8@sha256:f73bb749…`, generation 142, 2/2 ready. | Live Deployment. OP326 journal 3880. `internal/apiserver/task_outcome.go:18-57` |
| agent-run ≥2.75 for `show` | **Missing in this pod.** `/usr/local/bin/agent-run` is v2.45.0. The shared copy `~/.shared/handoffs/agent-run-v2.74.0` is 2.74.0. Neither sends `X-Dev-Env-Task-Outcome: v1` (`internal/agentrun/client.go:199`, new in 2.75), so the API strips the field (`task_outcome.go:71-72`). | `agent-run version` |
| CRD with `status.taskOutcome` | **Live.** CRD generation 21, Established, field present. Immutable (`agentsessions.yaml:2709`) and cannot be erased (`:3248-3250`). | `kubectl get crd`. OP326 journal 3870 |
| Portal display (OP326) | **In progress.** Worker `dev-env-1031-043708` has uncommitted code (journal 3878). No PR yet. The change touches the **operator's** portal reader as well as the portal. So it needs a release, **another operator pin** and a portal pin. The v2.75.0 portal (haynes-ops #4485, merged 04:59:50Z) does not show outcomes. | OP326 journals 3861, 3878 |
| Real before- and after-reclaim UI and API evidence | **None exists.** 0 of 326 AgentSessions have a `taskOutcome`. | `kubectl get agentsessions -n dev-agents -o json` |

Reclaim itself is proven in production. `dev-env-1031-033520` and three other
Codex tasks show `TaskComputeReclaimed`. A Claude task, `haynes-ops-1031-045309`,
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
2. **A Claude task does not use the rotating login.** The operator gives Claude
   task and local pods the static token. Only remote pods run on the keeper's
   access token (`pod.go:482-488`; `templates.yaml:89-92`). A task never sees a
   keeper rotation.
3. **Only a task writes an outcome and is reclaimed.** The API records an outcome
   only for `mode: task` (`internal/apiserver/task_outcome.go:27`). Reclaim scope
   is task-only (`internal/controller/terminal_task_reclaim.go:195-198`). Remote
   sessions are never reclaimed.
4. **There is no Claude task selector** (section 4).

Each auth limit in section 1 also applies to any task-based design. Running a
TUI next to a task inside one session is not a supported shape.

### Stage A: the outcome canary (recommended)

One fresh Codex task on 2.75.0. It writes one outcome file, is reclaimed, and is
read back.

**Prerequisites. None of these changes runtime:**

- OP326 is merged and released, and its operator pin and portal pin are live
  (under Q1 option a).
- Root (`dev-env-1019-205125`) records in OP that it releases the task-selector
  tuple. Its one matching session is already Reclaimed. If that session were
  resumed later, it would get the template's 2.71.0 instead of 2.63.0.
- agent-run 2.75.0 is extracted from the signed image, with recorded provenance.
  Same method as the 2.74.0 copy (crane, checked layer digest, binary sha256).
- Baseline snapshot: every session pod's UID, image and restarts; the operator
  args; the template image.
- Zero sessions carry the new key.

**Steps, with the evidence each one gives:**

| Step | Action | Evidence | Stop if |
|---|---|---|---|
| A1 | One haynes-ops PR. It changes exactly two operator args: `--task-pilot-agent-image=ghcr.io/thaynes43/dev-env:2.75.0@sha256:15dc76a6…` and `--task-pilot-agent-session=dev-env-1029-132039/outcome-canary-2750-20261031-01`. Review, guard, merge. Flux rolls the operator. | Args diff is 2 lines. Deployment generation goes up and is 2/2 ready. Every session pod UID and restart count is unchanged from the baseline. | Anything else differs, or any session pod changed. |
| A2 | The lead runs exactly one task: `agent-run --project dev-env --agent codex --model gpt-6.1-sol --effort low --size S --idempotency-key outcome-canary-2750-20261031-01 -p "<fixed prompt>"`. The prompt says: write `~/work/<session>.outcome.json` atomically with `prURLs` [dev-env PR 759], `headSHAs` [`942ed6b808f6619f70c543e7afda24f90fd5d4bc`] and a fixed summary such as "Outcome canary: retained record test, no code change". Change no repository. Open no PR. | Session name and UID. Pod UID. Pod image and imageID are `2.75.0@sha256:15dc76a6…`. | The image is not that digest. Record it; launch nothing more. |
| A3 | The task exits, in minutes. Read inside the 10-minute grace. | `kubectl get agentsession <n> -o jsonpath='{.status.taskOutcome}'` shows the record, with sessionUID and podUID equal to the Session and Pod. `agent-run show <n>` (2.75.0) prints "Task outcome", the PR and the head (`internal/agentrun/sessions.go:284-291`). `agent-run show` (2.45.0) prints none: the negotiation works. The portal session detail shows it in Tom's authenticated view. The condition is `TerminalTaskPodReclaim=Grace` with `dueAt`. | No outcome appears within 2 minutes of exit. Record "No outcome recorded". Do **not** relaunch with the same key, because a second session would match too. |
| A4 | The operator reclaims at `dueAt`. | Conditions `TaskComputeReclaimed` / `Reclaimed`. The pod is NotFound. The PVC is kept. | No reclaim by `dueAt` + 5 minutes. Record the reason (for example `ProofUnknown`). Delete nothing by hand. |
| A5 | Read again after reclaim. | The sha256 of the `taskOutcome` JSON is identical before and after. `agent-run show` (2.75.0) prints it in the Reclaimed phase. The portal shows it (authenticated view). | The record changed. That would be a defect: stop and report. |
| A6 | Close. Record the evidence on OP320 and OP326. Remove both task-selector lines, in the next routine operator PR or on their own, as Tom prefers. | The haynes-ops diff. | Not applicable. |

**Budget:** one Codex task (small; Codex allowance per OP85 is 51% used against a
75% ceiling). About 45 minutes elapsed, including the rollout and the grace. One
session at a time. One or two operator rollouts.

**Rollback and stop boundary:**

- Revert the A1 PR, or remove both lines. That rolls the operator only. No
  session pod changes.
- **Rollback floor:** once 2.75 records a `taskOutcome`, an operator below 2.75
  cannot update that session's status. A full-status write from an older
  operator drops the field, and the CRD refuses it with "original task outcome
  cannot be erased" (`agentsessions.yaml:3248-3250`; also
  `helmrelease.yaml:131-132`). This adds to the existing floor of 2.71 that
  reclaim records already set (`helmrelease.yaml:251`).
- The canary session stays Suspended/Reclaimed. Its HOME follows normal
  retention. No reap is needed.

**Guarantees, with evidence:**

- No template change. `templates.yaml` is not edited, so the revision does not
  change.
- No active session restarts or drains. Only operator args change. The selector
  acts only on a new pod of the exact new tuple. Drain is revision-based.
  Evidence pattern: OP326 journal 3880 (25 pods unchanged).
- No credentials, login, grant or network change. The task uses the existing
  profile and the Codex projection.

**What Stage A does not prove:** that a Claude task writes an outcome in
production. The writer is provider-independent (`runagent.go:441-475`). But a
Claude task on 2.75 waits for the template move.

### Stage B: the auth canary (held; scope unchanged)

The scope stays as reviewed (section 1). If Tom approves it later, the smallest
form is:

- Preconditions: the usage coordinator confirms it does not need
  `dev-env-1028-031951` kept on 2.52 across a rebuild. Custody is already
  released (journal 3718).
- One haynes-ops PR retargets `--pilot-agent-*` to the reviewed tuple. The image
  is 2.73.0@2e93a6c3 as reviewed, or 2.75.0@15dc76a6 under Q2. `git diff v2.73.0
  v2.75.0 -- internal/agentd/claude_continue.go` is empty, so the recovery code is
  the same.
- One Claude remote session with that key. A few short, ordinary turns at a fixed
  cadence, over a capped window: for example 24 hours or two natural keeper
  generations, whichever comes first. No forced 401, no login, no probes.
- Evidence and limits are as listed in section 1.
- End: reap the canary with `agent-run reap` (rescue first), then revert the
  selector. Revert after the reap, because a rebuilt canary pod would fall back to
  2.71.0, which lacks #741.

## 6. What Tom must decide

**Q1. Run Stage A: one fresh Codex task on 2.75.0 through the existing task
selector, retargeted from Root's spent key?**

- (a) Yes, once OP326's portal display is installed, so one run gives before and
  after evidence on API, CLI and portal. **Recommended.**
- (b) Yes, now. API and CLI evidence before and after reclaim. Portal evidence
  after reclaim only, later. The record is immutable, so later reads stay valid.
- (c) No. Wait for a global template move, so a Claude task can do it.

**Q2. The auth canary (Stage B)?**

- (a) Keep it held as reviewed. Decide after Stage A and the usage coordinator's
  answer. **Recommended.** Without a natural 401 it proves a soak, not the
  recovery. It would also displace the usage-pilot selector, whose dependency is
  unverified.
- (b) Approve it as reviewed, on 2.73.0.
- (c) Approve it on 2.75.0 instead of 2.73.0. The recovery code is the same, and
  both stages then use one image.

**Q3. Who gives the authenticated portal view before reclaim?** The window is
the 10-minute grace after the task exits. Agents have no portal login.

- (a) Tom views it at a launch time he picks. About 15 minutes of his time.
  **Recommended.**
- (b) Accept API and CLI evidence for "before", and Tom's portal view "after"
  only.

## 7. Kept distinct

- **Authenticated browser proof.** This is Tom's own Authentik-authenticated
  portal view. Agents cannot log in. Preview-harness screenshots (OP326) and
  agent Playwright runs do not replace it. Related browser acceptance is OP239.
- **Source fixtures.** #759 tests and red/green controls
  (`.agents/sagas/distributed-dev-env/verification/task-outcome-320.md`). OP326
  preview scenarios. #741's synthetic recovery tests. All of these are source
  and tested evidence, not installed or usable evidence.
- **Operator readiness.** Operator sha-fac30c8 live. CRD generation 21 with
  `taskOutcome`. Portal v2.75.0 pinned. These are readiness, not acceptance: no
  session has an outcome yet.

## Limits found

- The full planning artifact for the earlier proposal is not in this pod. Only
  the tracker summary (OP310 journal 3716) was read.
- `opwork search` matches subjects only. No item has "canary" in its subject.
- The provider's access-token life, and so the rotation cadence, is Unknown. It
  was not inspected by design.
- OP326's release version and timing are Unknown. Its operator change means one
  more operator pin before portal evidence.
- `helmrelease.yaml:200` has a stale comment (section 4).

## Evidence index

- Tracker: OP310 journals 3624, 3678, 3716, 3718, 3729, 3850. OP320 journals
  3789, 3823. OP326 journals 3834, 3861, 3870, 3878, 3880. OP85 REQ-AUDIT rows
  "Persistent authentication recovery" and "Compute reclaim and retained task
  outcomes". OP286 (task and remote selectors).
- haynes-ops `origin/main` 823b2cd7: `operator/app/helmrelease.yaml:130-134,
  165-214, 245-259`; `templates/app/templates.yaml:17, 89-117`;
  `crds/app/agentsessions.yaml:2664-2709, 3248-3250`.
- dev-env v2.75.0 (`fac30c80`): `internal/controller/pod.go:93-199, 482-488`;
  `drain.go:56-71`; `terminal_task_reclaim.go:89-215`;
  `internal/agentd/claude_continue.go:19-41, 198-271`; `daemon.go:536-538`;
  `runagent.go:441-475`; `status.go:63`; `internal/apiserver/task_outcome.go:18-78`;
  `internal/agentrun/client.go:199`; `sessions.go:284-291`;
  `docs/portal-session-runtime.md:283-330, 571`;
  `docs/workflow-guide.md:683-706`.
- Cluster reads at 05:01Z: 326 AgentSessions, 0 with a `taskOutcome`. 25 pods in
  dev-agents. Operator image `sha-fac30c8@f73bb749…`, 46 args. ConfigMap
  `dev-env-templates` image 2.71.0@a9421cd1.
- Signing: `gh release view v2.75.0`. publish-agent run 41169349678, job
  124443724051, log lines for push (04:42:14Z) and verification (04:42:21Z).
