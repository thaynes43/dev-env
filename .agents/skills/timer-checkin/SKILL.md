---
name: timer-checkin
description: Check in on supervised work on a timer, with the session timers Claude Code already has and a persisted state file, so a coordinator keeps custody across restarts and stops the timer only on a proved outcome, a cancellation or an exact blocker. Use while supervising long-running work. Skip for one wait you can cover with a single background command, and never build a new scheduler.
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
| `CronCreate`, `CronList`, `CronDelete` | Queue a prompt into this Claude session on a five-field cron in local time, recurring or once (`recurring: false`). | Session-only by default: held in memory and gone when the Claude session ends. Fires only while the REPL is idle, never mid-turn. Recurring jobs expire after 7 days: one last fire, then deletion. Some CLI builds offer `durable: true`, which writes `.claude/scheduled_tasks.json`; use it only if Tom asks, since it still fires only while a Claude session runs. |
| `ScheduleWakeup` | Paces `/loop` in dynamic mode. Call it before the turn ends with the same prompt; `stop: true` ends the loop. | Session-scoped. The delay is clamped to 60 to 3600 seconds. |
| `Monitor` | Streams each output line of a script as an event, for live watching: CI checks landing, a log line, a file change. | Each monitor ends at its timeout, at most 30 minutes; re-arm it to keep watching. Its filter must match failures too, or silence looks like progress. |
| Bash `run_in_background` | One notification when a command exits. | One event, not a schedule. |

The durable platform timers are different, and neither one is a check-in:

- **dev-env Reminders** ([DESIGN-032](../../sagas/distributed-dev-env/designs/032-reminders.md),
  `agent-run remind`, switched on in GitOps, Pushover only, by haynes-ops #4632)
  page Tom when due. They do not wake an agent or run work.
- **ScheduledWork** ([DESIGN-054](../../sagas/distributed-dev-env/designs/054-scheduled-agent-work.md))
  is source only and default off. It is not deployed. Do not plan on it.

Do not invent a scheduler: no sleep loops, no cron in the pod, no CronJob.

The timer lives in the coordinator's own session. On 2026-11-04 a native
subagent's tool set had `Monitor` but not `CronCreate` or `ScheduleWakeup`. A
headless `-p` task ends when its prompt returns, and its jobs end with it.
Supervise both from the coordinator's timer.

## 2. Persist the state first

Write a state file in the handoff directory, for example
`~/.shared/handoffs/<slug>/<NAME>-STATE.json`, atomically (temporary file, then
rename). It holds:

- `coordinator`: the session that has custody;
- the task identity: tracker item, PR, branch, worker and QA identities;
- the goal and a reference to its acceptance checks;
- `timer`: the mechanism, job ID, schedule, next check, created time and expiry;
- `stage`, and evidence references (paths, SHAs, CI run IDs), never raw logs;
- `lastCheck`: time and result;
- `stop`: reason, time and evidence, once the work ends.

Create the job, then write its job ID into the file. The fired prompt names the
state file and says what to do, because the context may have been compacted by
the time it fires. For example: "Timer check-in for OP385: read
`<state file>`, verify evidence, steer, update the state file; stop only on a
proved outcome, a cancellation or an exact blocker."

Pick a cadence that matches what you wait for: one check near the end of an
8-minute CI run rather than eight, 20 to 30 minutes for idle supervision. Avoid
minutes :00 and :30. The keeper pilot (OP385) uses `7,27,47 * * * *`.

## 3. Each check-in

1. Read the state file and the status record. Trust the files and fresh reads,
   not memory.
2. Check the evidence (see supervised-dispatch, section 5). Steer or correct.
3. Update `lastCheck`, `nextCheck`, `stage` and the evidence references.
4. Mirror a meaningful change on the tracker.

## 4. Recover after an interruption

A session-only job does not survive a restart, a resume on a new pod, or the
session's end. After any of these, and whenever `lastCheck` is more than two
intervals old:

1. Read the state file. If it names another coordinator, do not create a second
   timer; report what you see to that coordinator.
2. Run `CronList`. If the recorded job ID is missing, create the job again with
   the same prompt.
3. Write the new job ID and a recovery note: time, old job ID, and why.
4. Run one check-in now instead of waiting for the next fire.

Custody stays with the coordinator named in the file. Re-create a recurring job
before its 7-day expiry if the work will run longer, and record it the same way.
A long foreground command delays every fire until the turn ends, so keep turns
short while a timer is armed.

## 5. Complete

Stop the timer only on a proved outcome, a cancellation or an exact blocker, as
supervised-dispatch defines them. Waiting for an event a check can observe, such
as a natural keeper refresh, is pending, not blocked: keep the timer and report
it pending. When the work ends:

1. `CronDelete` the job (or `ScheduleWakeup` with `stop: true`), then `CronList`
   to confirm it is gone.
2. Write `stop` in the state file with the reason, time and evidence.
3. Record the end on the tracker.

This text is not operational proof. A real wake, re-arm, recovery and completion
are proved by the keeper pilot (OP385), not by this file.
