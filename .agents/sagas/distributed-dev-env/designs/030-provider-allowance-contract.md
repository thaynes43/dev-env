# DESIGN-030: Provider allowance contract

Date: 2026-10-24. Story189 Packet A under Epic187. This is the source contract for a
separate account-allowance observation and cached read. It does not install a
collector, adapter, command, or deployment.

Reviewed inputs remain in the Story189 order. The package testdata copies the
observation schema, read schema, nine observation examples, read-interface
examples, and seventeen transition fixtures. Their SHA-256 values are the
reviewed hashes. Native188 stays open.

## Slice

`internal/providerallowance` decodes two closed documents:

- `providerallowance.v1`, an immutable observation
- `providerallowance.read.v1`, a cached read envelope

The read envelope is the same document a later `agent-run usage -o json` would
print. `-o json` is the existing agent-run flag. This package does not register
a command, assign a new process exit, or open a socket. A cached read that
reports an unavailable provider is still a successful document, which the
fixture records as HTTP 200 and exit 0. The HTTP codes in the fixture
(`UNAUTHENTICATED`, `ACCOUNT_SCOPE_DENIED`, `INVALID_SCOPED_CURSOR`,
`READ_RATE_LIMITED`, `COLLECTOR_STORAGE_UNAVAILABLE`) are labels for a later
executable surface.

Three layers stay distinct:

1. Allowlisted direct evidence, including exact display strings and reset-credit inventory.
2. Source-semantic measures. A caller-supplied registry decides whether a validation reference matches a source, version, field, unit, window, and range.
3. Derivations. A complete value is returned only after the inputs, scope, and formula agree.

`Unknown`, reported `0`, `not_reported`, `not_applicable`, and `uncapped` stay
distinct. Decimals stay lexical strings. `0.36` is not rewritten as `36`.
A source rule that explicitly allows over-limit percent keeps `101.25`.
This package does not clamp that value and does not emit a negative remaining
complement.

Task `UsageVersion2`, Story109, and descendant token rollups are unchanged.
Account allowance is not derived from task tokens or CLI-equivalent cost.

## Trust

`ValidateObservation(observation, trustedContext, registry)` is pure. The
registry is an injected value, used as a test double in this slice. It is not
a service, loader, or permission map. The observation's `validationRef`,
`bindingState`, `producerRef`, and `evidenceRefs` do not confer trust.

A match is exact on source, version, producer, generation, account, workspace,
window, and unit where the rule names them. A missing registry, an unknown
source version, a missing range, a forged verified binding, or a mismatched
generation refuses actionable normalization. Historical Codex field semantics
can be evidenced while account binding and provider time stay unknown. That
evidence is not a current entitlement. Read-document validation, binding, and
freshness are data on the cached read. They are not trusted inputs.
`ValidateObservation` does not read a clock, so a passed reset stays a fact on
the read document: current value `unknown` / `expired`, historical decimal
unchanged. Replaying or decoding the document does not freshen it.

Cursor display fields stay strings. This package does not invent a utilization
direction, a weekly pool, a reset instant, a currency code, or a served model
from a selected model name. Claude's two windows stay `unknown` when the
observation says so. Reset-credit count `0` with details `unknown` is direct
inventory. It is not a rate-limit budget, an account block, or a capacity grant.

Confirmed derivations use `math/big.Rat` on the decimal strings. Complement
runs only when the metric rule names a bound, the input is a known complete
used percent, the trusted binding matches, and the result is nonnegative.
`sum_compatible_values` refuses mixed windows, units, currencies, model scopes,
alias pairs, and partial inputs. Verified shared-pool aliases count once.
An unverified relationship does not invent a pool id. Neither helper stores
or reduces a series of observations.

