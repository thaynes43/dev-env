# ADR-008: Private-history retention marks task sessions only

- **Status:** Accepted 2026-10-19 (Tom, Q-110-04, through the Claude native
  prompt: "Exempt interactive (my pick)").
- **Date:** 2026-10-19
- **Decider:** Tom
- **Amends:** [DESIGN-008](../designs/008-private-provider-history-retention.md)
  and [ADR-006](006-private-home-expiry-corrections.md) section 1 for scope
  only: which Sessions the retention latch is written for.
- **Retains:** everything about a Session that is already marked, the canary and
  pilot protection, `--enable-private-history-retention` as the switch, and
  Q-31's expiry policy.

## Context

With `--enable-private-history-retention=true` the operator wrote
`status.privateHistory` for every Session without a shared workspace, whatever
its mode. The latch is permanent, and `removePod` and `podRemovalAllowed` refuse
to rescue or delete a latched Session's pod. A suspend of a latched Session
therefore patched `operatingMode: Suspended` and stopped there: the pod stayed
Running, with no rescue.

ADR-006 section 1 and plan 11 follow-up F3 say a suspended private Session is
never stopped, so nothing could produce the stop evidence the suspend waited
for. A resume of a stopped latched Session is impossible too: its executor is
one-shot, and no replacement starts. Remote and local Sessions are long-lived
and meant to suspend and resume on the same HOME. Retention made both
impossible for them.

## Decision

Only a task Session (`spec.mode: task`) is newly marked with private-history
retention. A remote or local Session is not marked, so it takes the ordinary
path: rescue (D-51), pod delete, and a resume that recreates the pod on the same
HOME volume.

- **No unmarking.** A Session that already has `status.privateHistory` stays
  protected in every mode. The latch is authoritative and the operator never
  clears it. Such a Session ends by a reap or a replacement, not by a suspend.
- **Canaries and pilots are unchanged.** They stay protected by their own
  binding, whatever their mode.
- **No new flag and no GitOps change.** The retention flag still gates task
  marking, and the canary requirements for it in `main.go` still hold.

## Consequences

- A remote or local Session gets no executor-identity retention, and its HOME
  is no longer held by the retention latch. After a verified rescue, D-62's
  autoarchive of a suspended Session (after `archiveAfter`) and D-51's archive
  on a reap both delete the Session's HOME volume, provider history included,
  as they did before retention existed. Resume before `archiveAfter` to keep it.
  The legacy rescue exec runs in the pod again. DESIGN-008's "no legacy rescue
  exec in a provider HOME" holds for task Sessions only.
- Q-31's expiry reaches only a latched Session, so a remote or local Session
  never expires through it.
- Not done here: the executor-stop flag, any HOME archive or deletion, and H7 or
  H8. Plan 11 follow-up F3 is unchanged.

## Verification

`TestPrivateHistoryIsNewlyMarkedOnlyForTaskSessions`,
`TestPrivateHistoryAlreadyMarkedInteractiveSessionStaysProtected` and the
envtest `TestInteractiveSessionSuspendsWithPrivateRetentionOn` (rescue, pod
delete and `Suspended` with retention on).
