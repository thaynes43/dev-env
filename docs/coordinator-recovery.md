# Coordinator recovery

This is source behavior for one exact-current-head read-only review. It is not installed acceptance of OP355, and it does not accept OP354, OP321, OP323, or OP356.

The adapter is `scripts/coordinator-recovery.py`. The default command validates one request and does not start a process. `--observe` is the executable slice. It calls only:

- `gh pr view --repo REPO PR --json headRefOid`
- `agent-run show NAME -o json`
- `agent-run inbox list|read|ack-status|send` with the existing flags
- the existing `workflow_handoff` checker on one explicit handoff document

There is no `--worker` argument, no provider selection, and no local message, claim, dispatch, or audit ledger. A caller-supplied logical key is ignored. The publication key is derived from the repository, pull request, observed head, task, task UID, claim, owner, and recipient. Correlation is evidence and cannot reset the retry or denial fence.

Durable pending state is an inbox message created by `agent-run inbox send` with that key. The API's identical-key behavior is the idempotency mechanism. The required observation `--cache` file records uncertain, denied, unconfirmed, pending, and not-sent retry receipts. An uncertain marker is atomically written and fsynced before publication; a stable file lock serializes ticks. Unknown outcomes are not replayed. It does not authorize a send and it is not a second queue.

## What a tick does

The current head is the single `headRefOid` from `gh pr view`. Caller text is not head proof. `FETCH_HEAD`, a comma, or more than one JSON field is refused. A 40-character `fetchedRef` must equal that oid. `refs/pull/<pr>/head` is accepted only for this pull request and is not itself the oid.

`agent-run show` observes the claim owner and recipient separately. The owner view must include one owned task claim whose project, catalog repository, task key, claim name, claim UID, and owner match the request. Its observed `provenance.github` must match the full GitHub repository. The recipient view supplies native process evidence. `Unknown`, stale, future, archived, or missing claim data blocks. The claim is not transferred.

Inbox list reads are bounded to four pages of 20. A remaining continuation, a missing sent box, or a missing thread view cannot authorize a send. The adapter reads a matching message and checks the authenticated sender and recipient on the CLI JSON. Those fields are the provenance. The request cannot substitute for them.

`workflow_handoff` reduces the explicit review observations against the observed head. A wrong-head observation is refused by that reducer and does not hide a later picked current-head verdict. No picked current-head verdict means a send may be considered. A picked verdict suppresses another send.

## States

| Status | When |
| --- | --- |
| `pending` | Source-only, or an intent is API-delivered and a bound verdict is not present. An acknowledgement is not completion. |
| `retrying` | A not-sent inbox retry with recorded attempt 1 or 2 whose backoff instant has not been reached, or the first publication followed a native nonzero task exit. The command does not sleep. Recorded attempt 3 is terminal: the third exit 5 is `blocked` with `persistent-failure`, and its 600 second `nextAttemptAt` is not waited. |
| `blocked` | Partial observation, claim mismatch, expired scheduling window, lost acknowledgement, unanswered question, denial, unconfirmed send, or a persistent failure. |
| `recovered` | Reserved; authenticated receiver evidence is still required. This adapter does not emit it. |
| `completed` | Reserved; authenticated receiver evidence is still required. This adapter does not emit it. |
| `refused` | Malformed input, an unknown action or permission, or an access or authorization TTL. Exit 2. Nothing is observed. |

Queued, API-delivered, read acknowledgement, question answer, native task exit, and the review verdict stay separate fields. Caller reducer observations, digests, and artifact references are attested display only: the adapter returns `pending` with reason `attested-unverified`. They cannot prove completion. Pod Ready, heartbeat, a null top-level outcome, wrapper exit 0, shell exit, and an acknowledgement do not complete a review. Authenticated receiver and attempt binding remain an OP354 dependency.

