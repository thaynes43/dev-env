---
name: timer-checkin
description: Check in on supervised work on a timer, with the session timers Claude Code already has and a persisted state file, so one custodian keeps it across restarts and handoffs and stops it only on a demonstrated outcome or a recorded stop (a cancellation or pause from Tom or his verified coordinator, an exhausted authorized budget, a genuine unresolvable blocker, or a denied safety or permission decision; a pending one stops only its own action). Use while supervising long-running work. Skip for one wait you can cover with a single background command, and never build a new scheduler.
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
`run_in_background` until-loop that polls at a sensible interval and exits on
its condition **or at its deadline**, whichever comes first. A loop with no
deadline is unbounded even when it greps for a phase: a task's phase stays
Running after its task ends, and there is no Succeeded phase, so such a loop
can wait forever.

A bounded wait for one headless child. It ends when the child ends or stops,
when reads keep failing or are refused, at the deadline, or at once when its
own inputs are bad. It prints one `END` line for each way it ends and one
`READ-FAIL` line for each failed read, so a `Monitor` filter on
`END|READ-FAIL` sees failures too:

```bash
# The deadline is absolute, from the custody record, so a re-armed wait keeps it.
child=CHILD-NAME; dir=$HOME/work/OP000; fails=0
deadline=$(date -u -d 2026-11-04T15:00:00Z +%s 2>/dev/null)
case "$deadline" in '' | *[!0-9]*) echo "END bad deadline"; exit 2 ;; esac
mkdir -p "$dir" || { echo "END bad dir $dir"; exit 2; }
while :; do
  if agent-run show "$child" -o json > "$dir/child.json.tmp"; then
    fails=0; mv "$dir/child.json.tmp" "$dir/child.json"
    st=$(jq -r '.agentStatus.state // "none"' "$dir/child.json")
    ph=$(jq -r '.phase // "none"' "$dir/child.json")
    case "$st" in
      exited | failed | interrupted) echo "END child ended state=$st phase=$ph"; exit 0 ;;
    esac
    case "$ph" in
      Failed | Suspended | Archived) echo "END child stopped state=$st phase=$ph"; exit 0 ;;
    esac
  else
    rc=$?; fails=$((fails + 1)); echo "READ-FAIL exit=$rc count=$fails"
    case "$rc" in 2 | 3 | 4) echo "END read refused exit=$rc"; exit 3 ;; esac
    [ "$fails" -ge 5 ] && { echo "END reads failing exit=$rc"; exit 3; }
  fi
  [ "$(date +%s)" -ge "$deadline" ] && { echo "END deadline state=${st:-unknown}"; exit 4; }
  sleep 120
done
```

Save it as a script and run it with `bash` through Bash `run_in_background`,
which reports once when it exits, or under a `Monitor` for at most 30 minutes,
then re-arm it with the same deadline. Never run it in the foreground
(section 4). Take the deadline from the work order. If it has none, get one
from whoever owns the work order, and supervise with check-ins until then;
never run a wait without a deadline. What its exits mean:

- **0** the child ended or stopped. `END child ended` means agentd reported the
  task's end (`exited`), or that it could not start (`failed`), or that its
  process is gone without a result (`interrupted`). `END child stopped` means
  the pod stopped or failed, or the Session was suspended or archived, while
  the task had not reported an end: not that the task finished. Neither means
  the task succeeded.
- **2** its own inputs are bad: a deadline that does not parse, or a directory
  it cannot make. It never called agent-run. Fix the input; never run it
  without a deadline.
- **3** reads were refused or kept failing. agent-run exit 2 (a usage error,
  such as a bad name), 3 (auth or 403) and 4 (not found) do not get better on
  retry, so they end it at once. Other exits are counted, five in a row.
- **4** the deadline passed. That ends the wait, not the work: run a check-in.

