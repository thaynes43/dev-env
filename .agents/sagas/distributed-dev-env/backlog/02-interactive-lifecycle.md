# 02: interactive sessions and lifecycle

**Status:** in progress (started 2026-10-07); step 12's client is built (#103, D-68), released as signed 2.8.0 (#104), and deployed and verified by haynes-ops #3579. Steps 12 and 13 still need Tom's laptop check; Q-17 awaits his kubeconfig prerequisite answer.
**Depends on:** 01
**Parallel with:** 07 (the access broker) in the architecture; this run finishes 02 before resuming 07 (README decisions 40 and 44).

## Goal

Interactive (`local`) sessions in their own pods, with the full lifecycle: idle
detection, timers, suspend, resume with the conversation intact, restore from a
bundle. declare-activity moves to the API. Tom can run `agent-run` from his laptop.

## Scope

- agentd: Claude status from `~/.claude/sessions/<pid>.json`, Codex activity from
  process CPU and rollout mtime, attached tmux clients, and v1's `wt_busy` git and
  file signals (DESIGN-001 4.2); `agentd ctl prepare-restart` and resume on boot.
  Resume takes the conversation id from `~/.agentd/launch.json`; plan 01 never
  starts a launched task again (D-42), so resume is the only way back in.
- Operator: the timers of D-09, the reaper, `suspend`, `resume`, `restore`,
  `/v1/sessions/{id}/log` (logs also copied to the shared volume), the child-session
  limit (4 per parent, depth 2).
- Operator, from plan 01 step 5 (D-51), which left these out:
  - **A rescue pod.** A reaped session whose last pod is gone (preempted), ended
    (evicted) or never started has no pod to rescue in, so its volume stays and the
    session waits with `RemovalBlocked`. The rescue pod mounts the volume, the
    shared volume and the gh token, runs agentd without starting the agent (a new
    `agentd hold`, say, so no task runs twice, D-42), takes a small size, and lets
    the operator run the same exec rescue; it also retries a rescue that failed for
    a passing reason (CephFS down), and stays up while one fails so a human can exec
    in and fix the worktree. It is the "Failed → Suspended: rescue what is on the
    volume" edge of 4.1. One session already waits for it: `dev-agents/dev-env-1007-050756`,
    plan 01's Pending check (size L, 2026-10-07). Its pod never started, so it keeps an
    empty 20Gi `gasha01-rbd` volume and shows `RemovalBlocked` (`DeleteNeedsRescue`)
    in `agent-run list`. When the rescue pod lands, check that it archives this one.
  - **The archive timer** for a suspended session (D-09: 7 days, from the
    templates, not code), and what `resume` does after an archive: a new volume
    would start the task again, so an archived session should be refused and a
    restore offered instead.
  - **Bundles:** prune `rescue/<session>/` after D-09's 30 days, and list them for
    `GET /v1/rescues` and `agent-run rescue list|restore` (the restore recipe is in
    D-48).
  - **The page on `RescueFailed`** (D-10 "blocks archive and pages"): a metric or
    an alert on the condition, routed to Tom.
- `agent-run attach|detach` for Tom through `kubectl exec` (workbench or laptop; not
  from agent pods, D-19); `suspend`, `resume`, `rescue list|restore`, `msg`.
- Messaging tier 3 (D-16): `/v1/sessions/{id}/messages` → `agentd ctl deliver`
  (TUI paste for Claude, `codex queue` for Codex).
- `Activity` CRD and `/v1/activities` with the v1 limits enforced server-side;
  `declare-activity` v2 client. In haynes-ops: dev-env-ops' `dev-activity-check.sh`
  reads both the v1 files and `kubectl get activities`; the OPERATOR-tier
  ClusterRole gains read on `dev-env.haynesops.com`.
- Laptop path (D-05): `agent-run` mints a `dev-env-human` token with the kubeconfig
  and port-forwards to the API.

Already built by plan 01: the child-session limit (4 unfinished children per parent,
two levels deep, D-46).

## Progress

One PR per step unless a step says otherwise, in this order. Tick a step in the PR
that lands it. A step that changes `config/crd/` needs its haynes-ops copy before the
operator pin that writes the new fields (CLAUDE.md). A step that changes agentd or
`agent-run` ships in the agent image: the release PR, then `publish-agent.yml`, then
the `dev-env-templates` pin in haynes-ops. Operator changes ship as the
`dev-env-operator:sha-<short>` pin. A step is done when it is deployed and checked in
the cluster.

