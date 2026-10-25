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
valid payload for that identity, including one with a different observation ID,
suppresses its entire ambiguous group. The deterministic `withheldConflicts`
metadata retains that group's session UID, attempt ID, and the fixed conflict
reason without retaining either payload or emitting totals. Therefore an
all-conflicting input is distinguishable from no input. Input ordering does not
change rows or conflict metadata. Different explicit attempts remain separate
rows.

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

## Private on-demand export (C2)

`agent-run usage-export` is the deliberately narrow private caller of this
projector. Its strict local manifest names a finite set of Session name/expected
UID bindings; optional project, repository, and task labels remain caller
metadata and apply only to their exact UID. Each named Session read is one
no-redirect, no-retry GET using existing provider-v1 attribution and direct-v1
capture support. The exporter validates the returned name and UID, validates a
direct observation's source UID, and uses the existing capture normalizer before
calling `Project`.

The command never lists or discovers Sessions, scans descendants, or reads
history, logs, consoles, provider data, credentials, or an auth store. A
refusal, mismatch, or malformed response stops subsequent reads and writes only
safe selection/coverage metadata; raw API errors and Sessions cannot enter the
report. It accepts only supplied, strictly decoded existing
`providerallowance.read.v1` snapshots and never polls a provider. Missing input
is explicitly `Unconfigured`, not zero or a currentness claim.

The report and concise text summary are private local files in a newly created
0700 directory, both 0600. They are bounded evidence only, not a public artifact
or live usage monitor.
