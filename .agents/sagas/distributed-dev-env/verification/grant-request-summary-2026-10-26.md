# Generic access decision source checkpoint

Tom's 2026-10-26 direction covers individually approved internet, LAN,
Kubernetes and credential requests. Haynesnetwork services are test data, not
an access product or a blanket grant.

The existing grant view carries the grant and session UIDs and a digest of the
complete request. Its digest is a comparison, not authentication. The proposed
decision route is default off: a TokenReview-authenticated human, or a configured
coordinator for its direct child, submits a decision bound to grant UID, session
UID, pod UID, generation, request digest and grant resource version. The broker
rechecks the live request, session and pod before its status update. The operator
does not write grant status. No requester may approve its own grant.

A GrantPolicy annotated `dev-env.haynesops.com/decision-approver` names an exact
`coordinator/<namespace>/<serviceaccount>` principal and bounds a manual
decision using existing policy matching. Such a policy never auto-approves,
including when its annotation is empty. Other policies retain their existing
standing behavior. The broker records the coordinator as the decision maker;
a phone answer or model-supplied text is not a human attestation.

Activation requires the separately reviewed handoff annotation admission guard,
required CI and independent security review. Credential manual approval remains
refused; selected-field credential installation and old-session expiry cleanup
are not implemented. Mobile answer provenance and routing beyond the configured
coordinator host are not implemented. No deployment or usable access is claimed.
