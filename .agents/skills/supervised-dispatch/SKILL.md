---
name: supervised-dispatch
description: Hand long-running work to a coordinator and supervise it live until a proved outcome, a cancellation or an exact blocker. Use when the work needs more than one session turn, needs workers, or must keep going while you steer. Skip for a lookup, a single bounded edit you can finish yourself, or any task you cannot give an acceptance check.
---

# Supervised dispatch

Tom's rule (2026-11-04): "You should just be reviewing what the agents are doing
and steering live, not fire and forget sets of instructions that do not have goals
the agents have to demonstrate to stop working."

This skill is for the session that hands work to a coordinator and then supervises
it. The coordinator runs the build loop with [worker-qa-loop](../worker-qa-loop/SKILL.md).
Both check in on a timer with [timer-checkin](../timer-checkin/SKILL.md). This skill
grants no scope, access or approval. Read the linked rules; do not copy them here.

- Ground rules, delivery gates, model and subagent tiers: [CLAUDE.md](../../../CLAUDE.md) and `~/.claude/CLAUDE.md`
- Session routes, task results and outcome files: [workflow guide](../../../docs/workflow-guide.md), sections 3 and 9
- Keeping custody of a result when its worker exits: [coordinator result handoff](../../../docs/coordinator-result-handoff.md)
- Routine questions between sessions: [ask your coordinator](../../../docs/ask-your-coordinator.md)

## 1. Write the goal and its proof before you dispatch

Put the story in OpenProject first (`opwork search`, then `create` or `claim`).
Write down:

- **Goal.** One outcome in one sentence, for a named user.
- **Acceptance checks.** For each check: the command or observation, the result
  that passes, the evidence that proves it (an exact head SHA, a CI run ID, a test
  log path, an image digest), and the state it proves: source, tested, installed
  or usable. Keep those four states apart.
- **Boundaries.** Repositories, worktrees, files and runtime resources the
  coordinator may change. Everything else is out of scope.
- **Holds and stops.** Every hold that applies, and the stop rules in section 5.

If you cannot name a check that fails today and passes when the work is done, the
goal is not ready. Fix the goal before you dispatch anything.

## 2. Keep one status record

The working record is a small JSON file in a handoff directory, for example
`~/.shared/handoffs/<slug>/WORK-STATUS.json`. Write it atomically: a temporary
file in the same directory, then a rename. Record at least: the goal, each
acceptance check with its state and evidence reference, the coordinator, each
worker, the stage, holds, blockers and the next step. Mirror the same facts on the
tracker with `opwork comment` or `opwork checkpoint`. The file is for steering;
the tracker is the durable queue. They must agree.

`~/.shared` is read by every session. Keep the record short and keep secrets,
prompts, transcripts and raw logs out of it. Point to logs by path instead.

## 3. Record identities

- **Coordinator.** Its session name (the pod name, also in `AGENTD_SESSION`), or
  for a native subagent its task title.
- **Separate pod.** `agent-run -p` prints the session name. Record it with its UID
  from `agent-run show NAME -o json`. A headless task runs its prompt once and does
  not take messages. It reports through its PR and the outcome file
  `~/work/<session>.outcome.json`, which `agent-run show` reads back.
- **Native subagent.** Its task title and the launch settings you asked for, such
  as `subagent_type: opus-worker`. Label the model as the launch setting, not as a
  verified fact, unless you observed it.
- **Parent.** Who dispatched whom.

Never record an identity, model or result you did not observe as verified. Write
"owner-reported" or "not re-verified here" instead.

## 4. Dispatch, then wait for real work

The work order states the goal, the acceptance checks, the claims, the boundaries,
the holds, the stop rules, the status record path and an ACK file path. The
receiver's first action is to write that ACK file atomically: schema, time,
receiver, accountable session, worktree, branch, base commit, claims, `accepted`
and the next milestone.

**An assignment or an ACK is not proof of execution.** An assignment says you
asked. An ACK says the receiver accepted the scope. Proof is a commit on the
claimed branch, a test log, a CI run on the exact head, or a PR state read from
GitHub. Record "assigned", "acknowledged" and "first concrete work seen" as
separate stages.

If no ACK arrives by the first check-in, read the receiver's state
(`agent-run show`, or the subagent's notification) before you send anything else.
Delivery is not acknowledgement. Do not resend blindly.

## 5. Steer live

At every check-in (see [timer-checkin](../timer-checkin/SKILL.md)):

1. Read the status record and the newest ACK and RESULT files.
2. Check each claim against its evidence yourself: `git log` on the claimed
   branch, `gh pr view N --json headRefOid,statusCheckRollup`, the test log the
   claim cites. A claim you cannot check stays "unverified", not "done".
3. Compare progress with the goal. Look for drift, scope creep, a stale head or a
   repeated failure.
4. Correct course. Continue a native subagent with `SendMessage`. Use
   `agent-run msg SESSION -` only for a TUI session when delivery is safe, never
   while a question prompt is open; otherwise use `agent-run inbox`. Record each
   correction and the evidence that prompted it.

The work does not stop until one of these is true:

- **A proved outcome.** Every acceptance check passes, with evidence checked by
  someone other than the agent that did the work.
- **A cancellation.** Tom, or his verified coordinator, cancels. Record who and when.
- **An exact blocker.** A named, evidenced condition that stops the next step: a
  denial, a missing approval, a hold, a prerequisite that is not ready, the
  recorded budget or context threshold, or 60 minutes without progress or three
  failed attempts at the same blocker.

"Waiting", "looks done", "the worker said so", a finished turn and an expired
timer are not stop reasons. At an exact blocker, save a checkpoint with the next
safe step, end only the work that depends on it, and keep doing independent
authorized work. A hold gates only its own action. Do not route around a denial.
Running agent sessions are sacred. Ask Tom real decisions one at a time with
AskUserQuestion, at the moment they arise.

## 6. Close

Check the outcome yourself against every acceptance check. Then complete the
tracker item with its evidence (`opwork complete --evidence`), write the final
status record with the stop reason, delete the timer, and report. A report says
what was proved, how, and what is still open with its reason.
