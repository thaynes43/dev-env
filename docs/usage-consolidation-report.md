# Consolidated usage report (Story190 C1)

`internal/usageconsolidation` is a typed, pure in-memory projection. Its caller
chooses already-authorized `providerallowance.ReadResponse` snapshots,
`protocol.UsageObservation` initial-invocation DTOs, and exact session-UID label
associations. It has no adapter, registry, storage, clock, I/O, RPC, CLI, HTTP,
portal, scheduler, permission map, or account authorization role.

## Separate views

Allowance snapshots are copied as original `ReadResponse` values. Their account,
window, pool/product, source, version, freshness, reset, category, timestamps,
zeroes, and Unknown values are unchanged. The projector does not sum windows,
categories, aliases, or shared pools; infer credits from tokens; recompute
freshness; or freshen `servedAt`, capture, or provider timestamps. At report
generation, currentness and an availability recommendation are explicitly
`Unknown`. A passed reset does not imply replenishment or zero, and retained
expired/historical values remain retained.

Attribution rows contain only valid `InitialManagedInvocation` observations. The
row preserves requested provider/model separately from provider-reported served
models, every per-metric Unknown versus explicit zero, partial observations, and
`ProviderCLIEquivalentEstimate` as an estimate rather than an actual charge. It
does not emit task, model, account, token, currency, or dollar totals. Replayed
identical `(SessionUID, AttemptID)` observations make one row. Any different
payload for that identity suppresses its entire ambiguous group; input ordering
does not change the result. Different explicit attempts remain separate rows.

Only exactly matching caller-supplied session-UID associations label a row.
Missing or conflicting associations return `Unknown` project, repository, and
task labels. Nothing is inferred from provider, model, title, branch, or
observation content.

## Coverage and limits

The attribution view is explicitly initial-managed-invocation-only. Later turns,
uncaptured/off-platform work, and missing associations are coverage gaps, not
zero usage. Allowance and attribution coverage do not prove one another. Cursor,
Grok, mandatory full-V1, and native188 gaps remain explicit.

The caller supplies finite input limits for snapshot count, snapshot items,
observations, and associations. The report deep-copies outputs so subsequent
caller mutation cannot alias report data. JSON round-tripping is supported for
the report DTO; serialization itself neither calls a provider nor reads time.
