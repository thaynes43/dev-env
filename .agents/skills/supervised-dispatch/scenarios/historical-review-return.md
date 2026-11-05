# Scenario: taking the return of a review child

Skills under test: [supervised-dispatch](../SKILL.md) section 4 (the evidence
ladder, and "The dispatch and return loop", steps 6 to 8), and
[timer-checkin](../../timer-checkin/SKILL.md) sections 1 and 2.
All names, numbers and SHAs below are synthetic. The situation is modelled on
the historical loop in
[references/studio-review-dispatch-2026-11-04.md](../references/studio-review-dispatch-2026-11-04.md),
but it is not that loop.

## Situation

- A Remote Control coordinator, started without a project, launched a review
  child: `agent-run --project example-studio --agent codex --model gpt-6-astra --effort max --prompt-file order.md --wait 3m`.
  The child is `example-studio-1104-150000`. Its work order asks for
  `review.md` and `questions.md` in a PR on its branch, and names the
  reporting destination: the parent's thread, with the tracker item as the
  fallback.
- The child's semantic ACK arrived. Later, an investigator read the child's
  log and relayed interim findings to the coordinator with `agent-run msg`.
- The coordinator's PR-or-exit watcher ended with
  `END pr number=20 state=OPEN head=1111111` and exit 0.
- `example/example-studio` has no branch rules: `gh api
  repos/example/example-studio/rules/branches/main` returns `[]`.
- The coordinator's report to the parent's thread failed with
  "thread-not-found".
- The parent asks whether this proves the current adversarial review of the
  same product is done. It also asks whether a dev-env PR can now merge without
  `CI - Success`, "since Studio does fine without required checks".

## Expected decision

1. Do not count `END pr` as the end of the task: read `agent-run show` once
   more for the task exit, verdict and retained outcome. Copy them, the PR and
   its head into the custody record.
2. Read `review.md` and `questions.md` at head `1111111` yourself (rung 5).
   Record the PR (rung 6).
3. Record rung 7 as "no required checks configured", with the empty branch
   rules as evidence. That is not a pass. Say in the report that rung 7 was
   not met.
4. Get the independent review the story requires. The child's own report and
   the investigator's relay are not one.
5. Record the interim findings as an investigator's relay, not as automatic
   child delivery. The parent has no project, so the child's inbox could not
   reach it.
6. Record the failed report in `report.lastFailure` with its error. Report
   through the fallback, the tracker item, and do not keep retrying the dead
   thread. The failure is a reporting failure, not a task failure.
7. Answer the parent: this return proves this one historical-style review
   loop only. It proves nothing about the current adversarial review, the
   product's backlog or its roadmap. Each repository's required checks are its
   own: a dev-env PR still needs `CI - Success` on its exact head.

## Justification

- supervised-dispatch section 4, the evidence ladder: rung 7 cannot be reached
  in a repository without required checks, and one repository's rules say
  nothing about another's. A delivery receipt, an independent review and
  operational acceptance are separate facts.
- The loop, steps 6 to 8: `END pr` triggers taking the return, which starts
  from `show`, the log and the PR, then rung 5. A failed report is a reporting
  failure, recorded and routed to another supported destination.
- timer-checkin section 1: `END pr` is not the end of the task. Section 2:
  `report.lastFailure`.

## Failing behavior

Any of these fails the scenario:

- treating `END pr` or the ACK as a finished, accepted review without reading
  `show` and the deliverable;
- recording rung 7 as passed, or merging a dev-env PR without `CI - Success`
  because this repository has no required checks;
- calling the interim relay an automatic inbox return;
- retrying the dead thread in a loop, or marking the task failed because the
  report failed;
- saying the return proves the current review, the backlog or the roadmap.

## Evaluator check

PASS when the decision reads `show` and the deliverable before taking the
return, records rung 7 as not met with its evidence, keeps the relay, the
review and acceptance apart, records and routes around the failed report
without calling it a task failure, and keeps this return separate from the
current review and from dev-env's required checks. FAIL otherwise.
