# DESIGN-058: Cursor as a v2 task provider

- **Status:** Proposed, 2026-11-09. Source only. Not deployed and not run live.
- **Tracking:** [Story OP500](https://work.haynesnetwork.com/work_packages/500),
  project haynes-engineering: "Cursor/Grok as a v2 task provider".
- **Builds on:** the task launch of [DESIGN-001](001-dev-env-v2.md)
  (D-40, D-42), the managed Codex task path, and v1's pinned Cursor installer in
  haynes-ops `kubernetes/main/apps/dev/dev-env/app/resources/cursor-worker.sh`.

## 1. Outcome

```bash
agent-run --project <P> --agent cursor --model <cursor-model-id> -p "<task>"
```

This starts a headless v2 task session. Its pod runs the Cursor CLI
always-allow on the task. The exit and the result reach the task result and
the outcome the same way a Claude or Codex task's do. Grok is chosen by its
Cursor model id; it is not a separate provider.

## 2. Owner rulings (2026-11-09)

- Goal, in the owner's words: "an agentic coordinator can dispatch
  Cursor/Codex/Claude bots to build the studio platform".
- Always-allow. Asked whether Cursor/Grok workers launch always-allow like
  Codex and Claude, the owner chose "Approve always-allow (Recommended)".
- Credential. Asked which key v2 Cursor pods get, the owner chose "Reuse
  CURSOR_MEATBALL_KEY". It is a property of an existing 1Password item.
  haynes-ops syncs it into a Secret in `dev-agents` in a later PR.
- Coordinator decisions, the same day. The proprietary Cursor CLI is never
  bundled in the public image: Cursor's terms restrict reproduction. agentd
  installs a pinned archive with a SHA-256 check onto the session volume at
  boot, as v1 does. No feature flag. No model allowlist.

## 3. Decisions

1. **Provider `cursor`** joins the AgentSession agent enum. Task mode only. A
   schema rule refuses a cursor session in local or remote mode, or with an
   effort. agentd, the API and agent-run refuse the same, in one wording.
2. **Enablement is configuration.** dev-env-templates gains
   `cursor.apiKey: {name, key}`, the Secret and key that hold the Cursor API
   key. The operator sets `CURSOR_API_KEY` from it on cursor pods only.
   `CURSOR_API_KEY` is in the reserved names, so no template or profile env
   can set it. A profile's envFrom is not checked (the operator reads no
   Secrets), so agentd also removes the name from every Claude and Codex
   agent's environment. Without the reference the API refuses a cursor create
   with code `provider_unconfigured` (422) before any session or pod exists,
   and the controller builds no cursor pod.
3. **CLI install.** For agent cursor only, agentd installs the pinned archive
   at boot into `~/.local/share/cursor-agent/versions/<version>/` on the
   session volume, Cursor's own layout and v1's. It downloads over https
   only, checks the SHA-256 before it unpacks anything, unpacks in a staging
   directory (regular files, directories and relative symlinks that resolve
   inside the package, chains included; nothing written through a symlink),
   checks that the launcher prints the pinned version, then moves it into
   place. A staging directory left by an interrupted install is removed
   before a retry. Every version check runs with the cursor agent's scrubbed
   environment and no API key, keeps at most 4 KiB of output, and kills the
   launcher's process group on timeout. A mismatch, an unsafe archive or
   an unexpected install at the pinned path fails the boot with that reason,
   before any launch record exists. An unexpected install is kept for
   inspection, never replaced.
4. **Version drift.** run-agent runs the launcher's `--version` before every
   launch. If it no longer prints the pin, the task is not started: the
   result records exit 1, subtype `cli_version_mismatch`. agentd runs the
   pinned path directly, never `~/.local/bin/agent`.
5. **Launch.** See section 4.
6. **Output.** A Cursor stream parser of its own, `internal/agentd/cursor_stream.go`.
   The init event's model is logged as "CLI-reported", never as provider
   reported. The result event gives the subtype, the error flag and the text.
   The text is logged when the flag is set. Usage, cost, turns and the served
   model are Unknown: the task result carries no usage, and the usage
   observation has every metric Unknown. A schema rule and the protocol
   refuse any reported metric on a cursor observation.
