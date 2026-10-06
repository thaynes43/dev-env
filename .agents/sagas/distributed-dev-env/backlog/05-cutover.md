# 05: cutover from v1

**Status:** backlog
**Depends on:** 03, 04, 07 (break-glass must exist before v1 and its headlamp habit
go), and Tom's explicit approval
**Parallel with:** nothing

## Goal

v2 carries all agent work. v1 is retired without losing anything on its volume.

## Scope

- **Workbench:** a small Deployment in `dev-agents` with code-server, `agent-run`
  and `kubectl`, behind traefik-internal and Authentik like v1. It runs no agents by
  default.
- **dev-env-ops** reads only `Activity` resources.
- **v1 drain:** announce, let v1 sessions finish or move them (`agent-run` v2 from
  inside v1), run `agent-run sweep` once more so v1's WIP lands on rescue branches,
  then scale v1 to zero. Its PVC stays 30 days.
- **haynes-ops cleanup** after the 30 days: delete `apps/dev/dev-env`, the v1
  Dockerfile and build workflow, the v1 Renovate carve-outs and holds, and the
  Kyverno attestor for haynes-ops-built `dev-env*` images. These are held drafts,
  merged at a natural break, because they touch the v1 pod.
- Move v1's runbook knowledge that still applies (Max login renewal, hw-ssh, pve,
  declare-activity) into this repo's docs and the pod CLAUDE.md in haynes-ops.

## Acceptance

- Tom approves the cutover in writing (a dated ruling in the decision log).
- One week after scale-to-zero, nobody has needed v1.
- v1's PVC is deleted only after its rescue branches are bundled to the shared volume.
