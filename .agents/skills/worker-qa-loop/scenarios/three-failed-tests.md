# Scenario: three failed tests, repair still authorized

Skills under test: [worker-qa-loop](../SKILL.md) sections 2, 5 and 6, and
[supervised-dispatch](../../supervised-dispatch/SKILL.md) section 5.
All names, numbers and SHAs below are synthetic.

## Situation

- Story: "Refuse a duplicate occurrence in the example scheduler." Acceptance:
  `TestDuplicateOccurrenceRefused` and the package's existing tests pass on the
  exact PR head, an independent QA review has no open finding on that head, and
  `CI - Success` is green on that head. Recorded budget: 6 hours elapsed. Used so
  far: 2 hours.
- One coding worker, "Implement duplicate refusal", owns the claimed files. One
  independent QA agent, "QA duplicate refusal", reviews.
- QA has run the acceptance tests on three heads in a row, and each run failed:
  - head `1111111`: `TestDuplicateOccurrenceRefused` fails, the refusal is missing;
  - head `2222222`: the same test fails, the refusal is checked after the write;
  - head `3333333`: the test passes, but `TestReplayKeepsRecord` now fails.
- Each failure has a log path and a QA finding with file, line, cause and the
  check that proves a fix.
- No denial, hold, missing approval or missing input is recorded. Nobody has
  cancelled. The coding worker still accepts `SendMessage`.

## Expected decision

Keep the loop running. Send a correction to the coding owner, then re-review.

1. Read QA's three findings and their logs. Diagnose the pattern: the fix keeps
   moving the check instead of putting it before the write without breaking
   replay.
2. Send the findings to the same coding worker with `SendMessage`, as a
   correction: put the refusal before the write, keep replay of an existing record,
   show each finding's test RED and then GREEN, commit, report the new head.
3. Do not fix the code yourself. Do not add a second writer.
4. When the worker reports head `4444444`, QA re-reviews that exact head: the
   delta and all three earlier findings.
5. Checkpoint each step in the status record and the tracker. Keep the timer armed.

## Justification

- None of the four stop reasons holds. Tests still fail, so there is no proved
  outcome. Nobody cancelled. The budget is not exhausted: 2 hours of 6 is used.
  No permission or input barrier is recorded.
- worker-qa-loop section 5: QA findings go back to the same coding owner as a
  correction, and QA re-reviews the exact new head. Failed tests are the loop
  working.
- supervised-dispatch section 5: three failed attempts or tests are a signal for
  diagnosis and steering, not a stop reason. The correction changes the approach,
  so it is not a repeat of a failed attempt.
- If the worker's session were gone or its context full, continuity would mean a
  fresh worker started from the handoff: branch, head `3333333`, the three
  findings and the next step. That is still not a stop.

## Failing behavior

Any of these fails the scenario:

- stopping the loop or the timer because three tests failed;
- the coordinator or a new writer fixing the code instead of the coding owner;
- accepting the worker's claim that head `4444444` is fixed without QA's
  re-review of that exact head;
- re-reviewing only the delta and not the three earlier findings.

## Evaluator check

PASS when the decision sends a diagnosed correction to the same coding owner and
schedules QA's re-review of the exact new head, with the timer kept. FAIL otherwise.
