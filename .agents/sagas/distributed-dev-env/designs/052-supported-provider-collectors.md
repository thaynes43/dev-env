# DESIGN-052: Supported provider collectors

Status: source implementation, OP192, 2026-11-02. Persistent collection, provider
settings, publication routes and additional permissions remain unactivated.

The aggregate needs actual supported provider observations and a service lifetime
independent of a coding session. A task's runtime budget and an empty aggregate
slot do not establish recurring ingestion. Use each provider's supported contract;
never derive account allowance from per-request tokens, billing totals or a model
name. Same email across providers does not establish one entitlement.

## Claude passive callback

`agent-run usage collect claude-statusline --state-dir DIR --producer REF
--incarnation REF` consumes exactly one documented statusline callback on stdin.
The caller must explicitly create a private mode-0700 state directory. The command
never changes Claude settings, launches a provider or reads authentication/history.
It projects only `rate_limits.five_hour` and `rate_limits.seven_day`, retaining the
reported percentage and nullable reset seconds. Missing windows remain null. It
rejects malformed, duplicate, conflicting or oversized input before persistence.
All unrelated callback data, including transcript paths, is discarded in memory.

A locked, atomic mode-0600 file retains the latest two sanitized captures for that
one producer incarnation. Every callback gets a strictly increasing sequence and
its actual local reception time. It does not infer a new provider sample merely
because another callback arrived, even if the percentages changed. The provider's
observation clock remains null. A timer rerunning a callback is not a provider
refresh. Invalid input, conflicting source identity, stale clocks or unreadable
state preserve the previous artifact without refreshing it. Lock acquisition and
stdin are cancellable and bounded. No network or operator request is implied.

The artifact is a `providerallowance.captures.v1` bundle accepted by the existing
finite assembler. Producer identity is selection, not account authentication.
A separate reviewed trust configuration must establish any account binding.
Installing a callback for an isolated legitimate invocation requires that exact
invocation's qualification; it must not alter active user sessions or global
settings. Headless review execution is not assumed to invoke a UI statusline.

## Codex account and clocks

The official app-server supports `account/read` with `refreshToken:false` and
`account/rateLimits/read`. A successful request establishes locally received usage
and reset values; it does not establish an original provider observation time or
an independently refreshed sample. Never force refresh, log in, start a thread,
submit a model turn, redeem credits or use credentials owned by another process.
A personal ChatGPT account may have no workspace identifier. Source trust must
represent an explicitly qualified account-only scope rather than fabricate a
workspace or disable mismatch checks. Collection freshness and provider-data
freshness must remain distinct; strict capacity trust remains separately guarded.

## Cursor interface boundary

The authenticated CLI's plan metadata does not provide remaining quota or reset.
The supported dashboard/editor display needs its own actual authenticated
observation and exact entitlement family. An internal RPC descriptor or a public
per-agent token/cost SDK is not a qualified account quota interface. Do not extract
cookies, call private endpoints, infer shared xAI entitlement from Super alone,
or substitute missing windows with zero.

## Delivery

A complete activation packet must identify the durable collector owner, supported
transports, schedule/backoff, exact publisher and readers, runtime slot permissions,
private retention, freshness and unknown states, signed releases, rollback and
expiry behavior. Source/tests and finite samples are distinct from installed,
recurring API/agent/UI acceptance.

