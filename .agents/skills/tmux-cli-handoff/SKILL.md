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

**Task records** are the work order that assigned this task (including the one
you received), its ACK, RESULT and status record, the tracker item,
`agent-run show NAME -o json`, a launch record with the helper's owner marker,
and observations written by a session that supervises or holds the task. Cite
each one by path. An observation is evidence, not authority, and a
recommendation in one is not an authorization.

## 1. Prove the actor can act

Take the host (and its namespace, for a pod) from the task records. Then run
these as the actor, and save each command, its exit status and its output.
Never print credentials or auth files.

1. **The CLI.** Check only the session's own CLI, one name at a time, for
   example `command -v agent` (Cursor), `codex` or `claude`, then
   `<cli> --version` and `<cli> --help` (and `codex resume --help`). Do this on
   the host where the session runs; on the actor it is only a note.
2. **The host's terminal.** Same pod: the tmux server answers
   (`tmux list-sessions -F '#{session_name}'`, names only). Another pod:
   `kubectl auth can-i create pods/exec -n <namespace>` (it prints `no` and
   exits 1 when denied). A v2 session pod is reached only through `agent-run`;
   agents have no exec in `dev-agents`.
3. **The route.** A `yes` from `can-i` means RBAC would allow exec. It is not a
   route. Exec only where the work order or the host's runbook names exec as the
   route for this session. Never drive another environment's provider login.

Tell these failures apart, because each has a different owner and next step:

| Class | Closed error | Evidence | It means |
|---|---|---|---|
| `cli-missing` | `cli-not-found` | `command -v` prints nothing on the host that must run the CLI | That host cannot run or resume the session. Installing it is not part of this skill. |
| `actor-rbac` | `pods-exec-denied` | `can-i` answers `no` | The actor has no terminal on that host. It cannot check anything on the host. |
| `wrong-session` | `session-unmapped`, `pane-mismatch` | Step 2 finds no record mapping the task to a session, or step 4 finds a different pane | The actor may be able to act, but not on this session. |
| `no-live-session` | `no-local-session` | Step 2's listings show no matching local chat, persistent session or pane | There is nothing to resume or type into. Only a recorded decision can continue the work. |
| `custody` | `other-custodian`, `custodian-unrecorded` | Step 3 finds another custodian with no handoff to you, or no record names a custodian | You may not deliver, even with a terminal. |
| `modal-pending` | `modal-trust`, `modal-permission`, `modal-approval`, `modal-unknown` | Step 4's read of the verified pane shows a local trust, permission, approval or confirmation prompt | Not an auth failure, not an auto-review denial and not the wrong session. It needs an explicit, recorded answer before anything is sent. |

A CLI missing on the actor alone is a note, not a blocker: it does not block a
send into a reachable pane. It does block reading the installed version and
help, unless you read them on the host through the same route.

After `actor-rbac` or `cli-missing`, skip every check that needs the host and
record each one as `unknown` (for example whether a local session exists). Run
steps 2 and 3 from the task records whatever step 1 or step 2 found; nothing is
sent until steps 1 to 3 all pass. Record every blocker you find (step 6), even
two in the same step: a grant for one blocker must not look like it would
unblock the rest.

## 2. Map the task to the session from task records

Build one map from task records: provider task ID (for example Cursor
`bc-...`), local chat or session ID (or `none`), host, tmux session, pane ID,
worktree, branch and head.

- Never infer the map from names, from `agent ls` or `codex resume` pickers, or
  from "most recent" (`--continue`, `agent resume`, `codex resume --last`).
- Never create a session as a fallback: no `agent create-chat`, no
  `claude --fork-session`, no fresh `codex`, no `&` push to the cloud.
- A Cursor cloud agent (`bc-...`) runs on Cursor's backend, here through a My
  Machines worker. The public CLI docs say a cloud agent is picked back up on
  web or mobile (cursor.com/agents); they document no CLI follow-up into one and
  no mapping from it to a local chat ID. The worker's pane is not the chat. A
  local CLI reaches a cloud agent only if a task record names a local chat for
  it.

No complete map from records: `wrong-session` (closed error `session-unmapped`).

**When the author has ended.** The session that did the work may no longer
exist: a cloud agent task that completed, a headless task that exited, a TUI
that was closed. Confirm it on the host with the read-only listings that the
installed `--help` documents, for example Cursor's `agent persist list`, and
`agent ls` opened and left without selecting a chat. Then read the panes' names,
commands and working directories only
(`tmux list-panes -a -F '#{session_name} #{pane_id} #{pane_current_command} #{pane_current_path}'`).
Save the installed `--help` that documents each listing as evidence. A listing
can show that no matching local session exists; it never selects one. With no
matching local chat, persistent session or pane: `no-live-session` (closed
error `no-local-session`).

Keep two findings apart. "No mapped local CLI session" says nothing about the
author itself. A missing local chat, persistent session or pane, a clean tree
and an unchanged PR head do not prove that a cloud author has ended or released
its edit custody; a backend writer can still exist. That needs its own evidence:
the provider's own task status, or a completion record the author released (for
example a RESULT with `complete: true` and its time). Without it, record the
author's state as `unknown`.

Do not feed the cloud task ID to a local `--resume`, and do not start a chat in
its place. Ask the parent or custodian for a recorded decision, one of:

1. **An explicit single-writer successor.** Only after evidence that the
   author has ended or released custody (above). A named new session becomes
   the custodian on record, in the same worktree and branch, after an idle check
   (no process in the worktree, clean tree at the recorded head). It starts from
   a handoff packet (branch, head, findings, the correction), and the old author
   is recorded as ended (timer-checkin, section 5). Starting it is its own
   authorized dispatch (supervised-dispatch), not part of this skill. Its launch
   record names the tmux session, pane ID, pid and owner marker (the helper
   records them at `start`, and `inspect` returns them), and the chat ID once
   the CLI shows it, so that the
   next correction maps by step 2. Prove its consumption of the packet by step 5.
2. **A verified follow-up interface** for that task, such as the provider's own
   follow-up channel for a cloud agent, used only when it is authorized for this
   actor.

If the work order forbids both (for example "no new worker or provider route"),
the decision belongs to whoever set that constraint: name it in
`decisionNeeded`. A cloud agent's own follow-up channel (its web or mobile UI,
or an API) is outside this skill; the CLI is the route only where a CLI session
exists or is authorized. Until a decision is recorded, the correction stays
staged where the custodian can read it, and nothing is sent.

## 3. Prove single-writer custody

- **Owner.** The task records name one custodian, and the target session must
  be it. You deliver only as that custodian, as its recorded supervisor or
  driver, or on its explicit handoff (timer-checkin, section 5). Otherwise stop with `custody` (closed error `other-custodian`, or
  `custodian-unrecorded` when no record names one): stage the correction and
  give it to your parent, who routes it to the custodian through that
  custodian's recorded channel (its tracker item, its PR, or `agent-run inbox`).
  If you may not contact the custodian, you do not use that channel yourself.
- **Worktree.** On the host, the tracked tree is clean
  (`git -C <worktree> status --porcelain --untracked-files=no` prints nothing),
  any untracked files are the task's own recorded receipts or its recorded
  handoff packet, and the branch head matches the record; or the PR head on
  GitHub matches it. Stage a correction outside the author's worktree where you
  can, in the task's handoff route. If you cannot reach that route, stage it in
  your own handoff route and give the path to your parent.
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

**A pending local permission modal** (`modal-pending`), such as Cursor's
"Workspace Trust Required" for the worktree, is answered only like this:

1. Never auto-accept it, and never relaunch, kill the pane or add a flag such as
   `--trust` or `--force` to get past it.
2. Get the specific approval of whoever owns that access decision (Tom,
   unless a recorded rule delegates it), for exactly that answer and exactly
   that path, through a verified channel. A work order's handoff gate, a custody
   ACK, a launch authorization or a coordinator's recommendation never
   authorizes accepting a trust or permission prompt. Send the exact question to
   your parent: the session with Tom's phone route asks him, one question at a
   time (CLAUDE.md). Say what the choice allows and whether it persists after
   this session; if the provider does not document that, say it is unknown.
   Until the approval arrives, record the modal as a pending blocker and send
   nothing; the handoff is not complete.
3. Read the pane again (`inspect`) just before answering, and confirm the same
   modal, pane, pid and owner marker. Then answer it once, in that pane, with the
   single choice it shows, for example `tmux send-keys -t <pane-id> a`. The
   helper's `reply` refuses modals by design; this key press is the one step
   outside it.
4. Record the decision: who authorized it, the exact path, the choice, the pane
   and the time.
5. Read the pane again (`inspect`, for a helper-started session) and confirm the
   modal is gone and a normal empty prompt shows. Only then send the
   instruction by the route above.

## 5. Prove consumption

Delivery is not consumption. A `sent` state, your text echoed on screen, or the
CLI accepting the line proves only delivery. Require, each with a timestamp
later than the send:

1. the provider's own ACK artifact that restates the correction (evidence
   ladder rung 3, supervised-dispatch section 4). If it stays on a host you
   cannot reach, the custodian copies it to a route you can read; until then
   only the branch head on GitHub (rung 4) is yours to observe;
2. actual edits: a new commit or head on the claimed branch (rung 4);
3. the provider's test runs, with commands and results;
4. your independent check of those results (worker-qa-loop, section 6).

## 6. When a step is blocked

Write one record to the status record your work order names, and mirror it on
the tracker item it names. If it names neither, put the record in your report
to your parent and let the parent file it; do not open a new tracker item or
status record for it. Then stop this handoff. Use the shape in
[references/blocker-record.md](references/blocker-record.md): the staged
correction's path, every blocker with its class, closed error and evidence, the
checks left `unknown`, notes, what you did not do, who can act, and the decision
needed.

Use only capabilities you already have. Do not request a grant, switch identity,
install a CLI, read credentials, open a UI, start a duplicate session or try
another route unless the work order authorizes it. Retry a step only on
materially new evidence. A blocked handoff is an exact blocker under
supervised-dispatch section 5: end this handoff and its check-in timer, if one
exists, record who can act, and keep doing independent authorized work.

Behavior scenarios for an evaluator: [no terminal from a v2 pod](scenarios/no-terminal-from-v2-pod.md),
[completed cloud agent](scenarios/completed-cloud-agent.md) and
[workspace trust modal](scenarios/workspace-trust-modal.md).