7. **Failures.** A non-zero exit is a failed task. A stream that ends without
   a result event is a failed task with subtype `missing_result` and exit 1.
   A result without `is_error` counts as an error. A second result is a
   conflict: the first stays, and the observation is Partial.
8. **Common rules.** Cursor reads the repository's `CLAUDE.md` and
   `AGENTS.md` itself. Its docs name no file for user-level rules: they are
   set in the editor's settings. So the prompt starts with one line that
   names the platform rules (`~/.claude/CLAUDE.md`, the mounted common text)
   and the task's saved project rules. The platform's task guard follows, as
   Claude gets it in its system prompt. Then the task.
9. **agent-run.** `--agent cursor` needs `--model` and `--project`. The model
   is checked for shape only: letters, digits and `. _ : / -`, starting with
   a letter or digit, at most 128 bytes (a subset of the inbox's model
   pattern). `--effort` and `--max-turns` are
   refused. `--local` and `--interactive` are refused.
10. **Rights.** A cursor session's pod runs as `dev-env-agent` on its
    profile, like any task. Its API token has the same caller class and
    rights as a Claude or Codex task on that profile. Its inbox messages name
    provider `cursor`.
11. **Every provider switch is explicit.** BuildLaunch and BuildResume name
    each agent and refuse one they do not know, instead of running it as
    Claude. A cursor task never resumes. Restart reports it not resumable.
    The history archive accepts provider `cursor`, so a cursor task's HOME
    expiry is not blocked.

### Decisions this story made that the work order did not settle

- **Coordinators may create cursor tasks.** The coordinator class already
  creates Claude and Codex project tasks. Without cursor in that list the
  owner's goal fails.
- **A project is required.** A cursor task needs an accepted project snapshot,
  like a managed Codex task. It gives the rules file and a private worktree.
- **No shared workspace.** A cursor session with a workspace binding is
  refused.
- **No budget-bound dispatch.** Budgeted dispatch binds retained common
  inputs, which a cursor session does not take, so a budget-bound caller's
  cursor create is refused.
- **Not offered to cursor:** coordinator instructions, the instruction
  publisher, managed output capture, child decisions, canaries and pilots.
  Their existing Claude/Codex-only checks refuse cursor.
- **The sandbox stays at the CLI's default.** No `--sandbox` flag is passed.
- **Grant policies are unchanged.** A standing GrantPolicy that names agents
  does not match cursor until a policy names it.

## 4. Launch contract

```text
<HOME>/.local/share/cursor-agent/versions/2026.10.14-0f9edde/cursor-agent \
  -p --force --trust --output-format stream-json \
  --model <id> --workspace <worktree> "<prompt>"
```

