# CLAUDE.md

Guidance for Claude Code and other agents working in this repository. Codex reads
`AGENTS.md`, which points here: this file is the one source of the rules.

For the current takeover, read the
[2026-10-13 Claude pickup brief](.agents/CLAUDE-RESUME-2026-10-13.md) in full. It
records the deployed state, counters, rulings, next work with owner gates and
the questions still to ask Tom. The
[2026-10-12 brief](.agents/CLAUDE-RESUME-2026-10-12.md) still holds Tom's
requirement history, the six-step roadmap and the failed routes. Earlier next
steps are historical; changing drivers does not reset stopped tasks or consumed
IDs.

The [workflow guide](docs/workflow-guide.md) gives the high-level feature set,
working quick start, Codex session model, freshness contract and delivery gates.
**Controlling scope: [ADR-003](.agents/sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md).** Agents share rules, coordination
and authorized live/stopped context. Repositories and task worktrees stay per pod.
Shared Git/RWX and its storage trial are no longer prerequisites. Delivery is in
[plan 11](.agents/sagas/distributed-dev-env/backlog/11-project-workspaces.md).

Before launching work that has an explicit hardware, host-operations, or
workbench role, follow the [launch-profile preflight](docs/launch-profile-preflight.md).
It documents the existing profile selectors and their limits; it grants neither
credentials nor approval.

**Start with [`.agents/HANDOFF.md`](.agents/HANDOFF.md).** It gives the current
state, what happens next, where every file is, the ruling index, and what an agent
can and cannot do outside the cluster.

## What this repo is

**dev-env v2**: the distributed version of the in-cluster development environment
where Tom's AI coding agents (Claude Code, Codex) work. v1 is one big pod in the
`main` cluster. V2 has bounded managed session pods and persistent Codex hosts,
managed through the operator/API. Native threads remain on their actual host;
only supported dispatch creates a separate worker pod.

