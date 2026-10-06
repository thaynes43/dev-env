# Saga: distributed-dev-env (dev-env v2)

**Status:** design, Proposed (2026-10-05). Nothing is built. The architecture is in
[ADR-001](adrs/001-distributed-dev-env.md) and the detail in
[DESIGN-001](designs/001-dev-env-v2.md). Five questions wait on Tom (Q-01 to Q-05,
below); the spikes in [backlog 00](backlog/00-spikes.md) can run before they are
answered.

**Working rules:** this repo's [CLAUDE.md](../../../CLAUDE.md). Docs first. Ask Tom
one question at a time with AskUserQuestion, and fold each answer back into the
design and the decision log below with its date.

**v1** is the single dev-env pod deployed from haynes-ops
(`kubernetes/main/apps/dev/dev-env/`; saga `.agents/sagas/dev-env/` there). It keeps
running, unchanged, until Tom approves the cutover in phase 5.

## Vision (Tom, 2026-10-05)

| # | Tom's point | Where the design answers it |
|---|---|---|
| 1 | An operator with an API that creates and destroys agent pods, one per session, each with its own CPU and memory requests and limits, spread across nodes, keeping the control-plane masters (EMQX, traefik) protected | DESIGN-001 sections 3, 7 |
| 2 | `agent-run` becomes a CLI that calls that API and runs anywhere: in an agent pod, on a laptop, from a phone-driven session | 3.4, 3.5 |
| 3 | The operator spins up and prunes the fleet, replacing worktrees, the stranded-session sweep and rescue branches | 4 |
| 4 | Rolling updates move sessions to a new version; upgrading the operator never takes sessions down | 5 |
| 5 | Codex needs will grow: the remote-control daemon, phone pairing, the requirements floor, MCP rendering, auth | 6.3 |
| 6 | Later: agents reserve time on the local LLMs and 3090 GPUs; design the seam now | 8 |

## Architecture at a glance

- **dev-env-operator** (namespace `dev-env-system`): control plane only. HTTPS API,
  `AgentSession` and `Activity` resources, idle detection, rescue, drain-and-resume.
- **dev-env-keeper** (same namespace): the single owner of every rotating credential
  and of the GitHub App key. Agent pods only ever get short-lived tokens.
- **Session pods** (namespace `dev-agents`, worker nodes only): one agent each, with
  `tini`, an `agentd` supervisor, a size class with mandatory CPU and memory limits,
  and its own volume. v1's OPERATOR-tier RBAC and egress at most.
- **`agent-run`**: one static CLI with v1's verbs, calling the API from anywhere.
- Shared state, item by item, is in DESIGN-001 section 6.

## Hard news

1. **The Max login cannot be shared by pods.** Two processes refreshing one Claude Max
   login revoked it mid-task on 2026-08-29. Remote Control needs that login; the
   static token cannot register Remote Control. The target design gives pods access
   tokens only, which relies on CLI behaviour that spike S-1 must prove. If it fails,
   Remote Control sessions share one coordinator host pod.
2. **Native agent messaging stops at the pod boundary.** Claude Code ties its session
   registry, inbox sockets and locks to the pid namespace. Across pods, only Remote
   Control sessions talk natively; everything else goes through the operator.
3. **More parts, more to run.** An operator, a keeper, an in-pod supervisor and CRDs
   replace one pod and some bash. That is the price of limits, spreading and rolling
   updates.
4. **More parallel sessions burn the Max plan faster.** The plan's 5-hour and weekly
   windows are shared with Tom's own use. The fleet cap is a quota, not just capacity
   planning.
5. **Each session clones fresh.** Large repos (haynes-quest: 934M of history) may need
   a shared mirror.
6. **Agent pods still hold broad credentials on day one.** Profile `full` keeps v1's
   Secrets and egress so nothing regresses; tightening comes after.

## Decision log

| # | Decision | Status | Outcome |
|---|---|---|---|
| 1 | Where v2's saga and code live | **DECIDED** 2026-10-05 (Tom) | A new private repo, **thaynes43/dev-env**, keeping the image name `ghcr.io/thaynes43/dev-env`. Manifests and pod config stay in haynes-ops (GitOps). Recorded in haynes-ops as ADR-001 of its dev-env saga. |
| 2 | Architecture: one pod per session run by an operator | **PROPOSED** 2026-10-05 | [ADR-001](adrs/001-distributed-dev-env.md) |
| 3 | Settled design decisions D-01 to D-19 | **PROPOSED** with ADR-001 | [DESIGN-001 section 16](designs/001-dev-env-v2.md#16-decisions-settled-in-this-design) |
| 4 | Q-01: build the pod-and-volume layer, or adopt kubernetes-sigs/agent-sandbox | **OPEN** | recommended: build |
| 5 | Q-02: language for the operator and CLI | **OPEN** | recommended: Go |
| 6 | Q-03: what happens to running sessions when the image or config changes | **OPEN** | recommended: drain on idle, then resume |
| 7 | Q-04: default session size and fleet cap | **OPEN** | recommended: S/M/L, default M (4 CPU / 8Gi), cap 24 pods and 48 CPU of limits |
| 8 | Q-05: where repos, worktrees and agent state live | **OPEN** | recommended: a ceph-block volume per session plus one small shared CephFS volume |

The full options and consequences for Q-01 to Q-05 are in
[DESIGN-001 section 15](designs/001-dev-env-v2.md#15-open-questions).

## Plan backlog

v1 stays live throughout. No v2 plan edits haynes-ops'
`apps/dev/dev-env/app/resources/**` (that restarts the v1 pod and every session in it).

| Plan | Depends on | Parallel? |
|---|---|---|
| [00: spikes](backlog/00-spikes.md) | nothing | yes, with Tom's answers |
| [01: foundation, task mode](backlog/01-foundation.md) | Q-01, Q-02, Q-04, Q-05 | |
| [02: interactive sessions and lifecycle](backlog/02-interactive-lifecycle.md) | 01 | |
| [03: Remote Control](backlog/03-remote-control.md) | 02; spikes S-1, S-2, S-5, S-6 | |
| [04: rolling updates and Codex](backlog/04-rolling-updates-codex.md) | 02, Q-03; spikes S-3, S-4 | with 03 |
| [05: cutover from v1](backlog/05-cutover.md) | 03, 04; Tom's approval | last |
| [06: later](backlog/06-later.md) | 05 | each item on its own |
