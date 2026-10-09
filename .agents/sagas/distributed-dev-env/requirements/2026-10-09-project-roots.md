# Owner requirements: shared projects and Codex remote pods

Received 2026-10-09 America/New_York. R1–R6 below preserve Tom's supplied
requirement text. They supersede the earlier Q-20 app-choice premise: the project
concept must serve both Claude Code and Codex.

**Additional owner requirement, R7:** multiple Codex remote links must span pods
and share a workspace. One replacement hub is insufficient. Shared project and
task files must be distinguished from each remote host's enrollment, daemon
state, conversation ownership and credentials. How that is implemented is still
under design; do not describe the current per-session private homes as satisfying
this requirement.

**Audit correction to the supplied background:** on 2026-10-09, haynes-ops
[PR #3633](https://github.com/thaynes43/haynes-ops/pull/3633) was open, not merged.
The detached anchors already existed under `/home/dev/codex`; their common Git
directories were under `/home/dev/repos`, on the v1 RWO home volume. This confirms
folder existence, not GitOps reconciliation, cross-pod sharing, rules propagation
or fresh boot acceptance. The mounted-resource PR can restart v1 if merged and
belongs to the owner's natural-break workflow.

The [workflow guide](../../../../docs/workflow-guide.md) draws the proposed user
journeys. [Plan 11](../backlog/11-project-workspaces.md) holds the acceptance work.
D-74 records the scope change without selecting a management UI or storage
backend. No architecture implementation or v1 restart is commissioned by the
documentation update.

---

## Requirement: Project roots for Claude Code and Codex

### Background (how dev-env v1 works today)
- Each repo has a canonical clone at `~/repos/<repo>`. Nobody works in it; it is only fetched.
- Every task gets its own worktree, `~/work/<task>` on branch `agent/<task>`.
- `wt-sweep` reaps any worktree in `~/work` that has been idle for more than 3 days.
- The Codex app needs a project folder before it can start any session in it. Agents used to create those folders as worktrees in `~/work`, so the sweeper deleted them whenever a project went quiet.
- The v1 fix, added as a ground rule in haynes-ops PR #3633, puts project roots in `~/codex/<repo>`. Each one is a worktree of `origin/main` with a detached HEAD, used only to anchor the project and never edited. A project that spans several repos is a plain folder `~/codex/<project>/` with one such worktree per repo and an `AGENTS.md` that maps folders to repos. Example: `~/codex/sigo-alumni/` holds sigo-alumni, sigoalumni-org and sigmaphiomicron-com.
- Gaps in v1: the roots are made by hand, nothing keeps them current, the setup is Codex-only, and project-level rules sit in a file that one agent may not read.

### Requirements

**R1. Project roots are declared in GitOps.**
A single list in the dev-env config declares every project: its name, its repos, an optional default branch per repo, and its project-level rules. Boot, or a `dev-env project sync` command, makes the disk match the list:
- clone any canonical repo that is missing;
- create any project root that is missing;
- report, but never delete, a root that is no longer declared.

Creating a project by hand must remain possible. A `project add <name> <repo>...` command writes the declaration and creates the root in one step.

**R2. One project concept for both agents.**
The same project root serves Claude Code (CLI, remote-control and desktop) and Codex (app, remote and CLI). Starting either agent "in project X" opens a session in the same root and sees the same repos.
- Codex: the project is trusted automatically. Today that is `[projects."<path>"] trust_level = "trusted"` in `~/.codex/config.toml`, which must come from the GitOps config and not from hand edits.
- Claude Code: multi-repo projects get every repo in scope, for example through `--add-dir` or the equivalent setting.
- `agent-run` takes a project as well as a repo: `agent-run --project sigo-alumni`.

**R3. Project rules are written once and read by both agents.**
Each project's rules live in one GitOps-managed source. They are rendered to both `AGENTS.md` (Codex) and `CLAUDE.md` (Claude Code), or one file imports the other, e.g. a `CLAUDE.md` that contains only `@AGENTS.md`. No agent should ever be the only one to see a rule. This uses the same pattern as the pod-wide rules today, where `AGENTS.header.md` plus the shared `CLAUDE.md` are rendered to `~/.codex/AGENTS.md`.

**R4. Project rules follow the session into its task worktrees.**
Today a session starts in `~/codex/<project>` and then moves to `~/work/<task>`. Both agents look for instruction files from the git root down to the working directory, and `~/work/<task>` is not under the project folder. So the project's rules stop applying exactly when work begins. v2 must close this gap. Possible approaches:
- create task worktrees under the project, e.g. `~/codex/<project>/.work/<task>`, and have the sweeper scan there;
- write a project pointer into each task worktree when it is created;
- inject the project rules through each agent's session config.

Whichever approach is chosen, write a test that proves a session in a task worktree sees the project rules.

**R5. Project roots stay current and are never reaped.**
- The sweeper never touches project roots. The rule is "sweep `~/work` only", and it must stay true however R4 is solved.
- At boot and daily, each anchor checkout is refreshed to `origin/<default>` (`fetch` + `checkout --detach`), but only when it is clean. A dirty anchor is reported and left alone, because someone broke the never-edit rule.
- Task worktrees created from a project are swept exactly like any other task worktree, whichever agent made them.

**R6. The canonical clones are verified healthy.**
At boot and in the daily sweep, check each `~/repos/<repo>` and report any of these failures:
- it is not on its default branch;
- it is on a detached HEAD;
- it has staged or unstaged changes;
- it is behind its remote.

Auto-repair a clone only when nothing can be lost: no untracked files, and the index tree exactly matches a commit already in the remote's history. Fast-forward it, or reset it to `origin/<default>`.

Seen in v1 on 2026-10-09: hass-sandbox and sigoalumni-org both had an index still matching their original clone commit while HEAD had moved on. `git status` showed hundreds of staged "reverts" that weren't real work, and any session started there inherited that state. Other clones were sitting on a detached HEAD, or were 258 commits behind.

### Acceptance
- After a fresh boot from an empty PVC, every declared project exists and contains the correct repos.
- A Claude Code session and a Codex session opened in the same multi-repo project both report the same project rules, both at the root and from inside a task worktree they created.
- A project root that has been idle for 30 days still exists and is current with `origin`.
- A canonical clone with a stale index or detached HEAD is reported, and is repaired automatically when that is safe.