This repo will hold the operator, the CLI, the agent container image
(`ghcr.io/thaynes43/dev-env`) and their CI. The **deploy manifests stay in
[thaynes43/haynes-ops](https://github.com/thaynes43/haynes-ops)** (GitOps via Flux):
this repo publishes signed images, haynes-ops pins and deploys them.

**Current scope, 2026-10-10:** preserve sessions through upgrades, distribute
load, guard powerful access, expose live/stopped work and context, and attribute
model usage/cost. Plans 01/02 provide the tested Claude task/terminal subset.
As of October 13, 2026 (EDT), the 2.28.0 shared-history correction is installed:
operator `sha-cbf0700` (actual binary version `v2.28.0-1-gcbf0700`, haynes-ops
#3906) and the signed 2.28.0 task agent (haynes-ops #3907). The
[delivery record](.agents/sagas/distributed-dev-env/verification/d92-shared-history-delivery-2026-10-13.md)
separates source, release, publication and the two verified GitOps updates.
Rule loading is accepted for both providers. Private GitHub authorization, a
`Ready` private catalog bootstrap and a fresh task pin are accepted for Claude.
HOME expiry (DESIGN-025 H1 to H6) is installed with only the terminal record
on; H4's `history-archive` waits for the shelf's H7 image. Full Codex coordination, history-preserving archive, guarded access and
truthful cost reporting remain unaccepted. Read the handoff for exact digests
and evidence. The
historical twenty-row audit guides migration; it is not a universal pilot gate.
V1 stays running until required workflows have replacements and Tom explicitly
approves retirement. Build sessions follow the
[2026-10-13 Claude pickup brief](.agents/CLAUDE-RESUME-2026-10-13.md) and its
next work, which continues the 2026-10-12 brief's six-step roadmap;
[`KICKOFF.md`](.agents/sagas/distributed-dev-env/KICKOFF.md) is the
historical first-build order. Deploys remain GitOps.

## Layout

```
.agents/HANDOFF.md       the front door: state, next steps, rulings, inside vs outside
.agents/sagas/<saga>/    saga README (vision, decision log), KICKOFF, adrs/, designs/, backlog/, research/
.github/workflows/       ci.yml (lint, test, build, check-generated, image builds; aggregate `CI - Success`), publish.yml (operator image from main, signed), publish-agent.yml (agent image from a v2.x.y tag, signed), publish-git-wait.yml (the git-wait trial fixture's
                         git-wait-sha-<sha> tag from main, signed), release-please.yml, Claude review + @claude
.github/renovate.json5   Renovate config (gomod, Actions SHA pins, Dockerfile digests, ARG and Makefile pins)
tools/h7-metadata-checker/  isolated Python metadata checker and bounded Mac adapter; no operator dependency or deployment
tools/h7-metadata-checker/celhelper/  separate Go module: pinned CEL lexer/parser, token comparison only
tools/h7-metadata-checker/transport/  separate Go module: existing-Mac GET-only transport, fixed authority and request budgets
.github/workflows/h7-native-continuation-diagnostic.yml  bounded hosted diagnostic for the exact synthetic native continuation fixture
tools/h7-retain-action/  isolated one-time history-PV retention action; owner decision required before execution
.github/workflows/h7-retain-action.yml  finite offline Retain-action fixtures and Mac artifact build
release-please-config.json, .release-please-manifest.json   one repo version, `go` type, first release 2.0.0
api/v1alpha1/            CRD types, group dev-env.haynesops.com (AgentSession; AccessGrant and GrantPolicy, D-54; Activity, D-66;
                         CredentialJob); depends on apimachinery only. Its envtest suite proves the schema's rules (D-39) on a real API server
api/coordination/v1alpha1/  CRD types, the separate protected group coordination.dev-env.haynesops.com (TaskClaim, InboxMessage,
                         InboxReadAck, PublishedContext, SessionDirectory); its CRDs go to config/crd/ and haynes-ops like the others
cmd/dev-env-operator/    the operator, and the access broker as its second mode, `dev-env-operator broker` (broker.go, D-61)
cmd/dev-env-keeper/      the keeper, its own binary in the operator image (D-38); main only, the work is internal/keeper
cmd/dev-env-portal/      the internal read-only portal process; main only, the assembly is web/portal/session
cmd/agentd/              the in-pod supervisor: `run`, `hold` (the rescue pod, D-55), `shelf [--history-archive]` (the shelf pod,
                         D-67, DESIGN-025), `run-agent`
                         (a task, or a TUI, D-58), `claude-login[-status|-idle]` (the keeper pod's Claude login helper, T09 increment 5), `render`, `ctl status|rescue [--stop-agent]|prepare-restart` (D-40 to D-43,
                         D-48, D-58), `ctl deliver|log` (D-65), `ctl codex-pair` (Story110 T17), `ctl login url|code|status` (remote sessions), `ctl rescues|hold-rescue|prune` (D-67),
                         `ctl grant-install|grant-remove|grant-list|grant-use` (D-63),
                         `ctl history-export|history-archive` (DESIGN-025 H4), `ctl coord-archive put [--mark]|mark|lookup|get` (DESIGN-024 R2; journal grace, recorded-time or pinned get)
cmd/agent-run/           the CLI, one static binary (CGO_ENABLED=0, D-06); main only, the commands are internal/agentrun
internal/version/        the build identity every binary's `version` prints
internal/testenv/        starts envtest (kube-apiserver + etcd) with config/crd/ installed, for test suites
internal/templates/      parses and checks dev-env-templates, the GitOps data pods are built from; its revision (D-44)
internal/controller/     the AgentSession reconciler: each session's pod and volume (D-44), the reap finalizer and the
                         guarded deletes (D-45): the rescue by exec before a suspend deletes a pod, and the archive of
                         a reaped session's volume after a verified rescue (D-51), in a hold pod when the volume has no pod (D-55); a private
                         Session's HOME expiry up to Archived, default off (DESIGN-025 5.4: private_expiry.go, history_relay.go). Its envtest suite proves DESIGN-001
                         5.1 (no owner reference to the operator; no pod or volume write or delete outside the guards;
                         delete and suspend wait for rescue); a fake rescuer stands in for exec. The one Pod write
                         besides create and the guarded deletes is DESIGN-025's stop request (private_stop.go), pinned
                         by TestOnlyStopPrivateExecutorPatchesPods
internal/apiserver/      the operator's /v1 API (D-46): HTTPS runnable, TokenReview auth, caller classes, sessions,
                         heartbeat, suspend and resume (D-60), fleet, grants (D-56), activities (D-66) and rescues (D-67) handlers;
                         also decisions, project canaries and their results, task claims, the inbox, published contexts, the
                         session directory, task budgets and archived history (DESIGN-025 8). Unit tests use the fake client; its envtest suite mints real
                         tokens and serves through a manager wired as the operator's
internal/podexec/        the one pods/exec client (WebSocket, SPDY fallback): the rescue, and the API's log and message routes (D-65)
internal/apiserver/apiv1/  the API's wire types, error codes and Claude effort table, standard library and agentd's standard-library protocol only, for agent-run
internal/activity/       the reaper of expired declare-activity declarations (D-66), a leader-only runnable in the operator
internal/reminders/      the reminder engine (DESIGN-032, #206): schedule arithmetic with explicit DST rules, target allowlist, a CalendarAdapter interface with a fake, and the leader-only Scheduler over the Reminder CRD; the keeper hosts it behind `--enable-reminders` (default off), the operator serves `/v1/reminders` behind `--enable-reminders-api` (default off)
internal/maintenancecontract/ bounded offline upgrade-shepherd envelope validator and correlation evaluator; a candidate is not operational deferral
internal/standby/       the leader-only standby runnable (Story110 T25, plan 03): keeps one `remote` Claude AgentSession ready, found by its label, behind `--enable-standby-session` (default off); a ConfigMap-backed circuit breaker allows three creations in 30 minutes
internal/shelf/          the operator's side of the shelf pod (D-67): finds it, lists and prunes rescues there by exec; the pruner is
                         a leader-only runnable; ExecSlots, the per-replica limit on archive execs (DESIGN-024 5, DESIGN-025 6.4);
                         HistoryReader, the history archive's reads for the API (DESIGN-025 8)
internal/agentrun/       agent-run's commands (D-50, D-58, D-60, D-65, D-66, D-67): -p, --local, list, show, log, msg, reap, suspend, resume, rescue list|restore, history list|show|read (DESIGN-025), declare-activity, attach, detach, login, fleet, rc start|status (DESIGN-038, client observations), maintenance check, pages list|show (offline, DESIGN-031); finds the API and a token in a session
                         pod, in another pod (a minted token) or from flags; imports the standard library, apiv1 and
                         agentd's protocol, and the standard-library helpers maintenancecontract, pages, version, and tokenfile (`make build` checks the binary does not link Kubernetes); tests run against an httptest TLS server
internal/keeper/         the keeper (D-52), including the default-off Ask notifier (DESIGN-034): mints the haynes-dev-bot token into dev-agents/dev-env-gh-token every 40 minutes
                         behind a Lease. Only that non-rotating token is a Keeper Job; the rotating Codex login and the
                         Proxmox credential grants are separate journaled, Lease-fenced leader runnables. Unit tests use a fake
                         GitHub (httptest) and a fake clock; its envtest suite runs it as its ServiceAccount with exactly
                         the Roles haynes-ops gives it
internal/broker/         the access broker (D-61): decides AccessGrants (GrantPolicy match, Decide for the approval page,
                         a Notifier), makes kube, break-glass and egress grants (a ServiceAccount and RoleBindings or a
                         ClusterRoleBinding per grant) and revokes them; ExecInstaller installs kube tokens on stdin into the pod's grants tmpfs (D-63), guarded by pod UID; older pods fail/revoke after three attempts. Its envtest
                         suite runs it as dev-env-broker under haynes-ops' RBAC and broker guard, copied into testdata/
internal/homeguard/     exact original HOME binding validation, metadata only; default-off OP252 lifecycle/start guard
internal/grantattest/    the agent attestation of a grant (DESIGN-001 D-88): one annotation, its bounds and the scope digest, shared by the operator's API and the broker; pure. A claim, not verified human identity
internal/egress/         the scoped CiliumNetworkPolicy builder and ownership checks (D-64), shared by broker and operator
internal/grantexpiry/    the operator's separate egress expiry backstop (D-64), watches grants, reads CNPs by name only,
                         deletes by verified ownership and UID, and preserves the broker's grant audit record
internal/agentd/         agentd: config rendering (the dev-init.sh port), clone and worktree, the task runner, heartbeat,
                         status, rescue and its bundle on the shared volume (D-48), the private history export and the
                         shelf's history archive (DESIGN-025 7.1; historyexport_layout.go classifies every provider and
                         agentd name, and its scans keep the table complete); tests fake claude and tmux and run
                         git against a bare repo in t.TempDir()
internal/agentd/protocol/  what agentd and the operator exchange: the session document, the status, the heartbeat route,
                         the rescue report, and the rescue layout and manifest on the shared volume; the history
                         archive's wire types and the canonical JSON both sides hash (canonical.go, DESIGN-025 5.4)
internal/agentd/hostexecutor/  owns one foreground native computer for one immutable budget binding (no budget authority, no pairing)
internal/codexrc/        pure pinned Codex project/host discovery and rc-launch error mapping; no I/O, launch or pairing authority
internal/codexauth/      the access-only Codex document the keeper publishes and agentd reads; it cannot carry a refresh token
internal/claudeauth/     the access-only Claude projection document (dev-env-claude-live) and the merge of its access fields into a pod's .credentials.json; it cannot carry a refresh token; pure, no I/O (Story110 T09 increment 1)
internal/claudelogin/    the keeper-owned Claude login ceremony (Story110 T09 increment 5): the control socket's wire types and client, and the helper that runs the pinned `claude auth login` in a memory-backed staging HOME, prints only the sign-in link and reads the one-time code on stdin; reads no credential
internal/tokenfile/      bounded reads of rotating projected bearer-token files
internal/projectcatalog/ parses and validates the GitOps project catalog and its per-task snapshot; no filesystem or network authority
internal/projectsync/    agentd's bounded, model-free GitOps project management (`agentd project-sync|project-refresh`): actor, API, receipt
internal/pages/          bounded keeper Pushover page records, Alertmanager snapshots and Gatus current-check snapshots (DESIGN-031); redaction and coverage text; standard library only, no I/O
internal/providerallowance/  typed providerallowance.v1 observation and providerallowance.read.v1 cached-read contract: strict decode, caller-supplied trust, and derivation checks; no collector, adapter, storage, or network
internal/usageconsolidation/  bounded pure report projector over caller-authorized allowance reads and initial-invocation observations; no aggregation, collector, provider, clock, authorization, or I/O
internal/projectcanary/  validates the two finite operator-owned project rule canaries and binds a session to one
internal/projectcanary/testutil/  synthetic canary fixtures for tests
internal/commonrules/    retains the common instruction inputs (common text, Codex header) bound to an enabled managed task
internal/commonrules/testutil/  synthetic common-source fixtures for tests
internal/taskclaim/      the TaskClaim store: immutable explicit ownership, Create only, uncached reads; also the bounds
                         that all three coordination stores apply and their record-count metrics (DESIGN-010)
internal/inbox/          the inbox store: immutable API messages and explicit read acknowledgements, Create only, uncached reads
internal/asks/           the Ask store: create, list, withdraw, a set-once answer and the operator's leader-only CAS expiry sweeper behind --enable-asks; no inbox delivery in v1 (OpenProject Epic #254)
internal/contextpublication/  the PublishedContext store: immutable published excerpts, Create only, uncached reads
internal/sessiondirectory/  the SessionDirectory store: bounded original session metadata and CAS witnesses
internal/taskbudget/     the task budget ledger: retained, deterministic task admission in ConfigMaps behind CAS (KubeStore)
internal/coordretention/ read-only paged coordination-record summaries and fail-closed release eligibility evaluation (DESIGN-024 R3 prerequisite)
config/crd/              CRDs generated by controller-gen; haynes-ops gets a copy by PR with every change, before the
                         operator pin that needs it (a stale copy prunes status fields silently; haynes-ops #3502)
images/operator/         Dockerfile: ghcr.io/thaynes43/dev-env-operator (operator + keeper); build args COMMIT, VERSION
images/portal/           Dockerfile: ghcr.io/thaynes43/dev-env-portal, the internal read-only portal; build args COMMIT, VERSION; publish.yml signs sha-<short>
images/agent/            Dockerfile: ghcr.io/thaynes43/dev-env, tag line 2.x (D-53); smoke-test.sh; tools/ (pve, hw-ssh, v1 copies). CI only, never in the pod
images/codex-login/      Dockerfile: ghcr.io/thaynes43/dev-env-codex-login, the keeper's login-only helper (D-78); published by publish.yml
images/git-wait/         the git-wait trial fixture: a patched Git build recipe; publish-git-wait.yml, fixture tags only
scripts/trials/          bounded synthetic trial scripts and their checks (git-wait, workspace CephFS); run only in reviewed fixture Jobs
scripts/trials/native_lifecycle_profile/  renders the isolated native lifecycle profile; provisions nothing
scripts/trials/pilot_rendezvous/  default-off bounded existing-CLI pilot gate/one-dispatch harness; offline injected checks
scripts/trials/workflow_handoff/  offline coordinator handoff checker; validates one bounded record and prints the next manual action; --watchdog is finite output-only PR-bound owner-wake and escalation planning; does not dispatch, grant, or write tracker state
scripts/trials/stopped_handoff/  offline synthetic stopped-handoff fixture driver; validates one record and local git fixtures only; does not dispatch, stop a live executor, or change a TaskClaim
scripts/delivery/        source-only wrapper for the pinned haynes-ops delivery guard; verifies that checkout, then invokes it once; no reconcile, release, or deployment
images/THIRD_PARTY.md    what each published image bundles and under which license; update it with the Dockerfiles.
                         It and LICENSE are copied into both images, whose license directories CI checks (#55)
Makefile                 generate, check-generated, check-layout, lint, test, envtest, build, build-agent-run-darwin, tools
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
and on `workflow_dispatch`. Its push paths include the release-version manifest
and the workflow itself, so reviewed release bumps and publication fixes publish
automatically. It pushes `dev-env-operator:sha-<short>` (never
`latest`, never an existing tag) and signs the digest with keyless cosign, pinned like
haynes-ops v1 (see the workflow header and haynes-ops #3092). It needs no Docker in the
pod: never build images here.
Manifest-changing commits take their version from that checked-in release record,
so publication cannot race tag creation. Other commits retain the Git-derived version.

Releases: `release-please.yml` runs on every push to main and keeps one release PR
open (one version for the repo; both images are tagged with it, and the first release
is 2.0.0 because the agent image line is 2.x; it shipped on 2026-10-07, #52). Its
token is a GitHub App's (`thaynes43-dev-env-release`), from the repo variable
`RELEASE_APP_ID` and secret `RELEASE_APP_PRIVATE_KEY`, so release PRs run CI like any
PR; the automatic advisory review deliberately skips the release App's PRs.
Claude Code review is optional, including release PRs. When available, an
explicit `@claude` request through `claude.yml` can obtain a review; read and
resolve any findings it produces. A skipped green job is not a review. Tom
confirmed on 2026-10-11 that the review service is down: record it as unavailable
and proceed with required CI and native independent review; do not retry or
repair its authentication as a delivery prerequisite. (It did run on most PRs on
2026-10-12 and 2026-10-13; see the review section below.) If
`RELEASE_APP_ID` or `RELEASE_APP_PRIVATE_KEY` is missing, release-please falls back
to `GITHUB_TOKEN`, and a PR opened that way starts no workflows: then close and reopen
the release PR (`gh pr close <n> && gh pr reopen <n>`) as haynes-dev-bot so
`CI - Success` runs, after every update to it. This applies only to a release PR that
started no workflows. It is not a way to re-run failed CI on any other PR. If a re-run
or any other action is denied, stop and report the denial on the tracker; do not close
and reopen, push empty commits or use another route or identity. Agents squash-merge a green release PR
themselves, like any other PR, once they have checked that the version and changelog
are sane, any available advisory findings are resolved, and `CI - Success` is green on
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
`internal/controller/`. Name each one here when it lands: every Go package under
`api/`, `cmd/`, `internal/` and `scripts/` needs its own line in the Layout block,
and `make check-layout` (run in CI beside `check-generated`) fails without one.

## How work happens here

- **Docs first.** A change in behaviour starts in the saga: ADR for the decision,
  design for the how, a backlog plan for the steps, then code. Docs change in the same
  PR as the behaviour they describe. An Accepted ADR is never edited; a new ADR
  supersedes it.
- **Questions go to Tom one at a time through the native phone prompt**, at the
  moment the decision arises. Claude uses AskUserQuestion. Codex must use an
  available native user-question tool **once phone delivery is verified**; that
  delivery is currently unverified for this client and is a plan 11 acceptance
  requirement. Plain commentary, final-message
  lists, PR comments and a `Q-NN` record are not delivery. Record the question in
  the design, push the prompt, then fold the answer back as a dated ruling.
  Verify the premise before asking. If the available client cannot deliver the
  phone prompt, report that capability gap and keep the dependent decision pending;
  continue independent authorized work and do not invent a notification channel.

- **Worktree per task, never push to main.** Branch `agent/<task>`, open a ready PR,
  squash-merge it yourself once required checks are green and the review findings
  are handled.
- **Plain writing.** Short sentences, concrete nouns, no filler. Use Mermaid for editable
  engineering diagrams; SVG/PNG workflow drawings are also supported.

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
`claude.yml`. Release App PRs are excluded from the automatic job; their explicit
review is optional too. **Claude Code reviews are optional and are currently
unavailable (Tom, 2026-10-11).** They are not required checks or merge gates.
Record a failed/skipped/unavailable result without claiming a review pass, and
continue with required CI and native independent review. Read available
findings before merging. Fix each one, or answer it on the PR with a concrete reason
it is wrong; never "merging anyway". Both workflows need the Claude GitHub App on the
repo and the `CLAUDE_CODE_OAUTH_TOKEN` repo secret, otherwise they skip green and
review nothing.

*Observed 2026-10-12 and 2026-10-13:* the advisory review ran on most PRs in
these two days and posted reviews; several had findings that led to fixes (for
example #251).
Tom's rule above is unchanged: the review stays optional and is not a gate, a
missing result is recorded as unavailable, and native independent review stays
mandatory. Check the PR rather than assuming it ran or did not.

## Repository instruction acceptance

For an explicit instruction acceptance probe, report the repository marker
`dev-env-repository-rules-20261011-6d807bf2`. Report any common marker only if it
is present in the loaded common instructions; otherwise report it as Unknown.
Do not fetch files or run commands to obtain markers for that probe. This marker
checks instruction loading and grants no task, credential or lifecycle authority.
