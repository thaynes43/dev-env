# HOME capacity defaults without session drains

Status: source proposal for [OP349](https://work.haynesnetwork.com/work_packages/349),
2026-11-01. Deployment and live PVC expansion remain separate evidence.

Increasing the HOME allocation for new sessions must not make existing pods
outdated merely because their volume was created with a smaller capacity.
The controller creates HOME claims once; it does not update existing claims
when the template changes. Pod construction uses the claim name, not its size.

Normalize only `home.size` to the historical `20Gi` value when calculating the
pod revision. Keep the parsed allocation unchanged for new claim construction.
The frozen value preserves the existing hash for the deployed 20Gi default;
omitting the field would change every revision during adoption. Storage class,
image, environment, limits and other existing revision inputs stay significant.

This prevents a capacity-only template update from creating a new reason to
drain sessions. It does not suppress drains caused by other template changes or
cancel drains already underway. Existing HOME requests, PVs, filesystems and
session identities remain untouched. Online expansion needs its existing
authorized workflow, backend headroom and filesystem verification.

Regression checks compare the new revision against the legacy hashing result,
verify several capacity values share a revision without changing their parsed
size, and keep storage-class changes significant. A controller test verifies
that a larger allocation creates the larger new claim while producing the same
pod and leaving a current idle pod ineligible for a revision drain.

Delivery order: reviewed source and required CI, signed operator publication,
coordinated GitOps adoption on every operator replica preserving sessions, then
the separate larger HOME default. Do not adopt the default under an older
operator that still hashes capacity. Before reverting to an older operator,
restore the default to 20Gi while the fixed operator remains installed. Preserve
existing canary and pilot template-freeze rules. No access, credential, security,
network or retention policy changes.
