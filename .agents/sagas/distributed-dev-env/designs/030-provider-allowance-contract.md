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
missing. A JSON null stays distinct from a missing required key. Decode does
not call a clock or a provider.

`ValidateObservation` applies those schema, enum, date-time, count, and
decimal checks to a Go value as well as to a decoded document. An unsupported
schema or other shape failure is not actionable. An allowlist miss,
reset-credit misuse, or display-only promotion also clears `Actionable` on
every measure. `SemanticsEvidenced` can still say that a source rule matched,
including historical Codex fields and the known meters in a partial
observation. Unknown meters stay unknown.

## Schema tests

Schema tests use `github.com/santhosh-tekuri/jsonschema/v5` v5.3.1
(`go.sum` hash `h1:lZUw3E0/J3roVtGQ+SCrUrg3ON6NgVqpn3+iol9aGu4=`) because it
implements JSON Schema Draft 2020-12, asserts `date-time` when
`AssertFormat` is set, embeds its metaschema, and has no module dependencies.
The test compiler refuses URL and file loads. Non-test code does not import it.
A schema pass does not prove the trusted-registry checks. Those tests are
separate. The seventeen transition fixtures remain Packet B1 specifications.

## Still open

Native188 Cursor weekly/shared-pool/reset/source precision/parity and Claude
availability remain Unknown. Packets B–F stay held. Required CI and independent
native review apply to the PR head. This design does not accept Epic187 or a
first-version release.
