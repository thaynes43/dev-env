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

**Status: design complete; ADR-001 awaits Tom's ratification (2026-10-06).** Nothing
is built yet. The first build session follows
[`KICKOFF.md`](.agents/sagas/distributed-dev-env/KICKOFF.md). v1 keeps running from
haynes-ops (`kubernetes/main/apps/dev/dev-env/`, `scripts/dev-env/Dockerfile`) until
v2 proves itself and Tom approves the cutover. The saga:
[`.agents/sagas/distributed-dev-env/`](.agents/sagas/distributed-dev-env/README.md).

## Layout

```
.agents/HANDOFF.md       the front door: state, next steps, rulings, inside vs outside
.agents/sagas/<saga>/    saga README (vision, decision log), KICKOFF, adrs/, designs/, backlog/, research/
.github/workflows/       CI (Claude review + @claude today; builds arrive with the code)
```

Code directories (operator, CLI, image) are added by the build phases in the saga
backlog. Name each one here when it lands.

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
  tokens, 1Password values, SSH keys and Proxmox tokens. haynes-ops is a public repo,
  so the same care applies to anything that might move there.
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
`claude.yml`. The review is **advisory**: it is not a required check. Read its
findings before merging. Fix each one, or answer it on the PR with a concrete reason
it is wrong; never "merging anyway". Both workflows need the Claude GitHub App on the
repo and the `CLAUDE_CODE_OAUTH_TOKEN` repo secret, otherwise they skip green and
review nothing.