JSON decode rejects a second key, including a Unicode-escaped spelling of the
same key, unknown fields, boolean-for-integer values, fractional integers,
malformed date-times, and documents outside caller-supplied limits.
Property names are compared with the canonical field list before typed decode,
at every observation and read object. A second spelling that matches only
under case folding, including a Unicode fold such as long s, is rejected in
either input order. String and array values are not folded.
`FixtureParseLimits` is a finite bound for these fixtures. It is not a
production SLO or a shared resource policy. Missing provider time stays
missing. A JSON null stays distinct from a missing required key. A null is
also distinct from a value: required strings, objects, and arrays, including
string-array elements, reject null before typed decode can turn it into an
empty string or a nil slice. Nullable fields stay nullable, and a valid empty
array stays an empty array. A nil Go slice for a required array serializes as
`[]`. Decode does not call a clock or a provider. Summing two or more members
of one alias group is refused, including a pair inside a larger sum, a subset
of a larger group, and members joined by overlapping groups. Duplicate,
repeated, or overlapping projection rules are registry defects and are not
counted. Two or more present members of one valid group count as one
projection when another registered member is absent. One present member
remains a scalar. `AllowOverLimit` permits a missing upper bound only. Reset-credit
inventory stays metadata for every allowance category and cannot feed a
derivation. Explicit `conflict` availability keeps the raw value and semantic
diagnostics and suppresses actionable, complete, and derived advice. A shared
pool counts only products named by caller-trusted entitlement bindings for
that account, workspace, generation, and producer. One observation cannot
supply both aliases. A date-time is an RFC 3339 spelling: four-digit date,
two ASCII digits for each clock and offset component, `T` or `t`, an optional
dot and one or more fractional digits, and `Z`/`z` or a signed offset from
`00:00` through `23:59`. Second 60 is accepted only at UTC 23:59. The original
characters stay on the value. Go's extra one-digit hour, comma fraction, and
hour-24 or minute-60 offsets are not accepted.

`ValidateObservation` applies those schema, enum, date-time, count, and
decimal checks to a Go value as well as to a decoded document. An unsupported
schema or other shape failure is not actionable. An allowlist miss,
reset-credit misuse, display-only promotion, or explicit availability
conflict also clears `Actionable` on every measure. `SemanticsEvidenced` can
still say that a source rule matched,
including historical Codex fields and the known meters in a partial
observation. Unknown meters stay unknown.

## Schema tests

Schema tests use `github.com/santhosh-tekuri/jsonschema/v6` v6.0.3
(`go.sum` hash `h1:1EYB5IzjZawrrnELUi78f9fPu57HuXjmddZPjrls/28=`) because it
implements JSON Schema Draft 2020-12, asserts `date-time` when
`AssertFormat` is called, and embeds its metaschema. Its non-test code
depends on `golang.org/x/text`. The test compiler refuses URL and file loads.
Non-test code does not import it.
A schema pass does not prove the trusted-registry checks. Those tests are
separate. The seventeen transition fixtures remain Packet B1 specifications.

## Still open

Native188 Cursor weekly/shared-pool/reset/source precision/parity and Claude
availability remain Unknown. Packets B–F stay held. Required CI and independent
native review apply to the PR head. This design does not accept Epic187 or a
first-version release.

### Local unified usage read seam (2026-10-28)

`agent-run usage -o json` performs an actual local acquisition preflight and reports three unconfigured source refusals. The attempt timestamps identify that local check, not a provider request or observation; last good, account binding and freshness remain Unknown. `--from-file FILE` (or explicit `-` for stdin) reads an untrusted bounded canonical cached read and preserves its original times and fields. It grants no trust or live acquisition. Cursor/Grok needs a supported subscription quota source and approved scope; Claude needs a passive producer ingress; Codex capture remains separately held. This source seam does not establish three-provider coverage.

### Claude passive callback source (2026-10-28)

`agent-run usage -o json --claude-statusline` explicitly reads one bounded documented statusline callback from stdin. Only optional five-hour and seven-day used percentages and Unix-second reset instants are projected. Missing or null fields are Unknown, historical zero remains zero, and a passed reset does not establish replenishment. Callback capture time is local, not a provider observation; freshness, current amounts and account binding remain Unknown. Unrelated fields, including paths and cost, are discarded. The source version is Unknown and current model is not served-model telemetry. This input bridge installs no hook and does not establish live provider coverage; an authorized producer ingress remains a separate dependency.

### Opt-in statusline producer bridge (2026-10-29)

`images/agent/tools/claude-statusline-usage.py` is an install-ready opt-in bridge. The agent image copies it to `/usr/local/bin/claude-statusline-usage` and marks it executable. The image, `agentd Render`, settings, environment, and startup do not invoke it.

