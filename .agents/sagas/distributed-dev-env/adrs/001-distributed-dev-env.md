# ADR-001: Distributed dev-env, one pod per agent session, run by an operator

- **Status:** Proposed
- **Date:** 2026-10-05; Tom's rulings on Q-01 to Q-05 folded in 2026-10-06
- **Deciders:** Tom Haynes (owner). Drafted by an agent. Tom decided the repository
  location (2026-10-05) and the design's questions Q-01 to Q-05 (2026-10-06); the
  ADR itself is not yet Accepted.
- **Design:** [DESIGN-001](../designs/001-dev-env-v2.md)

## Context and problem statement

v1 of the dev-env is one pod in the `main` cluster (`dev/dev-env`, replicas 1,
`Recreate`). Every Claude Code and Codex session runs in it, in tmux, on one 256Gi
RWO volume. Since 2026-09-23 it runs on the control-plane nodes.

That shape has three problems.

1. **One blast radius for CPU.** The pod has no CPU limit. On 2026-10-05, from about
   23:00Z to 00:11Z, a flake-reproduction subagent ran dozens of busy loops and wide
   parallel vitest runs. Node talosm02 reached load 222 on 20 cores, and EMQX,
   traefik, authentik and cloudnative-pg, all on that node, went into liveness-kill
   loops. A CPU cap for v1 is pending as held draft haynes-ops#3381, but a cap on
   one shared pod only moves the starvation from the node to the other sessions.
2. **One blast radius for change.** Any image or config change rolls the pod and
   kills every session mid-turn. That is why dev-env image bumps can never
   auto-merge, and why changes under `app/resources/` wait for Tom as held drafts.
3. **One machine for everything.** The fleet cannot spread across nodes, cannot size
   one heavy build differently from a chat session, and dies whole with its node.

Tom's vision (2026-10-05) asks for: an operator with an API that creates and destroys
one pod per agent session, each with its own CPU and memory limits, spread across
the cluster and kept off the latency-critical masters; `agent-run` as a client that
runs anywhere; the operator pruning the fleet the way the worktree sweep and rescue
branches do today; rolling updates that move sessions to new versions while operator
upgrades never take sessions down; room for Codex to grow; and later, leases on the
local LLMs and GPUs.

His rulings of 2026-10-06 add to that. Pods carry requests and limits and Kubernetes
schedules them, so the cluster can grow without tuning a dispatcher; there is no
fleet cap. GPU use needs real design once local LLMs arrive. Agents should be able
to start specialised tools on other pods (image, video and audio generation,
transcription, Blender, 3D-printer tools), some needing a GPU. Agents may use the
Proxmox-hosted Ceph (gasha01). And today's security "feels overly restrictive":
agents cannot reach the web, their kubectl access is limited, and they exec into the
headlamp pod to do what he asks. He wants agents to skip their vendor's permission
prompts while he keeps control of cluster and external access, granted on request,
and wants local models such as Qwen to work the same way.

## Decision drivers

- No agent can starve a node that runs household or control-plane workloads.
- An operator or keeper upgrade never interrupts a running session.
- No rotating OAuth refresh token is ever used by two processes. Both the Claude Max
  login (2026-08-29) and Codex `auth.json` revoke the whole token family on reuse.
- Uncommitted or unpushed work is rescued before anything is deleted.
- Capacity grows with the cluster: placement is the Kubernetes scheduler's job, with
  no per-cluster numbers to retune.
- Agents work without approval prompts; the platform (RBAC, egress, grants) is the
  boundary, and Tom approves anything beyond the baseline from his phone.
- Household workloads, GPU ones included, always win over agents.
- Tom's experience stays the same or gets better: same `agent-run` verbs, Remote
  Control from the phone, the monthly login renewal, one Codex phone entry.
- GitOps stays the deploy path: config and manifests in haynes-ops, code here.

## Considered options

1. **Keep v1, add a CPU limit, move it off the masters.** Smallest change. Fixes the
   node starvation, not the shared blast radius, not the roll-kills-everything
   problem, and not spreading.
2. **Static per-flavor pods** (haynes-ops saga backlog 08: one pod for Claude, one
   for Codex). Halves each blast radius; keeps every other problem.
3. **One pod per session, run by our own operator** with an API, a credential keeper
   and an in-pod supervisor.
4. **Option 3, with kubernetes-sigs/agent-sandbox managing the pods and volumes.**
5. **Adopt Coder** (workspaces plus "Coder Tasks").

## Decision outcome

Chosen option: **3, one pod per session run by our own operator**, because it is the
only option that meets every driver, and the parts that make it hard (credential
ownership, Remote Control, rescue, drain-and-resume, grants) are ours to build under
any option. Tom ruled for 3 over 4 on 2026-10-06 (design Q-01): our own small
operator, written in Go (Q-02), with its resources modelled on agent-sandbox's so a
later switch stays mechanical. Option 5 is rejected: it brings a second control plane
(coderd, Postgres, Terraform templates, its own UI and auth) and solves none of the
problems above.

The parts, in one paragraph each, with the design section that specifies them:

- **Operator** (control plane only): an HTTPS API and controllers for
  `AgentSession`, `Activity`, `ToolPool`, `ToolSession` and `LLMLease`. It owns no
  running work. Session pods and volumes are owned by their `AgentSession`, never by
  the operator Deployment, and the CRDs are never pruned (DESIGN-001 3.1 to 3.4, 5.1).
- **Broker**: the operator binary in a second Deployment with its own ServiceAccount.
  It approves grant requests that match a standing policy in git, sends the rest to
  Tom as a Pushover link to an approval page behind Authentik, and creates and
  revokes time-boxed RoleBindings and network policies. Break-glass replaces the
  headlamp path (DESIGN-001 6.12).
