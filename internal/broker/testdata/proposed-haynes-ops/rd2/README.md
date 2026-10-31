# PROPOSED RD2 fixtures; never apply this directory

These are source-only, non-authoritative fixtures for [DESIGN-043](../../../../../.agents/sagas/distributed-dev-env/designs/043-rd2-minimal-admin-lease.md).
No kustomization, broker startup or deployment loads them. The existing verbatim
`testdata/identity-guard/identity-guard.yaml` stays unchanged.

- `admin-role.yaml` proposes the existing logical role's effective root resource
  rules plus all non-resource URLs. It has no subject. Check existing bindings
  before any future deployment; do not expand unknown leases implicitly.
- `identity-guard.yaml` preserves every validation in haynes-ops `a2d2a961`.
  Only the match condition adds an exemption for the **synthetic** exact username
  AND SA UID. Neither literal is a real grant. A prefix never proves authority.
- `exec-guard.yaml` makes the same exact static match-condition change to
  Kyverno's `dev-env-exec-guard`. It retains `failurePolicy: Fail` and Deny.
  Envtest cannot run or prove this Kyverno policy.

The prototype compiler is default off, has no production call site or CLI flag,
and returns `false` on missing/revoked/expired/mismatched inputs or read errors.
The tests render that result into the proposed identity guard. This models
**proposal compilation**, not an admission-time GET or a live revoke controller.
A static exemption cannot independently inspect Active state or wall-clock expiry.
Deleting the original SA/binding and bounding the token expiration are mandatory;
cache delay and derived authority remain. See DESIGN-043 before drawing conclusions
from a positive envtest admission result.
