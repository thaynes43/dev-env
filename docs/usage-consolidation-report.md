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
capture support. The exporter validates the returned name and UID. An invalid direct
observation or one whose source UID does not match stops the selection before capture
normalization or any later GET; that observation is never projected. A missing direct
observation is a coverage gap, not an error or zero usage. Otherwise the exporter
uses the existing capture normalizer before calling `Project`.

The command never lists or discovers Sessions, scans descendants, or reads
history, logs, consoles, provider data, credentials, or an auth store. A
refusal, mismatch, or malformed response stops subsequent reads and writes only
safe selection/coverage metadata; raw API errors and Sessions cannot enter the
report. It accepts only supplied, strictly decoded existing
`providerallowance.read.v1` snapshot claims and never polls a provider. Local DTO
validation is not an independent source, binding, validation, or authorization
attestation. Missing input is explicitly `Unconfigured`, not zero or a currentness
claim; report currentness is `Unknown`.

The report and concise text summary are private local files in a newly created
0700 directory, both 0600. The summary preserves each projected row's requested and
actual model state, served-model observations, time, and Unknown-versus-zero token
counters. It labels costs only as CLI-equivalent estimates, never charges, and emits
no totals. They are bounded evidence only, not a public artifact or live usage
monitor.

## Portal Sessions view (U1)

The portal's Sessions page shows each dev-env session's mode, agent state, last
activity, requested model, provider-reported served models, the four
initial-invocation token counters and the CLI-equivalent cost estimate. It reads
them from the operator's opt-in `GET /v1/sessions?view=activity`
([portal read boundary](portal-read-boundary.md)). This is a per-session view of
the same validated observation `agent-run show` prints, not this projector's
report. It covers task sessions' initial managed invocations only; interactive
and remote sessions, later turns and account allowance stay `Unknown`. It emits
no totals, does not add cache read tokens to input tokens, and labels cost as an
estimate, not a charge.

## Codex native on-demand input (C3 source qualification)

The source contains an unwired helper for one metadata-only read on an already
qualified same-UID managed-host socket. It initializes the connection, calls
`account/read` with `refreshToken: false`, then `account/rateLimits/read` with
`excludeResetCreditDetails: true` and `supportsLunaReserve: false`. It does not
start a host, read authentication files, call a provider HTTP endpoint, or
perform a thread, turn, login, refresh, reset, redemption or model operation.
There is no production call site or collector; `usage-export --allowance-read`
continues to consume caller-supplied JSON and does not invoke this helper.

The caller supplies bounded capture identifiers and an evidence reference;
these are assertions, not trusted provenance or account binding. The source
version records the app server's reported version after matching the pinned
protocol version, not independently verified CLI identity. Its new label is
not automatically actionable in the existing registry. Provider-observed time,
freshness and account binding remain Unknown. Legacy and keyed bucket views
remain separate; this helper does not infer account allowance, reset, cost,
task attribution or permission. Synthetic tests do not establish a live read.
