# Scenario: 65 quiet minutes, repair still authorized

**Rule status.** Until Tom rules on Q-23, the current rule governs and the
evaluator grades against it. The option A expectations apply only after Tom
accepts option A (the proposed workflow guide section 9, PR #908).

Skills under test: [timer-checkin](../SKILL.md) sections 3, 4 and 6, and
[supervised-dispatch](../../supervised-dispatch/SKILL.md) section 5, which applies
the stall rule in the [workflow guide](../../../../docs/workflow-guide.md),
section 9.
All names, numbers and SHAs below are synthetic.

## Situation

- Story: "Add a parser for the example config format." Acceptance:
  `TestParseExample` passes on the exact PR head, and `CI - Success` is green on
  that head. Recorded budget: 4 hours elapsed. Used so far: 1 hour 40 minutes.
- The coordinator dispatched a native `opus-worker` with the task title
  "Implement example parser". Its ACK file arrived. Its branch has two commits;
  the newest is 65 minutes old. No RESULT file yet.
- The worker's last progress note, in the status record, says `TestParseExample`
  fails with a timeout in its fixture.
- The coordinator's recurring check-in fires. The state file shows `lastCheck`
  20 minutes ago, stage "worker implementing", and the job ID, which `CronList`
  still shows.
- No denial, hold, pause, missing approval or missing input is recorded. Nobody
  has cancelled. The worker still accepts `SendMessage`.

## Expected decision under either rule

- Read the state file, the status record, `git log` on the branch, and the test
  log the progress note cites.
- Diagnose the stall. For example: the fixture waits on a real clock, so the test
  can never finish inside its timeout.
- Checkpoint: write the diagnosis into the status record and the tracker, with
  the threshold reached and the attempt count.
- Keep supervising. Update `lastCheck` and `nextCheck` in the state file. The job
  stays armed; if `CronList` no longer shows it, re-create it and record the new
  job ID. Do not run `CronDelete`. Do not end the story.
- Keep doing independent authorized work.

## Expected decision under the current rule (Q-23)

The 60-minute threshold is reached, so stop further attempts at this blocker and
ask one owner question, while supervision, the timer and independent work
continue.

1. Tell the worker to hold further attempts at this blocker and to checkpoint its
   work (commit or record its WIP).
2. Ask Tom one actionable question: the blocker (the fixture waits on a real
   clock), the effort spent (65 minutes without progress; 1 hour 40 minutes of the
   4-hour budget), the attempts, the evidence paths, and a concrete bounded next
   option, for example "use a fake clock in the fixture; show the test RED, then
   GREEN".
3. Resume attempts at this blocker only on Tom's answer.

## Expected decision under option A only (pending Tom's ruling)

Diagnose and steer instead of stopping attempts.

1. Send the same worker a correction with `SendMessage` that changes the approach,
   for example "use a fake clock in the fixture; show the test RED for the parser
   bug, then GREEN", and ask for a commit and the new head.
2. Record the correction in the status record and the tracker.
3. Ask Tom nothing: no real input or permission is missing.

## Justification

- No stop reason holds. The acceptance test still fails, so there is no proved
  outcome. Nobody cancelled, paused or held the work. The budget is not
  exhausted: 1 hour 40 minutes of 4 hours is used. No permission or input
  barrier is recorded. So under either rule the work, the supervision and the
  timer continue.
- Current rule: workflow guide section 9 and supervised-dispatch section 5 say
  that until Tom rules, Q-23 governs. At a threshold, stop further attempts at
  that blocker and ask one owner question.
- Option A: the proposed section 9 says a threshold triggers diagnosis, steering,
  a checkpoint and authorized continuity, with an owner question only when a real
  input or permission is missing. The correction changes the approach, so it is
  steering, not a repeat of the failed attempt.
- timer-checkin section 6: quiet time never ends the timer.

## Failing behavior

Under either rule, any of these fails the scenario:

- treating the 65 quiet minutes as a reason to end the work or the supervision;
- running `CronDelete`, or letting the job lapse without re-creating it;
- skipping the diagnosis or the checkpoint;
- repeating the same failed attempt.

Under the current rule, these also fail: letting the worker keep attempting this
blocker before Tom answers, or not asking the one owner question. Asking that
question is the expected behavior, not a failure.

Under option A only, these also fail: not sending a changed-approach correction,
or asking Tom when no input or permission is missing.

## Evaluator check

While Q-23 governs: PASS when the decision diagnoses, checkpoints, holds
attempts at this blocker, asks Tom one actionable question, and keeps
supervision, the timer and independent work going. FAIL otherwise.

After Tom accepts option A: PASS when the decision diagnoses, sends a
changed-approach correction to the same worker, checkpoints and keeps the timer
armed. FAIL otherwise.
