# HANDOFF: start here

The front door for any agent that opens this repo, in the cluster's dev-env pod or on
Tom's own machine. Read this page, then [CLAUDE.md](../CLAUDE.md) (the rules), then
the saga. To start building, follow
[KICKOFF.md](sagas/distributed-dev-env/KICKOFF.md).

## State on 2026-10-06

- **The dev-env v2 design is complete.** It covers the architecture (ADR-001), the
  details (DESIGN-001, D-01 onward), 16 spikes, backlog plans 00 to 10 and two
  research notes.
- **Every design question is ruled except Q-12.** Tom answered Q-01 to Q-11, Q-13
  and Q-14 on 2026-10-06 (index below). Q-12 (branch protection) is open but not
  asked yet: the repo has no ruleset (`GET /repos/thaynes43/dev-env/rulesets`
  returned `[]` on 2026-10-06), and the question's premise is GitHub's warning when
  the ruleset is created, which happens once `CI - Success` has reported, after B2.
  Ask Tom then. The settings only Tom can click are in a handoff for an agent on his
  laptop (below).
- **ADR-001 is Accepted.** Tom ratified it on 2026-10-06: "Accept as written".
  DESIGN-001 is Accepted with it.
- **KICKOFF B1, the Go skeleton, is built** (#14). The module, the `AgentSession` types and
  CRD, the four binaries and the Makefile are on main; only `agent-run version` does
  real work. D-38 records that the keeper is its own binary. B2 added `ci.yml` and the
  operator Dockerfile; B3 added `publish.yml`, which pushes and signs
  `dev-env-operator:sha-<short>` from main. The agent image (B5) is still to come. The
  operator package stays private until Tom flips it (laptop handoff, part 2).
  **B4 added Renovate and release-please** (#18): the first release is 2.0.0 (one repo
  version on the agent image's `2.x` line). Until Tom adds the release App, the
  release PR is opened with `GITHUB_TOKEN`, so close and reopen it as haynes-dev-bot
  to get `CI - Success` to run (again after each update); never merge it without
  asking. Renovate auto-merge is off until the ruleset exists.
- **Plan 01 step 1, the `AgentSession` CRD, is built** (#24). The schema enforces
  the per-session rules as CEL, and spec is immutable after create except
  `operatingMode` and `lifecycle` (D-39). `make test` runs an envtest suite against
  kube-apiserver 1.35 (the main cluster's minor) that proves each rule;
  `internal/testenv` starts it, for later suites too.
- **Plan 01 step 4, agentd, is built** (#25, #27, #28). `agentd run` renders the
  config (the `dev-init.sh` port), clones the repo and adds the worktree, runs the
  task once in tmux session `agent` on the static token, heartbeats to
  `POST /v1/sessions/{name}/heartbeat`, and forwards the pod's SIGTERM to the CLI.
  `agentd ctl status` and `ctl rescue` answer the operator (D-40 to D-43). The
  operator's heartbeat route is step 3's; the pod that sets agentd's inputs is
  step 2's; the bundle is step 5's.
- **Plan 01 step 2 is built** (#29, #30). `dev-env-operator` runs a
  controller-runtime manager whose reconciler builds each session's pod and volume
  from `dev-env-templates` (D-44: the format, placement, no probes, 60 s grace). It
  creates what is missing and never updates a pod or volume; envtest proves that
  across an operator restart and a template change. Deleting a session is a reap
  (D-45): a finalizer keeps the session and its volume until rescue, and the one
  guarded delete path refuses until plan 01 step 5 builds `rescued()`. Until then a
  deleted session stays, with `RemovalBlocked`, unless it never got a pod or volume.
- **The v1 bounce landed on 2026-10-06.** haynes-ops #3381, #3342, #3336, #3294 and
  #3274 merged at about 21:05Z, and #3330 at 21:15Z. #3241 was closed. The v1 pod
  restarted at 21:17Z on `ghcr.io/thaynes43/dev-env:0.6.8` with a CPU limit of 8.
- **Spikes S-1, S-1b and S-7 are done** (2026-10-06). S-1 and S-1b passed, so D-11
  takes the keeper-owned target (DESIGN-001 6.2). S-7 found no repo needs a mirror.
- **S-6, S-15 and S-16 are done** (2026-10-06). S-6 passed: `--resume` brings back the same
  Remote Control entry with its history. It also found that a SIGTERM to the CLI
  archives the entry and the resume unarchives it (so agentd forwards the pod's
  SIGTERM to the CLI), and that agentd must seed
  `oauthAccount` (DESIGN-001 6.2, 6.7). S-16: the keeper can read plan usage with
  `GET /api/oauth/usage` (7.3). S-15 passed: the CLI's archive call works on an
  access token and the entry leaves Tom's list; `claude --resume` alone unarchives
  it (6.7).
- **v1 keeps running.** The single dev-env pod and the `dev-env-ops` executor are
  deployed from haynes-ops until the cutover (plan 05) and plan 10.
- **Q-08 is live.** Kyverno `default-cpu-request` (haynes-ops #3406) went live on
  2026-10-06. At 03:34Z no Running pod in the cluster was BestEffort.

## What happens first

1. **Tell Tom about the laptop handoff** ([`handoffs/2026-10-06-tom-laptop-settings.md`](handoffs/2026-10-06-tom-laptop-settings.md)).
   Part 1 (auto-merge, GHCR Actions access, Renovate, the release-please App) is
   needed before B4 and B5. Part 2 (make the operator package public) waits for B3's
   first publish. Part 3 (the Protect Main ruleset) waits for `CI - Success` to report
   once, after B2. Q-12 is asked then, if GitHub will not enforce it.
2. **Track B, plan 01: the repo skeleton and CI.** B1 (the Go skeleton) is done;
   B2 (CI), B3 (`publish.yml`) and B4 (Renovate, release-please) are done; continue with B5 in
   [KICKOFF section 3](sagas/distributed-dev-env/KICKOFF.md#3-track-b-the-first-prs-in-this-repo).
   Those PRs depend on no spike, so they run while the in-pod spikes run
   ([KICKOFF section 1](sagas/distributed-dev-env/KICKOFF.md#1-objective-of-phase-1)
   says why). ADR-001 is Accepted, so code may start.
3. **Collect the spike results.** S-1, S-1b and S-7 are done, and S-2 is already
   answered (the static token cannot register Remote Control). S-6, S-15 and S-16
   are done, and S-3 passed (keeper-owned Codex auth, DESIGN-001 D-12 step 2), so
   group 1 is complete. Group 2 (S-12, S-8) waits for its phase-1 haynes-ops PRs.
   Each result lands in its own PR. The order and pass criteria are in
   [KICKOFF section 2](sagas/distributed-dev-env/KICKOFF.md#2-track-a-spikes).
4. **The rest of the MVP, then beyond it.** The MVP ends at the cutover (plan 05);
   [the saga README](sagas/distributed-dev-env/README.md#plan-backlog) lists which
   plans are in it, their order, and the plans that follow.

## Where everything is

| Path | What it holds |
|---|---|
| [CLAUDE.md](../CLAUDE.md) | The rules for this repo, the single source. Codex reads [AGENTS.md](../AGENTS.md), which points there. |
| [.agents/sagas/README.md](sagas/README.md) | Saga conventions: statuses and the D/Q/C/S id schemes. |
| [distributed-dev-env/README.md](sagas/distributed-dev-env/README.md) | Tom's vision, the architecture at a glance, the hard news, the decision log and the plan index. |
| [distributed-dev-env/KICKOFF.md](sagas/distributed-dev-env/KICKOFF.md) | The work order for the first build session. |
| [adrs/001-distributed-dev-env.md](sagas/distributed-dev-env/adrs/001-distributed-dev-env.md) | The architecture decision (Accepted 2026-10-06), with the ratification summary at the top and consequences C-01 to C-21. |
| [designs/001-dev-env-v2.md](sagas/distributed-dev-env/designs/001-dev-env-v2.md) | The detail: components, API, lifecycle, credentials, RBAC, egress, GPUs. Spikes are in section 13, risks in 14, Q-01 to Q-14 with rulings (Q-12 still open) in 15, the decisions (D-01 onward) in 16. |
| [research/R-01](sagas/distributed-dev-env/research/R-01-summoned-agents-audit.md) | An audit of summoned agents today, with v2 requirements V-01 to V-17. |
| [research/R-02](sagas/distributed-dev-env/research/R-02-remote-control-identity.md) | Remote Control identity, the evidence behind S-1, and proposals P-1 to P-12. |
| [backlog/00-spikes.md](sagas/distributed-dev-env/backlog/00-spikes.md) | S-1 to S-16: steps, safety rules and pass criteria. |
| [backlog/01-foundation.md](sagas/distributed-dev-env/backlog/01-foundation.md) | Plan 01: operator, agentd, `agent-run`, the agent image, task mode on the static token. |
| [backlog/02](sagas/distributed-dev-env/backlog/02-interactive-lifecycle.md) | Plan 02: interactive sessions, idle detection, suspend, resume, laptop access. |
| [backlog/03](sagas/distributed-dev-env/backlog/03-remote-control.md) | Plan 03: Remote Control, the keeper-owned Max login and the console. |
| [backlog/04](sagas/distributed-dev-env/backlog/04-rolling-updates-codex.md) | Plan 04: drain on idle with resume, and the codex hub. |
| [backlog/05](sagas/distributed-dev-env/backlog/05-cutover.md) | Plan 05: cutover from v1 (needs Tom's written approval). |
| [backlog/06](sagas/distributed-dev-env/backlog/06-later.md) | Later items, each a future plan. |
| [backlog/07](sagas/distributed-dev-env/backlog/07-access-broker.md) | Plan 07: the access broker, grants and break-glass. |
| [backlog/08](sagas/distributed-dev-env/backlog/08-tool-pods.md) | Plan 08: tool pods (Blender, audio, image, whisper, printer, video). |
| [backlog/09](sagas/distributed-dev-env/backlog/09-gpu-local-llm.md) | Plan 09: the VRAM budget, LLM pools, satellites and opencode. |
| [backlog/10](sagas/distributed-dev-env/backlog/10-summoned-sessions.md) | Plan 10: summoned sessions move into v2 and `dev-env-ops` retires. |
| [.github/workflows/](../.github/workflows/) | The Claude advisory review and the `@claude` handler. Build CI arrives with plan 01. |
| haynes-ops [`.agents/sagas/dev-env/adrs/001-v2-lives-in-own-repo.md`](https://github.com/thaynes43/haynes-ops/blob/main/.agents/sagas/dev-env/adrs/001-v2-lives-in-own-repo.md) | Accepted: v2 lives here, and v1 and every manifest stay in haynes-ops. |

## Rulings (Tom, 2026-10-06; full text in DESIGN-001 section 15)

| Id | Ruling |
|---|---|
| Q-01 | Build our own small operator, modelled on kubernetes-sigs/agent-sandbox. |
| Q-02 | Go for the operator, broker, agentd and a static `agent-run`. |
| Q-03 | When the image or config changes, drain each session on idle and resume its conversation on the new version. |
| Q-04 | No fleet cap. Every pod gets requests and limits from S/M/L presets (default M) and the scheduler places it. |
| Q-05 | A block volume per session on `gasha01-rbd`, plus a small shared CephFS volume in the cluster. |
| Q-06 | A dynamic VRAM budget per card, control-plane cards included. Larger models run on satellite machines. |
| Q-07 | The Proxmox operator token and the hw-ssh key leave session pods and come as short-lived broker grants. |
| Q-08 | A Kyverno LimitRange gives every non-system namespace a 50m default CPU request (live, haynes-ops #3406). |
| Q-09 | No household GPU app lends VRAM. Agents get only what is left, and that grows as GPUs are added. |
| Q-10 | Satellites are used only while awake and not in use by Tom, and are never woken. |
| Q-11 | The link that survives restarts is the Claude Max login. The keeper owns it, and the console renews it each month. |
| Q-13 | Make `ghcr.io/thaynes43/dev-env-operator` public (A). "Public package write a prompt for an agent on my laptop to flip it": a laptop agent flips it after B3's first publish, because GitHub has no API for package visibility. |
| Q-14 | A GitHub App key secret for release-please (A): "GitHub App key secret (Recommended)". Repo variable `RELEASE_APP_ID`, repo secret `RELEASE_APP_PRIVATE_KEY`; the App also needs Issues read and write for `autorelease:` labels. |

**Open** (full entry in DESIGN-001 section 15):

| Id | Question | Recommended |
|---|---|---|
| Q-12 | Branch protection on this private repo, if his plan does not enforce the ruleset. **Not asked yet:** the repo has no ruleset, and the premise is GitHub's warning when it is created, once `CI - Success` has reported (after B2). Ask Tom then, as one question. | A: GitHub Pro (B: make the repo public; C: convention only) |

**Settings only Tom can click** (no decision needed). An agent on his laptop does
them from [`handoffs/2026-10-06-tom-laptop-settings.md`](handoffs/2026-10-06-tom-laptop-settings.md),
which lists exactly what to change:

- [ ] Part 1, before B4 and B5: on the `ghcr.io/thaynes43/dev-env` package, "Manage
      Actions access": give `thaynes43/dev-env` Write (before KICKOFF B5).
- [ ] Part 1: repo settings: allow auto-merge (Renovate's `platformAutomerge` needs
      it).
- [ ] Part 1: the Mend Renovate app covers this repo.
- [ ] Part 1: the release-please GitHub App (Q-14), with repo variable
      `RELEASE_APP_ID` and repo secret `RELEASE_APP_PRIVATE_KEY` (before B4's
      release-please part). Until then the workflow's `GITHUB_TOKEN` fallback cannot
      open the release PR at all (it failed on 2026-10-06), so also allow Actions to
      create PRs (handoff part 1, step 5).
- [ ] Part 2, after B3's first publish: make `ghcr.io/thaynes43/dev-env-operator`
      public (Q-13).
- [ ] Part 3, after `CI - Success` has reported once (B2): the Protect Main ruleset on
      the default branch, with `CI - Success` required. If GitHub will not enforce it
      on this private repo, that is Q-12. **Then an agent flips Renovate's auto-merge
      on:** set `automerge: true` in the first two `packageRules` entries of
      `.github/renovate.json5` (Go modules and GitHub Actions) (B4 left it off).

## Working rules (the summary; CLAUDE.md is the source)

- **Worktree per task.** Branch `agent/<task>` and never push to main.
- **Open ready PRs, not drafts.** The `Claude advisory review` check posts findings.
  Read every one. Fix it, or answer it on the thread with a concrete reason it is
  wrong. "Merging anyway" is not an answer.
- **Merge your own green PRs** with a squash merge, once required checks pass and
  every finding is handled.
- **Only questions wait on Tom.** Ask one at a time, when it comes up, with the
  recommended option first. Claude Code uses AskUserQuestion; Codex asks in its
  conversation. Record each question as a `Q-NN` in the design, then fold Tom's
  answer back in as a dated ruling and update the decision log. Check a question's
  premise before you ask it.
- **Docs first.** A behaviour change starts in the saga and lands in the same PR as
  its code. An Accepted ADR is never edited; a new ADR supersedes it.

## Hard constraint: no CPU burners

No CPU burners, stress tools, busy loops or wide parallel test runs on any shared
node. Set a CPU limit on anything you run in the cluster. Cap test parallelism
(`go test -p 2`, vitest `--maxWorkers=2`) and run one suite at a time.

Why: from 23:42Z on 2026-10-05 to 00:12Z on 2026-10-06, a dev-env subagent's busy
loops plus parallel vitest runs saturated talosm02. Its BestEffort pods (EMQX,
traefik, authentik, cloudnative-pg) got no CPU and went into liveness-kill loops.
The kubelet itself peaked at only 0.18 cores.

## Inside the cluster or outside it

| Capability | In the v1 dev-env pod | Outside (Tom's machine) |
|---|---|---|
| gh, git push, GHCR | Yes, as haynes-dev-bot. Prefix commands with `GH_TOKEN=$(cat /creds/gh_token)`. | Yes, with your own gh login, so PRs author as Tom. GHCR pulls of public images are anonymous. |
| Code, Go builds, unit tests, envtest | Yes, lightly, under the pod's CPU cap. | Yes. |
| `kubectl`, `flux` | OPERATOR tier: read plus targeted runtime writes. | None. |
| `declare-activity` | Yes. | No. It writes files on the pod's volume. |
| MCP servers on cluster DNS (grafana-mcp, home-assistant, blender, audio, vexa, the haynesnetwork hop) | Yes. | No. |
| The Claude Max login and Codex `auth.json` | Yes, on the pod's volume. Never copied out. | No. Never fetch them. |

**Spikes by where they can run:**

- **Hand to an in-pod session.** These need the live Max login or Codex auth on the
  v1 volume, or a measurement taken inside the cluster: S-1 (and S-1b), S-3, S-6,
  S-7, S-15, S-16, and the S-2 re-probe on each CLI bump.
- **Through haynes-ops PRs that deploy test workloads.** An in-pod session then reads
  the results: S-8 and S-12 in phase 1, and S-9 and S-13 on talosw04 before plan 09.
- **Inside v2 session pods, once they exist:** S-10 (phase 2), S-5 (phase 3), S-4
  (phase 4) and S-11 (before plan 09).
- **Outside is fine:** S-14 steps 1 to 3, on Tom's own machines with Tom there. Step
  4 needs a session pod.

To hand a spike to the pod from outside, file an issue here that links the spike's
section in `00-spikes.md`. Then ask Tom (one question) to dispatch it:
`agent-run --repo dev-env --agent claude --effort xhigh -p "Run spike S-N per
thaynes43/dev-env#<issue>"`.

## Cross-repo boundaries

- **This repo** holds code (operator, broker, keeper, agentd, `agent-run`,
  `dev-env-satellite`), image builds, CI and the saga.
- **thaynes43/haynes-ops** holds every manifest and all pod config, changed only
  through PRs that Flux applies. Never `kubectl apply`. No v2 PR touches
  `kubernetes/main/apps/dev/dev-env/app/resources/**`, because that restarts v1 and
  every session in it.
- **haynes-ops is public.** Nothing secret goes there: no tokens, login URLs or codes.
- **Most v2 changes are two PRs:** the code and image here, then the pin bump in
  haynes-ops.
- **Cluster-scoped haynes-ops PRs** (CRDs, admission policies, clusterwide network
  policies, Kyverno) may be authored from outside. They are merged and verified by
  an in-pod session that can declare activity and read the cluster.

## Open items elsewhere that touch v2

- [haynes-ops #3392](https://github.com/thaynes43/haynes-ops/issues/3392): v1's
  OPERATOR tier lets an agent act as another namespace's ServiceAccount. The fix
  for v1 is Tom's decision. v2 answers it with the baseline guard (D-19, S-12).
- [haynes-ops #3414](https://github.com/thaynes43/haynes-ops/issues/3414)
  (**pending**): gives `dev-env-ops` its own second Max login, so `wo-*` and `esc-*`
  sessions register Remote Control. The wiring is merged as
  [#3422](https://github.com/thaynes43/haynes-ops/pull/3422). The login itself still
  needs Tom's relay ceremony (haynes-ops
  [`.agents/runbooks/agentic-remediation.md`](https://github.com/thaynes43/haynes-ops/blob/main/.agents/runbooks/agentic-remediation.md),
  section "dev-env-ops Max login"). `rc-selftest.sh` printing
  `SELFTEST: REGISTERED` verifies it. The issue stays open until then. Until plan 10,
  Tom renews up to three Max logins a month.
- [haynes-ops #3415](https://github.com/thaynes43/haynes-ops/issues/3415): the
  executor is fragile. Config merges restart it, and a busy escalation lane holds
  new escalations without paging. Plan 10 retires the executor.
- [haynes-ops #3405](https://github.com/thaynes43/haynes-ops/issues/3405): Kyverno
  `kyverno.io/v1` ClusterPolicies need migrating to the CEL policy types. Write
  v2's new Kyverno policies on the new API where it can express them.
- Also relevant: [#3092](https://github.com/thaynes43/haynes-ops/issues/3092)
  (Kyverno reads legacy `.sig` cosign signatures, but cosign v3 writes bundles; this
  bears on how v2 images are signed) and the held v1 CPU cap
  [#3381](https://github.com/thaynes43/haynes-ops/pull/3381).
