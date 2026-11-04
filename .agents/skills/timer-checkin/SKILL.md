---
name: timer-checkin
description: Check in on supervised work on a timer, with the session timers Claude Code already has and a persisted state file, so one custodian keeps it across restarts and handoffs and stops it only on a demonstrated outcome or a recorded stop (a cancellation or pause from Tom or his verified coordinator, an exhausted authorized budget, a genuine unresolvable blocker, or a required safety or permission decision). Use while supervising long-running work. Skip for one wait you can cover with a single background command, and never build a new scheduler.
---

# Timer check-in

A check-in is a scheduled turn in which the coordinator reads the status record,
checks the evidence and steers. This skill uses only existing mechanisms. The
steering itself is in [supervised-dispatch](../supervised-dispatch/SKILL.md), and
the loop it watches is in [worker-qa-loop](../worker-qa-loop/SKILL.md). This skill
grants no scope, access or approval.

## 1. What exists

| Mechanism | What it does | Limits |
|---|---|---|
| `CronCreate`, `CronList`, `CronDelete` | Queue a prompt into this Claude session on a five-field cron in local time, recurring or once (`recurring: false`). | Session-only by default: held in memory and gone when the Claude session ends. Fires only while the REPL is idle, never mid-turn. Recurring jobs expire after 7 days: one last fire, then deletion. The CLI also has a gated `durable: true` option that writes `.claude/scheduled_tasks.json`; do not use it unless Tom asks. Even then a job fires only after a Claude session starts again, so it is not an autonomous wake. |
| `ScheduleWakeup` | Paces `/loop` in dynamic mode. Call it before the turn ends with the same prompt; `stop: true` ends the loop. | Session-scoped. The delay is clamped to 60 to 3600 seconds. |
| `Monitor` | Streams each output line of a script as an event, for live watching: CI checks landing, a log line, a file change. | Each monitor ends at its timeout, at most 30 minutes; re-arm it to keep watching. Its filter must match failures too, or silence looks like progress. |
| Bash `run_in_background` | One notification when a command exits. | One event, not a schedule. |

The durable platform timers are different, and neither one is a check-in:

