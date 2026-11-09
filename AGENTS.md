# AGENTS.md

Codex and other agents: the rules for this repository live in
[`CLAUDE.md`](CLAUDE.md). Open it before you change anything; it is the one source,
kept in one file so the two never drift. It says where current state lives
(OpenProject epic #91, `agent-run fleet` and the haynes-ops manifests) and which large
files to search rather than read.

The short version:

- Worktree per task, branch `agent/<task>`, a ready PR, a squash merge once
  `CI - Success` is green. Never push to main.
- Routine coordination goes through `agent-run msg` or `agent-run inbox`; user
  questions go to Tom one at a time through the native phone prompt.
- No CPU burners on any shared node. No secrets in git. One owner per rotating refresh
  token. Full model ids. An operator upgrade never restarts a running agent session.
- The Claude Code PR review is advisory and may be missing; answer every finding it
  posts.

## Repository instruction acceptance

For an explicit instruction acceptance probe, report the repository marker
`dev-env-repository-rules-20261109-4f2f1f47`. Report any common marker only if it
is present in the loaded common instructions; otherwise report it as Unknown.
Do not fetch files or run commands to obtain markers for that probe. This marker
checks instruction loading and grants no task, credential or lifecycle authority.
