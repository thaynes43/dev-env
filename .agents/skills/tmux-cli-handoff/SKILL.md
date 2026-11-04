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
thing, and nothing is sent until steps 1 to 3 pass. It never starts, forks,
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

**Task records** are the work order, ACK, RESULT and status record of this
task, the tracker item, `agent-run show NAME -o json`, the helper's owner
marker, and observations written by a session that supervises or holds the
task. Cite each one by path. An observation is evidence, not authority.

## 1. Prove the actor can act

Take the host (and its namespace, for a pod) from the task records. Then run
these as the actor, and save each command, its exit status and its output.
Never print credentials or auth files.

1. **The CLI.** `command -v agent cursor-agent codex claude`, then
   `<cli> --version` and `<cli> --help` (and `codex resume --help`) on the host
   where the session runs.
2. **The host's terminal.** Same pod: `tmux has-session -t <session>`. Another
   pod: `kubectl auth can-i create pods/exec -n <namespace>` (it prints `no` and
   exits 1 when denied). A v2 session pod is reached only through `agent-run`;
   agents have no exec in `dev-agents`.
3. **The route.** A `yes` from `can-i` means RBAC would allow exec. It is not a
   route. Exec only where the work order or the host's runbook names exec as the
   route for this session. Never drive another environment's provider login.

Tell these failures apart, because each has a different owner and next step:

| Class | Evidence | It means |
|---|---|---|
| `cli-missing` | `command -v` prints nothing on the host that must run the CLI | That host cannot run or resume the session. Installing it is not part of this skill. |
| `actor-rbac` | `can-i` answers `no` | The actor has no terminal on that host. It cannot check anything on the host. |
| `wrong-session` | Step 2 finds no record mapping the task to a session, or step 4 finds a different pane | The actor may be able to act, but not on this session. |
| `no-live-session` | Step 2 shows the author has ended: no matching chat, persistent session or pane exists | There is nothing to resume or type into. Only a recorded decision can continue the work. |
| `custody` | Step 3 finds another custodian and no handoff to you | You may not deliver, even with a terminal. |

A CLI missing on the actor alone does not block a send into a reachable pane.
It does block reading the installed version and help, unless you read them on
the host through the same route.

After `actor-rbac` or `cli-missing`, skip every check that needs the host, but
still do steps 2 and 3 from the task records. Record every blocker you find
(step 6): a grant for one blocker must not look like it would unblock the rest.

## 2. Map the task to the session from task records

Build one map from task records: provider task ID (for example Cursor
`bc-...`), local chat or session ID (or `none`), host, tmux session, pane ID,
worktree, branch and head.

- Never infer the map from names, from `agent ls` or `codex resume` pickers, or
  from "most recent" (`--continue`, `agent resume`, `codex resume --last`).
- Never create a session as a fallback: no `agent create-chat`, no
  `claude --fork-session`, no fresh `codex`, no `&` push to the cloud.
- A Cursor cloud agent (`bc-...`) runs on Cursor's backend, here through a My
  Machines worker. The public CLI docs document no follow-up into a cloud agent
  and no mapping from it to a local chat ID, and the worker's pane is not the
  chat. A local CLI reaches it only if a task record names a local chat for it.

No complete map from records: `wrong-session` (closed error `session-unmapped`).

**When the author has ended.** The session that did the work may no longer
exist: a cloud agent task that completed, a headless task that exited, a TUI
that was closed. Confirm it on the host with the read-only listings that the
installed `--help` documents, for example Cursor's `agent persist list`, and
`agent ls` opened and left without selecting a chat. Then read the panes' names,
commands and working directories only
(`tmux list-panes -a -F '#{session_name} #{pane_id} #{pane_current_command} #{pane_current_path}'`).
A listing can show that no matching session exists; it never selects one. With
no matching chat, persistent session or pane: `no-live-session`.

Do not feed the cloud task ID to a local `--resume`, and do not start a chat in
its place. Ask the parent or custodian for a recorded decision, one of:

1. **An explicit single-writer successor.** A named new session becomes the
   custodian on record. It starts from a handoff packet (branch, head, findings,
   the correction), and the old author is recorded as ended (timer-checkin,
   section 5). Starting it is its own authorized dispatch
   (supervised-dispatch), not part of this skill. Then run this skill again
   against the successor.
2. **A verified follow-up interface** for that task, such as the provider's own
   follow-up channel for a cloud agent, used only when it is authorized for this
   actor.

Until one is recorded, the correction stays staged where the custodian can read
it, and nothing is sent.

## 3. Prove single-writer custody

- **Owner.** The tracker, the status record and the ACK name one custodian. You
  deliver only as that custodian or on its explicit handoff (timer-checkin,
  section 5). Otherwise stop with `custody`: stage the correction and give it to
  your parent, who routes it to the custodian through that custodian's recorded
  channel (its tracker item, its PR, or `agent-run inbox`). If you may not
  contact the custodian, you do not use that channel yourself.
- **Worktree.** On the host, `git -C <worktree> status --porcelain` and the
  branch head match the record, or the PR head on GitHub matches it.
- **No second writer.** One live process per chat. Resuming a chat whose TUI is
  still running makes two writers. No other agent is assigned in the status
  record, and nobody else is typing in the pane. Never push to the author's
  branch yourself.

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

Write one record to the status record your work order names (if you did not
dispatch the task, your parent's), mirror it on the tracker, then stop this
handoff. List every blocker found, with its own evidence:

```json
{"recordedAt": "<UTC>", "actor": "<session and ServiceAccount>",
 "target": "<task, host, session, PR and head>",
 "blockers": [
  {"step": 1, "closedError": "actor-rbac",
   "evidence": [{"command": "kubectl auth can-i create pods/exec -n <ns>",
                 "exitStatus": 1, "output": "no", "at": "<UTC of the command>"}]},
  {"step": 2, "closedError": "session-unmapped",
   "evidence": [{"record": "<path>", "finding": "names no local chat or pane"}]}],
 "notDone": ["exec", "new session", "grant request", "send-keys"],
 "whoCanAct": "<the custodian, or a session that already holds that terminal>",
 "decisionNeeded": "<for no-live-session: successor or follow-up interface>"}
```

Use only capabilities you already have. Do not request a grant, switch identity,
install a CLI, read credentials, open a UI, start a duplicate session or try
another route unless the work order authorizes it. Retry a step only on
materially new evidence. A blocked handoff is an exact blocker under
supervised-dispatch section 5: end this handoff and its check-in timer, if one
exists, record who can act, and keep doing independent authorized work.
