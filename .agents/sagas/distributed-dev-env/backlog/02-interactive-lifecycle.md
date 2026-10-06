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
- Operator: the timers of D-09, the reaper, `suspend`, `resume`, `restore`,
  `/v1/sessions/{id}/log` (logs also copied to the shared volume), the child-session
  limit (4 per parent, depth 2).
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
