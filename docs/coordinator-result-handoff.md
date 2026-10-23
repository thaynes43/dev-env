# Coordinator result handoff

This procedure is how a coordinator keeps custody of a dev-env result when the implementer exits. It is source guidance plus an offline checker. It does not dispatch work, subscribe to reviews, transfer a TaskClaim, merge a pull request, or prove that a live relay is connected.

Story [#177](https://work.haynesnetwork.com/work_packages/177) is this finite exercise. Broader [#117](https://work.haynesnetwork.com/work_packages/117) and [#101](https://work.haynesnetwork.com/work_packages/101) acceptance is unchanged. The recorded PR520 delay was coordinator pickup latency, not a task-result engine defect.

## Use the records that already exist

At baseline `380a759d947faf1131653867cdecf4b7ade878f5`:

- Task results are derived for an ended task (`internal/controller/reconciler.go` around the task-result derivation, with ended-task coverage in `internal/controller/status_test.go`). An exited container is not a review verdict. Keep the latest observation for the exact task, UID, and head. Prefer the later source time. Do not let an older Running observation replace a newer terminal verdict.
- Inbox delivery has no side effect (`api/coordination/v1alpha1/inbox_types.go`). A read acknowledgment is explicit. A delivery receipt is not an acknowledgment, an owner, or completion.
- TaskClaim ownership is immutable and does not expire (`api/coordination/v1alpha1/taskclaim_types.go`). Lease expiry, reconnect, and a lost process do not release it and do not create a successor.
- Session create is idempotent for the same caller and key (`internal/apiserver/sessions.go`). Reusing a key with a changed request is rejected. That is not permission to rerun a finished task.
- A headless `-p` task runs its prompt once and does not accept `msg` (`docs/workflow-guide.md`). A supported successor is a new authorized pass, not a revival of the exited task.
- Launch admission is not authority (`docs/launch-profile-preflight.md`). A managed-task prerequisite being present does not grant a review, a merge, or a credential.

The checker records those facts. It does not call the APIs.

## Before the worker starts

Write one handoff record with:

- the canonical story, repository, pull request, and exact 40-character head
- the accountable coordinator
- the executor identity, dispatch time, deadline, and pass identity
- the review task, UID, and head that a later verdict must match
- the current owner-scope reference, including any instruction it supersedes
- every pending action-specific hold
- who consumes the result and the next pickup time

A delivery of that record to a parent is not acknowledgment and not ownership.

## When a result or a stop arrives

1. Match a review observation to the bound task, UID, and head. Pick one terminal verdict. Ignore a repeated observation. Refuse a wrong head or UID. An equal-time pair of different verdicts needs manual disposition; do not pick a winner by sort order.
2. Treat executor exit as exit only. It does not release the parent outcome and it does not change TaskClaim.
3. Record a successor only after a verified, unambiguous stop and a separate externally authorized supported assignment. Otherwise write blocked custody and keep the coordinator. Duplicate assignment, reconnect, and lease expiry do not add another successor. A later different successor for the same key does not replace the recorded one.
4. Keep the latest owner-scope reference and every unresolved hold. A standing merge instruction does not clear a hold on a different action. A hold changes only when an externally established trusted disposition names that action and permits reconsideration.
5. At the worker deadline, mark the executor turn ended and pending a verified stop. Do not recommend more writes and do not invent a new allowance. The coordinator still owns review, repair, required CI, and the outcome. A later pass needs its own identity, its own external authorization, and it keeps the earlier budget in the record.
6. For an agent-opened dev-env pull request, recommend the existing merge and release workflow only when the exact head is unchanged, required CI passed, a native review verdict is recorded, findings are disposed, and no action-specific hold covers merge or release. Review-job success is not that verdict. Publication is not deployment. The checker does not merge, publish, or deploy. A missing gate or a matching hold names the blocker.

## Current owner instruction

Keep this with the ordinary dev-env merge and release path. It does not clear a separate hold and it does not authorize access, security, production, or media work.

User, Sentinel_686bdbe09ca88191932046a3d8655f20: "I have told you over and over and over and over that agents do not need permission to merge PRs they opened"

Normal authorized merge and release still waits for unchanged-head preflight, required CI, independent native review, and disposition of every finding. Do not ask for redundant permission for that path. Do not treat the sentence as a waiver for a denied action.

Configured UI model and cost are not serving evidence. Record serving and cost as Unknown unless the handoff itself says Reported. Do not copy a configured model name into serving.

## Handoff template

One JSON document, at most 128KiB. Fields are evidence references. The file does not authenticate its author and does not grant authority. Do not store prompts, tokens, or private thread history. The verbatim line below is the short owner instruction the record must preserve.

```json
{
  "schema": "dev-env.workflow-handoff/v1",
  "story": "177",
  "relatedStories": ["117", "101"],
  "repository": "github.com/thaynes43/dev-env",
  "pullRequest": 0,
  "head": "0123456789abcdef0123456789abcdef01234567",
  "coordinator": "sol-native01a164b6-4fdd-73f2-b49b-738c23b5a709",
  "reviewBinding": {"task": "native-review-1", "uid": "uid-review-1"},
  "modelProvenance": {"serving": "Unknown", "cost": "Unknown", "configuredUI": ""},
  "events": []
}
```

Event `kind` values are `worker-pass`, `executor-state`, `deadline-reached`, `stop-release`, `delivery-receipt`, `acknowledgment`, `review-observation`, `scope-instruction`, `hold`, `hold-disposition`, `successor-assignment`, `lease-expiry`, `reconnect`, `gate-evidence`, and `taskclaim-mutation`. Each event has `identity` and `sourceTime` (`RFC3339`, no fractional seconds). A repeated identity with the same payload is ignored. The same identity with a different payload is a conflict.

`taskclaim-mutation` is always refused. The checker leaves TaskClaim unchanged.

## Checker

From a checkout:

```bash
GOTOOLCHAIN=local CGO_ENABLED=0 \
  go run ./scripts/trials/workflow_handoff testdata/e-merge.json
```

The command reads one file, or stdin when the argument is `-`. It prints one JSON result and does not write the input, the tracker, or the network.

| Exit | Status | Meaning |
| --- | --- | --- |
| 0 | `reduced` | The record reduced. Blocked custody and a merge blocker are results, not tool failures. Read `nextAction`. |
| 2 | `refused` | Malformed document, unknown field, oversize input, or invalid provenance. Nothing was applied. |
| 3 | `conflict` | Same identity with a different payload, or equal-time authoritative inputs that disagree. Manual disposition is required. No lexicographic winner. |

`checkerAuthority` is always `none`. `completion` is always false. `deployment` is always `not-performed`. `publication` stays distinct from deployment.

Finite fixtures under `scripts/trials/workflow_handoff/testdata/` cover review pickup, successor custody, scope and holds, the worker deadline, and the merge recommendation. They prove the checker and this procedure only.
