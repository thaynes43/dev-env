# DESIGN-008: Private provider-history preservation boundary

**Scope:** the first bounded source unit of #174. **Decision:** ADR-003.
No new retention TTL, context-export permission or inference follows from this
slice. Once a private lifecycle is protected, its persisted retention latch
continues to guard release even if the startup flag is later disabled.
Claude Code review is optional and currently unavailable; required CI
and native independent review govern merge (Tom, 2026-10-11).

## Contract

An explicit, default-off operator setting enables conservative private-home
retention for managed sessions without a shared workspace. Reap and suspended
autoarchive preserve the provider HOME PVC and durable AgentSession identity
instead of allowing legacy Git rescue or an empty/foreign-volume fallback to
destroy provider history. Shared sessions keep their existing typed lifecycle.
No existing session is restarted by enabling this setting.

Record the original operator-bound home claim name/UID, SessionUID and last
observed executor PodUID/node/template revision. Preserve that record when
live status clears absent-pod fields. Record observed provider/conversation
identity as provenance, without credential or transcript bodies. Bind observed
resources to their actual owner and generation; refuse replaced claims,
foreign executors, ambiguous old bindings and unconfirmed writes. An old or
missing binding remains Unknown; it is not silently backfilled as stop proof.
Persist a one-way private executor-attempt intent before first creation. A lost
acknowledgement can adopt only a verified executor from that same causal
intent; an absent later Pod cannot authorize a replacement model. This guard
is not general #160 recovery and has no automatic rearm.

Guard every private archive, volume deletion/release and session-finalizer
release path before legacy fallback. Keep existing protective finalizers and
ownership intact. Verify that foreground/background deletion cannot bypass
home preservation; if an existing claim lacks a provable retention boundary,
report PreservationUnknown and refuse completion. Do not detach or orphan a
home or release its protection as a shortcut.

This slice adds no private typed termination proof. A missing Pod, expired
heartbeat, PID-only boolean or legacy shared-workspace proof cannot establish
that an independent private executor stopped. Preserve stop state as Unknown
where current evidence is insufficient. It does not authorize a stopped-history
export or a replacement model execution.

Suspended retention remains Suspended with Ready false. Reap remains HTTP 202
and Reaping with an explicit RetentionPending or PreservationUnknown condition;
do not set ArchivedAt, label archival complete or claim resource deletion.
Repeated requests remain discoverable and idempotent. The existing API view
exposes bounded retention and last-executor metadata under its current ACL;
new peer read permission must not grant mutation. Existing coordinator
direct-child/sibling denial is preserved; ordinary session callers retain the
legacy broader read/reap behavior. That legacy behavior is not the authorization
contract for future peer context. No broader scope or new context endpoint is
introduced.

DeletionTimestamp continues to end grants and deny credential installation.
Retaining data does not retain access. The keeper's globally fenced refresh
lease remains untouched; the source has no per-session refresh lease to invent.
Claude/Codex subscription operation remains intact, and the metadata contract
can represent a future local provider without starting local inference.

## Paths and acceptance

- AgentSession types, validation and generated schema/deepcopy.
- Controller archive/reap/volume/finalizer guards and durable observation.
- Existing API view/session/coordinator ACL tests; operator settings.
- Workflow guide, HANDOFF and #174/#91 delivery receipts.

Use focused fake-client lifecycle tests and bounded file fixtures. Prove private
HOME identity/bytes/owners survive reap, timer and operator reconstruction;
missing/replaced claims, stale generation and unconfirmed writes fail closed;
absent executors never become StopConfirmed; live observation survives pod
absence; repeated DELETE remains 202/discoverable; current sibling ACL denies
unauthorized reads/mutations; and deletion still revokes grants/credential
installation. Keep legacy shared lifecycle tests meaningful. Run tests at low
parallelism under nice; no stress, wide loops or local image builds.

## Remaining acceptance

Publish/promote the reviewed operator and schema with the guard explicitly
enabled, then verify that operator replacement preserves running sessions.
This preservation boundary is not full #174 acceptance. A subsequent bounded
unit must define authorized provider-history paths and retained-context
manifests/cursors, private stop/completeness proof and reads without a Ready
source Pod or a new model. Retention expiry/destruction requires an explicit
policy; no earlier shared-Git TTL is adopted. Keep #174 and #91 open and retain
the failed native fixture and its original ledger.
