# ADR-016: Refuse uncovered delegate decisions and recover invalid Pending slots

- **Status:** Proposed source correction, 2026-10-31; OP335 lead review held. Activation held.
- **Amends:** ADR-015's treatment of the shared decision slot and Forbidden identity reads. ADR-015 remains unchanged as the historical accepted record.
- **Tracking:** OpenProject #335, origin #233; supersedes the held S2 source in PR #791.
- **Authority:** OP335's bounded continuation work order permits source preparation and one replacement draft PR, with no ready action, merge or activation.

An authenticated, eligible delegate must have live delegated GrantPolicy coverage
before recording either an approval or denial. The API lists policies through its
uncached reader in `--policy-namespace`, defaulting to the operator namespace and
matching the broker's configuration. An uncovered request returns 403 and writes
nothing. Policy read errors return a non-2xx response and write nothing. The
broker's existing live apply-time coverage check remains unchanged: coverage lost
after the API read cannot authorize a grant.

A permanently refused Pending delegate record may be removed by the broker so
another legitimate decision can use the shared slot. Removal requires a live
comparison of the same grant UID, Pending phase and exact annotation value,
followed by a resourceVersion CAS. A changed decision, replaced grant or changed
status is left for a later reconciliation; cleanup never retries its patch against
a replacement. Human records are never eligible for this removal. Temporary read
errors and busy maintenance targets retain their records. Successful cleanup
emits an audit event and log; it grants no access. This supersedes ADR-015's rule
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