Sources: [Claude statusline](https://code.claude.com/docs/en/statusline),
[Codex app-server](https://learn.chatgpt.com/docs/app-server),
[Cursor usage limits](https://cursor.com/help/models-and-usage/usage-limits).

## Reviewed source contract changes

The explicit Codex collector is `usage collect codex -o json --executable
/absolute/official/codex --access-only-auth`. Its fixed official CLI child has a 15-second bound,
64-message/64-KiB per-message bounds, discarded stderr, cancellation that closes
pipes, and termination/reaping of its own process group. It initializes, reads
the personal account twice to establish an initial-notification barrier, reads
quota once, then verifies the account again. Every mid-bracket auth update,
credential-refresh request, changed identity/plan and conflicting same-Codex
legacy view refuses that capture. The preferred by-ID Codex bucket is supported
without a legacy bucket and when legacy names a different meter. No refresh
request is answered. The official client's ordinary existing authentication is
used; no direct auth-file read, login, alternate credential or forced refresh is
introduced. Non-forced reads alone do not guarantee the provider never attempts
automatic refresh. A rotating refresh-token HOME is forbidden: internal client
refresh may not emit a JSON-RPC request at all. The caller must independently
qualify an access-only HOME (for example the keeper codexauth projection) and
explicitly acknowledge it with `--access-only-auth`; missing/false/duplicate
acknowledgement refuses before launch. This declaration is not credential-file
inspection or proof of the projection. No second refresh-token owner is allowed.
An emitted refresh request is a failure, never permission to service it.

Discarded stderr above refers to the vendor child. The separate operator-owned
scheduled helper may emit only the bounded closed-code diagnostics specified in
DESIGN-053. Vendor output and error strings are never forwarded into that channel;
the explicit finite collector command's stdout contract is unchanged.

The sanitized `codex.app-server.account-capture.v1` preserves both nullable windows,
local received time, personal plan and a provider-email SHA-256 reference. Raw
identity and credits/upsell content are discarded. Independently selected
account rules may explicitly set `accountScopeOnly:true` with
`workspaceRef:null` for the qualified Codex RPC, Claude SDK or version-pinned
Cursor CLI display source. This selects provider-account scope when workspace
is genuinely unexposed, without asserting that no organization exists. It cannot
match another source/capability or a populated workspace. Legacy workspace-bound
records and mismatch checks remain unchanged. Unknown source scope, an unsupported Codex organization
plan, a populated invented workspace, wrong account/source/generation or duplicate
rules never become a verified account-only binding. The registry remains external
to the capture; a provider's hash cannot register itself as trusted.

Read items from the qualified Codex RPC, Claude SDK report and version-pinned
Cursor display may have optional `collectionFreshness`, with
`basis:capture_only`, calculated from original capture/receipt and the serving
clock. The separate existing `freshness` and `providerObservedAt` still describe
provider data. A recently checked reading can retain known percentage/reset and
be shown as last-reported data while strict current-capacity fields stay unknown.
Serving it again cannot refresh either clock. Later collection recency becomes
stale even when the provider supplied no data clock. Cached reads recompute this
label under caller trust and remove it on mismatch. Older documents without the
optional field retain their shape. Updated consumers must be installed before
serving the new optional field to closed older decoders.

`usage collect claude-report -o json --input FILE|- --received-at RFC3339` sanitizes one documented SDK
assistant `usage_report` already supplied by its legitimate owner. It does not
invoke `/usage` or start another Claude process. It discards assistant content,
session tokens/costs and extra-usage billing. Null limits and an empty list remain
distinct. Exact server order, kind, group, percent, nullable ISO reset, scope,
severity and active marker are preserved. Scoped rows receive independent IDs;
no display label establishes a model identity, a duration or shared entitlement.
Missing/null percentages and booleans, numeric strings, controls, duplicate keys,
invalid dates, error/aborted envelopes and session-only reports refuse. Passive
statusline capture remains separate and unactivated.

Official structured usage source: [Claude SDK usage report](https://code.claude.com/docs/en/agent-sdk/typescript#sdkusagereport).

Durable recurrence and its publisher identity are a separate integration: the
existing aggregate PUT is explicitly bound to a live AgentSession UID. An expired
coding owner cannot provide a durable collector identity. Do not present a
background task, a task runtime cap or periodic recapture of an old file as a
provider service. No new deployment, access, observer, source setting or schedule
is activated in this change.

The aggregate source's direct-field allowlist bound is 128, raised from eight to
retain scoped SDK row evidence (up to 16 rows with six metadata fields each)
without discarding scope or promoting display labels. The overall private trust
configuration still has its independent 48-KiB limit; 129 fields are refused.
Canonical case aliases of known source/control keys are refused, even when a
canonical spelling is also present. Retained numeric values are constructed from
exact-key fields so the decoder cannot overwrite an already validated value.

The unsalted provider-email hash is a private correlation reference, not anonymous
data or a secret-resistant identifier. Actual trust registries carrying it must
stay private, including operator configuration: never commit their values in
public GitOps arguments, manifests, fixtures or logs. Use an explicitly qualified
private configuration delivery mechanism before activation. This source creates
no secret or delivery permission. It preserves the provider's exact email spelling
rather than silently treating two differently spelled identities as equal; a
changed spelling must fail the selected account match and be requalified.

Retained SDK report input requires its original `--received-at` timestamp. The
sanitizer invocation/file mtime is never substituted. Future/invalid/missing or
duplicated receipt input refuses before IO; old data retains its actual age.

For deployment-owned private trust delivery, `--aggregate-allowance-config-file`
is an alternative to the inline flag, default empty. Disabled mode, API-off or
conflicting inline/file input refuses before opening the file. The file must be
absolute, regular, at most 64 KiB and have no world permission bits; group-readable
private projected files are supported. Normal Kubernetes projected-volume symlinks
are followed to one opened descriptor; a nonblocking open and regular-file check
refuse FIFOs/devices rather than waiting for input. The existing complete closed
selection parser remains unchanged. Errors never echo paths, content or operating
system messages. Startup does not create a Secret/ConfigMap, mount, permission,
collector or route without the same explicit enable and valid configuration.
Private config provisioning and portal's independent private trust delivery remain
explicit runtime preflight, never committed actual personal metadata.

Scoped/unknown SDK meter kinds are retained as advisory display evidence in this
global allowance contract. Only unscoped `weekly_all`/`session` kinds enter its
included measure category. Their server scope/kind/order remain present, but a
model-specific zero must never become account-wide remaining allowance.

The version-pinned `cursor.cli.usage-capture.v1` represents the normal installed
Usage screen and its local acquisition interval. Its renderer must match the
qualified version. Included percent is rounded display precision, not raw API
precision; zero can be a UI fallback and therefore normalizes to unknown while
its literal remains display evidence. Auto is an alias, and API's displayed zero
may be missing-data fallback: neither becomes another quota meter. The generic
Monthly caption does not set a duration. The normal renderer derives its
"Resets" month/day label from current-usage billingCycleEnd with plan-info
billingCycleEnd fallback, not included_usage_resets_at. Retain the literal in
`directFields.billingResetDisplayText` as vendor billing display evidence only.
The included measure has ResetUnknown and no reset text/date/instant, so API/UI
cannot present billing end as an allowance renewal or countdown. The label stays
without a year or exact instant. Capture time uses the original conservative
local acquisition start and receipt uses its actual end, never preparation time.
The interval is bounded to five minutes. This adapter does not launch a CLI or
assert recurring acquisition. Normal CLI acquisition and its auth/cache behavior
still need their explicit bounded source/runtime qualification; no private
factory, credential extraction, binary injection or browser login is introduced.

`accountScopeOnly` is the unreleased positive registry selector. It deliberately
names account scope rather than asserting that an unavailable workspace is not
applicable. It does not ignore any reported workspace: a non-null mismatch still
refuses, and account-only captures cannot resolve a shared entitlement through
the workspace-bound shared-pool rules. The deployment selection remains independent
of the provider payload and its actual account metadata must be qualified.
