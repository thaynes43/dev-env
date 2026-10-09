# Saga: distributed-dev-env (dev-env v2)

**Status:** design, Accepted 2026-10-06. Tom ratified ADR-001 as written (drafted
2026-10-05, his rulings folded in 2026-10-06): see the
[ratification summary](adrs/001-distributed-dev-env.md#ratification-summary) at the
top of ADR-001. Phase 1 is built: plan 01 (task mode) is done since 2026-10-07. The
operator and the keeper run in the cluster, the agent image 2.0.0 is released, and
the first task session ran from the v1 pod and opened its PR. The architecture is in
[ADR-001](adrs/001-distributed-dev-env.md) and the detail in
[DESIGN-001](designs/001-dev-env-v2.md). Tom ruled on every question, Q-01 to Q-11,
on 2026-10-06 and widened the scope: tool pods, a GPU budget, satellite inference
workers, local models, access without in-pod prompts, summoned sessions kept as a
first-class path, and one console for links, archives and the login renewal. Three
repo-setup questions: Q-13 (public package) and Q-14 (a GitHub App key secret) were
ruled on 2026-10-06, and Q-12 (branch protection) was settled on 2026-10-07, when Tom
made the repo public. Research notes
[R-01](research/R-01-summoned-agents-audit.md) (summoned agents) and [R-02](research/R-02-remote-control-identity.md) (Remote Control
identity) are folded into the design. The spikes in
[backlog 00](backlog/00-spikes.md) come first: S-1, S-3, S-6 and S-15 passed on 2026-10-06, S-7
and S-16 are done, and S-2 is answered (re-probed on CLI 2.1.292). The
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
- **dev-env-broker** (same namespace, same binary, own Deployment): temporary access
  grants. Standing policies in git can approve requests; none are currently live.
  A management console is planned for sessions, links, archive and login renewal.
  Human approvals, if implemented, belong inside the Claude Code app under Q-16;
  no route is selected or deployed. D-70 requires effective Headlamp parity before
  replacing that path, rather than treating its existing powers as new access.
- **dev-env-keeper** (same namespace): designed as the single owner of rotating
  credentials, the Max login and GitHub App keys. GitHub token issuance is built;
  keeper-owned Max login remains future work. Sessions receive short-lived GitHub
  and privileged grants; accepted reader/ADC baseline references remain under R-04.
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
   access tokens only. Spike S-1 proved the CLI behaviour this relies on (2026-10-06,
   DESIGN-001 6.2): an access-token-only file registers Remote Control on a cold home,
   and a merged token is picked up without a restart. Each refresh revokes the old
   token in every pod at once, so agentd must merge fast. The coordinator host pod
   stays the fallback for a CLI release that breaks this. v1 now
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
8. **The broker is powerful.** It creates temporary identities and bindings to
   named catalog roles. Human approval and the break-glass catalog are not deployed.
   The original restricted break-glass proposal does not replace Headlamp's
   cluster-admin task scope. Effective parity must be proved before removing that
   route; a separate broker workload alone does not establish an approval boundary.
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
| 1 | Where v2's saga and code live | **DECIDED** 2026-10-05 (Tom) | A new repo, **thaynes43/dev-env** (private at first, public since 2026-10-07, row 19), keeping the image name `ghcr.io/thaynes43/dev-env`. Manifests and pod config stay in haynes-ops (GitOps). Recorded in haynes-ops as ADR-001 of its dev-env saga. |
| 2 | Architecture: one pod per session run by an operator | **DECIDED** 2026-10-06 (Tom) | [ADR-001](adrs/001-distributed-dev-env.md) |
| 3 | Settled design decisions D-01 to D-37 (D-34 to D-37 added 2026-10-06) | **DECIDED** 2026-10-06 (Tom) with ADR-001; D-02, D-18, D-19 and D-20 **REVISED** 2026-10-06 | [DESIGN-001 section 16](designs/001-dev-env-v2.md#16-decisions-settled-in-this-design) |
| 4 | Q-01: build the pod-and-volume layer, or adopt kubernetes-sigs/agent-sandbox | **DECIDED** 2026-10-06 (Tom) | Build a small operator modelled on agent-sandbox (A). |
| 5 | Q-02: language for the operator and CLI | **DECIDED** 2026-10-06 (Tom) | Go, for the operator and a static `agent-run` (A). |
| 6 | Q-03: what happens to running sessions when the image or config changes | **DECIDED** 2026-10-06 (Tom) | Drain on idle, then resume the conversation on the new version (A). |
| 7 | Q-04: default session size and fleet cap | **DECIDED** 2026-10-06 (Tom) | Fleet cap rejected. Every pod has requests and limits (S/M/L presets, default M, low PriorityClass); the scheduler places them; no allocation logic to retune (D-21). Scope added: GPUs for local LLMs, tool pods. |
| 8 | Q-05: where repos, worktrees and agent state live | **DECIDED** 2026-10-06 (Tom) | A block volume per session plus a small shared CephFS (A), and agents may use gasha01: session volumes on `gasha01-rbd`, the shared volume on in-cluster CephFS (D-22). |
| 9 | Access model: no prompts in the pod, control at the platform, grants on request | **DECIDED** 2026-10-06 (Tom) | [DESIGN-001 6.10 to 6.13](designs/001-dev-env-v2.md#612-access-no-prompts-in-the-pod-control-at-the-platform) (D-23 to D-27, D-33) |
| 10 | Tool pods, GPUs and local LLMs | **DECIDED** 2026-10-06 (Tom) | [DESIGN-001 section 8](designs/001-dev-env-v2.md#8-tool-pods-gpus-and-local-llms) (D-28 to D-32) |
| 11 | Q-06: GPU placement for agents | **DECIDED** 2026-10-06 (Tom) | Dynamic allocation, not a static per-node rule: a VRAM budget per card, control-plane cards included (D-34, narrowed by Q-09); satellite inference workers on his Mac and PCs (D-35). |
| 12 | Q-07: do the root-equivalent credentials (Proxmox operator token, hw-ssh key) stay in every session pod? | **DECIDED** 2026-10-06 (Tom) | A: they move behind the broker as short-lived credential grants. |
| 13 | Q-08: how does every household pod get a CPU request? | **DECIDED** 2026-10-06 (Tom) | A: a Kyverno-generated LimitRange with a 50m default CPU request in every non-system namespace; a cluster-wide v1 fix in haynes-ops, merged and live 2026-10-06 (#3406). |
| 14 | Q-09: which household GPU apps may lend their burst VRAM to agents while idle? | **DECIDED** 2026-10-06 (Tom) | None: "None but I bring online more GPUs in cluster". Agents get only what is left above every household app's full reservation; new cards join the budget automatically (D-34). |
| 15 | Q-10: when may agents use Tom's satellite machines? | **DECIDED** 2026-10-06 (Tom) | A: only while awake and not in use by Tom; never woken. |
| 16 | Ratify ADR-001 | **DECIDED** 2026-10-06 (Tom: "Accept as written") | [Ratification summary](adrs/001-distributed-dev-env.md#ratification-summary) |
| 17 | Q-11: which link that survives restarts did Tom mean? | **DECIDED** 2026-10-06 (Tom) | The Claude Code auth (the Max `/login` on the PVC). The keeper is its sole owner; pods get access tokens only; the monthly renewal is a console page behind Authentik, replacing the chat relay; the console lists every session's link and status with an archive button; the codex hub keeps its own single enrolment (D-11, D-37). |
| 18 | Summoned sessions are a first-class requirement | **DECIDED** 2026-10-06 (Tom: "we need to preserve the functionality") | [DESIGN-001 3.7](designs/001-dev-env-v2.md#37-summoned-sessions), D-36, R-01 V-01 to V-17, plan 10 |
| 19 | Q-12: branch protection on this repo, if Tom's plan does not enforce rulesets on a private one | **DECIDED** 2026-10-07 (Tom) | B, public: "I made dev-env public so I can go to bed but make sure it's good and safe". Actions billing had stopped CI on the private repo. The ruleset is enforced free, and it is live since 2026-10-07 (laptop handoff part 3). [DESIGN-001 section 15](designs/001-dev-env-v2.md#15-open-questions). |
| 20 | Q-13: visibility of `ghcr.io/thaynes43/dev-env-operator` | **DECIDED** 2026-10-06 (Tom) | A, public: "Public package write a prompt for an agent on my laptop to flip it". A laptop agent flips it after B3's first publish ([handoff](../../handoffs/2026-10-06-tom-laptop-settings.md)). |
| 21 | Q-14: how release-please gets release PRs checked by CI | **DECIDED** 2026-10-06 (Tom) | A, a GitHub App key secret: "GitHub App key secret (Recommended)". Names for B4: variable `RELEASE_APP_ID`, secret `RELEASE_APP_PRIVATE_KEY`. The App also needs Issues read and write (release-please creates `autorelease:` labels). |
| 22 | D-38: the keeper as its own binary or a mode of the operator | **DECIDED** 2026-10-06 (agent, delegated by KICKOFF B1) | Its own binary, `dev-env-keeper`, shipped in the operator image; the broker stays a mode of the operator ([DESIGN-001 3.1](designs/001-dev-env-v2.md#31-components)). |
| 23 | D-39: what the AgentSession schema enforces, and which spec fields may change | **DECIDED** 2026-10-06 (agent, plan 01 step 1) | The per-session rules of 3.3 and 3.7 as CEL validations; spec immutable after create except `operatingMode` and `lifecycle`; per-caller rules (priority, fallback model, no `full` in a policy) stay in CallerPolicy's schema ([DESIGN-001 3.3](designs/001-dev-env-v2.md#33-the-agentsession-resource)). |
| 24 | D-40: how agentd reads its session, and the layout its `dev-init.sh` port keeps | **DECIDED** 2026-10-06 (agent, plan 01 step 4) | One JSON document in `AGENTD_SESSION` (`internal/agentd/protocol`); pod settings default to v1's paths; Playwright browsers linked from the image; onboarding and trust seeded ([DESIGN-001 3.6](designs/001-dev-env-v2.md#36-agentd-and-the-session-pod)). |
| 25 | D-41: how agentd reports its status | **DECIDED** 2026-10-06 (agent, plan 01 step 4) | `POST /v1/sessions/{name}/heartbeat` every 60 s with the pod's projected token; `agentd ctl status` prints the same document ([DESIGN-001 3.6](designs/001-dev-env-v2.md#36-agentd-and-the-session-pod)). |
| 26 | D-42: how agentd runs a task | **DECIDED** 2026-10-06 (agent, plan 01 step 4) | Once per volume, under `agentd run-agent` in tmux session `agent`, prompt on stdin; the pod's SIGTERM forwarded to the CLI, which gets 30 s ([DESIGN-001 3.6](designs/001-dev-env-v2.md#36-agentd-and-the-session-pod)). |
| 27 | D-43: how `agentd ctl rescue` commits | **DECIDED** 2026-10-06 (agent, plan 01 step 4) | v1's rules, committed through a copy of the index so the worktree stays as it was; it prints the refs origin lacks for step 5's bundle ([DESIGN-001 3.6](designs/001-dev-env-v2.md#36-agentd-and-the-session-pod)). |
| 28 | D-44: how the operator builds a session's pod and volume, and what the templates hold | **DECIDED** 2026-10-06 (agent, plan 01 step 2) | One bare pod and one volume per session from a strict `dev-env-templates`; placement, security and grace in code; no probes; never updated or deleted after create; a template change only marks the session `Outdated` ([DESIGN-001 3.6](designs/001-dev-env-v2.md#36-agentd-and-the-session-pod)). |
| 29 | D-45: what deleting or suspending a session does before rescue exists | **DECIDED** 2026-10-06 (agent, plan 01 step 2) | Deleting is a reap: a finalizer on the session and its volume holds both until rescue; one guarded function deletes pods, and a source test keeps it the only one ([DESIGN-001 5.1](designs/001-dev-env-v2.md#51-operator-broker-and-keeper-upgrades-never-touch-sessions)). |
| 30 | D-46: the `/v1` API of plan 01 | **DECIDED** 2026-10-06 (agent, plan 01 step 3) | HTTPS on 8443 on every replica, a TokenReview per request; callers by class (Tom, clients such as the workbench and the v1 pod, sessions by their pod's token); the token sets a session's parent, depth and profile; idempotency by label per caller; reap is a delete the rescue finalizer holds ([DESIGN-001 3.4](designs/001-dev-env-v2.md#34-the-api)). |
| 31 | D-47: how the `dev-agents` CPU and memory ceiling is enforced | **DECIDED** 2026-10-06 (agent, plan 01 step 8.6) | A Kyverno policy (`dev-env-require-cpu-limit`, haynes-ops #3470), not the LimitRange 7.2 first called for: a LimitRange makes the API server fill every unset limit with its default, so the "no CPU limit" rule could never fire. It denies a pod with no CPU limit, a CPU limit above 8, or a memory limit above 24Gi ([DESIGN-001 7.2](designs/001-dev-env-v2.md#72-size-classes)). |
| 32 | D-48: what `agentd ctl rescue` writes to the shared volume, and how it checks it | **DECIDED** 2026-10-06 (agent, plan 01 step 5) | One bundle per clone of its unpushed refs, thin against `origin/HEAD`, in `rescue/<session>/<stamp>/` with `manifest.json` last; verified, copied, read back and verified again before the report says `ok`; `--stop-agent` stops the CLI first ([DESIGN-001 3.6](designs/001-dev-env-v2.md#36-agentd-and-the-session-pod)). |
| 33 | D-49: what the session config ConfigMaps hold, and where Codex's `requirements.toml` goes | **DECIDED** 2026-10-06 (agent, plan 01 step 8.8a) | Four ConfigMaps in `dev-agents` from a v2 app in haynes-ops: v2 copies of v1's `mcp.json`, subagents, Codex config and `bashrc.sh` (no `GH_TOKEN` export), and a new `CLAUDE.md` for a session pod. `requirements.toml` keeps v1's floor, because bubblewrap still cannot run under the pod's seccomp profile, in its own ConfigMap mounted at `/etc/codex` in every pod ([DESIGN-001 3.6](designs/001-dev-env-v2.md#36-agentd-and-the-session-pod)). |
| 34 | D-50: `agent-run` v2 of plan 01 | **DECIDED** 2026-10-06 (agent, plan 01 step 7) | `-p`, `list`, `show`, `reap`, `fleet` with v1's verbs, flags and defaults; aliases and unhonoured effort levels refused before sending, from the API's own table; retries keep the idempotency key; the scheduler's reason printed at once; the API found in a session pod, minted for in any other pod, named by flags elsewhere; exit codes 0 to 5 ([DESIGN-001 3.5](designs/001-dev-env-v2.md#35-agent-run-v2)). |
| 35 | D-51: how the operator rescues, suspends and archives | **DECIDED** 2026-10-06 (agent, plan 01 step 5) | `agentd ctl rescue --stop-agent` by exec before a suspend deletes a pod that ran; the verdict in `status.rescue` before the delete; a resume supersedes it; only a reap archives, after a verified rescue of the volume's last pod; a rescue pod and the archive timer are plan 02's ([DESIGN-001 4.4](designs/001-dev-env-v2.md#44-rescue-before-reap)). |
| 36 | D-52: the minimal keeper | **DECIDED** 2026-10-06 (agent, plan 01 step 6) | The haynes-dev-bot token minted v1's way, from an App directory read at every mint, into `dev-agents/dev-env-gh-token` by one merge patch; every 40 minutes or two thirds of its life, retries 10 s to 5 minutes with jitter; one replica behind a Lease (plans 03 and 04 fence each refresh); Secrets `patch` only; ready while its token lives; nothing secret logged ([DESIGN-001 6.4](designs/001-dev-env-v2.md#64-github-app-token)). |
| 37 | D-53: how the agent image is built and published | **DECIDED** 2026-10-06 (agent, KICKOFF B5) | `images/agent/Dockerfile`; smoke-tested in CI on PRs; `dev-env:2.x.y` only, from a release tag by `publish-agent.yml`, signed keyless; Kyverno trusts it on `refs/tags/v2.*` ([DESIGN-001 D-53](designs/001-dev-env-v2.md)) |
| 38 | S-8: where size L's session volume lives | **DECIDED** 2026-10-07 (spike S-8) | `gasha01-rbd` was 1.55 to 1.81 times slower than `ceph-block` for a clone, `pnpm install` and one test file, under D-22's line of two, so every size stays on `gasha01-rbd` ([00-spikes](backlog/00-spikes.md#s-8-gasha01-rbd-against-ceph-block-phase-1)). |
| 39 | Who merges release-please release PRs | **DECIDED** 2026-10-07 (Tom: "Merge, and let agents merge releases") | Agents squash-merge a green release PR themselves, like any other PR, after checking the version and changelog are sane and `CI - Success` is green on its head. Replaces the earlier "Tom's call" rule in CLAUDE.md. |
| 40 | Pacing while the weekly plan quota is high | **DECIDED** 2026-10-07 (Tom, through the coordinator: "one plan at a time") | Plan 02 continues alone; plan 07 pauses after step 3 with step 4's WIP on branch `agent/plan07-install`, and resumes when the coordinator says so (the quota resets 2026-10-12). Plans 02 and 07 stay parallel in the plan, not in time. |
| 41 | Q-15: how a Proxmox credential grant gets its short-lived token | **DECIDED** 2026-10-07 (Tom) | A, the keeper mints it over SSH: "Keeper mints over SSH (Recommended)". With a certificate from its own SSH CA it runs `sudo pvesh create /access/users/dev-env@pve/token/<grant> --expire <end> --privsep 0` on a node and deletes the token at the grant's end. No new Proxmox user, no long-lived token in v2, port 22 from the keeper to the nodes. Unblocks plan 07 step 8 (paused, decision 40). |
| 42 | Resume plan 07 on Codex | **DECIDED** 2026-10-07 (Tom through the coordinator work order) | Codex resumes step 4, then builds egress grants and the operator expiry backstop, and deploys/verifies the broker. Plan 02 continues in parallel; each PR rebases on main. Step 4 installs kube grants in tmpfs with stdin-only, UID-fenced exec (D-63). Round 1 finished steps 4 and 5 (D-64) and verified H2 runtime, including real expiry with the broker stopped; temporary fixtures and the test session were removed. Human approval deployment follows step 6. |
| 43 | Q-16: parity first, approvals in the Claude Code app; dev-env v2 benched | **DECIDED** 2026-10-08 (Tom, from his phone) | No new guard may cut what agents do today (no Authentik, outpost, Traefik or postgres16 lockdown, no git review or CODEOWNERS gate); standing auto-approved grants cover the whole v1 capability set, and human approval gates only capabilities beyond it. Approvals happen inside the Claude Code app, not Pushover plus a web page. Plan 07 step 6 is redesigned; PR #90 and haynes-ops #3550 were closed with pointers (issue #91). v2 is benched from 2026-10-08 until the plan usage resets (the Claude weekly limit resets 2026-10-12). Full text: DESIGN-001 Q-16. |
| 44 | Resume v2 with the Codex coordinator | **DECIDED** 2026-10-08 (Tom's work order) | Codex resumes after its usage reset. Finish plan 02 laptop access and acceptance first, then the docs-only Claude Code approval spike, the v1 capability parity check, and keeper SSH minting. Preserve Q-15 and Q-16; use GPT-6.1 Sol subagents, one task worktree each, and ask Tom one question at a time. Laptop access follows D-68. |
| 45 | Session access follows Tom's current workflow | **DECIDED** 2026-10-08 (Tom, correcting Q-17) | Use the `agent-run` CLI or ask agents to start sessions; a web UI is another possible session-management client. No laptop kubeconfig ceremony or laptop test blocks plan 02. Validate the existing in-cluster CLI workflow instead. D-68 remains an optional external path whose real external-machine use is unverified. This supersedes decision 44's laptop acceptance requirement and preserves the remaining priority order and Q-16. |
| 46 | Effective v1 parity includes Headlamp access | **RECORDED** 2026-10-08 (coordinator fact-check prompted by Tom's question; D-70) | Agents already use Headlamp's cluster-admin identity. Secret reads, drains, snapshots and broad workloads are existing reachable powers, not enhancements merely because the direct OPERATOR role lacks them. Parity preserves accepted tasks through an equivalent route, including Tom's live directive for Headlamp work; this is not blanket standing admin access or a new owner ruling. Temporary identities, expiry, attribution and an approval workflow improve the access mechanism. No blanket direct admin grant or approval route is selected; the coordinator withdraws Q-18's earlier premise. |
| 47 | Retire Headlamp after guarded parity | **DECIDED** 2026-10-09 UTC (Tom, D-71) | Guardrails may be needed changes. Replace Headlamp with guarded access preserving accepted v1 tasks and existing owner rules; prove parity and guardrails, migrate callers, then retire it through GitOps. Headlamp is a migration fallback, not the final architecture. No specific approval implementation or blanket standing admin grant is selected. |

The full options, consequences and rulings for Q-01 to Q-16, and the premise
corrections withdrawing Q-17 and Q-18, are in
[DESIGN-001 section 15](designs/001-dev-env-v2.md#15-open-questions).

## Plan backlog

v1 stays live throughout. No v2 plan edits haynes-ops'
`apps/dev/dev-env/app/resources/**` (that restarts the v1 pod and every session in it).

| Plan | Depends on | Parallel? |
|---|---|---|
| [00: spikes](backlog/00-spikes.md) | nothing | yes, with Tom's answers |
| [01: foundation, task mode](backlog/01-foundation.md) (done 2026-10-07) | Q-01, Q-02, Q-04, Q-05 (all decided); spikes S-7, S-8, S-12 | |
| [02: interactive sessions and lifecycle](backlog/02-interactive-lifecycle.md) (done 2026-10-08 under Q-17's corrected scope) | 01 | with 07 |
| [07: access broker](backlog/07-access-broker.md) | 01; Q-07 (decided) | with 02 |
| [03: Remote Control](backlog/03-remote-control.md) | 02, 07's deployed broker core; R-03/Q-18 propose separating login/Remote Control core from management UI; spikes S-1, S-5, S-6, S-15 | |
| [04: rolling updates and Codex](backlog/04-rolling-updates-codex.md) | 02, Q-03 (decided); spikes S-3, S-4 | with 03 |
| [05: cutover from v1](backlog/05-cutover.md) | 03, 04, 07; Q-08's LimitRange live in haynes-ops; Tom's approval | |
| [08: tool pods](backlog/08-tool-pods.md) | 02; spike S-10 | with 03, 04 |
| [09: GPUs, satellites and local LLMs](backlog/09-gpu-local-llm.md) | 08; Q-06, Q-09, Q-10 (decided); spikes S-9, S-11, S-13, S-14 | |
| [10: summoned sessions](backlog/10-summoned-sessions.md) | 02, 03, 07; spike S-16 | with 04, 08 |
| [06: later](backlog/06-later.md) | 05 | each item on its own |

**MVP, and what comes after it.** The MVP is v2 replacing v1: plans 01, 02, 07, 03 and
04, ending with the cutover in plan 05, which needs Tom's written approval. Phase 1
(plan 01, task mode) is its first slice. The cutover does not wait for plan 10:
`dev-env-ops` keeps serving every summoned lane listed in plan 10 until plan 10
moves it. Everything after the MVP stays in this saga: 08 (tool pods),
10 (summoned sessions), 09 (GPUs and local LLMs) and the 06 items.
