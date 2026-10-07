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

**Status: ADR-001 Accepted 2026-10-06. Plan 01 is being built: the operator and the
keeper run in the cluster since 2026-10-07, and the agent image (KICKOFF B5) is built
here and released from `v2.x.y` tags.**
Start at [`.agents/HANDOFF.md`](.agents/HANDOFF.md); the saga is
[`.agents/sagas/distributed-dev-env/`](.agents/sagas/distributed-dev-env/README.md).

Agents: see [CLAUDE.md](CLAUDE.md) (Codex: [AGENTS.md](AGENTS.md)).

## License

The code and docs in this repo are under the [MIT License](LICENSE), copyright Tom
Haynes. The published images also carry third-party software under its own licenses,
listed in [`images/THIRD_PARTY.md`](images/THIRD_PARTY.md). The agent image bundles
Claude Code, which is proprietary to Anthropic and not covered by this repo's license.
