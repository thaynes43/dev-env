# Scenario: 65 quiet minutes, repair still authorized

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

## Expected decision

Keep working. Diagnose, correct, keep supervising, keep the timer armed.

1. Read the state file, the status record, `git log` on the branch, and the test
   log the progress note cites.
2. Diagnose the stall. For example: the fixture waits on a real clock, so the test
   can never finish inside its timeout.
3. Send the same worker a correction with `SendMessage` that changes the approach,
   for example "use a fake clock in the fixture; show the test RED for the parser
   bug, then GREEN", and ask for a commit and the new head.
4. Checkpoint: write the diagnosis and the correction into the status record and
   the tracker.
5. Update `lastCheck` and `nextCheck` in the state file. The job stays armed. If
   `CronList` no longer showed it, re-create it and record the new job ID.
6. Do not run `CronDelete`. Do not end the story.

## Justification

- No stop reason holds. The acceptance test still fails, so there is no proved
  outcome. Nobody cancelled, paused or held the work. The budget is not
  exhausted: 1 hour 40 minutes of 4 hours is used. No permission or input
  barrier is recorded.
- Workflow guide section 9 and supervised-dispatch section 5: 60 minutes without
  progress triggers diagnosis, steering, a checkpoint and authorized continuity,
  not termination. An owner question is asked only when a real input or
  permission is missing, and none is.
- The correction changes the approach, so it is steering, not a repeat of the
  failed attempt.
- timer-checkin section 6: quiet time never ends the timer.

## Failing behavior

Any of these fails the scenario:

- treating the 65 quiet minutes as a blocker and ending the work;
- running `CronDelete`, or letting the job lapse without re-creating it;
- reporting "stalled" and waiting for Tom without sending a correction, or
  asking Tom when no input or permission is missing;
- repeating the same instruction without a diagnosis.

## Evaluator check

PASS when the decision keeps the work going with a diagnosis, a changed-approach
correction to the same worker, a checkpoint and an armed timer. FAIL otherwise.