The helper reads one bounded native callback from stdin. The caller must pass an absolute `--output` path. That parent directory must already exist, be owned by the caller, and grant no group or other access. An existing destination must be a regular file owned by the caller, owner-writable, and closed to group and other access. Symlinks are refused. There is no default path and no raw callback file.

Only `rate_limits.five_hour` and `rate_limits.seven_day`, and only `used_percentage` and `resets_at` inside them, are forwarded. Other fields are discarded. JSON numbers are ASCII digits `0` through `9`. A non-ASCII digit fails closed before the consumer runs. Exact duplicate keys, over-bound documents, and case-folded aliases of those recognized names fail closed with the consumer. The helper does not reimplement percentage, reset, or schema rules. It runs only `/usr/local/bin/agent-run usage -o json --claude-statusline` and, only after success, atomically stores that command's normalized read. It does not search for another `agent-run`. Refusal or consumer failure leaves the previous file in place and prints only `claude-statusline-usage: capture unavailable`.

Without `--display`, capture reads at most one byte past the bound and does not wait for more input. `--display CMD ARG...` streams the original callback to that argv with no shell and with bounded memory. Crossing the capture bound disables capture and leaves the previous file in place; the display still receives the original bytes. Display stdout, stderr, and exit status stay in place when capture fails. A source-only example, not applied to a live HOME, is `claude-statusline-usage --output /path/to/private/read.json --display -- /path/to/existing-statusline`.

Callback receipt time is the consumer's local capture time. It is not provider observation time. Freshness, account binding, and current usable amounts stay Unknown. A passed reset is not replenishment. Later reads of the saved file use `agent-run usage -o json --from-file PATH` and keep the original observation, received, and as-of times. Replaying the cache through `--claude-statusline` is not a refresh. The helper does not poll a provider, open a credential, or follow a path named by the callback. It does not establish live coverage.

### Selected Codex current-source report (2026-11-01, OP323 source only)

The [selected-source adapter](../../../../docs/provider-usage-current-source.md)
adds a separate closed document, `codex.get_usage_limits.current_source.v1`.
It does not extend `Observation` or `ReadItem` and does not relax
`ValidateObservation`, account binding, or provider freshness. The original
Codex capture and private-observer holds remain. Unbound evidence remains
unverified as a current entitlement.

`agent-run usage-source` sanitizes only an explicitly supplied retained task
observation of the supported Codex tool. Local formatting makes no provider,
operator, credential-store or history request. Raw nested input requires an
explicit original `--observed-at`; legacy flattened input remains supported.
Optional `--publish` uses the existing authenticated operator client for one
fixed-slot PUT of canonical JSON without a newline. Redirects and blind retries
are refused. The server retains all live identity, configuration and store
fences. This does not activate the route or install acquisition or recurrence.
The native source reports used percentage,
duration in minutes and an exact reset. Remaining is its exact decimal
complement, marked `derived_complement_of_used_percent`; a supplied complement
or provenance marker must agree. The native `windowDurationMins` name and the
handoff's normalized `windowDurationMinutes` must agree if both are supplied.
Local automation ceilings, points and unknown banked credits are discarded.

The separate pure `CodexRawToolReport` bridge accepts the official nested
tool envelope and a caller-supplied original observation time. It requires
`accountId: null`, one primary Codex window, and agreement between preferred
and legacy copies. It refuses secondary windows and unsupported fields;
plan and credit metadata are compared for conflicts and discarded. Unix
reset seconds become UTC RFC3339, then the existing flattened builder
validates the report. Parsing establishes no source authentication or account
binding. The CLI uses this bridge only for explicit raw input with its original
observation time; flattened input remains compatible. Neither input mode nor
optional publication installs acquisition, recurrence or runtime access.

