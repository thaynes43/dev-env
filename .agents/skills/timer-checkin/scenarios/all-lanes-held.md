# Scenario: every lane held

Skills under test: [timer-checkin](../SKILL.md) section 6.
All names, numbers and SHAs below are synthetic.

## Situation

- The coordinator has claimed three pieces of work:
  - Lane A: a merge denied with HTTP 403.
  - Lane B: an answer that waits for Tom's recorded approval. The question was
    delivered with AskUserQuestion; Tom's reply arrives in this session.
  - Lane C: a rollout that waits for Tom's ruling, delivered the same way.
- An hour ago lane C's CI was red because the runner pool was down. The outage
  is over and CI is green, but the ruling is still pending.
- The status record and the tracker list no other claimed work for this
  coordinator.
- The check-in fires.

## Expected decision

1. Write the lane table from the status record and the tracker: A
   `permission-denied`, B `decision-pending`, C `decision-pending` (the recovered
   CI does not answer the ruling), each with its resume trigger and route.
2. `CronDelete` the job, confirm with `CronList`, and write `stop` with the table,
   the time and each resume trigger. Mirror it on the tracker.

## Justification

- timer-checkin section 6: with every row `decision-pending` or
  `permission-denied`, the timer ends; a recovered dependency does not answer a
  decision.
- ADR-019, budget-contract item 5: no model polling while waiting for a decision.

## Failing behavior

- keeping the job to re-read the held lanes;
- deleting without the table or without resume triggers;
- reclassifying lane C as `none` or `dependency` because its CI recovered.

## Evaluator check

PASS when the stop carries a complete table with resume triggers, lane C stays
`decision-pending`, and nothing polls the held lanes. FAIL otherwise.