- [x] 1. This list (docs only, #65).
- [x] 2. **The rescue pod** (D-51's gap; D-55, #68; deployed 2026-10-07). `agentd hold` holds the volume and starts no
  agent (D-42). `agentd ctl rescue` reports an empty volume, with no clone and no
  worktree (what a pod that never started leaves), as a valid rescue with nothing
  to save. The operator gives a reaped session whose
  volume has no valid rescue and no pod a hold pod (the session's pod, size S, no
  agent token), runs the usual exec rescue in it, retries a failed rescue, and keeps
  the hold pod up while the rescue fails, so a human can exec in. A hold pod goes as
  soon as nothing needs it. Done when `dev-agents/dev-env-1007-050756` is archived and
  gone, checked with kubectl. **Done 2026-10-07:** agent image 2.2.0 went into the
  templates (haynes-ops #3529), then the operator at `sha-06841c8`, which carries
  #68, rolled out (plan 07's pin). At 21:21Z the operator created the hold pod
  `dev-env-1007-050756` on talosw02 at size S. Its rescue `20261007-2121` found the
  volume empty (`VolumeEmpty`, recorded `CleanAndPushed`). The hold pod went, and the
  volume was archived; the events are `HoldPod`, `VolumeEmpty` and `Archived`.
  `home-dev-env-1007-050756` is gone, and `agent-run show dev-env-1007-050756` answers
  404.
- [x] 3. **The `RescueFailed` page** (D-10, D-57, #69; haynes-ops #3532). The
  operator serves `dev_env_session_rescue_failed`; haynes-ops scrapes it and pages
  Tom with `DevEnvRescueFailed`, and with `DevEnvOperatorMetricsAbsent` when the
  series vanish.
- [x] 4. **Local sessions and resume on boot** (D-58, this PR). The API serves `mode: local` for
  Claude. agentd starts the TUI in tmux session `agent` with a new conversation id.
  On any later boot of a volume that has a launch record, task or local, it starts
  `claude --resume <id>` as a TUI instead: a task's `-p` never runs twice (D-42).
  `agentd ctl prepare-restart`. `agent-run --local`, and `attach` and `detach` through
  `kubectl exec` for Tom.
- [x] 5. **Idle detection** (4.2, D-59, this PR). agentd reads Claude's `sessions/<pid>.json` status,
  the attached tmux clients and v1's `wt_busy` signals, and the heartbeat reports
  them (the Codex signals come with plan 04's Codex pods).
- [x] 6. **Suspend, resume and the idle timer** (D-09, D-60, this PR). `POST
  /v1/sessions/{id}/suspend` and `/resume`, `agent-run suspend` and `resume`. The
  operator suspends a session idle past its window (a finished task after 1 h, an
  interactive session after 3 days), with the defaults in the templates and
  `spec.lifecycle` per session.
- [x] 7. **The archive timer** (D-62, this PR) (D-09: 7 days after suspension, from the templates).
  It archives a suspended session's volume after a valid rescue (the hold pod of step
  2 when it has none). `resume` refuses an archived session and points at a restore.
- [x] 8. **Logs** (D-65, this PR). agentd copies the task log to `~/.shared/logs/`; `GET
  /v1/sessions/{id}/log?tail=N` and `agent-run log`.
- [x] 9. **Messages** (D-16 tier 3, D-65, this PR). `POST /v1/sessions/{id}/messages`, `agentd ctl
  deliver` (a paste into the Claude TUI; `codex queue` for Codex) and `agent-run msg`.
- [x] 10. **Activities** (D-17, D-66, this PR; the haynes-ops half follows). The `Activity` CRD in `dev-env-system`,
  `/v1/activities` with v1's limits enforced by the API, expiry, and `declare-activity`
  as a client of it. In haynes-ops: the CRD, the operator's rights, read on the group
  for v1's OPERATOR tier and `dev-env-ops`, and `dev-activity-check.sh` reading both
  sources.
- [x] 11. **Rescues: list, restore and pruning.** `GET /v1/rescues`, `POST
  /v1/rescues/{id}/restore`, `agent-run rescue list|restore` (D-48's recipe), and
  bundles pruned after D-09's 30 days. *Built (D-67):* the shelf pod (`agentd
  shelf`, a haynes-ops Deployment) that the operator lists and prunes through,
  `GET /v1/rescues`, a restore as `restore` on `POST /v1/sessions` with the fetch on
  the first boot, the leader's pruner (logs too), and `agent-run rescue
  list|restore`. *Deployed 2026-10-08:*
  - haynes-ops #3551 added the CRD with `spec.restore`.
  - haynes-ops #3552 added the shelf Deployment, dev-env 2.7.0 and operator
    sha-035dfaa.

  *Verified 2026-10-08:*
  - The shelf pod started and found the shared volume ("shelf ready",
    `rescues=1`).
  - `agent-run rescue list` showed `dev-env-1007-050430/20261007-0506`, complete,
    kept until 2026-11-06.
  - `agent-run rescue restore` of that rescue made session `dev-env-1008-011600`.
    On its first boot, its clone held
    `refs/rescued/heads/rescue/dev-env-1007-050430-20261007-0506`, carrying plan
    01's uncommitted `e2e-probe.txt`. The worktree sat at origin/main, because the
    bundle held no agent branch. The hold moved the rescue's keep date to
    2026-11-07.
  - The reap rescued the new session (`CleanAndPushed`) and archived its volume.
  - The leader's first prune ran ten minutes after start: retention 720h, 0
    removed, 3 kept.
- [ ] 12. **The laptop path** (D-05, D-68; client built, laptop check pending). `agent-run` outside the cluster mints a
  `dev-env-human` token and port-forwards with the kubeconfig, and checks the API's
  certificate against the pinned CA by its service name. A handoff for an agent on
  Tom's laptop runs the check.
  - Built in #103; signed agent `2.8.0` shipped by #104 and
    `publish-agent.yml` run `37776465007`. Signed operator `sha-12a97c4` was
    published by run `37775926348`. Deployment pins: haynes-ops #3579.
  - **Deployed and verified 2026-10-08:** templates and operator Flux targets
    are Ready on `8f3e104`; operator is updated/ready/available 2/2, with both
    running image IDs matching its signed digest. `agent-run fleet` with v1's
    own identity returned revision `2.8.0-dcdc99d358`, no sessions. Shelf and
    v1 pod UIDs and every container's zero restart count were preserved. The
    scoped activity declaration was ended after verification.
  - Focused unit tests prove context consistency, human token refresh,
    service-name and CA refusal, proxy bypass, bounded forward startup and
    cleanup. Linux static and darwin/arm64 builds passed, with no Kubernetes
    dependency in the CLI. Full CI and Claude advisory review passed.
  - A CI run exposed a map-order assumption in the rescue API test fixture.
    #103 sorts the fixture's repo keys; its focused test and full CI passed.
  - **Pending:** Q-17 (working laptop admin kubeconfig), then the real
    list-and-attach check in
    [the laptop handoff](../../../handoffs/2026-10-08-laptop-access.md).
- [ ] 13. **The acceptance run** below, with the evidence under each item.

**The Codex half of "`agent-run msg` reaches a Codex session".** No v2 pod can run
Codex before plan 04, because the keeper owns its login there (D-12, S-3). Step 9
builds and tests `deliver`'s Codex path; the live check runs with plan 04's first
Codex session.

## Acceptance

- A `local` session is suspended after its idle window, resumed with
  `agent-run resume`, and `claude --resume` shows the earlier conversation.
  *Verified 2026-10-08* (session `dev-env-1008-001530`, Haiku 4.5, `--local
  --idle-suspend-after 4m`):
  - The agent's last activity was at 00:20:29Z. The operator suspended the
    session at 00:24:32Z with the event `IdleSuspend`. The rescue was
    `CleanAndPushed`, and `suspendedBy` read `idle-timer`.
  - `agent-run resume` started a new pod with `claude --resume` on the same
    conversation id. Asked by `agent-run msg` for the code word given before the
    suspend, the agent answered `PINEAPPLE-42`.
- A declared activity shows up in dev-env-ops' check output with the declaring
  session id. *Verified 2026-10-08:*
  - `declare-activity start` ran in session `dev-env-1008-001530`'s pod, by exec.
    The agent itself rightly declined the request, because it came by
    `agent-run msg`, which it treats as information, not instructions.
  - dev-env-ops' `dev-activity-check.sh dev-env-system` listed `act-002201-640020
    by=session/dev-env-1008-001530` as matched (haynes-ops #3546).
  - The run found jq 1.6 parsing `expiresAt` an hour late under the pod's New
    York time zone. haynes-ops #3549 fixed that, and the same bug in
    health-gate and alert-responder.
- `agent-run msg <id> "…"` reaches a Claude TUI session and a Codex session in
  another pod. *Claude, verified 2026-10-08:*
  - `agent-run msg dev-env-1008-001530` from the v1 pod delivered into the
    Haiku TUI.
  - The paste arrived framed as from `client/dev/dev-env`, and the agent replied
    `noted` as asked.
  - The Codex half runs with plan 04 (above).
- Tom lists and attaches to sessions from his laptop.
  **Pending:** the laptop client is built, signed and deployed, but this item requires
  Tom's actual laptop. An in-pod fleet query with v1's identity passed; v1
  cannot mint the human token or port-forward, so it cannot stand in for this
  acceptance check. The earlier acceptance items remain verified as above.