- **Keeper**: the single owner of the Claude Max login, the Codex login and the
  GitHub App key. It writes only short-lived results (access tokens, the gh token)
  into Secrets that agent pods mount (DESIGN-001 6.2 to 6.4).
- **Session pod**: `tini`, an `agentd` supervisor, tmux and one agent (Claude Code,
  Codex or opencode for local models), running with no approval prompts; worker
  nodes only; a size class that presets requests and mandatory limits, placed by the
  scheduler with no fleet cap; its own volume on gasha01. v1's OPERATOR-tier verbs
  with a field-level admission guard that closes haynes-ops #3392's escalations;
  open web egress, with LAN and cluster targets behind grants; nothing in the three
  dev-env namespaces, so no agent can reach the keeper, the broker or a sibling
  session except through the API (DESIGN-001 3.6, 6.6, 6.10 to 6.13, 7).
- **Tool pods**: specialised tools as `ToolPool`s that agents claim with a
  `ToolSession`; started on demand, stopped when idle, reached through a loopback MCP
  gateway in the session pod, placed by the scheduler, with a GPU when needed
  (DESIGN-001 8.1).
- **GPUs**: VRAM is counted through the device plugin by every GPU workload,
  household ones included; household pods preempt agent GPU pods, never the reverse;
  LLM pools and leases give agents model servers (DESIGN-001 8.2, 8.3).
- **`agent-run`**: a static CLI with v1's verbs that calls the API from any pod or a
  laptop (DESIGN-001 3.5).
- **Lifecycle**: idle detection from the agent's own status, timers, rescue to an
  in-cluster git bundle before any volume is deleted, resume with the conversation
  intact (DESIGN-001 4).
- **Rolling updates**: new sessions get the new revision; running sessions move on
  their next idle moment and resume (Tom's ruling on Q-03, 2026-10-06; DESIGN-001 5.2).
- **Storage**: a block volume per session on gasha01 (the Proxmox Ceph), which keeps
  agent disk load off the in-cluster OSDs on the control-plane nodes, and a small
  shared CephFS volume in the cluster (Tom's ruling on Q-05; DESIGN-001 6.6).
- **Repository**: code lives in **thaynes43/dev-env** (Tom, 2026-10-05); manifests
  and pod config stay in haynes-ops (DESIGN-001 10).

### Consequences

| ID | Consequence |
|----|-------------|
| C-01 | Good: a runaway agent is capped at its pod's CPU limit and never lands on a control-plane node. |
| C-02 | Good: an operator or keeper upgrade, or an operator outage, leaves running sessions untouched; only new-session and lifecycle actions pause. |
| C-03 | Good: image and config changes reach sessions on idle without cutting a turn, so v2 image bumps can auto-merge (Q-03 decided). |
| C-04 | Good: rotating credentials have exactly one owner, ending the class of bug that revoked the Max login on 2026-08-29. |
| C-05 | Good: a node failure costs only the sessions on that node, and they resume from their volumes. |
| C-06 | Bad: several new components to build and run (operator, keeper, agentd, CRDs). v1 keeps running until v2 proves out. |
| C-07 | Bad: the target Max-login design rests on undocumented CLI behaviour (spike S-1). The fallback, one coordinator host pod for Remote Control sessions, is proven but brings back a small shared pod. |
| C-08 | Bad: native ListAgents and SendMessage stop at the pod boundary; across pods only Remote Control sessions talk natively, and the rest go through an operator relay. |
| C-09 | Bad: each session clones its repo fresh, so start-up costs a clone (spike S-7 measures it; a shared mirror is the remedy for large repos). |
| C-10 | Neutral (revised 2026-10-06): agent pods get open web egress, broader than v1, and keep v1's Secrets on day one except what design Q-07 moves behind grants. A tricked agent can leak what its pod holds; the credential set, not the allowlist, is the control. |
| C-11 | Good: capacity grows with the cluster with no numbers to retune; a session that does not fit waits visibly in the scheduler's queue. |
| C-12 | Bad: with no fleet cap, the Max plan's own windows are the only brake on parallel sessions; the operator shows quota state but does not ration it. |
| C-13 | Good: agents never stall on a prompt, and anything beyond the baseline is a time-boxed, audited grant; the headlamp path is closed and replaced by break-glass. |
| C-14 | Bad: the broker can bind `cluster-admin`. It is the most privileged v2 component and must stay small, isolated from agents, and approve only on Tom's Authentik identity. |
| C-15 | Bad: GPU accounting needs every household GPU workload to declare its VRAM, a change to household manifests in haynes-ops; the accounting is cooperative, not enforced. |
| C-16 | Good: tools and local models become fleet members: started on demand, stopped when idle, and household AI is never displaced by agents. |
| C-17 | Neutral: session volumes depend on the Proxmox Ceph, an HDD-backed cluster outside Kubernetes. Its outage stops new sessions and stalls running ones; it already carries Prometheus and Loki. |

## More information

- Design: [DESIGN-001](../designs/001-dev-env-v2.md). Q-01 to Q-05 were ruled on by
  Tom on 2026-10-06; Q-06 (GPU tool pods on control-plane nodes) and Q-07
  (root-equivalent credentials behind the broker) are open. All are in its section 15.
- haynes-ops #3392: the v1 RBAC escalation finding that the baseline guard answers.
- v1 saga and its decision log: haynes-ops `.agents/sagas/dev-env/`. Its ADR-001
  records that v2 lives in this repo.
- agent-sandbox: <https://github.com/kubernetes-sigs/agent-sandbox>.
