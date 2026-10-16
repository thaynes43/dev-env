# Retained pilot cohort correction — 2026-10-16 (#350)

The earlier v2.29.1 schema correction is installed with both admission paths closed.
The old fixed seal cannot authorize a changed window/image/template digest. This
source correction gives a new reviewed policy distinct retained cohort identities.
It does not reuse, replace, reseed or delete the old seal or original Session names.

`cohort` is optional and omitted when empty. A pre-change fixture digest is pinned
at `fe1974daeb0dc9fc7ab23bcc459d50847a3ad0a0686055350c58ffbf78ee242c` (from the accepted pre-cohort API fixture); it still matches. Legacy Session/key names and
retained record decoding remain unchanged. Named cohorts use distinct deterministic
seal/Session/key names; `v1` is rejected as a named alias. Existing digest, immutable
spec binding, retained annotations and finalizers bind the cohort without a CRD or
RBAC change. Both API and controller use policy-derived slot names, including B.

Local evidence: 11 methods and 49 named subcases passed across
projectcanary and API, under shared flock, nice19, GOMAXPROCS2 and -p1 with downloads
off. Cases include invalid IDs, legacy golden compatibility, wrong/old/cohort-swapped
seals, cross-cohort records/keys, both slots, DryRunAll no persistence, replay UID
preservation and third request refusal. The synthetic retained-seal fixture initially
kept a resourceVersion on Create; it was corrected, and the failed capture retained.
Controller tests compile locally but cannot execute without envtest binaries. Required
hosted CI must execute controller tests and the unchanged actual real-API schema
regression; compilation is not a controller test pass.

Independent Mac exact-head review and required CI are still pending at this source
checkpoint. No installed fresh-cohort, successful operator preflight or actor launch
is claimed. Tom authorized the complete bounded retry for October 16, 18:00–18:30 UTC
(2:00–2:30 PM EDT), with exactly two Codex gpt-6.1-sol/high actors, S resources, 20Gi
isolated HOME, 15-minute tasks and 20-minute Pods. This requires normal signed release,
reviewed GitOps actual-UID policy/seal/inverse and supported operator-API preflight.
General private admission remains false. There is no direct Kubernetes create retry,
new RBAC/auth/egress, child, replacement, window extension, v1 restart or cutover.
Failed validation or a missed window stops the pilot and closes admission.

Durable owner order: `/home/dev/work/orders/dev-env-chapter7-fresh-cohort-1016-1145/`.
All earlier stopped candidates, failed pilot, denied direct dry-run and phase clocks
remain recorded; this claim resets none of them.