| Part | Meaning | Source |
| --- | --- | --- |
| `-p` | Print mode, for scripts. It has write and shell tools. | [parameters](https://cursor.com/docs/cli/reference/parameters) |
| `--force` | "Force allow commands unless explicitly denied". Without it, print mode only proposes changes. This is the always-allow ruling. | [parameters](https://cursor.com/docs/cli/reference/parameters), [headless](https://cursor.com/docs/cli/headless) |
| `--trust` | Trust the workspace without prompting, headless only. | [parameters](https://cursor.com/docs/cli/reference/parameters) |
| `--output-format stream-json` | NDJSON events: `system`/`init`, `user`, `assistant`, `tool_call`, `result`. | [output format](https://cursor.com/docs/cli/reference/output-format) |
| `--model <id>` | The model, passed as given. | [parameters](https://cursor.com/docs/cli/reference/parameters) |
| `--workspace <path>` | The task's worktree. | [parameters](https://cursor.com/docs/cli/reference/parameters) |
| prompt | The positional argument. Stdin is empty. | [parameters](https://cursor.com/docs/cli/reference/parameters), [headless](https://cursor.com/docs/cli/headless) |
| `CURSOR_API_KEY` | The key, from the environment; never `--api-key`, which would put it in argv. | [authentication](https://cursor.com/docs/cli/reference/authentication) |

The pin is v1's: version `2026.10.14-0f9edde`, SHA-256
`74fac931fe3578980d070701872b7dab54eb87a9abf9b6e2e6be19beeb9167a7`, from
`https://downloads.cursor.com/lab/<version>/linux/x64/agent-cli-package.tar.gz`
(haynes-ops commit 76a53ac9). The constants are in
`internal/agentd/cursor_cli.go`. Cursor's own installer is
`curl https://cursor.com/install | bash` ([installation](https://cursor.com/docs/cli/installation));
agentd does not run it.

The agent's environment loses every Claude and Codex credential and override
(the Codex unset list, the Claude tokens, `GH_TOKEN`, `AGENTD_SESSION`). It
keeps `CURSOR_API_KEY`. gh and git read the minted token file, as for every
agent.

Cursor takes the prompt as one exec argument, not on stdin. Linux refuses an
argument of 131072 bytes or more (`MAX_ARG_STRLEN`, which counts the NUL).
The task is already capped at `protocol.MaxPromptBytes` (64 KiB) by the API,
agent-run and `Session.Validate`, so the rules line and the guard leave
about 64 KiB of room. As a backstop, `buildCursorLaunch` refuses a composed
prompt of 131072 bytes or more before it returns a launch. The CRD's 262144
byte `spec.prompt` limit is not the effective cap.

### 4.1 What a cursor pod holds

The environment strip above covers the agent's environment only. Credential
mounts and profile env come from the session's profile, not from its agent.

On the live templates of 2026-11-09:

- Profile `full` mounts the keeper's Claude and Codex access projections and
  the GCP service-account key. It also carries profile env, such as the Omni
  service-account key.
- Profile `dev` mounts the Codex access projection.
- Every profile mounts the GitHub token, which a task needs.

An always-allow Cursor agent can read every file and variable its pod holds,
as a Claude or Codex agent can. The difference is the vendor that sees the
task's context.

Dispatch rule: run a cursor worker on a profile that mounts only what its task
needs, which is the GitHub token. That profile is chosen at deploy, together
with OpenProject #502 (a workbench coordinator names its children's profile).
This change adds no source enforcement of the rule.

## 5. UNCONFIRMED

None of these is settled by Cursor's documentation. Each has the conservative
choice in source.

- **Auto-update.** The docs say the CLI "will try to auto-update by default"
  and document no switch to stop it. Whether print mode updates, and whether
  an update ever changes the pinned version directory, is unknown. agentd runs
  the pinned path and checks its version before each launch.
- **Sandbox default.** The docs list `--sandbox enabled|disabled` but not the
  default in print mode. Unknown whether the default blocks network commands
  such as `git push` or `gh`. No flag is passed; the live run will show it.
- **Exit codes.** The docs say only that a failure exits non-zero and "the
  stream may end early without a terminal event". agentd treats any non-zero
  exit and any missing result as failure.
- **Result subtypes.** Only `success` is documented. Any other subtype is
  kept (bounded) and is not success.
- **`--version` output.** v1 compares it exactly with the version string.
  Not in the docs.
- **Package layout.** The archive's one top directory and its `cursor-agent`
  launcher come from v1's script, not the docs.
- **Rules file.** No documented file route for user-level rules; the
  prompt-line route is the fallback the work order named.
- **`--trust`.** Documented for headless mode; whether `-p` counts is
  assumed.
- **Model ids.** The docs do not list ids for Grok or any other model. The
  CLI refuses an unknown id at run time.
- **A newer CLI version.** `2026.10.22-27dcf5f` is named by the allowance
  collector work. This story keeps v1's pin; a bump needs its checksum from
  an official source in a later change.

## 6. Excluded

- The phone-visible thread adapter for Cursor/Grok stays with #491
  (`agent-run thread start --agent cursor` is still refused).
- The old scaffolds (`internal/agentd/cursor_*` before this story and
  `scripts/cursor-*.py`) are untouched. Deleting them is a follow-up.
- Deploy is later and separate: the haynes-ops ExternalSecret for
  `CURSOR_MEATBALL_KEY` in `dev-agents`, the CRD copy, the templates
  `cursor.apiKey` field, the operator and agent image pins, and the live run.
  Adding `cursor.apiKey` changes the templates revision, so running sessions
  drain at their next idle moment unless the rollout hold is used.
  The templates are decoded strictly, so the `cursor:` stanza lands only
  after the operator pin that knows the field.
- No RBAC, admission or network policy change. Cursor's hosts are public on
  443, which the web egress tier allows.
- Resume, `--local`, `--interactive`, MCP servers for Cursor, usage and cost
  reporting, and the Cursor CLI's own session history in the HOME archive
  (the archive keeps agentd's state, the stream and the task log).
