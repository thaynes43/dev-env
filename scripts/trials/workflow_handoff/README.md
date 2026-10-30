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

## Canonical OpenProject check-ins

`workflow_handoff --checkins <export.json|-> --contexts <contexts.json>` joins a
saved `opwork checkins -o json` export to explicit local contexts. It performs no
export retrieval. Use a previously authorized export from a CLI version that has
`checkins`; the mode needs neither opwork nor a network connection at runtime.
Both files are bounded to 128KiB, with at most 48 input items and 48 contexts.

```bash
GOTOOLCHAIN=local CGO_ENABLED=0 go run . \
  --checkins testdata/checkins-export.json \
  --contexts testdata/checkins-contexts.json
```

To take a real export in a session pod, run opwork under your own session
identity. The `opwork` on `PATH` in agent image 2.45.0 predates `checkins`.
The supported fallback is the pod's own haynes-ops checkout, which has had it
since haynes-ops #4406:

```bash
python3 -I ~/repos/haynes-ops/scripts/openproject/opwork.py checkins -o json --limit 20 \
  > "$XDG_RUNTIME_DIR/checkins.json"
```

Keep the export in `$XDG_RUNTIME_DIR` and never commit it: it holds live tracker
text. A page of 20 items is about 14KiB, well inside the 128KiB bound.

Two separate rules apply to such a run. First, an item with no matching context
stays `unbound` with no intents, so an empty contexts file leaves every item
unbound. Second, a page after the first (`page > 1`), a page with `has_more`, or
the owner scope "selected page" is reported as partial, and its recommendations
are withheld even for bound items. On 2026-10-30 the merged CLI read one real
export with an empty contexts file and exited 0: every item was unbound, and the
page was partial. The export came
from a clean haynes-ops checkout at
`2611885e77d3a8b6d159c87de9e5e657f6bdd3f6`. That run proves the export shape
and the refusal path only, on one selected page. It is not a full backlog
triage, and nothing was executed.

Use your own pod's checkout and your own session identity. Never use another
session's checkout or credentials. The `opwork` on `PATH` is left as it is, and
it still works for its existing commands.

The synthetic files above are a complete runnable example. The export is the
canonical object with `page`, `page_size`, `has_more`, `owner_filter_scope`,
`owner_match_total`, `total_before_owner_filter`, and `items`. `owner_match_total`
may be `"Unknown"`. Each item carries `id`, `subject`, `url`, `status`, `owner`,
`check_after`, `last_verified`, `evidence`, `handoff`, `next_action`, `reasons`,
`history_complete`, `provenance`, `metadata`, and nullable `comment_source`.
The output retains every item field, including unknown export fields, in
`sources`; export fields outside `items` remain in `visibility`. Canonical
`reasons`, status, ACK, blocked, evidence and provenance are opaque display
facts. This adapter does not reproduce opwork's reason engine or interpret those
facts as authority, receipt states, resolution evidence or holds. A relevant
hold must be recorded in the handoff for the existing reducer to enforce it.

The local contexts document has schema `dev-env.workflow-checkin-contexts/v1`,
an explicit RFC3339 `observedAt` (no wall clock is read), and a `contexts` array.
Each context supplies:

- `id` and the exact export `owner` string;
- `story` equal to the decimal tracker ID, `repository`, positive `pullRequest`,
  exact lowercase 40-character `head`, `task` and `uid`;
- `coordinator`, the bounded coordinator identifier in the existing handoff;
- an explicit obligation `kind` (`start_ack`, `progress`, `pickup`, or
  `user_question`), an existing `handoff` document, and a `receipts` array;
- optionally `ownerEscalationDueAt`, an explicit deadline for the existing planner.

The export owner label may contain spaces and pipes; the context explicitly maps
it to the handoff coordinator token. There is no owner-name inference. Every
material binding must match the handoff header and its review binding. Missing,
Unknown, foreign and pre-PR bindings remain `unbound` with an explanation and no
plan. An exact binding alone does not establish a usable picked review: the
existing handoff reducer still checks that evidence and suppresses advice when
it is refused, ambiguous, conflicted or blocked.

One obligation named `checkin-<id>` is passed to `planWatchdog`, using the context
kind and coordinator and the export's `check_after` as `dueAt`. A null, empty or
`"Unknown"` date supplies no due time. Other dates must follow the watchdog's
RFC3339 rules, including its refusal of fractional seconds. No deadline is
inferred from prose. The output embeds the unchanged watchdog plan, preserving
its receipt validation, replay, idempotency, escalation and refusal semantics.
`accepted` and `acknowledged` receipts do not mean done. New `nextReceipts` are
recommendations only; this command does not persist them.

`page > 1`, `has_more: true`, or owner-filter scope `"selected page"` is visible partial-page
evidence. Incomplete or absent item history is also visible. Either condition
withholds recommendations, even with a matching context. Distinct item variants
are retained and sorted; exact duplicate JSON values collapse. Conflicting
items or contexts for the same ID yield `conflict` and no plan for that ID.
Other items remain independently visible. Ordering of items, contexts and object
keys does not choose a winner. Unknown fields in the strict local contexts,
handoff or receipt schemas retain the existing refusal rules; export extension
fields are not schema violations.

Exit 0 emits triage (including unbound, incomplete and blocked entries), exit 2
reports a refused document or watchdog wrapper, and exit 3 reports conflicting
item/context identities. Every report, item, watchdog plan and intent carries
`dryRun: true`, `executed: false`, and `checkerAuthority: none`. No service,
scheduler, tracker write, process launch, notification or live subscription is
part of this mode. Source fixtures do not prove live delivery or #211 acceptance.
