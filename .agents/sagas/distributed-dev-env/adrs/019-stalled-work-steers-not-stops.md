# ADR-019: Stalled-work thresholds trigger diagnosis and steering, not stopping

- **Status:** Recorded 2026-11-04, saga decision 94. Tom's words, relayed by
  session dev-env-1027-010058 (parent coordinator) as trusted conversation
  evidence; original message timestamp not available to this session. Tom did
  not answer the owner question about Q-23 directly. The quotes do not name
  Q-23: applying them to the stop clause of decision 55 / Q-23 was the
  acceptance of coordinator dev-env-1104-010332.
- **Amends:** [ADR-003](003-session-coordination-private-repositories.md), the
  sentence "The accepted 60-minute stall/three-failure policy remains in force",
  in its stop clause only.
- **Supersedes:** the stop clause of saga decision 55 (Q-23, D-84, 2026-10-09):
  "the first limit reached stops further attempts", followed by one owner
  question, in the [budget contract](../requirements/2026-10-09-task-budgets.md).
- **Retains:** the thresholds themselves (60 minutes without progress, three
  failed attempts at the same blocker), the ledger, the budget history, counters
  that carry across agents, pods, methods and resumes, and child work counting
  toward its parent's budget. Q-23 stays recorded as asked and answered.
- **Applied by:** [workflow guide, section 9](../../../../docs/workflow-guide.md#9-how-we-keep-the-project-on-track)
  and the agent-loop skills in [`.agents/skills/`](../../../skills/).
- **Tracking:** OpenProject #399, PR #908.

## Context

Tom's words on 2026-11-04, as relayed by session dev-env-1027-010058 (the parent
coordinator); the author of this ADR did not see them at their source:

> "That's a bad excuese, if I start an Opus session and simply give it
> instructions to work in a loop where it doesn't stop until the requirements are
> met via some test feedback things will get done. Media repair worked like this
> and burned tokens overnight. You should just be reviewing what the agents are
> doing and steering live, not fire and forget sets of instructions that do not
> have goals the agents have to demonstrate to stop working. You need a skill for
> dispatching a coordinator for long running work, then the coordinator needs a
> skill for dispatching workers and qa/review agents to create the loop. Then you
> need a timer based skill for checking in, using our timer framework"

> "Power through harden the mvp test and fix on a loop"

The 2026-10-09 rule stopped attempts at a threshold. That stops supervised work
that still has an authorized repair, which is the fire-and-forget failure Tom
describes. An Accepted ADR is never edited, so this ADR amends ADR-003's
restatement, and decision 94 supersedes the stop clause of decision 55.

## Decision

Work stops only on a demonstrated outcome, Tom's cancellation or pause (or his
verified coordinator's), a genuine unresolvable blocker, an exhausted authorized
budget, or a required safety or permission decision. Each stop is backed by
specific evidence.

An authorized budget is exhausted when the budget recorded on the story is
actually spent, or when a budget checkpoint or stop threshold recorded on the
story (its proposed budget and stop criteria) is reached. A stall threshold of
decision 55 (60 minutes without progress, three failed attempts at the same
blocker) is never a budget checkpoint or stop threshold, and a story cannot
record one as either, restated or renamed: reaching it triggers the response
below, never a stop. A budget checkpoint is a fence on the story's budget; it is
not the saved checkpoint that steering writes.

The time and attempt thresholds trigger diagnosis, steering, a checkpoint and
continuity. One owner question is asked only when real input or a permission
decision is missing.

The rest of the budget contract still applies: one outstanding question per task
and no model polling while waiting (item 5), and limits enforced by the
platform's deterministic API, controllers and supervisor (item 6).

## Consequences

- A threshold no longer stops attempts while authorized repair remains. Steering
  must change the approach; repeating a failed attempt is still not progress.
- The counters stay, so a threshold reached again is visible to the supervisor
  and in the record.
- Budgets still bound the work. An exhausted budget, including a budget
  checkpoint or stop threshold recorded on the story (never a decision 55 stall
  threshold), stops it.
- A limit the platform enforces stays a stop that agents never bypass: a budget
  campaign latch such as `OverallBudget`, `EffortBudget` or `Checkpoint`, and,
  until its own change, the old stop clause's `Stalled` and `RepeatedFailure`
  latches.
- Decision 55, its budget contract, DESIGN-001 D-84 and ADR-003 are not edited.
  Read them with this ADR.
- No source, flag or runtime behavior changes here. Source that enforces the old
  stop clause keeps doing so until its own reviewed change: `internal/taskbudget`
  latches a budget campaign as `Stalled` at `StallLimit` (60 minutes) and as
  `RepeatedFailure` at `FailureLimit` failures at one blocker.
