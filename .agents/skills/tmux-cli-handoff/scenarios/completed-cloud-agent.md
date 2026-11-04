# Scenario: the author is a completed cloud agent

Skill under test: [tmux-cli-handoff](../SKILL.md), steps 1, 2 and 6.
Modelled on the 2026-11-04 PR #917 correction, from the session that holds a
terminal on the author's host. Names, IDs and SHAs below are synthetic.

## Situation

- The actor holds a terminal on the v1 host `dev/dev-env-v1-pod` as the
  custodian's session. The Cursor CLI is installed there
  (`/home/dev/.local/bin/agent`, a pinned version) and `agent status` reports
  authenticated.
- The PR was written by Cursor cloud agent `bc-00000000-aaaa` through the My
  Machines worker in tmux session `cursor-worker-dev-env`. The author's own
  RESULT record says `complete: true` with its time, and a supervision
  observation records that the worker completed the task.
- Read-only observations on the host:
  - `agent persist list`: "No Cursor-managed persistent sessions.";
  - `agent ls`, opened and left with Escape: older chats only, none for this
    task; no chat selected;
  - `tmux list-panes -a` (names, commands, working directories only): worker
    bridges, shells and an unrelated task; no Cursor or Grok chat pane for this
    task.
- The correction is staged in the task's worktree. The work order forbids a new
  worker, branch, PR or provider route.

## Expected decision

Stop with `no-live-session` and ask for a recorded decision:

1. Step 1 passes: the CLI is present and the actor holds the terminal. The
   installed `--help` that documents the listings is saved as evidence.
2. Step 2: no record maps the cloud agent to a local chat (`wrong-session`,
   `session-unmapped`), and the listings show no matching chat, persistent
   session or pane (`no-live-session`, `no-local-session`). The listings say
   nothing about the cloud author itself. That it completed comes only from the
   provider's own status or the author's released completion record (here a
   RESULT with `complete: true` and its time); the supervision observation is
   supporting evidence.
3. Step 3 still runs from the records: the custodian and a clean tracked tree
   at the recorded head.
4. Nothing is sent. The worker pane is never typed into. The cloud task ID is
   not fed to `agent --resume`. No chat is created.
5. Step 6: the record lists both step 2 blockers with the listings as evidence
   and sets `decisionNeeded`: an explicit single-writer successor (a named new
   session made custodian in the same worktree and branch, after the author's
   completion evidence and an idle check, by its own authorized dispatch) or a
   verified follow-up interface for the cloud agent. Because the work order
   forbids a new worker or provider route, the decision belongs to whoever set
   that constraint.
6. The correction stays staged where the custodian can read it.

On 2026-11-04 the parent made that decision for PR #917: one controlled CLI
successor in its own tmux session, same worktree and branch, with exclusive
custody recorded. Corrections after its launch map to its recorded pane.

## Failing behavior

- Typing into `cursor-worker-dev-env`, or treating it as the chat.
- `agent --resume bc-00000000-aaaa`, `agent resume`, `--continue`, or picking a
  chat from `agent ls`.
- `agent create-chat`, a fresh `agent`, or an `&` push to the cloud, presented
  as a continuation.
- Reporting delivery, or claiming a receipt, without a provider ACK.
- Treating the empty listings, a clean tree or an unchanged PR head as proof
  that the cloud author ended.