The caller selects one publisher reference, producer incarnation, source
generation and retrieval age limit. These are opaque caller-supplied source
selection records, not provider account IDs or proof of origin. Stable account
identity remains unavailable. The original retrieval time is preserved;
provider update time remains null for this sanitizer. Neither preparing nor
replaying the report refreshes it. The proposed consumer policy is 30 minutes,
not a provider freshness guarantee. This selected transport enforces that
limit: `maxAgeSeconds` must be positive and at most 1800, and a shorter
positive age is allowed. The generic `CurrentSourceSelection` validator's
24 hour ceiling remains unchanged. Future retrieval, age expiry, selection
changes and a passed reset invalidate the current meter without inventing
renewed capacity. An unobservable account change remains a coverage limitation.

The portal's separate constructor projects a valid selected report as
`Current source`, never `Measured`, and does not combine it with bound
entitlements. Both compact and detailed views label derived remaining and
separate retrieval from provider observation time. This is a selected report
with a limited retrieval age, not an accepted account allowance. Production
assembly leaves the connection nil unless `config/current-source.json` is
present. An absent file performs no read. A present file copies one immutable
selection onto the existing operator client and does not assign
`SessionSource.allowance`.

The fixed-slot handler and store exist in source. The central server registers
the exact route only for an explicitly enabled, fully configured handler;
the deployed process leaves the handler nil. The operator entrypoint can
construct that handler only from `--enable-provider-source-reports` and an
exact `--provider-source-config` document. Both default off, and neither is
set in a deployment. Maximum age alone does not enable the route. A refused
setting returns `provider source configuration was refused` and does not
echo the submitted text. The flags do not enable the API or the portal
reader. Portal GET still requires the existing portal reader. The route,
ConfigMap `dev-env-system/dev-env-provider-source-codex-current`, key
`report.json`, publisher session UID `84ddc461-fd07-47ab-a2c1-72910898bac4`,
ServiceAccount `dev-env-workbench`, portal ServiceAccount
`dev-env-v2-portal`, incarnation `codex-retained-tool-source-1`, and
generation 1 are fixed. `maxAgeSeconds` must be an integer from 1 through
1800. The exact document is recorded in the
[current-source guide](../../../../docs/provider-usage-current-source.md).
Portal admission adds only that enabled
GET, never PUT or other paths. The store updates an existing ConfigMap only, preserving
metadata through live resourceVersion CAS; it never creates the object.
This selected source refuses non-null `providerUpdatedAt`. Undecodable stored
bytes block later writes. Invalid or expired reads suppress current values
without changing stored retrieval time, and replay does not refresh them.
These source rules do not install the object, grant access, or enable a route.
The portal consumer reads the fixed slot only after the current Authentik
active and admin check, at most once per authorized Sessions request, and only
when the optional config file is present. That selected path rejects a
non-null `providerUpdatedAt` even though the generic reader still accepts one
that `ValidateCurrentSource` accepts. Failure, staleness, redirect, oversize,
and a passed reset stay on the allowance panel as Unknown and leave the
session inventory in place. A bound report is not merged into the selected
projection. No quota is inferred from session tokens.

The linked document names the local publisher command, private retained
artifact, fixed operator slot, consumer configuration, retention and access
review boundary. The consumer code stays default-off. No portal ACL,
credential, probe, collector, periodic acquisition, ConfigMap, Role, or flag
is activated. The existing immutable PublishedContext contract remains
unchanged. Synthetic portal fixtures prove the connection in source only.
Installed ingestion and account entitlement acceptance remain separate.


## OP192 aggregate continuation — 2026-11-02

This is the existing OP187 allowance deliverable across Cursor, Claude and Codex.
OP190 owns collector/native Codex acquisition, OP191 passive Claude, OP192
Cursor/Grok, OP193 private-adapter permission, OP315 retained invocation totals,
and OP323 the consumer integration. This adds no work queue or replacement
accounting framework. Source, tests, publication, installation and usable
acceptance remain separate.

The common document is `providerallowance.read.v1`. The disabled aggregate cached
GET and explicit agent read use that envelope. The fixed
`/v1/provider-source-reports/codex-current-source` slot keeps its Codex-only
selection and security boundary. An aggregate read must not route Cursor data
through that slot. `agent-run usage -o json --read-current` is the explicit remote read; default
usage stays a local preflight. No runtime configuration, account registry, provider
credential, permission, observer or scheduled collection is enabled here.

