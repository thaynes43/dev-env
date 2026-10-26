# Story 178 CLOSED bootstrap source result

## State

Source WIP only. It is **not ready for review, merge, activation, or runtime use**.

- Baseline: `7e56810a4f4db54f03f903612efdc27e28b2b59a`.
- Branch: `agent/portal-closed-bootstrap-178-20261026`.
- Default: `--enable-portal-admission-bootstrap=false`; a permit object alone does not enable the route.
- Model: configured `gpt-5.6-terra` / medium; provider-reported model and cost Unknown (no probe).

## Implemented source direction

- Fixed POST `/v1/portal-admission/bootstrap` and a one-shot `agent-run portal-admission-bootstrap --request-id` client.
- Typed, body-bounded request and response; no caller supplied Kubernetes target, identity, payload, or verb.
- Exact fixed target/permit names and CLOSED payload; live exact-object check before permit consumption; no patch, delete, apply, or recreate.
- Permit validation binds request ID, session UID, pod UID, digest, expiry and revocation; resource-version update consumes before create.

## Evidence and gaps

- Private official Go 1.27.2 was installed after verifying the official release-index SHA-256 `ecbadb99091a3f46e31f5f934b068b1864eafa7995211b39eaddf76996045fe5`.
- Focused compile: `go test -p=1 ./internal/apiserver ./internal/agentrun ./cmd/dev-env-operator -run '^$'` PASS.
- Focused fake test: `TestPortalAdmissionBootstrapDisabledAndExistingClosed` PASS.
- Required gaps: no positive target-CREATE/race/restart/uncertain-receipt tests; no admission fixtures or envtest. The fake client does not generate a create UID like the API server, so the positive create path needs an explicit test interceptor or envtest fixture.
- No real cluster request, permit, RBAC/admission/GitOps, deployment, release, or merge occurred.
