# 04: rolling updates and Codex

**Launch tooling proposal, 2026-10-29:**
[DESIGN-038](../designs/038-remote-control-launch.md),
[OpenProject #267](https://work.haynesnetwork.com/work_packages/267), proposes
healthy paired-host reuse with a fresh Codex thread. Its story 3/3 waits for the
supported remote project inventory and adapter; a connected daemon or an issued
pairing code is insufficient. Documentation only: caller/profile restrictions,
keeper ownership, enrollment, history and active threads remain protected.

**Status:** multiple hosts with common rules/context and private repos under ADR-003;
implementation and actual client/refresh acceptance remain backlog
**Depends on:** 02; Q-03 (drain on idle, then resume: decided 2026-10-06); spikes
S-3 for the first coordinator route; S-4 for later native forwarding
**Parallel with:** 03

## Goal

A new image or config reaches running sessions without cutting a busy turn, and Codex
gets multiple stable remote pod links, common rules and authorized session context,
with private repos/worktrees, enrollment/runtime state and one refresh owner.
[ADR-003](../adrs/003-session-coordination-private-repositories.md) supersedes shared-Git dependencies.

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
- **Codex remote hosts (D-74/D-80):** at least two logical hosts/pods, each owning its
  private enrollment/daemon state. Phone links survive supported host replacement and both
  discover the same rules, tasks and permitted context. Git remains local to each pod.
  The [first route](../../../../docs/codex-coordinator-hosts.md) uses two GitOps
  `OnDelete` StatefulSets with retained private homes and per-host repo caches.
  Native app threads stay on their host and request writable Claude/Codex task
  pods through a dedicated child-scoped coordinator API identity. Implement and
  prove pinned startup, independent pairing and idle replacement before use.
- **Codex auth source merged #138 (`bcb2599`), runtime off (S-3 passed 2026-10-06):** the keeper owns the Codex refresh and
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
- **Codex forwarding (S-4):** a later possible mechanism, separate from the first
  coordinator route. Do not claim native threads automatically become task pods.

## First owner-test source and deployment steps

1. Complete keeper auth source/control review, signed images and isolated helper
   GitOps; make a fresh keeper-owned login and prove the immediate one-shot
   refresh action without copying v1 state.
2. Add the default-disabled coordinator caller policy with live host identity,
   explicit project/profile/mode constraints and direct-child authorization.
   Preserve existing human/client/session rights and task OPERATOR permissions.
3. Add managed Codex launch/JSONL identity/resume and access-only admission. Do not
   replay an unconfirmed initial prompt. Bind the accepted catalog/rule snapshot.
4. Add retained native host supervision/version/updater checks and authenticated
   pairing/status. Deploy the two bounded worker hosts after native lifecycle,
   credential adoption and retained-state prerequisites; shared-Git trials are not gates.
5. Prove real link, project/task rules, refresh, idle one-host replacement and
   native phone-prompt/answer acceptance. Publish the exact quick-start commands.

Automatic image drains and S-4 forwarding continue under the broader plan after
this route; an `OnDelete` declaration alone is not drain acceptance. Every provider-home archive needs retained history independent of Git topology.
The [old shared-only retention contract](../../../../docs/shared-private-home-retention.md)
is reusable source, not acceptance of the new private-session requirement.

## Acceptance

D-78's [authentication workflow](../../../../docs/keeper-codex-auth.md) is merged
as a disabled source unit: bounded fresh helper login, private
durable refresh ownership and access-only agentd reload. Source tests precede
reviewed GitOps deployment and a real owner sign-in. Two paired hosts, rotation
and replacement remain acceptance work; this is not a claim that Codex creation
is already enabled.

- An image bump PR in haynes-ops reaches every idle session within an hour of
  merge; each resumes its conversation; no busy session restarts.
- Two independently enrolled Codex pods have usable phone links; common rules,
  task discovery and stopped-history reads work with independent Git paths; a one-host drain leaves
  both links usable without competing enrollment or refresh state.
- A keeper refresh reaches a running Codex session and all remote hosts without
  a restart, and no pod calls the refresh endpoint.
- `requirements.toml` holds in every pod that runs Codex (approval `never`, sandbox
  `danger-full-access`).

Project catalog, common rules, task coordination, history retention and local
repo freshness live in [plan 11](11-project-workspaces.md). No shared Git rollout
is needed; do not share writable provider homes. No new
management UI or v1/auth migration is selected by this plan update.

## Source note, 2026-10-17

Idle drain (DESIGN-001 5.2, Q-03) is in the operator source. Q-03 is unchanged.
This slice's idle moment is the agent reporting idle or waiting, with no
attached client. `status.agent.attached` is the heartbeat's client count.
`status.restart` stores the conversation id, the pod UID, and the session
generation. That proof comes only from `agentd ctl prepare-restart --dry-run`,
which reads the launch record and the live attach count and does not stop the
agent. The existing stop runs only after the proof is stored and that count is
zero; then `deletePod` runs. A read that is not resumable, a proof that was not
written, or a live attach count above zero does not stop the agent and does not
delete the pod. Busy, summoned, private-history, canary, and pilot sessions are
not drained. The 72h session message and the Pushover line are deferred: they
are not sent, and drain does not wait on that notice. D-50 egress stays. The
existing ensure path then starts the replacement on the current revision and
the same volume. This note is source only. It does not deploy the operator.
The acceptance list above is unchanged.

## Source note, 2026-10-22

OpenProject #106 source slice. `agent-run fleet --resources` sends
`Dev-Env-Fleet: resources-v1` and prints the bound current Pod UID, the Pod
spec node, and each ordinary and init container's configured CPU and memory
requests and limits. The figures are the Pod spec. They are not measured use,
free node capacity, or a placement decision. A missing or unverified binding
is Unknown. The owner reference has to be that session's controller. A Pod list that reaches its own read deadline is Unknown and the fleet phases stay; a canceled caller still fails the request. A configured zero stays zero, and an unset quantity stays absent.
Fleet output without the flag is unchanged, and that request does not list
Pods. This note is source only. It does not schedule Pods or accept live
placement.
