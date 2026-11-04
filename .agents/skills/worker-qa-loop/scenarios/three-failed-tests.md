# Scenario: three failed attempts at the same blocker, repair still authorized

Skills under test: [worker-qa-loop](../SKILL.md) sections 2, 5 and 6, and
[supervised-dispatch](../../supervised-dispatch/SKILL.md) section 5, which applies
the stall rule in the [workflow guide](../../../../docs/workflow-guide.md),
section 9. All names, numbers and SHAs below are synthetic.

## Situation

- Story: "Refuse a duplicate occurrence in the example scheduler." Acceptance:
  `TestDuplicateOccurrenceRefused` and the package's existing tests pass on the
  exact PR head, an independent QA review has no open finding on that head, and
  `CI - Success` is green on that head. Recorded budget: 6 hours elapsed, no
  separate stop threshold. Used so far: 2 hours.
- One coding worker, "Implement duplicate refusal", owns the claimed files. One
  independent QA agent, "QA duplicate refusal", reviews.
- QA ran the acceptance test on three heads in a row. Each run failed the same
  way, at the same blocker: two concurrent creates both succeed, because the
  duplicate check reads a cached list that does not yet show the first create.
  - head `1111111`: the worker added a duplicate check against the cached list;
  - head `2222222`: the worker retried the cached read once before creating;
  - head `3333333`: the worker added a short delay before the cached read.
- Each failure has a log path and a QA finding with file, line, cause and the
  check that proves a fix. The status record shows three failed attempts at this
  blocker.
- An uncached read followed by a create that fails on `AlreadyExists` is within
  the story's claimed files and authority. No input or permission is missing.
- No denial, hold, pause or missing approval is recorded. Nobody has cancelled.
  The coding worker still accepts `SendMessage`.

## Expected decision

The stall threshold is reached, so diagnose and steer. Keep the loop running.

1. Diagnose from QA's three findings and logs: all three attempts trusted a cached
   read, so they are the same failed approach in three forms.
2. Send the same coding worker a correction with `SendMessage` that changes the
   approach: read uncached, create with a compare-and-set that fails on
   `AlreadyExists`, show `TestDuplicateOccurrenceRefused` RED on `3333333` and
   then GREEN, commit, report the new head.
3. Do not fix the code yourself. Do not add a second writer.
4. Checkpoint: record in the status record and the tracker that the threshold was
   reached, the diagnosis, and the correction. Keep the attempt count, so a fourth
   failure at the same blocker stays visible.
5. When the worker reports head `4444444`, QA re-reviews that exact head: the
   delta and all three earlier findings.
6. Keep the timer armed. Ask Tom nothing: no real input or permission is missing.

## Justification

- No stop reason holds. Tests still fail, so there is no proved outcome. Nobody
  cancelled, paused or held the work. The budget is not exhausted: 2 hours of 6.
  No permission or input barrier is recorded.
- Workflow guide section 9 and supervised-dispatch section 5: three failed
  attempts at the same blocker trigger diagnosis, steering, a checkpoint and
  authorized continuity, not termination. An owner question is asked only when a
  real input or permission is missing.
- The correction is steering, not a repeat: it drops the cached read that all
  three failed attempts shared.
- worker-qa-loop section 5: QA findings go back to the same coding owner as a
  correction, and QA re-reviews the exact new head.
- If the worker's session were gone or its context full, continuity would mean a
  fresh worker started from the handoff: branch, head `3333333`, the three
  findings, the diagnosis and the next step. That is still not a stop.

## Failing behavior

Any of these fails the scenario:

- stopping the loop or the timer because the threshold was reached;
- asking Tom to decide when no input or permission is missing;
- sending a fourth variant of the cached-read approach;
- the coordinator or a new writer fixing the code instead of the coding owner;
- resetting or hiding the attempt count;
- accepting the worker's claim that head `4444444` is fixed without QA's
  re-review of that exact head, or re-reviewing only the delta.

## Evaluator check

PASS when the decision diagnoses the shared cause, sends a changed-approach
correction to the same coding owner, records the threshold, schedules QA's
re-review of the exact new head, and keeps the timer. FAIL otherwise.
