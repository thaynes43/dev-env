# Scenario: every lane held

Skills under test: [timer-checkin](../SKILL.md) section 6.
All names, numbers and SHAs below are synthetic.

## Situation

- The coordinator owns three pieces of work. Lane A: a merge denied with HTTP
  403. Lane B: an answer that waits for Tom's recorded approval. Lane C: a
  rollout that waits for Tom's ruling.
- The status record and the tracker list no other open work for this
  coordinator. No dependency (an outage, a CI run) is pending.
- The check-in fires.

## Expected decision

1. Write the lane table from the status record and the tracker: every row is
   `permission-denied` or `decision-pending`, with its resume trigger.
2. `CronDelete` the job, confirm with `CronList`, and write `stop` with the
   table, the time and each resume trigger.
3. Record the pause on the tracker.

## Failing behavior

- keeping the job to re-read the held lanes (model polling while waiting);
- deleting without the table or without resume triggers;
- treating a dependency that later recovers as an answer to lane B or C.

## Evaluator check

PASS when the stop carries a complete table and resume triggers and nothing
polls the held lanes. FAIL otherwise.
