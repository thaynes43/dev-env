# On-demand native allowance capture source (2026-10-28)

The explicitly enabled `agentd codex-host allowance-read` performs one sanitized native allowance read only for a current confirmed host owned by this Pod and workspace. It preserves the existing host-class refusal and never adopts retained, replaced, foreign, legacy or unbound hosts. It verifies local process, lifecycle, private binding and pinned version before the read, and rechecks ownership afterward; failure is unavailable. It does not perform the broader host readiness RPCs.

The sole native sequence is initialize/initialized, account/read with refresh disabled, and account/rateLimits/read with reset-credit detail excluded and Luna reserve unsupported. Capture identifiers describe this one observer invocation, not provider attestation or a trusted producer sequence. Account binding, provider observation time and freshness remain Unknown. Sanitized output contains no raw account or error payload.

This is source and synthetic-test evidence only. Runtime use requires an independently qualified eligible host and artifact; it does not install, start, pair, register, or authorize a host or collector. No live account, socket or credentials were accessed during this work.
