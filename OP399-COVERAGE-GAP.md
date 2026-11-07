# OP399 coverage gap

Source-only map of this checkout. Branch `agent/grok-op399-recovery-20261107-2`. HEAD and base `bf3f2493600c680959bfd60d90a81768d28e9674`. No history inspection. No live session, host, or API call.

Targets read: `scripts/coordinator-recovery.py`, `scripts/test_coordinator_recovery.py`, `docs/coordinator-recovery.md`, `.agents/skills/timer-checkin/SKILL.md`. The skill's backlog pointers are ADR-019 (OpenProject #399) and saga decision 94. Those records change no runtime and do not add a deterministic fixture. The skill states that its bounded-wait re-arm, restart recovery, and completion are not demonstrated, and that a real wake is OP385 pilot proof. This adapter does not supply that proof.

The adapter is one read-only review tick. It is not a custodian timer and it does not create a scheduler. `completed` and `recovered` are reserved and are not emitted.

## Custody transfer

Scheduling-bound transfer only. There is no custodian-session handoff in this adapter. Timer-checkin section 5 (one custodian, successor write, old timer delete, successor re-create) has no function here.

| Behavior | Function | Test |
| --- | --- | --- |
| Access TTL cannot renew a window | `validate_request` | `test_source_only_and_refusals_call_nothing` |
| Expired window renews only from the named custody message | `observe` sets `_renew`; `publish` calls `renew_window` | `test_expiry_rejects_future_and_foreign_ack_and_accepts_owner_ack` |
| Ack after `observedAt` is not a transfer | `renew_window` | same test, reason `future-ack-rejected` |
| Ack recipient must be the claim owner | `renew_window` | same test, reason `foreign-ack-rejected` |
| Body must match repository, pull request, task, correlation, and owner UID | `custody_body_ok` | exercised by the accepted owner ack |
| New bound is the request deadline | `renew_window` sets `schedulingUntil` | same test, `2026-11-02T05:00:00Z` |

## Original absolute-deadline re-arm and fire

The deadline is the request field parsed into `_deadline`. A retry re-arm stores `nextAttemptAt = observedAt + backoff` in the cache. `publish` checks `_deadline` before it consults that instant. `BACKOFF` is `(30, 120, 600)`. The command does not sleep.

| Behavior | Function | Test |
| --- | --- | --- |
| `windowExpiresAt` later than `deadline` is refused | `validate_request` | `test_proved_delivery_still_obeys_deadline_and_history` |
| Renewal does not invent a later deadline | `renew_window` | `test_expiry_rejects_future_and_foreign_ack_and_accepts_owner_ack` |
| Exit 5 attempt 1 re-arms 30 seconds ahead and does not send again during backoff | `publish` | `test_correlation_and_head_do_not_reset_retry_budget` |
| Observed time equal to the deadline blocks a new send | `publish` | `test_proved_delivery_still_obeys_deadline_and_history`, reason `deadline-elapsed` |

The deadline test starts from an `uncertain` fence that `reconcile_proven_outcome` removes. It never enters `publish` with `class: retry` and a `nextAttemptAt` still in the future. The backoff test stays inside `deadline` `2026-11-02T04:10:00Z`.

## Duplicate wake prevention

| Behavior | Function | Test |
| --- | --- | --- |
| One process lock around a tick | `main` `fcntl.flock` | `test_restart_and_concurrent_ticks_reuse_one_message` |
| Same head reuses one inbox message | `find_current`, `finish_existing` | same test; `test_head_change_keeps_old_message_and_sends_one_new` |
| Caller `logicalKey` is ignored | `logical_key`, `base_result` | `test_denial_and_unconfirmed_are_not_replayed` |
| Denied, unconfirmed, pending, conflict, and uncertain are not replayed | `NONREPLAY`, `publish` | denial, unconfirmed, pending, conflict, and timeout tests |
| Three same-family publications block another head | `same_family`, `publish` | `test_proved_delivery_still_obeys_deadline_and_history` |

## Completion cleanup

Cleanup here is removal of a non-terminal cache fence after one proved or confirmed publication. It is not `CronDelete`, not a `stop` record, and not status `completed`.

| Behavior | Function | Test |
| --- | --- | --- |
| One exact retained message clears `uncertain`, `unconfirmed`, or `pending` | `reconcile_proven_outcome`, `retained_proves_cached_intent` | `test_timeout_after_delivered_head_allows_one_later_head`, `test_pending_retained_delivery_allows_one_later_head`, `test_unconfirmed_retained_delivery_allows_one_later_head` |
| A wrong body, sender, recipient, family, digest, or a second copy does not clear the fence | `retained_proves_cached_intent` | `test_fenced_outcomes_need_one_exact_retained_message` |
| `denied` and `conflict` stay after a retained message | `reconcile_proven_outcome` returns unless the class is one of those three | same test |
| A confirmed send drops the pre-send `uncertain` marker | `publish` `cache["entries"].pop` | `test_timeout_after_delivered_head_allows_one_later_head` asserts the cache entry is gone |
| A picked caller verdict does not complete the review | `finish_verdict` | `test_current_verdict_with_stale_history_does_not_requeue` |

## Uncovered case

A not-sent retry re-arm whose `nextAttemptAt` is later than the original deadline. `publish` is written to stop with `deadline-elapsed` before the backoff check, so the later instant must not keep the tick `retrying` and must not send. No current test composes those two fences.

That case is the selected gap. It is deterministic and non-runtime. Live wake, resume, and OP385 pilot proof stay unestablished.

## Regression

`RecoveryAdapterTests.test_retry_rearm_fires_at_original_deadline` in `scripts/test_coordinator_recovery.py`. It uses the class fixture: compiled `workflow_handoff`, plus the Python `gh` and `agent-run` stand-ins. No product code change. The fence is already in `publish`.

First tick: `observedAt` `2026-11-02T03:05:00Z`, window `2026-11-02T03:05:10Z`, deadline `2026-11-02T03:05:20Z`, fixture send exit 5. Expect `retrying` / `not-sent-retry`, attempt 1, `nextAttemptAt` `2026-11-02T03:05:30Z`, no stored message.

Second tick: `observedAt` equal to that same deadline, while `nextAttemptAt` is still 10 seconds later. Expect `blocked` / `deadline-elapsed`, no second send, and the retry cache row unchanged.

Local command:

```text
nice -n 19 python3 -I -B scripts/test_coordinator_recovery.py RecoveryAdapterTests.test_retry_rearm_fires_at_original_deadline
```

Exit 1. `setUpClass` ran `nice -n 19 go build -o .../workflow_handoff ./scripts/trials/workflow_handoff`. stderr: `nice: 'go': No such file or directory`. `CalledProcessError` exit status 127. unittest: `Ran 0 tests`, `FAILED (errors=1)`. Go was not installed and was not searched for. This checkout therefore has no RED/GREEN result for the new test. The next check is the draft pull request's normal CI, which builds that checker. A passing CI job is still not a live wake, resume, or OP385 proof.
