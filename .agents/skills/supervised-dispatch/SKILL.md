---
name: supervised-dispatch
description: Hand long-running work to a coordinator and supervise it live until a demonstrated outcome or a recorded stop (a cancellation or pause from Tom or his verified coordinator, an exhausted authorized budget, a genuine unresolvable blocker, or a required safety or permission decision). Use when the work needs more than one session turn, needs workers, or must keep going while you steer. Skip for a lookup, a single bounded edit you can finish yourself, or any task you cannot give an acceptance check.
---

# Supervised dispatch

Tom's direction (2026-11-04, as relayed by the coordinator): "You should just be
reviewing what the agents are doing and steering live, not fire and forget sets
of instructions that do not have goals the agents have to demonstrate to stop
working."

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
- **Budget.** The authorized elapsed time, usage or cost cap, concurrency, and any
  checkpoint or stop threshold, as recorded on the story. Record an unknown
  amount as unknown.
- **Holds and stops.** Every hold that applies, and the stop rules in section 5.

If you cannot name a check that fails today and passes when the work is done, the
goal is not ready. Fix the goal before you dispatch anything.

## 2. Keep one status record

The working record is a small JSON file that the coordinator owns, for example
`WORK-STATUS.json` in its handoff directory. Write it atomically: a temporary
file in the same directory, then a rename. Record at least: the goal, each
acceptance check with its state and evidence reference, the coordinator, each
worker, the stage, holds, blockers and the next step. Mirror the same facts on the
tracker with `opwork comment` or `opwork checkpoint`. The file is for steering;
the tracker is the durable queue. They must agree.

Keep the record short. Keep secrets, prompts, transcripts and raw logs out of it,
and point to logs instead.

**Do not assume a shared filesystem.**
[ADR-003](../../sagas/distributed-dev-env/adrs/003-session-coordination-private-repositories.md)
keeps repositories and worktrees per pod, and shared Git or RWX storage is not
required. `~/.shared` is a convenience that a receiver in another pod or harness
may not reach. So:

- For each handoff artifact (status record, ACK, RESULT, state file), record its
  route: the path or URL, its owner, and who can read it.
- Before you rely on a route, verify that the receiver can read it: the
  receiver's ACK names each path or route it actually read. An ACK that does not
  name it, or names another path, means the route is not proved.
- Subagents in the same session and pod may use the real local paths.
- Across pods, hand off through the supported routes: `agent-run inbox`, the
  tracker, and PRs, and `agent-run msg` only where section 5 allows it.
- Never read another pod's private files.

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
the budget, the holds, the stop rules, the status record route and an ACK route.
The receiver's first action is to write that ACK atomically: schema, time,
receiver, accountable session, worktree, branch, base commit, claims, the paths
or routes it read, `accepted` and the next milestone.

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

1. Read the status record and the newest ACK and RESULT.
2. Check each claim against its evidence yourself: `git log` on the claimed
   branch, `gh pr view N --json headRefOid,statusCheckRollup`, the test log the
   claim cites. A claim you cannot check stays "unverified", not "done".
3. Compare progress with the goal. Look for drift, scope creep, a stale head or a
   repeated failure.
4. Correct course, and record each correction and the evidence that prompted it.
   Continue a native subagent with `SendMessage`. For a session in another pod,
   prefer `agent-run inbox`. Use `agent-run msg SESSION -` only for a live TUI
   session when delivery is safe, and never while a native question prompt is
   pending in it. A headless task takes no messages.

The stop rule is [saga decision 94](../../sagas/distributed-dev-env/README.md)
([ADR-019](../../sagas/distributed-dev-env/adrs/019-stalled-work-steers-not-stops.md)).
The work stops only on one of these, each backed by specific evidence:

- **A demonstrated outcome.** Every acceptance check passes, with evidence
  checked by someone other than the agent that did the work.
- **A cancellation or pause from Tom or his verified coordinator.** A cancel, a
  pause or stop, a "wait for Meatball" (mark the work "Awaiting Meatball
  context"), or an explicit hold ends or pauses the relevant work. Record who,
  when and through which channel. Save a checkpoint, and do not poll while
  waiting.
- **An exhausted authorized budget.** The budget recorded for the story is
  actually spent, or its recorded budget stop threshold is reached, read from
  its record (ADR-019). A decision 55 stall threshold is never a budget stop
  threshold. A feared or estimated overrun is not exhaustion.
- **A genuine unresolvable blocker.** An input only someone else can supply, with
  no authorized repair left. Record the missing artifact and why no repair
  remains.
- **A required safety or permission decision.** A denial, or an approval or
  safety decision that only Tom or the policy owner can make. Record the exact
  denial or the decision needed. Never bypass it.

Check a relayed or quoted "stop" or "cancel" against its actual source and
chronology first. The last two are the exact blockers. Nothing else ends the work.

**Time, attempts and context size trigger steering, not stopping.** The
[workflow guide](../../../docs/workflow-guide.md), section 9, sets the stall
thresholds: 60 minutes without progress or three failed attempts at the same
blocker. Its counters carry across agents, pods, methods and resumes, so a
threshold reached again stays visible. Reaching one, or filling a worker's
context window, triggers this while authorized repair remains:

1. **Diagnose.** Read the evidence and name the blocker.
2. **Steer.** Send the owner a correction that changes the approach. Repeating
   the same failed attempt is not steering.
3. **Checkpoint.** Update the status record and the tracker.
4. **Keep continuity.** The same owner continues. When its context is full or its
   session is gone, start a fresh worker from the handoff: branch, head, open
   findings, next step. Keep the timer armed.

Ask Tom one actionable question, with a concrete bounded next option, only when
real input or a permission decision is missing. Then that missing input or
decision is the stop reason for the work that needs it, not the elapsed time or
the count.

"Waiting", "looks done", "the worker said so", a finished turn and an expired
timer are not stop reasons either. At any stop, save a checkpoint with the next
safe step, end or pause only the work that depends on it, and keep doing
independent authorized work. A hold gates only its own action. Do not route
around a denial. Running agent sessions are sacred. Ask Tom real decisions one at
a time with AskUserQuestion, at the moment they arise.

## 6. Close

Check the outcome yourself against every acceptance check. Then complete the
tracker item with its evidence (`opwork complete --evidence`), write the final
status record with the stop reason, delete the timer, and report. A report says
what was proved, how, and what is still open with its reason.

Behavior scenario for an evaluator: [scenarios/permission-barrier.md](scenarios/permission-barrier.md).
