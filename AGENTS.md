# AGENTS.md

**Controlling scope, 2026-10-10:** [ADR-003](.agents/sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md) supersedes mandatory
shared Git/RWX. Share rules, task coordination and authorized live/stopped context;
keep repositories and worktrees per pod. Read the updated guide before selecting
implementation work. Source foundations are not runtime acceptance.

Codex and other agents: the rules for this repository live in
[`CLAUDE.md`](CLAUDE.md). Read it before you change anything; it is the single
source, kept in one file so the two never drift.

**Start with [`.agents/HANDOFF.md`](.agents/HANDOFF.md):** the current state, the
next steps, and what works outside the cluster. The first build session follows
[`KICKOFF.md`](.agents/sagas/distributed-dev-env/KICKOFF.md).

The short version:

- **Status: plans 01/02 are working; capability parity and plans 03/04 remain
  unfinished.** The [workflow guide](docs/workflow-guide.md) separates current
  features from planned ones. ADR-001 was Accepted 2026-10-06. The saga is
  [`.agents/sagas/distributed-dev-env/`](.agents/sagas/distributed-dev-env/README.md).
  Deploy manifests live in thaynes43/haynes-ops, not here.
- Docs first; one native phone-delivered question prompt at a time to Tom,
  recommended option first, recorded as `Q-NN` in the design. Use the provider's
  user-question tool once phone delivery is verified. Codex phone delivery is
  currently unverified; a question buried in commentary is not delivery.
- Worktree per task, branch `agent/<task>`, ready PR, never push to main.
- No CPU burners on any shared node; a CPU limit on anything you run in the
  cluster. No secrets in git. One owner per rotating refresh token. Full model ids.
  Never restart running agent sessions on an operator upgrade.

## Automated PR review (agents read this)

Every non-draft PR gets an advisory review from Claude Code
(`.github/workflows/claude-code-review.yml`); `@claude` mentions are handled by
`claude.yml`. Release App PRs skip the automatic job and require an explicit
`@claude` review before merge; follow CLAUDE.md's release procedure. The review is
**advisory**: it is not a required check. Read its
findings before merging. Fix each one, or answer it on the PR with a concrete reason
it is wrong; never "merging anyway". Both workflows need the Claude GitHub App on the
repo and the `CLAUDE_CODE_OAUTH_TOKEN` repo secret, otherwise they skip green and
review nothing. Every repo gets this reviewer (Tom, 2026-09-30); see CLAUDE.md.