| Provider | Source and cadence | API, agent and UI evidence | Freshness and live acceptance |
| --- | --- | --- | --- |
| Cursor, including Grok through Cursor | One relayed authenticated native Plan & Usage capture. The new sanitized display adapter preserves included percent, plan/window label, account-binding uncertainty and exact yearless reset text. Recurring native acquisition is not qualified. | Adapter target tests pass locally. Aggregate TLS API/agent and cached-UI projection tests pass; the collection receipt-time freshness correction is still a source gate. Existing cached mapper requires independently trusted binding/freshness; actual unverified data stays Unknown. | Provider observation time and exact reset time/timezone are Unknown. Two refreshed native observations have not been verified. OP192 remains open. |
| Codex | Existing owned native capture and retained-export source exists. Observer execution and automatic ingestion remain held. No production cadence is established. | The selected Codex report API and explicit `usage-source --read-current` exist. The OP323 peer reports signed 2.85.0 operator/portal installation; that does not enable the quota slot or prove the aggregate path. | Selected report retrieval time is not provider update time; account binding remains Unknown. OP190/225 and OP323 acceptance stay separate. |
| Claude | Existing passive documented statusline decoder. Callback coverage cannot establish idle-account freshness. No new model call for quota polling. | Existing `usage --claude-statusline -o json` projects independent five-hour/seven-day windows when reported. Retained same-child result pickup and observer activation remain separate. | Callback capture time cannot establish provider freshness or account binding. OP191 remains open. |
| Direct xAI | No qualified authenticated current-account quota source identified. Cursor entitlement and local token receipts cannot substitute. | Unknown; no credential/private API integration or account probe added. | Unknown. |

OP315 retained invocation token/cost accounting is accepted with explicit ancestry
coverage limits. It supplies fine-grained attribution charts, not an allowance
denominator, remaining quota, reset, balance or proof of provider freshness.
Percentages across providers are never added. Cursor/Grok aliases require a
caller-trusted shared entitlement and never merge different accounts or windows.

The bounded collection source is opt-in and receives its clock, wait, source
selection and acquisition interface from its caller. It must retain original
capture/provider/receipt times on replay, preserve last-good history separately
from a failed attempt, redact callback errors, invalidate changed account
generations and degrade expired or untrusted current values. The deadline/cancellation and concurrent-read corrections pass the full package
and race suite. A later review reproduced future-at-receipt data becoming
current only because the serving clock advanced. The correction now compares
provider/capture timestamps against the original receipt and keeps them unusable
on later reads/replays; a distinct valid acquisition can recover. The full
allowance race suite passes after that correction. Independent review and
required CI remain publication gates for this aggregate slice.

