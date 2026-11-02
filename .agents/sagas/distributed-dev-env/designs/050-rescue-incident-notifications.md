# DESIGN-050: Truthful rescue incident notifications

Status: source preparation, 2026-11-02, OP355. No ownership configuration,
notification route or operator rollout is activated by this source work.

Git rescue failure means preservation is not proved. Missing a declared clone
does not establish transcript failure, an empty HOME or permission to discard
data. A proven interrupted bootstrap still remains Failed and protected,
including when the Session already has a deletion timestamp. Notification
ownership never participates in rescue, pod/PVC removal or finalizer guards.

Retries and incidents have different identities. Keep newest attempt time and
stamp in status, but never use them, condition transition time or a hash of noisy
diagnostic text as material risk identity. A narrowly qualified missing-clone
failure can carry a notification key derived from closed typed facts: the
declared repository, stopped-agent facts and validated bootstrap proof (or its
explicit absence on an older producer). Only a single missing-clone failure,
with no listed repositories, bundle or other failure, qualifies. Other failures
remain unclassified and cannot use this ownership mechanism. New typed proof,
reason, generation, deletion state or Session UID invalidates the old match.

An optional deployment-owned bounded JSON file records independently verified
native ownership acknowledgments. Entries bind exact Session UID, generation,
notification key, deletion state, owner Session UID, acknowledgment reference and
SHA-256, acknowledgment time and expiry (at most 24 hours). This is trusted
operator configuration, not a provider payload or arbitrary Session annotation.
Reviewers must verify the actual native acknowledgment and its scope before
configuration adoption. A saved file, a Pod phase or a task budget is not proof
of continuing native availability. Eligible ownership additionally requires the
same native conversation in an existing nonterminal owner Session, ready agentd,
a current accepted heartbeat and busy/idle/waiting agent status; missing, stale,
exited or conflicting evidence fails unowned. No entries or automatic renewal ship here.
Unknown, stale, mismatched or expired ownership remains unowned. This config
grants no runtime, deletion, preservation or credential authority.

A separate notification metric retains Session UID, reason, risk key, deletion
state and eligible owner UID. The original rescue-failure metric is unchanged.
The alert rule uses the new metric when present and falls back to the original
metric for older operators. Latest attempt time is an annotation/query, never
an alert label. Unowned failures keep the existing critical route. An exactly
owned, unchanged failure remains visible and gets normal-priority notifications
with a longer repeat interval; it is never silenced. New risk, a new UID or
expired ownership returns to the urgent route. Unrelated critical alerts retain
their existing route.

The responder must honor the same exact owner label and distinguish changed
reason/risk from its old fingerprint claim. Its spend/state failures still fail
closed for model dispatch; Alertmanager's independent direct route retains
safety visibility. A durable responder claim is not a native owner acknowledgment.
No remediation, rescue trigger or storage action follows from these changes.

Acceptance requires capped serial tests for retry stability, new typed risk,
ownership expiry/mismatch, new UID, old producer compatibility, independent
direct routing and responder eligibility. A pending-delete fixture must prove
the Session finalizer, hold and PVC remain protected. Exact independent source
review and required CI precede a separately qualified rollout. Real private
incident and HOME evidence stays in the task handoff, outside public fixtures.
