# 04: rolling updates and Codex

**Status:** multiple-host/shared-file topology accepted in ADR-002/Q-21;
implementation and actual client/refresh acceptance remain backlog
**Depends on:** 02; Q-03 (drain on idle, then resume: decided 2026-10-06); spikes
S-3 and S-4
**Parallel with:** 03

## Goal

A new image or config reaches running sessions without cutting a busy turn, and Codex
gets multiple stable remote pod links that reach the same project/workspace files,
with private enrollment/runtime state and one refresh owner. The earlier one-hub
plan is superseded as the sole topology by owner requirement R7.

## Scope

- **Revisions:** the template ConfigMap's hash (D-04); `status.revision` per
  session; `GET /v1/fleet` lists outdated sessions.
- **Drain** per Q-03 (Tom 2026-10-06): on idle, `prepare-restart`, new pod on the new
  revision, resume. A session busy 72 h after going outdated gets a message; 24 h
  later Tom gets one Pushover line. `agent-run restart <id>` drains on demand.
- **Image pre-pull** DaemonSet on the workers (DESIGN-001 7.4).
- **Renovate:** with drain live, the `2.x` image may auto-merge (haynes-ops
  `.renovate/autoMerge.json5`: the v2 package gets the normal own-image rule; the v1
  carve-out stays until cutover).
- **Codex remote hosts (D-74):** at least two logical hosts/pods, each owning its
  private enrollment/daemon state. Phone links survive host replacement and both
  see the same project/workspace data. A single replacement hub is insufficient.
  Revise the agentd/operator/project contract and prove the actual pinned CLI
  path before enrollment or shared-storage implementation.
- **Planned Codex auth (S-3 passed 2026-10-06):** the keeper owns the Codex refresh and
  writes `dev-env-codex-live` (`id_token`, `access_token`, `account_id`, `exp`; no
  refresh token). agentd writes each pod's `auth.json` from it with an empty
  `refresh_token`, by atomic rename. Remote hosts must be validated on these too.
  The keeper's login comes from a fresh Codex login ceremony, never a copy of
  v1's or another live host's `auth.json`. Each remote host must use a validated
  access-token-only path. The keeper makes the
  refresh call itself, about a day before `exp`, because codex would wait until 5
  minutes before it (DESIGN-001 6.3). The refresh is fenced as plan 03 fences the
  Max login's (D-52): the keeper calls only while it holds its Lease with time to
  spare. Codex task and local sessions run in their own pods.
- **Codex forwarding (S-4):** `codex exec-server` remains a possible mechanism;
  reassess it against multiple remote hosts and R7 before implementation.

## Acceptance

D-78's [authentication workflow](../../../../docs/keeper-codex-auth.md) is being
implemented as a disabled source unit: bounded fresh helper login, private
durable refresh ownership and access-only agentd reload. Source tests precede
reviewed GitOps deployment and a real owner sign-in. Two paired hosts, rotation
and replacement remain acceptance work; this is not a claim that Codex creation
is already enabled.

- An image bump PR in haynes-ops reaches every idle session within an hour of
  merge; each resumes its conversation; no busy session restarts.
- Two independently enrolled Codex pods have usable phone links; shared project
  and task files resolve through consistent Git paths; a one-host drain leaves
  both links usable without competing enrollment or refresh state.
- A keeper refresh reaches a running Codex session and all remote hosts without
  a restart, and no pod calls the refresh endpoint.
- `requirements.toml` holds in every pod that runs Codex (approval `never`, sandbox
  `danger-full-access`).

Project catalog, common rules, task ownership, canonical health and shared
workspace acceptance live in [plan 11](11-project-workspaces.md). D-15/D-22 need
an explicit storage revision; do not share writable provider homes. No new
management UI or v1/auth migration is selected by this plan update.
