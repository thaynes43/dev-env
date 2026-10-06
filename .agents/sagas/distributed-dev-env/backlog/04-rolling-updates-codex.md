# 04: rolling updates and Codex

**Status:** backlog
**Depends on:** 02; Q-03 (drain on idle, then resume: decided 2026-10-06); spikes
S-3 and S-4
**Parallel with:** 03

## Goal

A new image or config reaches running sessions without cutting a busy turn, and Codex
gets its v2 home: one hub for the phone, then sessions in their own pods.

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
- **Codex hub:** a `codex-hub` session runs the remote-control daemon, owns the
  enrolment and (step 1) `auth.json`; the phone keeps one computer entry across
  drains. `agent-run codex-remote` manages it.
- **Codex step 2 (if S-3 passed):** the keeper owns the Codex refresh and writes
  `dev-env-codex-live`; Codex task and local sessions run in their own pods.
- **Codex step 3 (if S-4 passed):** hub threads execute in per-session pods through
  `codex exec-server`.

## Acceptance

- An image bump PR in haynes-ops reaches every idle session within an hour of
  merge; each resumes its conversation; no busy session restarts.
- A drain of the codex hub leaves the phone's entry working without re-pairing.
- `requirements.toml` holds in every pod that runs Codex (approval `never`, sandbox
  `danger-full-access`).
