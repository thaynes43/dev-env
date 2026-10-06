# dev-env

The development environment where Tom's AI coding agents (Claude Code, Codex) work,
inside the `main` Kubernetes cluster.

**v1** is one long-running pod. Its manifests and image live in
[thaynes43/haynes-ops](https://github.com/thaynes43/haynes-ops)
(`kubernetes/main/apps/dev/dev-env/`, `scripts/dev-env/Dockerfile`), and it keeps
running until v2 replaces it.

**v2**, built here, is distributed: one pod per agent session, each with its own CPU
and memory limits, created, upgraded and pruned by an operator that the `agent-run`
CLI calls from anywhere. This repo will publish the operator, the CLI and the agent
image (`ghcr.io/thaynes43/dev-env`); haynes-ops deploys them.

**Status: design.** Read the saga first:
[`.agents/sagas/distributed-dev-env/`](.agents/sagas/distributed-dev-env/README.md).

Agents: see [CLAUDE.md](CLAUDE.md) (Codex: [AGENTS.md](AGENTS.md)).
