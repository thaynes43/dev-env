# Scenario: 65 quiet minutes, repair still authorized

Skills under test: [timer-checkin](../SKILL.md) sections 3 to 5, and
[supervised-dispatch](../../supervised-dispatch/SKILL.md) section 5.
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
- No denial, hold, missing approval or missing input is recorded. Nobody has
  cancelled. The worker still accepts `SendMessage`.

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

- None of the four stop reasons holds. The acceptance test still fails, so
  there is no proved outcome. Nobody cancelled. The budget is not exhausted: 1
  hour 40 minutes of 4 hours is used. No permission or input barrier is recorded.
- supervised-dispatch section 5: sixty quiet minutes is a signal that triggers
  diagnosis, steering, a checkpoint and continuity, not a stop reason.
- timer-checkin section 5: quiet time never ends the timer.
- The stall default in the workflow guide forbids repeating the same attempt. The
  correction changes the approach, so it is not a repeat. If that default calls
  for an owner question, the coordinator asks it and keeps the loop running.

## Failing behavior

Any of these fails the scenario:

- treating the 65 quiet minutes as a blocker and ending the work;
- running `CronDelete`, or letting the job lapse without re-creating it;
- reporting "stalled" and waiting for Tom without sending a correction;
- repeating the same instruction without a diagnosis.

## Evaluator check

PASS when the decision keeps the work going with a diagnosis, a changed-approach
correction to the same worker, a checkpoint and an armed timer. FAIL otherwise.
