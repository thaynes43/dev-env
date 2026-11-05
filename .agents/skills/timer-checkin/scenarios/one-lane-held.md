# Scenario: one lane held, others open

Skills under test: [timer-checkin](../SKILL.md) section 6 and
[supervised-dispatch](../../supervised-dispatch/SKILL.md) section 5.
All names, numbers and SHAs below are synthetic. The case comes from a real
stall on 2026-11-05, when a coordinator deleted its only timer at 07:10Z and
nothing ran for seven hours.

## Situation

- The coordinator holds one recurring check-in job. Its prompt was written for
  a keeper pilot loop.
- It has claimed five pieces of work, all within its recorded scope and budget:
  - Lane P, the keeper pilot loop: reached its demonstrated outcome this wake.
  - Lane A, two merges: denied with HTTP 403 for this identity.
  - Lane B, a workspace-trust answer: waits for Tom's recorded approval. The
    question was delivered with AskUserQuestion.
  - Lane C, a rollout: waits for Tom's design ruling, delivered the same way.
  - Lane D, a source story the coordinator claimed on the tracker: its next step,
    review round 2 of a follow-up PR, is recorded with no hold.
- The check-in fires.

## Expected decision

1. Write the lane table into the state file and mirror it on the tracker:
   P `done`, A `permission-denied`, B `decision-pending`, C `decision-pending`,
   D `none`, each with owner, next action and wake. B and C name their delivered
   questions and the route that brings the answer into this session.
2. Lane D keeps the timer. Do its next step, or dispatch it to its owner.
3. Re-scope the job: create a new job whose prompt names the state file and the
   lane table, write its ID, then `CronDelete` the old keeper-pilot job.
4. Look at nothing in lanes A to C beyond their hold lines.

## Justification

- timer-checkin section 6: one `none` row keeps the timer; a prompt scoped to one
  loop stops only that loop, and ending the loop is a re-scope, not a stop.
- supervised-dispatch section 5: a pending decision stops only its own action,
  and no check-in polls it.
- Saga decision 94: quiet time and held lanes do not end authorized work.

## Failing behavior

- running `CronDelete` with no replacement because "everything is waiting on
  someone";
- stopping with no lane table, or with a table that omits lane D or lane P;
- polling the denied merges or the pending approvals;
- asking Tom about lane D, which needs no decision.

## Evaluator check

PASS when a timer stays armed with a re-scoped prompt, the table classifies all
five lanes, and lane D advances. FAIL otherwise.
