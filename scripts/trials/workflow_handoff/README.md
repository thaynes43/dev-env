# workflow_handoff

Offline checker for one coordinator handoff record. It validates the document, replays its events, and prints the recorded state plus the next manual action.

It does not dispatch, poll, grant authority, transfer TaskClaim, call a provider, or write tracker state. Stdlib only. One document, at most 128KiB, at most 48 events.

```bash
GOTOOLCHAIN=local CGO_ENABLED=0 go run . testdata/e-merge.json
go run . - < testdata/d-deadline.json
```

Exit 0 means the record reduced. Exit 2 means the input was refused. Exit 3 means a conflict needs manual disposition. The procedure and the JSON template are in `docs/coordinator-result-handoff.md`.

Fixtures in `testdata/` are synthetic. They do not prove a live subscriber, a deployed policy, or a native review.

The `deadline` field alone is display evidence. The coordinator owns the clock and must record an explicit `deadline-reached` event; this offline tool has no timer. A writable executor blocks merge advice. Same-instant hold dispositions leave the hold pending. Replay retains the exact source-time representation, while ordering and equal-time grouping compare UTC instants.

## Watchdog plan

`workflow_handoff --watchdog <file>` reads one `dev-env.workflow-watchdog-plan/v1` wrapper and prints a dry-run decision plan. The wrapper carries `observedAt`, one existing `dev-env.workflow-handoff/v1` document, at most eight obligations, and at most 48 receipts. The whole document is still at most 128KiB. The planner calls `check` on the embedded handoff and does not keep a second ownership model.

```bash
GOTOOLCHAIN=local CGO_ENABLED=0 go run . --watchdog testdata/watchdog-missed-start.json
go run . --watchdog - < testdata/watchdog-user-question.json
```

Exit 0 emits a plan, including a blocked plan with no intents. Exit 2 refuses the wrapper. The handoff checker's own exit code stays on the plan. A refused, conflicted, or custody-blocked handoff, an unresolved hold, a non-positive pull request, or a missing picked review produces a visible blocked plan and no intent.

Obligations are `start_ack`, `progress`, `pickup`, or `user_question`. The owner must be the handoff's current coordinator. A missing `dueAt` stays `unknown`; the planner does not invent a deadline. `waitUntil` and `waitReason` are paired. An unexpired wait suppresses intents, and expiry evaluates the original due again. `resolutionEvidence` is a bounded reference, not authority. Receipt states do not resolve an obligation.

Every receipt names the handoff repository, pull request, head, picked task, UID, and obligation kind, as well as its obligation, owner, stage, state, evidence, and time. Those binding fields must already match this handoff and the referenced obligation. A missing, mistyped, case-variant, or foreign binding is refused with no plan. The planner does not fill binding in from the current handoff. A previous owner's receipt with the same binding stays in the history and does not wake or escalate the current owner.

`accepted` records transport acceptance. It is not native acknowledgment and it is not completion. Once `ownerEscalationDueAt` has elapsed, one `user_escalation` may be planned for an unresolved obligation that already has a materially bound `owner_wake` receipt in state `accepted` or `acknowledged`. Native acknowledgment is not required. A `planned` owner wake is not escalated. `ambiguous` and `rejected` do not escalate and do not send that owner wake again. No default deadline is invented.

When `observedAt` is at or after an explicit due, an unresolved unwaited obligation with no current-owner `owner_wake` receipt yields one `owner_wake` intent. `nextReceipts` records that intent as `planned` and copies the same material binding. Submitting that receipt again suppresses a duplicate owner wake. Equal-time contradictory receipts block the plan. Equivalent UTC offsets are the same instant. Exact duplicates are idempotent. Input order does not change the semantic result. A receipt `recordedAt` strictly after `observedAt` is refused before folding or use; an equal instant in another offset spelling stays valid, and a future `dueAt`, `ownerEscalationDueAt`, or `waitUntil` remains an explicit deadline.

Every plan and intent has `dryRun: true`, `executed: false`, and `checkerAuthority: none`. `overallDone` is true only when the handoff checker reports completion and every obligation has resolution evidence for the current coordinator. Worker exit, delivery, acknowledgment, and a user question do not complete the task. Publication stays distinct from deployment. The planner performs neither.

This is a PR-bound simulation. It does not accept a pre-PR task, invent a pull request or head, or talk to Codex, Claude, or Pushover. `codex-native` and `claude-rc` in the fixtures are coordinator labels for the same rules, not live sessions. Full live wake, notification, scheduler, and non-PR task support remain future work.
