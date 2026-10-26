# Story 178 CLOSED bootstrap source result

## State

Source WIP only. It is **not ready for merge, activation, or runtime use**; Sol retains final review/CI/merge custody.

- Baseline: `7e56810a4f4db54f03f903612efdc27e28b2b59a`.
- Branch: `agent/portal-closed-bootstrap-178-20261026`.
- Default: `--enable-portal-admission-bootstrap=false`; a permit object alone does not enable the route.
- Model: configured `gpt-5.6-terra` / medium; provider-reported model and cost Unknown (no probe).

## Implemented source direction

- Fixed POST `/v1/portal-admission/bootstrap` and a one-shot `agent-run portal-admission-bootstrap --request-id` client.
- Typed, body-bounded request and response; no caller supplied Kubernetes target, identity, payload, or verb.
- Exact fixed target/permit names and CLOSED payload; live exact-object check before permit consumption; no patch, delete, apply, or recreate.
- Bounded strict request parsing rejects trailing bytes and duplicate/unknown JSON keys before mutation. Fresh live Session/Pod ownership, UID, ServiceAccount and deletion checks fence the consuming boundary.
- Permit validation binds request ID, permit UID, Session UID/generation, Pod UID, digest, expiry and revocation. A CAS consumption persists the observed target receipt or a create-attempt state; a consumed permit never enables a second create.

## Evidence and gaps

- Private official Go 1.27.2 was installed after verifying the official release-index SHA-256 `ecbadb99091a3f46e31f5f934b068b1864eafa7995211b39eaddf76996045fe5`.
- Focused compile: `go test -p=1 ./internal/apiserver ./internal/agentrun ./cmd/dev-env-operator -run '^$'` PASS.
- Focused fake tests: `TestPortalAdmissionBootstrapDisabledAndExistingClosed` and `TestPortalAdmissionBootstrapCreatesOnceAndRejectsAmbiguousBodies` PASS. The positive test deliberately intercepts Create after fake-client validation to simulate the immutable API receipt UID/RV.
- Required gaps: isolated admission/RBAC/envtest fixtures and the full concurrent/restart/receipt-failure matrix remain to be completed before source acceptance.
- No real cluster request, permit, RBAC/admission/GitOps, deployment, release, or merge occurred.
