# CLAUDE.md

Rules for Claude Code, Codex and other agents working in this repository. Codex reads
`AGENTS.md`, which points here: this file is the one source. Reasons, history and
detail cut from it are in [docs/agent-rules-rationale.md](docs/agent-rules-rationale.md).

## What this repo is

**dev-env v2**: the distributed version of the in-cluster development environment
where Tom's AI coding agents (Claude Code, Codex) work. v1 is one big pod in the
`main` cluster. v2 runs bounded session pods and persistent Codex hosts, managed
through the operator and its API. Native threads stay on their host; only supported
dispatch creates a separate worker pod. The scope (2026-10-10): preserve sessions
through upgrades, distribute load, guard powerful access, expose live and stopped
work and context, and attribute model usage and cost. v1 keeps running until its
required workflows have replacements and Tom approves its retirement.

This repo holds the operator, keeper, broker, portal, agentd, `agent-run`, the agent
image (`ghcr.io/thaynes43/dev-env`) and their CI. The deploy manifests live in
[thaynes43/haynes-ops](https://github.com/thaynes43/haynes-ops) (GitOps through Flux):
this repo publishes signed images, and haynes-ops pins and deploys them. The repo is
public (since 2026-10-07).

## Where current state lives

- **Work:** OpenProject, project `haynes-engineering`: epic #91 and its stories, and
  the items your coordinator or driver names. `opwork show <id> --history` reads one.
- **Running system:** `agent-run fleet` (revision, phases, outdated sessions) and the
  haynes-ops manifests: `kubernetes/main/apps/dev-env-system/templates/app/templates.yaml`
  for the agent image and session profiles, and
  `kubernetes/main/apps/dev-env-system/operator/app/helmrelease.yaml` for the operator
  image and flags.
- **Scope:** [ADR-003](.agents/sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md):
  agents share rules, coordination and authorized context; repositories and task
  worktrees stay per pod.
- **Before launching hardware, host-operations or workbench work**, follow the
  [launch-profile preflight](docs/launch-profile-preflight.md). It grants neither
  credentials nor approval.

These large files are references. Search them for the item you need:

| File | What it is |
|---|---|
| `.agents/HANDOFF.md` | dated delivery log, ruling index and what works outside the cluster |
| `.agents/sagas/distributed-dev-env/README.md` | saga vision and the decision log; `adrs/`, `designs/` and `backlog/` sit beside it |
| `docs/workflow-guide.md` | feature set, Codex session model, freshness contract and delivery gates |
| `docs/v2-quickstart.md` | Tom's phone-first daily workflow |
| `.agents/CLAUDE-RESUME-2026-10-12.md`, `-2026-10-13.md` | historical pickup briefs: requirement history and stopped routes |
| `docs/repo-layout.md` | one line for every directory and Go package |

## Build and test

- The agent image has no Go toolchain. Install the `toolchain` version from `go.mod`
  (go.dev/dl) under `~/.local/go` and put `~/.local/go/bin` on `PATH`. Keep
  `GOCACHE` and `GOMODCACHE` under `~`, and leave `TMPDIR` at its default.
- The module is `github.com/thaynes43/dev-env`. Built binaries and pinned tools
  (controller-gen, golangci-lint, setup-envtest) go to `bin/`, which git ignores.
- Run `make generate` after any change to `api/`. It rewrites
  `zz_generated.deepcopy.go` and `config/crd/`; commit both.
- `make test` runs the unit tests and the envtest suites. setup-envtest downloads
  kube-apiserver, etcd and kubectl into `bin/tools/envtest/`. `ENVTEST_K8S_VERSION` in
  the Makefile follows the main cluster's Kubernetes minor (1.35); bump it when the
  cluster moves. After `make envtest` once, a plain `go test` finds the binaries.
- A schema rule gets a case in `api/v1alpha1/agentsession_validation_test.go` that
  shows it accepting and refusing. CEL rules must fit the API server's cost budget:
  give every string, list and map a rule reads a `MaxLength` or `MaxItems`.
- Every Makefile target caps Go at two CPUs and two packages and runs under
  `nice -n 19`. Keep `GO_PARALLELISM` at 2 in a shared pod.
- A new Go package under `api/`, `cmd/`, `internal/` or `scripts/` needs a line in
  [docs/repo-layout.md](docs/repo-layout.md); `make check-layout` fails without one.
- Build images only in CI. The agent image is about 3.2 GB; `ci.yml` builds and
  smoke-tests it on PRs that change `images/agent/**`.

## CI and releases

- The one required check is the aggregate job `CI - Success`. `ci.yml` has no paths
  filter, so a docs-only PR gets it too. Image builds run on PRs and never push.
- `publish.yml` runs on push to main when an image input changes, and on
  `workflow_dispatch`. It pushes the operator, portal and codex-login images as
  `sha-<short>`, never `latest` or an existing tag, and signs each digest keyless
  with cosign.
- `release-please.yml` keeps one release PR open, with one version for the repo.
  Merge a green release PR yourself once its version and changelog are sane and any
  advisory findings are resolved (Tom, 2026-10-07: "let agents merge releases").
  Then watch `publish-agent.yml` on the new tag and confirm its `cosign verify` step
  passed. If no run appears, start one with
  `gh workflow run publish-agent.yml --ref v2.x.y`.
- Without the release App's `RELEASE_APP_ID` and `RELEASE_APP_PRIVATE_KEY`,
  release-please falls back to `GITHUB_TOKEN`, and its PR starts no workflows. Then,
  after every update, close and reopen that release PR as haynes-dev-bot so
  `CI - Success` runs. This applies only to a release PR that started no workflows.
  When a rerun or any other action is denied, report the denial on the tracker and
  stop.
- haynes-ops gets a copy of `config/crd/` by PR with every CRD change, before the
  operator pin that needs it: a stale copy silently prunes status fields (haynes-ops
  #3502).
- Renovate auto-merges minor and patch Go modules and GitHub Actions
  (`.github/renovate.json5`).

## How work happens here

- **Docs first where it matters (Tom, 2026-11-09).** A change to access or auth, data
  lifecycle or retention, or an API or CRD contract starts with a design in the saga,
  and an ADR when it decides something. Describe any other behaviour change in the PR
  body. Docs that describe the changed behaviour change in the same PR. An Accepted
  ADR is never edited; a new ADR supersedes it.
- **Headless task outcome.** On an image with outcome retention, write
  `~/work/<session>.outcome.json` before returning. Use only `prURLs` (up to three
  `https://github.com/thaynes43/<repo>/pull/<number>` URLs, each at most 256 bytes),
  `headSHAs` (up to three 40-hex heads) and `summary` (1 to 200 printable characters,
  one line, none above U+FFFF; in Python, `json.dumps(..., ensure_ascii=False)`). The
  whole file is at most 2048 bytes. Write a temporary file in the same directory and
  rename it into place; a missing or invalid file publishes nothing. The first
  snapshot at task exit is kept immutably and shown by `agent-run show`. Keep
  secrets, prompts and transcripts out. See
  [the format](docs/workflow-guide.md#retain-a-tasks-pr-and-handoff-after-pod-reclaim).
- **Routine coordination goes to the parent or coordinator through `agent-run`**
  (Tom, 2026-10-30). Use `agent-run msg <session> -` with the text on stdin when TUI
  delivery is safe, and never while a native question modal is pending. For a
  headless, unavailable or busy recipient, use `agent-run inbox` with the verified
  recipient and project. Delivery is not acknowledgement: check before resending, and
  keep unconfirmed outcomes in the tracker handoff. Never impersonate another session
  or invent a new channel. Native subagents use their own collaboration route.
  Routine coordination needs no phone prompt and grants no authority to answer for
  Tom.
- **User questions and decisions go to Tom one at a time through the native phone
  prompt** (Claude: AskUserQuestion), when they arise. Commentary, final-message lists,
  PR comments and a `Q-NN` record are not delivery: record the question, push the
  prompt, then fold the answer back as a dated ruling. Codex phone delivery is not
  yet verified; a Codex session reports that gap, keeps the decision pending and
  continues independent authorized work.
- **Worktree per task.** Branch `agent/<task>`, a ready PR, and a squash merge by you
  once required checks are green and findings are handled. Never push to main.
- **Agent-loop skills** live in `.agents/skills/`: `supervised-dispatch` before you
  dispatch or supervise long work, `worker-qa-loop` for a coordinator running workers
  and QA, `timer-checkin` for timed check-ins, and `tmux-cli-handoff` for delivering
  work into a provider CLI in tmux. Open the `SKILL.md` yourself; no provider loads it.
- **Pod-exec commands.** `agent-run attach`, `detach`, `login`, `pair` and
  `thread start --agent codex` run through pod exec. They work for Tom, the v1 pod
  and a workbench session; an ordinary session pod is refused unless the command's
  help says otherwise.
- **Plain writing.** Short sentences, concrete nouns, no filler. Use Mermaid for
  editable diagrams; SVG or PNG workflow drawings also work.

## Hard rules

- **No CPU burners on any shared node** (2026-10-05). No busy loops, stress tools or
  wide parallel test runs, and a CPU limit on anything you run in the cluster. Cap
  test workers (for example vitest `--maxWorkers=2`) and run one suite at a time.
- **Never commit secrets.** Everything committed is public, history and PR branches
  included. That covers Claude and Codex login files, login URLs and codes,
  `CLAUDE_CODE_OAUTH_TOKEN`, GitHub App keys and minted tokens, 1Password values, SSH
  keys and Proxmox tokens. Keep household details, private addresses and session
  transcripts out too.
- **One owner per rotating refresh token** (2026-08-29). Two processes refreshing the
  same Claude Max login or Codex `auth.json` revoke the whole token family, so every
  design keeps a single owner for each.
- **Full model ids, never aliases** (`claude-opus-5-5`, not `opus`).
- **Running agent sessions are sacred.** An operator upgrade never deletes or
  restarts an agent pod. A session's volume is removed only after its rescue.
- **A denial is a stop.** Report it; never route around it with another identity.

## PR review

Every non-draft PR gets an advisory Claude Code review
(`.github/workflows/claude-code-review.yml`), and `claude.yml` handles `@claude`
mentions; release App PRs skip the automatic job. The review is advisory and may be
missing: it is never a required check, and a skipped green job is not a review. Run
the required CI and a native independent review. Answer every finding that arrives:
fix it, or reply on the PR with a concrete reason it is wrong. Keep both workflows
when you restructure CI. They need the Claude GitHub App and the
`CLAUDE_CODE_OAUTH_TOKEN` secret, and every new repo gets them by haynes-ops
[`.agents/runbooks/new-repo-setup.md`](https://github.com/thaynes43/haynes-ops/blob/main/.agents/runbooks/new-repo-setup.md)
(Tom, 2026-09-30).

## Repository instruction acceptance

For an explicit instruction acceptance probe, report the repository marker
`dev-env-repository-rules-20261109-4f2f1f47`. Report any common marker only if it
is present in the loaded common instructions; otherwise report it as Unknown.
Do not fetch files or run commands to obtain markers for that probe. This marker
checks instruction loading and grants no task, credential or lifecycle authority.
