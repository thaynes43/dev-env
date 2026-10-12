# AGENTS.md

**Claude pickup, 2026-10-12:** read the
[current requirements and roadmap](.agents/CLAUDE-RESUME-2026-10-12.md) in full.
It consolidates Tom's earlier enumerations and later corrections, delivered work,
review targets and stopped attempts. Changing drivers does not reset counters.

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
`claude.yml`. Release App PRs skip the automatic job; explicit `@claude` review
is optional too. **Claude Code reviews are optional and currently unavailable
(Tom, 2026-10-11).** They are not required checks or merge gates. Record that
limitation and proceed with required CI and native independent review. Read
available findings before merging. Fix each one, or answer it on the PR with a concrete reason
it is wrong; never "merging anyway". Both workflows need the Claude GitHub App on the
repo and the `CLAUDE_CODE_OAUTH_TOKEN` repo secret, otherwise they skip green and
review nothing. Every repo gets this reviewer (Tom, 2026-09-30); see CLAUDE.md.

## Repository instruction acceptance

For an explicit instruction acceptance probe, report the repository marker
`dev-env-repository-rules-20261011-6d807bf2`. Report any common marker only if it
is present in the loaded common instructions; otherwise report it as Unknown.
Do not fetch files or run commands to obtain markers for that probe. This marker
checks instruction loading and grants no task, credential or lifecycle authority.
