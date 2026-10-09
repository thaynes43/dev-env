# dev-env

The development environment where Tom's AI coding agents (Claude Code, Codex) work,
inside the `main` Kubernetes cluster.

**v1** is one long-running pod. Its manifests and image live in
[thaynes43/haynes-ops](https://github.com/thaynes43/haynes-ops)
(`kubernetes/main/apps/dev/dev-env/`, `scripts/dev-env/Dockerfile`), and it keeps
running until v2 replaces it.

**v2**, built here, is distributed: one pod per agent session, each with its own CPU
and memory limits, created and pruned by an operator that the `agent-run` CLI calls.
Automatic image drains are still planned. This repo publishes the operator, CLI
and agent image (`ghcr.io/thaynes43/dev-env`); haynes-ops deploys them.

**Status: phase 1 built (2026-10-07).** ADR-001 was Accepted on 2026-10-06. Plan 01,
task mode, is done: the operator and the keeper run in the cluster, the agent image
`ghcr.io/thaynes43/dev-env:2.0.0` is released, and `agent-run -p` from the v1 pod
starts a session pod on a worker that does its task and opens its PR. Plan 02
(interactive sessions and lifecycle) is done under Tom's 2026-10-08 correction:
the existing in-cluster CLI workflow passed acceptance, and external CLI access
is optional. Plan 07's approval spike and corrected v1 capability audit are complete
(R-03/R-04). D-70 withdraws Q-18's earlier premise, with no approval route selected;
D-71 targets a guarded replacement before Headlamp retirement. Parity restoration
and keeper activation prerequisites remain.
Start at [`.agents/HANDOFF.md`](.agents/HANDOFF.md); the saga is
[`.agents/sagas/distributed-dev-env/`](.agents/sagas/distributed-dev-env/README.md).

Agents: see [CLAUDE.md](CLAUDE.md) (Codex: [AGENTS.md](AGENTS.md)).

## Workflow and quick start

Read the [workflow guide](docs/workflow-guide.md) for architecture diagrams,
the feature set, working v1/v2 commands, Codex session management, repository
freshness and cutover gates. V2 currently starts Claude task/local sessions;
Codex, phone sessions and automatic drains remain unfinished. Joint Claude/Codex projects and multiple remote pod links with shared
workspaces are owner requirements; their implementation is tracked in
[plan 11](.agents/sagas/distributed-dev-env/backlog/11-project-workspaces.md).

## License

The code and docs in this repo are under the [MIT License](LICENSE), copyright Tom
Haynes. The published images also carry third-party software under its own licenses,
listed in [`images/THIRD_PARTY.md`](images/THIRD_PARTY.md). The agent image bundles
Claude Code, which is proprietary to Anthropic and not covered by this repo's license.
