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
observation of the supported Codex tool. It makes no provider, operator,
credential-store or history request. The native source reports used percentage,
duration in minutes and an exact reset. Remaining is its exact decimal
complement, marked `derived_complement_of_used_percent`; a supplied complement
or provenance marker must agree. The native `windowDurationMins` name and the
handoff's normalized `windowDurationMinutes` must agree if both are supplied.
Local automation ceilings, points and unknown banked credits are discarded.

The caller selects one publisher reference, producer incarnation, source
generation and retrieval age limit. These are opaque caller-supplied source
selection records, not provider account IDs or proof of origin. Stable account
identity remains unavailable. The original retrieval time is preserved;
provider update time remains null for this sanitizer. Neither preparing nor
replaying the report refreshes it. The proposed consumer policy is 30 minutes,
not a provider freshness guarantee. Future retrieval, age expiry, selection
changes and a passed reset invalidate the current meter without inventing
renewed capacity. An unobservable account change remains a coverage limitation.

The portal's separate constructor projects a valid selected report as
`Current source`, never `Measured`, and does not combine it with bound
entitlements. Both compact and detailed views label derived remaining and
separate retrieval from provider observation time. This is a selected report
with a limited retrieval age, not an accepted account allowance. Production
assembly does not inject it; missing source configuration remains disconnected.

The linked document names the local publisher command, private retained
artifact, fixed proposed operator slot, consumer configuration, retention and
access review boundary. No new transport, portal ACL, credential, probe,
collector or periodic acquisition is activated. The existing immutable
PublishedContext contract remains unchanged. Synthetic sanitizer-to-renderer
proof establishes source behavior only; authorized retained transport,
installed ingestion and account entitlement acceptance remain separate.
