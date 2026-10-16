# Sealed pilot admission compatibility (#347)

The 2026-10-16 14:00 UTC request was rejected with HTTP 422 by the installed
common-rules and directory CEL rules. A was invoked once, neither actor existed,
and B was not invoked. Closure haynes-ops#3967 removed the flag, retained new
images and immutable seal, and preserved peers/v1. Original raw evidence is in
the Chapter7 pilot-runtime-1016-0907 order; it is not overwritten by this fix.

The preflight gap was fake-client handler coverage plus template-only preparation,
without exact-resource admission. Required proof now includes real API-server
DryRunAll of the API-built Session and refusal cases for unbound/mismatched rules,
canary mixing, altered caller/model/profile/resources/limits and mutation.

Use the existing launch invocation with --dry-run=server before any separately
authorized future launch. The dedicated endpoint must return dryRun=All and
persisted=false. Unsupported/denied/stale-schema responses stop the procedure;
never remove the option, fall back to create, add canary fields or change identity.
Preflight validates admission only, not provider credentials, actual model
serving, placement or a runnable two-agent outcome.

The installed schema remains the old generation until a separately authorized
GitOps rollout. Source/release pins and real installed-schema dry-run results
(or a precise authorization/schema blocker) belong in the durable result.