Then read the child's task exit, verdict and outcome from `child.json`, then
the deliverable (supervised-dispatch, section 4). The states and phases it
stops on are in `internal/agentd/protocol/status.go:26-42` and
`api/v1alpha1/agentsession_types.go:90`.

**Demonstrated and source only.** This script was run against a stub
`agent-run` that answers from a file, never against the API. Its re-arm, and
recovery of the wait after a coordinator restart or resume, are not
demonstrated. A Session that a reap releases answers 404, which ends the wait
with exit 3: keep the child's outcome, PR and head in the custody record
before that ([where the retained outcome lives](../../../docs/workflow-guide.md#where-the-retained-outcome-lives)).

**A bounded PR-or-exit watcher.** The same wait, for a child whose deliverable
is a PR (supervised-dispatch, section 4, "The dispatch and return loop"). It
also ends when a PR, open, merged or closed, appears on the child's branch.
Take the branch from the work order, or from `agentStatus.branch` in
`agent-run show`. It counts failed `gh` and `show` reads separately, five in a
row of either ends it:

```bash
# The deadline is absolute, from the custody record, so a re-armed wait keeps it.
child=CHILD-NAME; repo=OWNER/REPO; branch=CHILD-BRANCH; dir=$HOME/work/OP000
pr_fails=0; show_fails=0
deadline=$(date -u -d 2026-11-04T15:00:00Z +%s 2>/dev/null)
case "$deadline" in '' | *[!0-9]*) echo "END bad deadline"; exit 2 ;; esac
case "$repo" in */?*) ;; *) echo "END bad repo $repo"; exit 2 ;; esac
[ -n "$branch" ] || { echo "END bad branch"; exit 2; }
mkdir -p "$dir" || { echo "END bad dir $dir"; exit 2; }
while :; do
  if gh pr list --repo "$repo" --head "$branch" --state all --json number,state,headRefOid > "$dir/pr.json.tmp"; then
    pr_fails=0; mv "$dir/pr.json.tmp" "$dir/pr.json"
    n=$(jq -r '.[0].number // empty' "$dir/pr.json")
    if [ -n "$n" ]; then
      echo "END pr number=$n state=$(jq -r '.[0].state' "$dir/pr.json") head=$(jq -r '.[0].headRefOid' "$dir/pr.json")"
      exit 0
    fi
  else
    rc=$?; pr_fails=$((pr_fails + 1)); echo "READ-FAIL gh exit=$rc count=$pr_fails"
  fi
  if agent-run show "$child" -o json > "$dir/child.json.tmp"; then
    show_fails=0; mv "$dir/child.json.tmp" "$dir/child.json"
    st=$(jq -r '.agentStatus.state // "none"' "$dir/child.json")
    ph=$(jq -r '.phase // "none"' "$dir/child.json")
    case "$st" in
      exited | failed | interrupted) echo "END child ended state=$st phase=$ph"; exit 0 ;;
    esac
    case "$ph" in
      Failed | Suspended | Archived) echo "END child stopped state=$st phase=$ph"; exit 0 ;;
    esac
  else
    rc=$?; show_fails=$((show_fails + 1)); echo "READ-FAIL show exit=$rc count=$show_fails"
    case "$rc" in 2 | 3 | 4) echo "END read refused exit=$rc"; exit 3 ;; esac
  fi
  [ "$pr_fails" -ge 5 ] || [ "$show_fails" -ge 5 ] && { echo "END reads failing"; exit 3; }
  [ "$(date +%s)" -ge "$deadline" ] && { echo "END deadline state=${st:-unknown}"; exit 4; }
  sleep 120
done
```

Its exits mean the same as above: **0** a PR appeared (`END pr`), or the
child ended or stopped; **2** bad inputs, and nothing was called; **3** reads
refused or failing; **4** the deadline passed. `END pr` is not the end of the
task. The child can still be working, so read `agent-run show` once more
before you take the return. If the child is still busy, wait for its end with
the first script, on the same deadline. This one would end at once on the PR
that already exists. If the child's remaining work waits on a decision only
Tom can make, such as a merge or a trust approval, and the work order or a
recorded hold confirms it, stop the watcher instead: that work is a pending
decision. Record a checkpoint with its resume trigger,
and keep no timer, watcher or poll for it (section 6). A pending agent state is not an end either: a new
pod can be Running before its agent starts, and the watcher keeps reading the
same Session. It reads `show`, not `log`, so a log that is not found yet does
not end it.

**Demonstrated and source only.** The watcher was run against stub
`agent-run`, `gh` and `sleep` commands that answer from files, never against
the API or GitHub. The cases were: a PR on the third read; a merged PR after
pending reads; a closed PR after two failed `gh` reads; the child exiting; the
pod suspended; a refused `show`; five failed `show` reads; five failed `gh`
reads; a past deadline; and a bad deadline, repository or branch. Each ended
with the expected line and exit. Its re-arm and recovery after a restart are
not demonstrated.

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
- `timer`: the mechanism, job ID, schedule, next check, created time, expiry and
  the prompt (or its scope), which recovery re-creates the job from;
- `stage`, and evidence references (paths, SHAs, CI run IDs), never raw logs;
- `lastCheck`: time and result;
- `lanes`: the lane table of section 6, `[{work, owner, type, nextAction, wake}]`;
- `stop`: reason, time and evidence, once the work ends.

When the work is a child in another pod, for example a headless task started
by a Remote Control coordinator, this file is also the custody record. It names
the owner, the deadline, the checkpoints, the handoff and the outcome evidence,
so a successor can continue without the coordinator's context. A template;
values are placeholders, `null` means not observed yet:

```json
{
  "schema": "custody.v1",
  "story": "OP000",
  "custodian": "COORDINATOR-SESSION",
  "custody": [{"from": null, "to": "COORDINATOR-SESSION", "at": "2026-11-04T12:00:00Z", "reason": "launched the child"}],
  "child": {
    "name": "CHILD-NAME", "uid": "CHILD-UID", "parent": "COORDINATOR-SESSION",
    "agent": "codex", "requestedModel": "gpt-6-astra", "effort": "max", "servedModel": "Unknown",
    "project": "PROJECT", "repo": "REPO", "base": "main", "branch": "CHILD-BRANCH", "revision": "TEMPLATE-REVISION",
    "createdAt": "2026-11-04T12:00:00Z", "idempotencyKey": "op000-child-1",
    "launch": {"command": "agent-run --project PROJECT --agent codex ...", "exit": 0}
  },
  "deliverable": {"what": "review.md and a draft PR on the claimed branch", "acceptance": "the story's acceptance checks"},
  "deadline": {"wait": "2026-11-04T15:00:00Z", "budget": "as recorded on the story"},
  "timer": {"mechanism": "CronCreate", "jobId": null, "schedule": "7,27,47 * * * *", "nextCheck": null, "expires": null, "prompt": null},
  "lanes": [],
  "checkpoints": [
    {"rung": "launch accepted", "at": "2026-11-04T12:00:00Z", "evidence": "create exit 0; show -o json uid"}
  ],
  "lastCheck": {"at": null, "result": null},
  "handoff": {"route": "this file plus the tracker comment", "readableBy": "the coordinator's own session after a resume", "nextSafeStep": "read the child with agent-run show"},
  "report": {"destination": "where the ACK and the result go, from the work order", "lastAttempt": null, "lastFailure": null},
  "outcome": {"taskExit": null, "verdict": null, "taskOutcome": null, "pr": null, "head": null, "ci": null, "deliverableAccepted": false},
  "stop": null
}
```

`checkpoints` follow the evidence ladder in supervised-dispatch, section 4: one
entry per rung reached, with its time and evidence reference. `outcome` holds
what you read back from `agent-run show` and GitHub. The retained task outcome
is the child's own claim: record it as that, not as acceptance.
`deliverableAccepted` turns true only when the coordinator, or a QA agent other
than the child, has checked the deliverable. `report` names the reporting
destination. A failed report goes into `lastFailure` with its error; it is not
a task failure, and the next report uses another supported route
(supervised-dispatch, section 4, step 8).

Create the job, then write its job ID into the file. The fired prompt names the
state file and says what to do, because the context may have been compacted by
the time it fires. For example: "Timer check-in for OP385: read
`<state file>`, verify evidence, steer, update the state file; before any stop, write the lane table (section 6)
and stop only for a stop reason in supervised-dispatch section 5."

Pick a cadence that matches what you wait for: one check near the end of an
8-minute CI run rather than eight, 20 to 30 minutes for idle supervision. Avoid
minutes :00 and :30. The keeper pilot (OP385) uses `7,27,47 * * * *`.

## 3. Each check-in

1. Read the state file and the status record. Trust the files and fresh reads,
   not memory.
2. Check the evidence (see supervised-dispatch, section 5). Steer or correct; a
   correction into an existing provider CLI session in tmux follows
   [tmux-cli-handoff](../tmux-cli-handoff/SKILL.md).
   Quiet time or repeated failures trigger the stall rule in
   supervised-dispatch, section 5. They never end supervision or the timer.
   For a child in another pod, read `agent-run show NAME -o json` into a file
   and keep its exit status. A failed read is "unknown", not a stop
   (supervised-dispatch, section 4, "Waiting for a child"). A Running pod
   whose log is not found yet, or whose agent is still pending, is the same
   task starting: read it again at the next check. Never launch it again, and
   never mint or renew a token for it (supervised-dispatch, section 4, step 5).
3. Update `lastCheck`, `nextCheck`, `stage`, the evidence references and, for
   each new rung of the evidence ladder, a `checkpoints` entry.
4. Mirror a meaningful change on the tracker.

## 4. Recover after an interruption

A session-only job does not survive a restart, a resume on a new pod, or the
session's end. After any of these, and whenever `lastCheck` is more than two
intervals old:

1. Read the state file. If it names another custodian, do not create a timer:
   only the current custodian holds one. Report what you see to that custodian
   or to the parent.
2. Run `CronList`. If the recorded job ID is missing, create the job again with
   the prompt recorded in `timer`, the latest re-scoped one.
3. Write the new job ID and a recovery note: time, old job ID, and why.
4. Run one check-in now instead of waiting for the next fire.

Custody stays with the custodian named in the file. Re-create a recurring job
before its 7-day expiry if the work will run longer, and record it the same way.
A long foreground command delays every fire until the turn ends, so keep turns
short while a timer is armed.

## 5. Hand custody over

Only the current custodian holds a timer for the task. When it hands off,
because its context is filling, its session is stopping, it has lost its read
path to the task, or the parent reassigns the work:

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
explicit hold), an exhausted authorized budget (including a budget checkpoint or
stop threshold recorded on the story, never a stall threshold), a genuine
unresolvable blocker, or a denied safety or permission decision.
Quiet time, failed attempts, a full context window and a 7-day expiry never end
the timer. They trigger the stall rule in supervised-dispatch section 5, and the
job is re-armed or re-created. Waiting for an event a check can observe, such as
a natural keeper refresh, is pending, not blocked: keep the timer and report it
pending. A pending approval or safety decision is different: budget-contract
item 5, kept by [ADR-019](../../sagas/distributed-dev-env/adrs/019-stalled-work-steers-not-stops.md), forbids model polling while waiting.
Stop only the action that depends on it, with a checkpoint and its resume
trigger (the verified answer); no check-in looks at that action, its answer or
its pane (supervised-dispatch section 5; for example a prompt decision in
[tmux-cli-handoff](../tmux-cli-handoff/SKILL.md), step 4). Other independent
authorized work keeps this timer, and its check-ins record nothing about the
pending action beyond the existing hold line. If every remaining piece of work
depends on the decision, end the timer as below, recording the stop and the
resume trigger; a verified answer delivered into this session re-creates it from the state file.

Prove "every remaining piece" before you end the timer. The lane table gates
ending it, by `CronDelete` or `ScheduleWakeup` with `stop: true`. It does not
gate moving the timer: a custody handoff (section 5) or a re-scope (below) is not
an end.

Rows are only the authorized work this custodian has claimed, within its
recorded scope and budget. An unclaimed tracker item or an ungroomed "next" note
is not a lane; do not start it from here. Write the table into the state file's
`lanes` (section 2) and mirror it on the tracker, so a successor named under
section 5 can act if this session ends. Read the open work from the status record and the tracker, not
from memory. Give each row its owner, its type, its next action and what will
wake it. Types:

- `none`: authorized work can proceed now. It keeps the timer: do it, or
  dispatch it to its owner.
- `dependency`: an event that a read-only check can see and nobody has to decide,
  such as a CI run, an outage or a running worker. It keeps the timer at a
  sparse cadence, with a deadline; watch the event itself with a bounded wait
  (section 1) where one fits, rather than with model check-ins. Past the
  deadline the stall rule applies; once no repair remains, the row becomes
  `unresolvable`. A dependency's recovery does not lift a denial or answer a
  decision.
- `decision-pending`: waits, directly or through another lane, on a decision by
  Tom, his verified coordinator or a policy owner. The row names the delivered
  question (an AskUserQuestion delivery or an Ask ID) and a route that pushes
  the answer into this session, such as Tom's reply or a verified coordinator's
  `agent-run msg`. An Ask ID alone is not a push route: in v1 nothing delivers
  an Ask's answer into the session (DESIGN-034). Never poll `ask_status`. A
  question not yet delivered, or one with no push route, is a `none` row whose
  next action is delivering it or arranging the route. Copy the existing hold line; do not re-read the
  held action, its answer or its pane.
- `permission-denied`: blocked, directly or through another lane, by an actual
  denial, or by a latch the platform has actually
  set (`agent-run budget show`). Not polled, not retried.
- Stop types: `done` (a demonstrated outcome), `paused` (a pause or cancellation
  by Tom or his verified coordinator), `budget-exhausted` and `unresolvable`.

End the timer only when every row is `decision-pending`, `permission-denied` or
a stop type. One `none` or `dependency` row keeps it. Never end it after a number
of quiet or failed wakes; that is the stall rule above, not a stop reason.

A timer prompt scoped to one loop (one worker, one pilot) can stop only that
loop. When that loop ends and other rows stay open, re-scope: create a new job
whose prompt names the state file and the lane table, write its ID and prompt
scope into `timer`, then `CronDelete` the old job. That is a re-scope, not a stop.

When the work ends or pauses:

1. `CronDelete` the job (or `ScheduleWakeup` with `stop: true`), then `CronList`
   to confirm it is gone. Do not keep polling during a pause.
2. Write `stop` in the state file with the reason, time and evidence, and for a
   pause the condition that resumes it.
3. Record the end or the pause on the tracker. A verified resume delivered into
   this session re-creates the timer from the state file. If the session has
   ended, a successor named under section 5 acts from the tracker mirror.

This text is not operational proof. A real wake, re-arm, recovery and completion
are proved by the keeper pilot (OP385), not by this file.

Behavior scenarios for an evaluator: [scenarios/quiet-65-minutes.md](scenarios/quiet-65-minutes.md),
[scenarios/one-lane-held.md](scenarios/one-lane-held.md), [scenarios/all-lanes-held.md](scenarios/all-lanes-held.md),
and [new-pod-log-not-found.md](../supervised-dispatch/scenarios/new-pod-log-not-found.md) in supervised-dispatch.
