---
name: tmux-cli-handoff
description: Deliver work or a correction into an EXISTING provider CLI session (Cursor agent with Grok, Codex, Claude) that runs in tmux on a host you can legitimately reach, and prove the session consumed it. Use when a coordinator must steer a running CLI session instead of a UI. Skip for starting a new session, and never use it to create, fork, adopt or guess a session.
---

# tmux CLI handoff

Tom (2026-11-04, relayed by the coordinator): "What you need are proven skills
for how to execute actions you are struggling with", and "You should never need
cursor UI when you have CLI in dev-env".

This skill hands one bounded instruction to a session that already exists and
already owns the work. Work through the six steps in order. Each step proves one
thing; a step that fails stops the handoff at that step. It never starts, forks,
adopts or renames a session, and it grants no access.

It works inside the other loop skills: identities and the evidence ladder in
[supervised-dispatch](../supervised-dispatch/SKILL.md), corrections to the same
owner in [worker-qa-loop](../worker-qa-loop/SKILL.md), custody and check-ins in
[timer-checkin](../timer-checkin/SKILL.md). Reuse the tested tools below; do not
rebuild them.

- A Cursor CLI session started by the repo helper: [interactive Cursor session helper](../../../docs/cursor-interactive-session.md) (`inspect`, then `reply` with its receipt; it detects modals and drafts and refuses a pane it did not start).
- A v2 TUI session: `agent-run msg SESSION -` when delivery is safe, otherwise `agent-run inbox` ([CLAUDE.md](../../../CLAUDE.md), "How work happens here"; [workflow guide](../../../docs/workflow-guide.md), section 3).
- The v1 Cursor My Machines worker: haynes-ops `.agents/runbooks/cursor-v1-worker.md`. Its tmux sessions `cursor-worker-v1` and `cursor-worker-dev-env` run `agent worker`, not a chat. Never type into them.
- Cursor CLI flags: [using the CLI](https://cursor.com/docs/cli/using) and [parameters](https://cursor.com/docs/cli/reference/parameters).

## 1. Prove the actor can act

Run these as the actor and save each command, its exit status and its output.
Never print credentials or auth files.

1. **The CLI.** `command -v agent cursor-agent codex claude`, then
   `<cli> --version` and `<cli> --help` (and `codex resume --help`) on the host
   where the session runs.
2. **The host's terminal.** Same pod: `tmux has-session -t <session>`. Another
   pod: `kubectl auth can-i create pods/exec -n <namespace>`. A v2 session pod is
   reached only through `agent-run`; agents have no exec in `dev-agents`.
3. **The route.** A `yes` from `can-i` means RBAC would allow exec. It is not a
   route. Exec only where the work order or the host's runbook names exec as the
   route for this session. Never drive another environment's provider login.

Tell three failures apart, because each has a different owner:

| Class | Evidence | It means |
|---|---|---|
| `cli-missing` | `command -v` prints nothing on the host that must run the CLI | That host cannot run or resume the session. Installing it is not part of this skill. |
| `actor-rbac` | `can-i` answers `no` | The actor has no terminal on that host. Stop here: you cannot see the session, so steps 2 to 5 are unknowable. |
| `wrong-session` | Step 2 finds no record for the session, or step 4 finds a different pane | The actor may be able to act, but not on this session. |

A CLI missing on the actor alone does not block a send into a reachable pane.
It does block reading the installed version and help, unless you read them on
the host through the same route.

## 2. Map the task to the session from task records

Build one map from records about this task, and cite each record's path:
provider task ID (for example Cursor `bc-...`), local chat or session ID (or
`none`), host, tmux session, pane ID, worktree, branch and head.

- Good sources: the task's work order, ACK, RESULT and status record; the
  helper's owner marker (`@cursor_interactive_owner` and its state file);
  `agent-run show NAME -o json`; a session ID that the provider's ACK recorded.
- Never infer the map from names, from `agent ls` or `codex resume` pickers, or
  from "most recent" (`--continue`, `agent resume`, `codex resume --last`).
- Never create a session as a fallback: no `agent create-chat`, no
  `claude --fork-session`, no fresh `codex`.
- A Cursor cloud agent (`bc-...`) on a My Machines worker has no local chat unless
  a record names one. The public docs do not map cloud agents to local chat IDs.
  The worker's pane is not the chat.

No complete map from records: stop with `wrong-session` (closed error
`session-unmapped`).

## 3. Prove single-writer custody

- **Owner.** The tracker, the status record and the ACK name one custodian. You
  deliver only as that custodian or on its explicit handoff (timer-checkin,
  section 5). If another thread owns the work and you may not contact it, do
  not deliver: prepare the text and route it to that owner's channel.
- **Worktree.** On the host, `git -C <worktree> status --porcelain` and the
  branch head match the record, or the PR head on GitHub matches it.
- **No second writer.** One live process per chat. Resuming a chat whose TUI is
  still running makes two writers. No other agent is assigned in the status
  record, and nobody else is typing in the pane.

## 4. Use a supported input path

Prefer the first that applies:

1. **The session's own route.** A v2 TUI session: `agent-run msg` or
   `agent-run inbox`, as above. A helper-started Cursor session: `inspect`, then
   `reply --name <name> --receipt <receipt> --text-file <file> --submit`.
2. **Resume.** Only when the installed `--help` documents it, the chat ID is
   mapped in step 2, and the original process is stopped:
   `agent --resume <chatId>`, `codex resume <SESSION_ID>`,
   `claude --resume <session-id>`.
3. **tmux send-keys,** only into the verified pane. First read that pane's
   visible screen only (`tmux capture-pane -p -t <pane-id>`, no scrollback) and
   confirm an empty normal prompt. Then send one line literally and Enter
   separately: `tmux send-keys -t <pane-id> -l -- '<line>'`, then
   `tmux send-keys -t <pane-id> Enter`.

Never send while a modal, a question, a plan question, a trust or permission
prompt, a command approval (`y`/`n`) or a user draft is showing; a pending `y/n`
takes your keys as its answer (CLAUDE.md: never send TUI input while a native
question modal is pending). Keep the instruction to one line that points to an
authorized work-order file. Do not read other panes or scrollback history.

## 5. Prove consumption

Delivery is not consumption. A `sent` state, your text echoed on screen, or the
CLI accepting the line proves only delivery. Require, each with a timestamp
later than the send:

1. the provider's own ACK artifact that restates the correction (evidence
   ladder rung 3, supervised-dispatch section 4);
2. actual edits: a new commit or head on the claimed branch (rung 4);
3. the provider's test runs, with commands and results;
4. your independent check of those results (worker-qa-loop, section 6).

## 6. When a step is blocked

Record the block in the status record and the tracker, then stop this handoff:

```json
{"step": 1, "command": "kubectl auth can-i create pods/exec -n dev",
 "exitStatus": 0, "output": "no", "actor": "<session and ServiceAccount>",
 "target": "<task, host, session>", "closedError": "actor-rbac",
 "at": "<UTC>", "notDone": ["exec", "new session", "grant request"],
 "whoCanAct": "<the custodian, or a session that already holds that terminal>"}
```

Use only capabilities you already have. Do not request a grant, switch identity,
install a CLI, read credentials, open a UI, start a duplicate session or try
another route unless the work order authorizes it. Retry the same step only on
materially new evidence. A blocked step is an exact blocker under
supervised-dispatch section 5: end this handoff and its timer, record who can
act, and keep doing independent authorized work.