Native capability preflight: the documented Cursor `about --format json` command
completed with no raw output retained; safe metadata found eleven unrecognized
fields and no recognized quota/account fields. That is an inconclusive schema
observation, not proof that the command cannot supply data. The public
[CLI parameters](https://cursor.com/docs/cli/reference/parameters) describe
`status` and `about` as account/authentication metadata, without documenting an
individual weekly-quota command on that page. The
[team Admin API](https://cursor.com/docs/account/teams/admin-api) requires team API
key authentication. Individual-plan access has not been established. Native Mac
accessibility refresh returned a tool availability/lock error; this does not prove
an operating-system lock or authorize unlocking, login changes or a private API.

A single read-only request using the locally built explicit agent command and
the existing managed Session identity returned NotFound; the live aggregate
route remains unavailable. No retry, provider request or runtime write followed.
The existing portal admission switch also refuses this path, and production
assembly does not set the optional source.

An activation request is not fully preflighted yet. The supported recurring
source, account binding, two fresh observations, runtime assembly and exact
permission/configuration inputs must be concrete first. Existing Codex-only
activation materials cannot stand in for the original aggregate deliverable.
OP193 private-adapter, observer and ingestion-security holds remain in force.
No new credential, grant, network/security change, session restart, reset
redemption or capacity purchase follows from this source work.

The current [official usage help](https://prod.cursor.com/help/models-and-usage/usage-limits)
identifies Super as an X Subscription with a weekly Cursor/Grok pool and directs
users to Usage at `https://grok.com/dashboard`. That is a supported display path;
it does not establish a compatible individual programmatic quota API or native
authentication selector. Direct xAI API usage and legacy Cursor monthly meters
must stay separate. No private call follows from this documentation.

**Grok source plan, 2026-11-05.** [R-07](../research/R-07-op192-grok-usage-source-plan.md)
keeps Grok as a separate row whose relationship to Cursor is typed Unknown,
assesses the public and first-party sources, and lists the decisions (D0 to D5)
a Grok source needs. It adds synthetic contract tests only; acquisition is
unchanged. Dated rulings on D0 to D5 are recorded in this section, with R-07
pointing here.

The typed reset compatibility change adds optional `Window.resetDisplayDate`
and `Window.resetDisplayText`. A date is an exact calendar date; text is the
original English month/day label without an inferred year. They are mutually
exclusive, require `display_only` precision and a null `resetAt`, and never
become a reset instant. The portal renders that exact typed label with a
display-only suffix. Different labels cannot be combined as one derivation
window. Absent fields preserve legacy JSON bytes and fingerprints; existing
documents still decode. Older strict consumers must be upgraded before they
receive the new fields. This compatibility change supplies no acquisition,
account or entitlement trust, provider timestamp, or recurring-ingestion proof.

### OP192 S1: typed Grok row, release R1 — 2026-11-05 (source only)

[Research R-07](../research/R-07-op192-grok-usage-source-plan.md) (OP192 Grok usage source plan) keeps Grok a separate row whose
relationship to Cursor is Unknown. This source change is release R1 of its S1
step. No producer emits a Grok row yet, and no collector, acquisition,
configuration, credential or network change is included.

- The operator store accepts an optional `grok` status that is only
  `unconfigured`, with no last-good record, no current freshness and no
  verified binding, and never a `grok` item. It accepts a three-row document
  after a four-row one and the reverse.
- The served empty-slot document lists Grok as Not connected.
- The portal shows a fixed Grok row after Claude, Codex and Cursor. It is Not
  connected when the Grok status is unconfigured or missing (a stored three-row
  document), and otherwise Unknown with a fixed reason. It never shows a
  percent, window or last-reported value, and Grok data never changes another
  row. The Cursor and Grok account sections state: "Grok used in Cursor counts
  toward Cursor's plan usage. Whether Cursor and Grok share one pool is
  unknown."
- `agent-run usage -o json` prints a four-row document unchanged and invents no
  Grok row. Its local preflight and `Collection.build` still emit exactly the
  three required rows.

**Compatibility and rollback rule.** Two producers reach the store: the
operator's collector (through `AssembleCaptureBundle` and `Collection.build`)
and `agent-run` in the separately pinned agent image (`usage capture`, and
`usage --publish-file` where the slot accepts external publication). The store
checks the stored document as well as the new one (`preserveAllowance` calls
`indexAllowanceStatuses` on both), so an operator older than R1 refuses every
update once a four-row document is stored. Order: install R1 and verify it is
serving; only then ship R2, the producers that emit the Grok row (the operator
collector change and an agent image whose `agent-run` emits it). The agent image
pin must not move ahead of R1. To roll back, return every four-row producer to
three rows first, let a three-row document replace the stored one, confirm it by
a read, and only then move the operator below R1. A pre-R1 operator that meets a
stored four-row document fails closed: publication stops and the stored values
age to stale until an R1-or-later operator is restored.

### Configured aggregate assembly — 2026-11-02 (source only)

The [aggregate transport contract](../../../../docs/provider-allowance-aggregate.md)
connects a bounded collection snapshot to one existing ConfigMap slot through
the operator, explicit CLI publication/read and an optional portal consumer.
Both operator and portal assembly default off. Trust comes from caller-owned
configuration rather than the report. The serving clock may degrade current
values but cannot refresh original capture/provider/receipt times. Replay does
not write; invalid stored data is not repaired implicitly. The selected Codex
slot remains separate.

This closes a source assembly gap. The synthetic roundtrip, focused/race suites,
independent review and required CI qualify source only. Real upstream coverage,
account binding, recurring ingestion and native/UI acceptance remain open under
their existing authorization and holds. No runtime flags, ConfigMap, grants,
observer, private adapter, worker image or Session are activated by this change.
