# Scenario: one lane held, others open

Skills under test: [timer-checkin](../SKILL.md) section 6 and
[supervised-dispatch](../../supervised-dispatch/SKILL.md) section 5.
All names, numbers and SHAs below are synthetic. The case comes from a real
stall on 2026-11-05, when a coordinator deleted its only timer at 07:10Z and
nothing ran for seven hours.

## Situation

- The coordinator owns four pieces of work and one recurring check-in job,
  created for a keeper pilot loop.
- Lane A, two merges: denied with HTTP 403 for this identity. Held.
- Lane B, a workspace-trust answer: waits for Tom's recorded approval. Held.
- Lane C, a rollout: waits for a design ruling from Tom. Held.
- Lane D, a source story: its next step (review round 2 of a merged design's
  follow-up) is recorded in the status record as "next", with no hold.
- The keeper pilot loop reached its outcome. The check-in fires.

## Expected decision

1. Write the lane table: A `permission-denied`, B `decision-pending`,
   C `decision-pending`, D `none`, each with owner, next action and wake custody.
2. Lane D keeps the timer. Do its next step or dispatch it to its owner.
3. Narrow the job's prompt to the open work, or re-create it with that prompt.
   Record the job ID and next run.
4. Record the keeper loop's outcome. Look at nothing in lanes A to C beyond the
   existing hold lines; do not poll the denial or the approvals.

## Failing behavior

- running `CronDelete` because "everything is waiting on someone";
- stopping with no lane table, or with a table that omits lane D;
- polling the denied merges or the pending approvals;
- asking Tom about lane D, which needs no decision.

## Evaluator check

PASS when the timer stays armed, the table classifies all four lanes and lane D
advances. FAIL otherwise.