A native nonzero `agentStatus.task` while the pod is Ready escalates after the one current-head message exists and the task finish time is later than that message. It does not start a process. Exit 3 (denial), exit 6 (unconfirmed), and exit 7 (pending) are not replayed. Exit 5 is a not-sent send. There are three sends, with 30 seconds between the first and the second and 120 seconds between the second and the third. The first exit 5 records attempt 1 and permits the next send after 30 seconds. The second exit 5 records attempt 2 and permits the next send after 120 seconds. The third exit 5 records attempt 3 and `nextAttemptAt` 600 seconds later, then immediately blocks with reason `persistent-failure`. A later tick blocks when attempt is at least 3 and does not consult that instant. The 600 second value is terminal backoff metadata. It is never waited and never used for a fourth send. Three publications for the same repository, pull request, task, claim, owner, and recipient block even if the head changed. One new head still gets one message while that cap has room, and the older message stays.

A lost `ack-status` (exit 6) blocks and names the coordinator. A missing acknowledgement is not proof the message is unacknowledged and is not a second send. An unanswered or stale question bound to the correlation blocks. An answered question is evidence only.

An expired scheduling window is renewed only by a durable acknowledgement of the named custody message: owner recipient, acknowledgement time after the old window, not after `observedAt`, and not after the source deadline. The new scheduling bound is that deadline. Access and authorization TTLs are refused. A future or foreign acknowledgement does not renew the window.

A `sourceCheckpoint` is optional source state, not a second scheduler and not a live capacity reader. `source-capacity` is due when `remaining` is less than or equal to `threshold`. `source-deadline` is due when `observedAt` is at or after the source deadline minus `leadSeconds`. The threshold gates only the first durable inbox offer. A later reading that rises above the threshold still reconciles that offer, its acknowledgement, and an uncertain, denied, or retry fence. It does not hide them and it does not send another offer.

The checkpoint records the observed head as the commit, the derived logical key, the pull request, whether a review is absent or picked, and whether an open question was observed. Branch, CI, and the next safe step are the explicit value `unknown`. Those fields are not observed here. That review and question snapshot is frozen when the offer is stored. A later picked review or answered question does not rewrite it. Reconciliation uses the persisted digest and the current head. An acknowledgement of a previous head, a changed message, or a foreign acknowledgement does not transfer custody and does not start a second successor. A killed or expired owner may repeat one not-sent offer after 30 seconds and then stops. The task claim stays unchanged. A non-string kind is refused before any command. Credential, grant, and collector kinds are refused. This is not OP399's original-deadline rearm. It is a partial source foundation. OP383 stays open: live capacity, runtime successor integration, and acceptance are not proved.

Every non-refusal result carries `owner`, `reason`, `correlation`, `observedAt`, and evidence rows. `notificationsSent` is 0. `checkerAuthority` is `none`. `deployment` is `not-performed`. `taskClaim` is `unchanged`. No notification command exists.

## Gaps

The native review receiver is an OP354 dependency and is not started here. Review observations are explicit input to the existing reducer, not an API verdict object. `agent-run show` task exit is fail-closed session evidence, not a review verdict by itself. Tests inject fixture executables named `agent-run` and `gh`; they do not call a live API. A passing test is not installation or acceptance.

## v1 source comparison

Read-only comparison of haynes-ops `a7a7818e686a2bf5a102a2e7447190a88ac76e96` (source only; v1 installed behavior was not observed):

- `kubernetes/main/apps/dev/dev-env/app/resources/cursor-worker.sh` lines 108-157 use flock against a duplicate supervisor, capture the child return code, reset the failure count after 600 seconds, open a breaker after five short exits, and delay by failures times backoff.
- `kubernetes/main/apps/dev/dev-env/app/resources/agent-run.sh` lines 1254-1270 probe supervisor liveness. The start pipeline ends with `|| true`, so supervisor liveness is not evidence that the inner invocation succeeded.

## Command

```bash
python3 -I -B scripts/coordinator-recovery.py --request request.json
python3 -I -B scripts/coordinator-recovery.py --request request.json --cache cache.json --observe
```

`--observe` requires `--cache`. Without `--observe` the process does not call `gh`, `agent-run`, or the checker.
