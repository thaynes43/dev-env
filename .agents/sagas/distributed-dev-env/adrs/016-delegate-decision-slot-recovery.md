# ADR-016: Refuse uncovered delegate decisions and recover invalid Pending slots

- **Status:** Accepted 2026-10-31 for source preparation only. Activation held.
- **Amends:** ADR-015's treatment of the shared decision slot and Forbidden identity reads. ADR-015 remains unchanged as the historical accepted record.
- **Tracking:** OpenProject #335, origin #233; supersedes the held S2 source in PR #791.
- **Authority:** Lead ruling (dev-env-1029-132039, OP335) under the coordinator's direction for routine source-design decisions. This records source behavior, not activation consent; the PROPOSED RBAC and admission changes are not accepted.
- **Merge prerequisite:** Satisfied by the lead ruling above for source merge; the activation bundle remains held.

An authenticated, eligible delegate must have live delegated GrantPolicy coverage
before recording either an approval or denial. The API lists policies through its
uncached reader in `--policy-namespace`, defaulting to the operator namespace and
matching the broker's configuration. An uncovered request returns 403 and writes
nothing. Policy read errors return a non-2xx response and write nothing. The
broker also selects the delegated policy through its uncached APIReader; a cache
miss alone cannot refuse or clear a record. Its existing live named-policy
apply-time check remains unchanged: coverage lost after the API read cannot
authorize a grant. A live policy-list error returns an error for retry, without
applying or clearing the decision.

A permanently refused Pending delegate record may be removed by the broker so
another legitimate decision can use the shared slot. Removal requires a live
comparison of the same grant UID, Pending phase and exact annotation value,
followed by a resourceVersion CAS. A changed decision, replaced grant or changed
status is left for a later reconciliation; cleanup never retries its patch against
a replacement. Human records are never eligible for this removal. Temporary read
errors, busy maintenance targets and otherwise valid decisions dated ahead of
the broker clock retain their records. Future-dated decisions requeue until
within the allowed clock skew, bounded by the grant's existing Pending deadline.
Pending maintenance approvals classify only invalid envelopes, identity/ownership or eligibility
mismatches, and definitive NotFound reads as permanent maintenance identity
refusals. Other session, pod and node read errors (including 429, 503, timeout
and transport failures) return without ErrRefused, preserve the annotation and
emit no refusal event; a later reconciliation can apply the decision. This is
the Pending decision path; active maintenance participant/target failures keep
their existing stop-proof behavior. Successful cleanup emits an audit event and
log; it grants no access. This supersedes ADR-015's rule
that an uncovered or permanently invalid delegate record must occupy the slot
until release or timeout. Valid decisions remain set once across replicas.

Forbidden delegate pod/session identity reads are permanent refusals, including
while Active, and use the existing revoke/stop-proof tail. Transient read errors
retain the existing retry behavior. Host grant GET requires the requester's
original UID and the host's direct-child scope through `childForCoordinator`.

The held activation bundle must include the **PROPOSED** operator GrantPolicy
`get,list` permission in the configured policy namespace and account for the
broker's guarded removal in its protected decision-annotation admission rules.
No admission/RBAC change or haynes-ops write is made here. All runtime flags,
policies, templates, grants, credentials and access activation remain held.
S6's creator checks and the delegate, human, attested Pending order are unchanged.

Source evidence covers uncovered approve/deny with no write and subsequent human
202, covered 202 Pending then broker Active, policy loss at the broker's live
apply read, guarded cleanup races, Forbidden revocation and host read scope.
The local real-token envtest establishes policy coverage before accepting the
decision. This provides no installed or usable runtime acceptance.