- **dev-env Reminders** ([DESIGN-032](../../sagas/distributed-dev-env/designs/032-reminders.md),
  `agent-run remind`, switched on in GitOps, Pushover only, by haynes-ops #4632)
  page Tom when due. They do not wake an agent or run work.
- **ScheduledWork** ([DESIGN-054](../../sagas/distributed-dev-env/designs/054-scheduled-agent-work.md))
  is source only and default off. It is not deployed. Do not plan on it.

Do not invent a scheduler: no busy loops, no unbounded sleep loops, no cron in the
pod, no CronJob. A bounded wait is fine: a `Monitor` with a timeout, or a
`run_in_background` until-loop that polls at a sensible interval and exits when
its condition is met.

**The tool reality, in Claude Code 2.1.312 as installed here (2026-11-04).** The
coordinator's own session has `CronCreate` and `ScheduleWakeup`. A native
subagent has `Monitor` but not those tools. Timers are session-only. Nothing
discovers them automatically: another session cannot see, adopt or resume one.
There is no durable autonomous wake: nothing wakes a coordinator that is not
running. Recovery means a resumed coordinator re-creates the job from the state
file (section 4). A headless `-p` task ends when its prompt returns, and its jobs
end with it. Keep every timer in the coordinator's session and supervise
subagents and tasks from there.

## 2. Persist the state first

Write a state file, for example `<NAME>-STATE.json` in the coordinator's handoff
directory, atomically (temporary file, then rename). Put it where the
coordinator itself can read it after a resume on a new pod, which keeps the same
volume. Do not assume another pod can read it: mirror the task identity, stage and
next check on the tracker, so a successor elsewhere can pick up custody through
a supported route (see supervised-dispatch, section 2). It holds:

- `custodian`: the one session that holds custody and the only one with a timer
  for this task, and `custody`, the history of handoffs (section 5);
- the task identity: tracker item, PR, branch, worker and QA identities;
- the goal and a reference to its acceptance checks;
- `timer`: the mechanism, job ID, schedule, next check, created time and expiry;
- `stage`, and evidence references (paths, SHAs, CI run IDs), never raw logs;
- `lastCheck`: time and result;
- `stop`: reason, time and evidence, once the work ends.

Create the job, then write its job ID into the file. The fired prompt names the
state file and says what to do, because the context may have been compacted by
the time it fires. For example: "Timer check-in for OP385: read
`<state file>`, verify evidence, steer, update the state file; stop only for a
stop reason in supervised-dispatch section 5."

Pick a cadence that matches what you wait for: one check near the end of an
8-minute CI run rather than eight, 20 to 30 minutes for idle supervision. Avoid
minutes :00 and :30. The keeper pilot (OP385) uses `7,27,47 * * * *`.

## 3. Each check-in

1. Read the state file and the status record. Trust the files and fresh reads,
   not memory.
2. Check the evidence (see supervised-dispatch, section 5). Steer or correct.
   Quiet time or repeated failures trigger the stall rule in
   supervised-dispatch, section 5. They never end supervision or the timer.
3. Update `lastCheck`, `nextCheck`, `stage` and the evidence references.
4. Mirror a meaningful change on the tracker.

## 4. Recover after an interruption

A session-only job does not survive a restart, a resume on a new pod, or the
session's end. After any of these, and whenever `lastCheck` is more than two
intervals old:

1. Read the state file. If it names another custodian, do not create a timer:
   only the current custodian holds one. Report what you see to that custodian
   or to the parent.
2. Run `CronList`. If the recorded job ID is missing, create the job again with
   the same prompt.
3. Write the new job ID and a recovery note: time, old job ID, and why.
4. Run one check-in now instead of waiting for the next fire.

Custody stays with the custodian named in the file. Re-create a recurring job
before its 7-day expiry if the work will run longer, and record it the same way.
A long foreground command delays every fire until the turn ends, so keep turns
short while a timer is armed.

## 5. Hand custody over

Only the current custodian holds a timer for the task. When it hands off,
because its context is filling, its session is stopping, or the parent reassigns
the work:

1. The custodian writes the successor into the state file: `custodian` becomes
   the successor, and `custody` gains an entry with the old and new owner, the
   time and the reason. It mirrors this on the tracker, and sends the successor
   the handoff through a route the successor can read (supervised-dispatch,
   section 2).
2. The old custodian runs `CronDelete` on its own job and records that.
3. The successor re-creates the timer under its own session, writes the new job
   ID, and reports to the parent (or, for a top-level coordinator, to Tom or his
   verified coordinator): the new custodian, the job ID and the next check.
4. Until that report arrives, the parent treats custody as in transit and checks
   it at its own next check-in.

If the custodian's session ended before step 1, the parent names the successor
and records the transfer. A top-level coordinator has no parent session, so Tom
or his verified coordinator names its successor. No session takes custody or
creates a timer on its own.

## 6. Complete or pause

Stop the timer only for a stop reason in supervised-dispatch section 5
(saga decision 94), backed by evidence: a demonstrated outcome, a cancellation or
pause from Tom or his verified coordinator (including "wait for Meatball" and an
explicit hold), an exhausted authorized budget or its recorded stop threshold, a
genuine unresolvable blocker, or a required safety or permission decision.
Quiet time, failed attempts, a full context window and a 7-day expiry never end
the timer. They trigger the stall rule in supervised-dispatch section 5, and the
job is re-armed or re-created. Waiting for an event a check can observe, such as
a natural keeper refresh, is pending, not blocked: keep the timer and report it
pending. When the work ends or pauses:

1. `CronDelete` the job (or `ScheduleWakeup` with `stop: true`), then `CronList`
   to confirm it is gone. Do not keep polling during a pause.
2. Write `stop` in the state file with the reason, time and evidence, and for a
   pause the condition that resumes it.
3. Record the end or the pause on the tracker. A verified resume re-creates the
   timer from the state file.

This text is not operational proof. A real wake, re-arm, recovery and completion
are proved by the keeper pilot (OP385), not by this file.

Behavior scenario for an evaluator: [scenarios/quiet-65-minutes.md](scenarios/quiet-65-minutes.md).
