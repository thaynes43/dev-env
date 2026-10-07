# 02: interactive sessions and lifecycle

**Status:** backlog
**Depends on:** 01
**Parallel with:** nothing

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
    volume" edge of 4.1.
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

## Acceptance

- A `local` session is suspended after its idle window, resumed with
  `agent-run resume`, and `claude --resume` shows the earlier conversation.
- A declared activity shows up in dev-env-ops' check output with the declaring
  session id.
- `agent-run msg <id> "…"` reaches a Claude TUI session and a Codex session in
  another pod.
- Tom lists and attaches to sessions from his laptop.
