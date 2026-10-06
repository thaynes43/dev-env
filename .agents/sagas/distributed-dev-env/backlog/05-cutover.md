# 05: cutover from v1

**Status:** backlog
**Depends on:** 03, 04, 07 (break-glass must exist before v1 and its headlamp habit
go), Q-08's LimitRange live in haynes-ops (no BestEffort household pods), and Tom's
explicit approval
**Parallel with:** nothing

## Goal

v2 carries all agent work. v1 is retired without losing anything on its volume.

## Scope

- **Workbench:** a small Deployment in `dev-agents` with code-server, `agent-run`
  and `kubectl`, behind traefik-internal and Authentik like v1. It runs no agents by
  default.
- **dev-env-ops** reads only `Activity` resources (if plan 10 has not already retired
  it; its summoning lanes move in plan 10, not here).
- **v1 drain:** announce, let v1 sessions finish or move them (`agent-run` v2 from
  inside v1), run `agent-run sweep` once more so v1's WIP lands on rescue branches,
  then scale v1 to zero. Its PVC stays 30 days.
- **v1's Max login is retired with the pod.** Tom stops renewing it; it lapses about
  30 days after its last `/login`. Nothing is copied to the keeper, which has had its
  own login since plan 03. The `claude-login-check` page and the v1 renewal runbook
  give way to the console's login page.
- **v1's Codex login is retired with the pod too.** Before scale-to-zero, stop v1's
  daemon from a shell inside the v1 pod, with v1's own `agent-run codex-remote stop`.
  (In v2 the same command stops the codex hub, DESIGN-001 3.5.) Nothing refreshes that
  login again, and its `auth.json` goes with the PVC. Nothing is copied to the keeper or the codex hub, which have had their own
  logins since plan 04 (DESIGN-001 D-12). The phone's v1 computer entry goes offline.
- **haynes-ops cleanup** after the 30 days: delete `apps/dev/dev-env`, the v1
  Dockerfile and build workflow, the v1 Renovate carve-outs and holds, and the
  Kyverno attestor for haynes-ops-built `dev-env*` images. These are held drafts,
  merged at a natural break, because they touch the v1 pod.
- Move v1's runbook knowledge that still applies (hw-ssh, pve, declare-activity)
  into this repo's docs and the pod CLAUDE.md in haynes-ops. The Max login renewal
  is now the console page (DESIGN-001 3.8).

## Acceptance

- Tom approves the cutover in writing (a dated ruling in the decision log).
- One week after scale-to-zero, nobody has needed v1.
- v1's PVC is deleted only after its rescue branches are bundled to the shared volume.
