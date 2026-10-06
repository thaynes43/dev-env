# Saga: distributed-dev-env (dev-env v2)

**Status:** design, Proposed (2026-10-05; Tom's rulings folded in 2026-10-06), ready
for Tom to ratify: see the
[ratification summary](adrs/001-distributed-dev-env.md#ratification-summary) at the
top of ADR-001. Nothing is built. The architecture is in
[ADR-001](adrs/001-distributed-dev-env.md) and the detail in
[DESIGN-001](designs/001-dev-env-v2.md). Tom ruled on every question, Q-01 to Q-11,
on 2026-10-06 and widened the scope: tool pods, a GPU budget, satellite inference
workers, local models, access without in-pod prompts, summoned sessions kept as a
first-class path, and one console for links, archives and the login renewal. No
question is open. Research notes [R-01](research/R-01-summoned-agents-audit.md)
(summoned agents) and [R-02](research/R-02-remote-control-identity.md) (Remote Control
identity) are folded into the design. The spikes in
[backlog 00](backlog/00-spikes.md) come first, S-1 before all (S-2 is answered). The
first build session's work order is [KICKOFF.md](KICKOFF.md); the repo's front door is
[`.agents/HANDOFF.md`](../../HANDOFF.md).

**Working rules:** this repo's [CLAUDE.md](../../../CLAUDE.md). Docs first. Ask Tom
one question at a time with AskUserQuestion, and fold each answer back into the
design and the decision log below with its date.

**v1** is the single dev-env pod deployed from haynes-ops
(`kubernetes/main/apps/dev/dev-env/`; saga `.agents/sagas/dev-env/` there). It keeps
running, maintained in haynes-ops as today, until Tom approves the cutover in phase 5.

## Vision (Tom, 2026-10-05)

| # | Tom's point | Where the design answers it |
|---|---|---|
| 1 | An operator with an API that creates and destroys agent pods, one per session, each with its own CPU and memory requests and limits, spread across nodes, keeping the control-plane masters (EMQX, traefik) protected | DESIGN-001 sections 3, 7 |
| 2 | `agent-run` becomes a CLI that calls that API and runs anywhere: in an agent pod, on a laptop, from a phone-driven session | 3.4, 3.5 |
| 3 | The operator spins up and prunes the fleet, replacing worktrees, the stranded-session sweep and rescue branches | 4 |
| 4 | Rolling updates move sessions to a new version; upgrading the operator never takes sessions down | 5 |
| 5 | Codex needs will grow: the remote-control daemon, phone pairing, the requirements floor, MCP rendering, auth | 6.3 |
| 6 | Later: agents reserve time on the local LLMs and 3090 GPUs; design the seam now | 8.3 |
| 7 | (2026-10-06) Pods carry requests and limits; Kubernetes handles scheduling, so the cluster grows without tuning the dispatcher | 7.2, 7.3 |
| 8 | (2026-10-06) More advanced GPU handling when local LLMs arrive | 8.2, 8.3 |
| 9 | (2026-10-06) Agents spin up specialised tools on other pods, some with a GPU: image gen, whisper, Blender, 3D-printer tools, MiniMax and other video gen, audio gen | 8.1 |
| 10 | (2026-10-06) Agents may use the gasha01 storage | 6.6 |
| 11 | (2026-10-06) Agents skip their vendor's permission prompts; Tom keeps control of cluster and external access, which agents request; agents can reach the web; local models such as Qwen work the same way | 6.10 to 6.13 |
| 12 | (2026-10-06) GPU allocation adjusts dynamically to what the household needs VRAM for; nothing is lent, and more GPUs come online in the cluster; larger models run on satellite workers: the 128 GB M5 MacBook, the 5090 and 4090 PCs | 8.2 to 8.4 |
| 13 | (2026-10-06) Summoning a Max-plan agent from automation: "we need to preserve the functionality" | 3.7 |
| 14 | (2026-10-06) The Claude Code auth that survives restarts belongs to one owner, and its renewal is "baked into the front end", which also lists every session's link with an archive button | 3.8, 6.2, 6.7 |

## Architecture at a glance

- **dev-env-operator** (namespace `dev-env-system`, Go): control plane only. HTTPS
  API, `AgentSession`, `Activity`, `ToolPool`, `ToolSession`, `LLMLease` and
  `CallerPolicy` resources, idle detection, rescue, drain-and-resume, and the
  summoned-session lanes, watchdogs and digest that `dev-env-ops` runs today.
- **dev-env-broker** (same namespace, same binary, own Deployment): access grants and
  **the console**. Standing policies in git approve the routine; the rest goes to
  Tom's phone as a Pushover link. The console, behind Authentik, lists every session
  with its link and an archive button, takes approvals, and holds the monthly login
  renewal page. Break-glass replaces the headlamp path.
- **dev-env-keeper** (same namespace): the single owner of every rotating credential,
  the one Max login included, and of the GitHub App keys. Agent pods only ever get
  short-lived tokens.
- **Session pods** (namespace `dev-agents`, worker nodes only): one agent each
  (Claude Code, Codex or opencode for local models), with no approval prompts,
  `tini`, an `agentd` supervisor, requests and mandatory limits from a size class,
  placed by the scheduler with no fleet cap, and its own volume on gasha01. v1's
  OPERATOR-tier verbs under a field-level guard, open web egress, and nothing in the
  three dev-env namespaces.
- **Tool pods** (namespace `dev-tools`, any node that fits): Blender, audio, image,
  video, transcription, 3D-printer tools and local model servers, started on demand
  and stopped when idle; GPUs counted in VRAM; household apps keep their full
  reservation and agents get what is left, which grows as GPUs are added.
- **Satellites**: Tom's M5 MacBook (128 GB) and his 5090 and 4090 PCs serve models to
  agents through a small `dev-env-satellite` program, only while they are awake
  and he is not using them, never woken.
- **`agent-run`**: one static Go CLI with v1's verbs, calling the API from anywhere.
- Shared state, item by item, is in DESIGN-001 section 6.

## Hard news

1. **The Max login cannot be shared by pods.** Two processes refreshing one Claude Max
   login revoked it mid-task on 2026-08-29. Remote Control needs that login; the
   static token cannot register Remote Control (settled: the docs say so, and v1's
   executor saw 45 of 45 sessions rejected). The keeper owns the login and pods get
   access tokens only, which relies on CLI behaviour that spike S-1 must prove; R-02
   found the CLI re-reads its credentials file and revives Remote Control on a fresh
   token. If S-1 fails, Remote Control sessions share one coordinator host pod. v1 now
   has two logins (the dev-env pod's, and `dev-env-ops`'s from haynes-ops #3414, whose
   login ceremony is pending on 2026-10-06);
   v2's keeper replaces both with one, so Tom renews up to three a month until both
   v1 pods are gone.
2. **Native agent messaging stops at the pod boundary.** Claude Code ties its session
   registry, inbox sockets and locks to the pid namespace. Across pods, only Remote
   Control sessions talk natively; everything else goes through the operator.
3. **More parts, more to run.** An operator, a keeper, an in-pod supervisor and CRDs
   replace one pod and some bash. That is the price of limits, spreading and rolling
   updates.
4. **More parallel sessions burn the Max plan faster.** The plan's 5-hour and weekly
   windows are shared with Tom's own use. There is no fleet cap (Tom, 2026-10-06), so
   the plan's own wall is the brake; `agent-run fleet` shows it, and local-model
   sessions take bulk work off the plan.
5. **Each session clones fresh.** Large repos (haynes-quest: 934M of history) may need
   a shared mirror.
6. **Open web egress makes credentials the boundary.** Agents get the web they need,
   so a page that tricks an agent can leak whatever its pod holds. The allowlist never
   stopped that (github.com and both model vendors were always on it). So the
   root-equivalent credentials leave the default set and come as short-lived grants
   (Q-07, Tom 2026-10-06).
7. **GPU accounting touches household apps.** The scheduler cannot share GPUs fairly
   until every GPU workload, household ones included, declares its VRAM floor and
   burst. Today they pin cards by UUID and the scheduler sees nothing. Nothing is
   lent to agents (Q-09), so today the household reservations fill most cards and
   in-cluster agent GPU work stays small until Tom adds GPUs.
8. **The broker is powerful.** It can grant break-glass: every verb outside the
   dev-env namespaces except Secrets, token minting, RBAC and admission changes, for
   up to an hour. It runs apart from the operator, binds only a named catalog of
   roles, and approves only on Tom's Authentik login.
9. **Session volumes live outside the cluster.** gasha01 is HDD-backed Proxmox Ceph.
   Its outage stops new sessions and stalls running ones (DESIGN-001 6.6).
10. **No fleet cap means workers can saturate.** On 2026-10-05 the pods that failed
   were BestEffort (no CPU request, CPU weight 1); the kubelet was fine. A busy v2
   fleet could saturate a worker the same way, so every household pod needs a CPU
   request first (Q-08: a Kyverno LimitRange, live since 2026-10-06 as haynes-ops #3406).
11. **The big local models live on Tom's own machines.** The cluster's free VRAM is
   small (one worker 3090, already busy with the house). Large models run on the
   satellites, which are there only when Tom is not using them, so local-model agents
   wait or fall back to smaller models when he is.

## Decision log

| # | Decision | Status | Outcome |
|---|---|---|---|
| 1 | Where v2's saga and code live | **DECIDED** 2026-10-05 (Tom) | A new private repo, **thaynes43/dev-env**, keeping the image name `ghcr.io/thaynes43/dev-env`. Manifests and pod config stay in haynes-ops (GitOps). Recorded in haynes-ops as ADR-001 of its dev-env saga. |
| 2 | Architecture: one pod per session run by an operator | **PROPOSED** 2026-10-05 | [ADR-001](adrs/001-distributed-dev-env.md) |
| 3 | Settled design decisions D-01 to D-33 | **PROPOSED** with ADR-001; D-02, D-18, D-19 and D-20 **REVISED** 2026-10-06 | [DESIGN-001 section 16](designs/001-dev-env-v2.md#16-decisions-settled-in-this-design) |
| 4 | Q-01: build the pod-and-volume layer, or adopt kubernetes-sigs/agent-sandbox | **DECIDED** 2026-10-06 (Tom) | Build a small operator modelled on agent-sandbox (A). |
| 5 | Q-02: language for the operator and CLI | **DECIDED** 2026-10-06 (Tom) | Go, for the operator and a static `agent-run` (A). |
| 6 | Q-03: what happens to running sessions when the image or config changes | **DECIDED** 2026-10-06 (Tom) | Drain on idle, then resume the conversation on the new version (A). |
| 7 | Q-04: default session size and fleet cap | **DECIDED** 2026-10-06 (Tom) | Fleet cap rejected. Every pod has requests and limits (S/M/L presets, default M, low PriorityClass); the scheduler places them; no allocation logic to retune (D-21). Scope added: GPUs for local LLMs, tool pods. |
| 8 | Q-05: where repos, worktrees and agent state live | **DECIDED** 2026-10-06 (Tom) | A block volume per session plus a small shared CephFS (A), and agents may use gasha01: session volumes on `gasha01-rbd`, the shared volume on in-cluster CephFS (D-22). |
| 9 | Access model: no prompts in the pod, control at the platform, grants on request | **PROPOSED** 2026-10-06, from Tom's direction | [DESIGN-001 6.10 to 6.13](designs/001-dev-env-v2.md#612-access-no-prompts-in-the-pod-control-at-the-platform) (D-23 to D-27, D-33) |
| 10 | Tool pods, GPUs and local LLMs | **PROPOSED** 2026-10-06, from Tom's Q-04 ruling | [DESIGN-001 section 8](designs/001-dev-env-v2.md#8-tool-pods-gpus-and-local-llms) (D-28 to D-32) |
| 11 | Q-06: GPU placement for agents | **DECIDED** 2026-10-06 (Tom) | Dynamic allocation, not a static per-node rule: a VRAM budget per card, control-plane cards included (D-34, narrowed by Q-09); satellite inference workers on his Mac and PCs (D-35). |
| 12 | Q-07: do the root-equivalent credentials (Proxmox operator token, hw-ssh key) stay in every session pod? | **DECIDED** 2026-10-06 (Tom) | A: they move behind the broker as short-lived credential grants. |
| 13 | Q-08: how does every household pod get a CPU request? | **DECIDED** 2026-10-06 (Tom) | A: a Kyverno-generated LimitRange with a 50m default CPU request in every non-system namespace; a cluster-wide v1 fix in haynes-ops, merged and live 2026-10-06 (#3406). |
| 14 | Q-09: which household GPU apps may lend their burst VRAM to agents while idle? | **DECIDED** 2026-10-06 (Tom) | None: "None but I bring online more GPUs in cluster". Agents get only what is left above every household app's full reservation; new cards join the budget automatically (D-34). |
| 15 | Q-10: when may agents use Tom's satellite machines? | **DECIDED** 2026-10-06 (Tom) | A: only while awake and not in use by Tom; never woken. |
| 16 | Ratify ADR-001 | **PROPOSED** 2026-10-06 | [Ratification summary](adrs/001-distributed-dev-env.md#ratification-summary) |
| 17 | Q-11: which link that survives restarts did Tom mean? | **DECIDED** 2026-10-06 (Tom) | The Claude Code auth (the Max `/login` on the PVC). The keeper is its sole owner; pods get access tokens only; the monthly renewal is a console page behind Authentik, replacing the chat relay; the console lists every session's link and status with an archive button; the codex hub keeps its own single enrolment (D-11, D-37). |
| 18 | Summoned sessions are a first-class requirement | **DECIDED** 2026-10-06 (Tom: "we need to preserve the functionality") | [DESIGN-001 3.7](designs/001-dev-env-v2.md#37-summoned-sessions), D-36, R-01 V-01 to V-17, plan 10 |

The full options, consequences and rulings for Q-01 to Q-11 are in
[DESIGN-001 section 15](designs/001-dev-env-v2.md#15-open-questions).

## Plan backlog

v1 stays live throughout. No v2 plan edits haynes-ops'
`apps/dev/dev-env/app/resources/**` (that restarts the v1 pod and every session in it).

| Plan | Depends on | Parallel? |
|---|---|---|
| [00: spikes](backlog/00-spikes.md) | nothing | yes, with Tom's answers |
| [01: foundation, task mode](backlog/01-foundation.md) | Q-01, Q-02, Q-04, Q-05 (all decided); spikes S-7, S-8, S-12 | |
| [02: interactive sessions and lifecycle](backlog/02-interactive-lifecycle.md) | 01 | with 07 |
| [07: access broker](backlog/07-access-broker.md) | 01; Q-07 (decided) | with 02 |
| [03: Remote Control](backlog/03-remote-control.md) | 02, 07; spikes S-1, S-5, S-6, S-15 | |
| [04: rolling updates and Codex](backlog/04-rolling-updates-codex.md) | 02, Q-03 (decided); spikes S-3, S-4 | with 03 |
| [05: cutover from v1](backlog/05-cutover.md) | 03, 04, 07; Q-08's LimitRange live in haynes-ops; Tom's approval | |
| [08: tool pods](backlog/08-tool-pods.md) | 02; spike S-10 | with 03, 04 |
| [09: GPUs, satellites and local LLMs](backlog/09-gpu-local-llm.md) | 08; Q-06, Q-09, Q-10 (decided); spikes S-9, S-11, S-13, S-14 | |
| [10: summoned sessions](backlog/10-summoned-sessions.md) | 02, 03, 07; spike S-16 | with 04, 08 |
| [06: later](backlog/06-later.md) | 05 | each item on its own |
