# CLAUDE.md

Guidance for Claude Code and other agents working in this repository. Codex reads
`AGENTS.md`, which points here: this file is the one source of the rules.

**Start with [`.agents/HANDOFF.md`](.agents/HANDOFF.md).** It gives the current
state, what happens next, where every file is, the ruling index, and what an agent
can and cannot do outside the cluster.

## What this repo is

**dev-env v2**: the distributed version of the in-cluster development environment
where Tom's AI coding agents (Claude Code, Codex) work. v1 is one big pod in the
`main` cluster; v2 runs one pod per agent session, created and pruned by an operator
that agents and humans call through the `agent-run` CLI.

This repo will hold the operator, the CLI, the agent container image
(`ghcr.io/thaynes43/dev-env`) and their CI. The **deploy manifests stay in
[thaynes43/haynes-ops](https://github.com/thaynes43/haynes-ops)** (GitOps via Flux):
this repo publishes signed images, haynes-ops pins and deploys them.

**Status: phase 1 built (2026-10-07).** Plan 01 is done: the `AgentSession` CRD,
the operator's pod and volume reconciler and `/v1` API, agentd, rescue, suspend and
archive, the minimal keeper, `agent-run` v2, CI, and the agent image `dev-env:2.0.0`
(KICKOFF B1 to B5, plan 01 steps 1 to 9). The operator and the keeper run in the
cluster, and the first end-to-end task ran on 2026-10-07. Plan 02 is done under
Tom's 2026-10-08 correction: acceptance uses the existing in-cluster CLI, and
external CLI access is optional (Q-17). Plan 07's docs-only approval spike is complete (R-03, Q-18 pending);
the v1 capability parity audit is complete (R-04); all twenty gaps block cutover. The build sessions follow
[`KICKOFF.md`](.agents/sagas/distributed-dev-env/KICKOFF.md). v1 keeps running from
haynes-ops (`kubernetes/main/apps/dev/dev-env/`, `scripts/dev-env/Dockerfile`) until
v2 proves itself and Tom approves the cutover. The saga:
[`.agents/sagas/distributed-dev-env/`](.agents/sagas/distributed-dev-env/README.md).

## Layout

```
.agents/HANDOFF.md       the front door: state, next steps, rulings, inside vs outside
.agents/sagas/<saga>/    saga README (vision, decision log), KICKOFF, adrs/, designs/, backlog/, research/
.github/workflows/       ci.yml (lint, test, build, check-generated, image builds; aggregate `CI - Success`), publish.yml (operator image from main, signed), publish-agent.yml (agent image from a v2.x.y tag, signed), release-please.yml, Claude review + @claude
.github/renovate.json5   Renovate config (gomod, Actions SHA pins, Dockerfile digests, ARG and Makefile pins)
release-please-config.json, .release-please-manifest.json   one repo version, `go` type, first release 2.0.0
api/v1alpha1/            CRD types, group dev-env.haynesops.com (AgentSession; AccessGrant and GrantPolicy, D-54; Activity, D-66); depends on apimachinery only.
                         Its envtest suite proves the schema's rules (D-39) on a real API server
cmd/dev-env-operator/    the operator, and the access broker as its second mode, `dev-env-operator broker` (broker.go, D-61)
cmd/dev-env-keeper/      the keeper, its own binary in the operator image (D-38); main only, the work is internal/keeper
cmd/agentd/              the in-pod supervisor: `run`, `hold` (the rescue pod, D-55), `shelf` (the shelf pod, D-67), `run-agent`
                         (a task, or a TUI, D-58), `render`, `ctl status|rescue [--stop-agent]|prepare-restart` (D-40 to D-43,
                         D-48, D-58), `ctl deliver|log` (D-65), `ctl rescues|hold-rescue|prune` (D-67),
                         `ctl grant-install|grant-remove|grant-list|grant-use` (D-63)
cmd/agent-run/           the CLI, one static binary (CGO_ENABLED=0, D-06); main only, the commands are internal/agentrun
internal/version/        the build identity every binary's `version` prints
internal/testenv/        starts envtest (kube-apiserver + etcd) with config/crd/ installed, for test suites
internal/templates/      parses and checks dev-env-templates, the GitOps data pods are built from; its revision (D-44)
internal/controller/     the AgentSession reconciler: each session's pod and volume (D-44), the reap finalizer and the
                         guarded deletes (D-45): the rescue by exec before a suspend deletes a pod, and the archive of
                         a reaped session's volume after a verified rescue (D-51), in a hold pod when the volume has no pod (D-55). Its envtest suite proves DESIGN-001
                         5.1 (no owner reference to the operator; no pod or volume write or delete outside the guards;
                         delete and suspend wait for rescue); a fake rescuer stands in for exec
internal/apiserver/      the operator's /v1 API (D-46): HTTPS runnable, TokenReview auth, caller classes, sessions,
                         heartbeat, suspend and resume (D-60), fleet, grants (D-56), activities (D-66) and rescues (D-67) handlers. Unit tests use the fake client; its envtest suite mints real
                         tokens and serves through a manager wired as the operator's
internal/podexec/        the one pods/exec client (WebSocket, SPDY fallback): the rescue, and the API's log and message routes (D-65)
internal/apiserver/apiv1/  the API's wire types, error codes and Claude effort table, standard library only, for agent-run
internal/activity/       the reaper of expired declare-activity declarations (D-66), a leader-only runnable in the operator
internal/shelf/          the operator's side of the shelf pod (D-67): finds it, lists and prunes rescues there by exec; the pruner is
                         a leader-only runnable
internal/agentrun/       agent-run's commands (D-50, D-58, D-60, D-65, D-66, D-67): -p, --local, list, show, log, msg, reap, suspend, resume, rescue list|restore, declare-activity, attach, detach, fleet; finds the API and a token in a session
                         pod, in another pod (a minted token) or from flags; imports the standard library, apiv1 and
                         agentd's protocol only (`make build` checks); tests run against an httptest TLS server
internal/keeper/         the keeper (D-52): mints the haynes-dev-bot token into dev-agents/dev-env-gh-token every 40 minutes
                         behind a Lease; one Job per credential, so plans 03, 04 and 10 add theirs. Unit tests use a fake
                         GitHub (httptest) and a fake clock; its envtest suite runs it as its ServiceAccount with exactly
                         the Roles haynes-ops gives it
internal/broker/         the access broker (D-61): decides AccessGrants (GrantPolicy match, Decide for the approval page,
                         a Notifier), makes kube, break-glass and egress grants (a ServiceAccount and RoleBindings or a
                         ClusterRoleBinding per grant) and revokes them; ExecInstaller installs kube tokens on stdin into the pod's grants tmpfs (D-63), guarded by pod UID; older pods fail/revoke after three attempts. Its envtest
                         suite runs it as dev-env-broker under haynes-ops' RBAC and broker guard, copied into testdata/
internal/egress/         the scoped CiliumNetworkPolicy builder and ownership checks (D-64), shared by broker and operator
internal/grantexpiry/    the operator's separate egress expiry backstop (D-64), watches grants, reads CNPs by name only,
                         deletes by verified ownership and UID, and preserves the broker's grant audit record
internal/agentd/         agentd: config rendering (the dev-init.sh port), clone and worktree, the task runner, heartbeat,
                         status, rescue and its bundle on the shared volume (D-48); tests fake claude and tmux and run
                         git against a bare repo in t.TempDir()
internal/agentd/protocol/  what agentd and the operator exchange: the session document, the status, the heartbeat route,
                         the rescue report, and the rescue layout and manifest on the shared volume
config/crd/              CRDs generated by controller-gen; haynes-ops gets a copy by PR with every change, before the
                         operator pin that needs it (a stale copy prunes status fields silently; haynes-ops #3502)
images/operator/         Dockerfile: ghcr.io/thaynes43/dev-env-operator (operator + keeper); build args COMMIT, VERSION
images/agent/            Dockerfile: ghcr.io/thaynes43/dev-env, tag line 2.x (D-53); smoke-test.sh; tools/ (pve, hw-ssh, v1 copies). CI only, never in the pod
images/THIRD_PARTY.md    what each published image bundles and under which license; update it with the Dockerfiles.
                         It and LICENSE are copied into both images, whose license directories CI checks (#55)
Makefile                 generate, check-generated, lint, test, envtest, build, build-agent-run-darwin, tools
.golangci.yml            golangci-lint v2 config
LICENSE                  MIT, copyright Tom Haynes
```

Go module `github.com/thaynes43/dev-env`, with a `go` and a `toolchain` line in
`go.mod`. Built binaries and the pinned tools (controller-gen, golangci-lint,
setup-envtest) go to `bin/`, which git ignores. `make generate` after any change to
`api/`: it rewrites `zz_generated.deepcopy.go` and `config/crd/`, and both are
committed.

Tests: `make test` runs the unit tests and the envtest suites. setup-envtest downloads
kube-apiserver, etcd and kubectl into `bin/tools/envtest/` and passes the directory in
`KUBEBUILDER_ASSETS`. The API server's version is `ENVTEST_K8S_VERSION` in the
Makefile, which follows the main cluster's Kubernetes minor (1.35), not go.mod's client
libraries; bump it when the cluster moves. After `make envtest` once, a plain `go test`
finds the binaries too. A schema rule goes with a case in
`api/v1alpha1/agentsession_validation_test.go` that shows it accepting and refusing.
CEL rules must fit the API server's cost budget: give every string, list and map a
`MaxLength` or `MaxItems` that a rule reads, or the CRD install fails in envtest.

CI (`ci.yml`) runs the Makefile targets, so what passes in the pod passes there. The
one required check is the aggregate job `CI - Success`; the workflow has no `paths:`
filter, so a docs-only PR still gets it. Image builds run on PRs and never push.
`publish.yml` runs on push to main when an image build input changed (a paths filter),
and on `workflow_dispatch`; run it by hand after editing it. It pushes `dev-env-operator:sha-<short>` (never
`latest`, never an existing tag) and signs the digest with keyless cosign, pinned like
haynes-ops v1 (see the workflow header and haynes-ops #3092). It needs no Docker in the
pod: never build images here.

Releases: `release-please.yml` runs on every push to main and keeps one release PR
open (one version for the repo; both images are tagged with it, and the first release
is 2.0.0 because the agent image line is 2.x; it shipped on 2026-10-07, #52). Its
token is a GitHub App's (`thaynes43-dev-env-release`), from the repo variable
`RELEASE_APP_ID` and secret `RELEASE_APP_PRIVATE_KEY`, so release PRs run CI like any
PR; the automatic advisory review deliberately skips the release App's PRs.
Request an actual review with `@claude` through `claude.yml`, then read and resolve
its findings before merging. A skipped green job is not a review. If either goes
missing, it falls back to
`GITHUB_TOKEN`, and a PR opened that way starts no workflows: then close and reopen
the release PR (`gh pr close <n> && gh pr reopen <n>`) as haynes-dev-bot so
`CI - Success` runs, after every update to it. Agents squash-merge a green release PR
themselves, like any other PR, once they have checked that the version and changelog
are sane, the actual advisory review is resolved, and `CI - Success` is green on
its head (Tom, 2026-10-07: "Merge, and let
agents merge releases"). Merging it tags the release, so watch `publish-agent.yml` on
the new tag and confirm its `cosign verify` step passed. If no run appears (a tag made
with the `GITHUB_TOKEN` fallback starts none), start one with
`gh workflow run publish-agent.yml --ref v2.x.y`. Renovate auto-merge is on (since the Protect Main ruleset began requiring
`CI - Success`, 2026-10-07): minor and patch Go modules and GitHub Actions only;
`.github/renovate.json5` has the rules.

The agent image is `images/agent/Dockerfile` (D-53): v1's Dockerfile plus `tini`, agentd,
`agent-run`, Codex and `kubectl-cnpg`, with `pve` and `hw-ssh` in `images/agent/tools/`.
It is about 3.2 GB on disk, so **never build it in the pod**: `ci.yml` builds and smoke-tests it
(`images/agent/smoke-test.sh`) on PRs that change `images/agent/**`. `publish-agent.yml`
pushes `dev-env:2.x.y` from a `v2.x.y` release tag (or `gh workflow run publish-agent.yml
--ref v2.x.y` when the tag was made with `GITHUB_TOKEN`, which starts no workflow), never
`latest`, never an existing tag, and signs it keyless.

Every Makefile target caps Go at two CPUs and two packages at a time and runs under
`nice -n 19`, so it is safe in the shared pod (Hard rules). Do not raise
`GO_PARALLELISM` there.

Directories still to come: later controllers (ToolSession, LLMLease) beside
`internal/controller/`. Name each one here when it lands.

## How work happens here

- **Docs first.** A change in behaviour starts in the saga: ADR for the decision,
  design for the how, a backlog plan for the steps, then code. Docs change in the same
  PR as the behaviour they describe. An Accepted ADR is never edited; a new ADR
  supersedes it.
- **Questions go to Tom one at a time**, with the AskUserQuestion tool, at the moment
  they come up. Record each one as a `Q-NN` entry in the design (options with the
  recommended one first, each with its consequence), then fold his answer back in as
  a dated ruling. Check a question's premise before you ask it.
- **Worktree per task, never push to main.** Branch `agent/<task>`, open a ready PR,
  squash-merge it yourself once required checks are green and the review findings
  are handled.
- **Plain writing.** Short sentences, concrete nouns, no filler. Diagrams as mermaid.

## Hard rules

- **No CPU burners on any shared node**, the dev-env pod included. No busy loops,
  stress tools or wide parallel test runs, and a CPU limit on anything you run in
  the cluster. On 2026-10-05 a flake-reproduction run in the v1 pod (which had no CPU
  limit) pushed control-plane node talosm02 to load 222 on 20 cores from 23:42 to
  00:12Z, and EMQX, traefik, authentik and cloudnative-pg went into liveness-kill
  loops. The kubelet was not starved (it peaked at 0.18 cores): BestEffort pods,
  with no CPU request and so CPU weight 1, got no CPU on the saturated node. Cap test
  workers (for example vitest `--maxWorkers=2`) and run one suite at a time.
- **Never commit secrets.** That covers Claude Max credentials and login URLs or
  codes, `CLAUDE_CODE_OAUTH_TOKEN`, Codex `auth.json`, GitHub App keys and minted
  tokens, 1Password values, SSH keys and Proxmox tokens. This repo is public (since
  2026-10-07, DESIGN-001 Q-12), like haynes-ops: everything committed, history and
  PR branches included, is published. Keep household details, private addresses and
  session transcripts out too.
- **One owner per rotating refresh token.** Two processes refreshing the same Claude
  Max login or Codex `auth.json` revokes the whole token family (proven on
  2026-08-29). Designs here must keep a single owner for each.
- **Full model ids, never aliases** (`claude-opus-5-5`, not `opus`).
- **Running agent sessions are sacred.** Upgrading the operator must never delete or
  restart an agent pod. Removing a session's volume always comes after its rescue.

## Every repo gets the Claude Code PR reviewer

Standing rule (Tom, 2026-09-30): every repo gets the advisory Claude Code PR reviewer
and the `@claude` handler. This repo was set up with haynes-ops
[`.agents/runbooks/new-repo-setup.md`](https://github.com/thaynes43/haynes-ops/blob/main/.agents/runbooks/new-repo-setup.md).
Keep both workflows when you restructure CI, and use the same runbook for any new
repo this project creates.

## Automated PR review (agents read this)

Every non-draft PR gets an advisory review from Claude Code
(`.github/workflows/claude-code-review.yml`); `@claude` mentions are handled by
`claude.yml`. Release App PRs are excluded from the automatic job: explicitly
request their review with `@claude` before merge, as described above. The review is
**advisory**: it is not a required check. Read its
findings before merging. Fix each one, or answer it on the PR with a concrete reason
it is wrong; never "merging anyway". Both workflows need the Claude GitHub App on the
repo and the `CLAUDE_CODE_OAUTH_TOKEN` repo secret, otherwise they skip green and
review nothing.
