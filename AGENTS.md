# AGENTS.md

Codex and other agents: the rules for this repository live in
[`CLAUDE.md`](CLAUDE.md). Read it before you change anything; it is the single
source, kept in one file so the two never drift.

The short version:

- **Status: design.** Start with
  [`.agents/sagas/distributed-dev-env/`](.agents/sagas/distributed-dev-env/README.md).
  Deploy manifests live in thaynes43/haynes-ops, not here.
- Docs first; one question at a time to Tom, recorded as `Q-NN` in the design.
- Worktree per task, branch `agent/<task>`, ready PR, never push to main.
- No CPU burners in the dev-env pod. No secrets in git. One owner per rotating
  refresh token. Full model ids. Never restart running agent sessions on an
  operator upgrade.

## Automated PR review (agents read this)

Every non-draft PR gets an advisory review from Claude Code
(`.github/workflows/claude-code-review.yml`); `@claude` mentions are handled by
`claude.yml`. The review is **advisory**: it is not a required check. Read its
findings before merging. Fix each one, or answer it on the PR with a concrete reason
it is wrong; never "merging anyway". Both workflows need the Claude GitHub App on the
repo and the `CLAUDE_CODE_OAUTH_TOKEN` repo secret, otherwise they skip green and
review nothing. Every repo gets this reviewer (Tom, 2026-09-30); see CLAUDE.md.
