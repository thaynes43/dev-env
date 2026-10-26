# ADR-009: Scoped grant decision handoffs

Status: proposed source, 2026-10-26. Applies only after review, tests and the
admission guard; no rollout or usable access is asserted here.

Tom approved general individually scoped access requests for internet, LAN,
Kubernetes and credentials, with a permitted coordinator or Tom deciding a
specific request. The media services are initial test data, not a product-specific
bundle. This is the authority for revising D-27/D-56's earlier page-only model.

An authenticated human, configured coordinator or explicitly delegated current
parent session for its direct child can
submit a decision bound to the exact request, session and pod. The operator is
trusted to authenticate the caller and write the protected handoff. Its authority
therefore includes submitting a decision, though it still cannot write grant
status. The broker alone validates and writes status. A coordinator or parent session must match
an explicitly marked GrantPolicy scope; marked policies do not auto-approve.
The status records the authenticated coordinator as a delegate, never as a human.
Unknown, stale, self and out-of-scope requests fail closed.

The AccessGrant annotation guard and pod-label reservation guard are mandatory
before enabling either default-off flag. Manual egress selects the approved pod's
UID label; standing policy behavior remains unchanged. Broker pod patch may only
set that label to the actual pod UID. No daemon restart is involved.

A phone conversation alone is not authenticated human attestation. Mobile
provenance, generic selected-field credential execution, old-session cleanup and
end-to-end acceptance are unresolved. Credential approval remains unavailable
without a standing policy until separately implemented and reviewed.

A session delegate principal binds its session name, session UID and current pod UID.
The child retains its immutable parent name. Older children do not all record a
parent session UID; the policy grants the current explicitly named parent incarnation,
and any available historical parent UID must agree.
