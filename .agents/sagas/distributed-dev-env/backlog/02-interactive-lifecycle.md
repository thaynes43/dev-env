# 02: interactive sessions and lifecycle

**Status:** in progress (started 2026-10-07)
**Depends on:** 01
**Parallel with:** 07 (the access broker)

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
- [ ] 7. **The archive timer** (D-09: 7 days after suspension, from the templates).
  It archives a suspended session's volume after a valid rescue (the hold pod of step
  2 when it has none). `resume` refuses an archived session and points at a restore.
- [ ] 8. **Logs.** agentd copies the task log to `~/.shared/logs/`; `GET
  /v1/sessions/{id}/log?tail=N` and `agent-run log`.
- [ ] 9. **Messages** (D-16 tier 3). `POST /v1/sessions/{id}/messages`, `agentd ctl
  deliver` (a paste into the Claude TUI; `codex queue` for Codex) and `agent-run msg`.
- [ ] 10. **Activities** (D-17). The `Activity` CRD in `dev-env-system`,
  `/v1/activities` with v1's limits enforced by the API, expiry, and `declare-activity`
  as a client of it. In haynes-ops: the CRD, the operator's rights, read on the group
  for v1's OPERATOR tier and `dev-env-ops`, and `dev-activity-check.sh` reading both
  sources.
- [ ] 11. **Rescues: list, restore and pruning.** `GET /v1/rescues`, `POST
  /v1/rescues/{id}/restore`, `agent-run rescue list|restore` (D-48's recipe), and
  bundles pruned after D-09's 30 days.
- [ ] 12. **The laptop path** (D-05). `agent-run` outside the cluster mints a
  `dev-env-human` token and port-forwards with the kubeconfig, and checks the API's
  certificate against the pinned CA by its service name. A handoff for an agent on
  Tom's laptop runs the check.
- [ ] 13. **The acceptance run** below, with the evidence under each item.

**The Codex half of "`agent-run msg` reaches a Codex session".** No v2 pod can run
Codex before plan 04, because the keeper owns its login there (D-12, S-3). Step 9
builds and tests `deliver`'s Codex path; the live check runs with plan 04's first
Codex session.

## Acceptance

- A `local` session is suspended after its idle window, resumed with
  `agent-run resume`, and `claude --resume` shows the earlier conversation.
- A declared activity shows up in dev-env-ops' check output with the declaring
  session id.
- `agent-run msg <id> "…"` reaches a Claude TUI session and a Codex session in
  another pod.
- Tom lists and attaches to sessions from his laptop.
