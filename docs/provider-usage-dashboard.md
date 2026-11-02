# Provider usage dashboard

The Usage view draws one SVG bar chart per metric from typed fields on each
usage cell. The chart reads `ChartAmount`, `ChartUnit`, and `ChartMetric`. It
does not parse the display `Value`. An empty `ChartAmount` with a metric set is
unknown, not zero. A reported zero is the exact decimal `0`.

## Where the numbers come from

`sessionsPage` fills those fields from the session source.

- A session row copies `protocol.TokenObservation` and `protocol.CostObservation`.
  A reported token count, including zero, is the exact integer decimal. A
  reported USD estimate keeps the source decimal. `big.Rat.SetString` accepts
  that decimal. Unknown stays an empty amount and still names the metric.
- A breakdown row copies `usageTotal`. Token sums are `big.Int` decimals. A
  cost sum uses `big.Rat.RatString`: the exact rational `a/b`, or `a` when the
  denominator is 1. The renderer accepts that form with `big.Rat.SetString`.
  The table may still show a sum under one cent as `<0.01 USD`. The bar keeps
  the positive rational, so a tiny estimate is not drawn as zero.
- Cache read and cache create stay in the table. They are not chart metrics,
  and they are never added to input. Input follows the provider's own
  convention. The chart does not assume uniform cache inclusion across
  providers and does not add cache reads again.
- A sum that skipped one or more Unknown reports sets `ChartPartial`. The
  missing reports are not counted as zero. A conversation row whose result is
  Partial is also a lower bound, so its bar is partial. Conversation charts
  use only that table's token sums. They are not added to initial-invocation
  totals, and they have no cost.
- Bar width is relative to the largest reported amount in that same chart. It
  is not a quota, a cap, or an account allowance. Input, output, and estimated
  cost stay separate metrics. Tokens and USD are never mixed.
- Cost is a CLI-equivalent list-price estimate, not a charge.

The session list stays at its existing row cap. A breakdown stays at 20 groups,
with the rest merged into Other. Charts use those same rows. The charts are
rendered before the closed tables.

`ProductPage.AsOf` is the render clock. `sessionsPage` sets it from the read
clock passed in. Preview pages set an explicit synthetic as-of. A zero clock
draws no allowance countdown. Preview code does not call `time.Now`.

## Account allowance is not this chart

The default configuration selects no allowance reader or bound provider
observation. That path renders Claude, Codex, and Cursor as Not connected.
Optional readers are described below; source support does not establish a live
provider observation. Session tokens and cost are not turned into a quota.

`MapProductAllowance` can project a decoded `providerallowance.read.v1`
document when a caller already has one. A provider with no source stays
Unknown, with the reason "No percent allowance reported" and no windows. That
is the Cursor result for a document that only carries Claude or Codex. Not
connected is the unconfigured availability. Neither state invents a Cursor quota.

The optional cached reader is a separate lane. This chart path does not
collect, fetch, or inject allowance, and it does not claim a live allowance.

## Source wiring and remaining acceptance

The separate [selected Codex source adapter](provider-usage-current-source.md)
now sanitizes an explicitly supplied retained tool observation and projects it as
Current source. It preserves retrieval time and labels remaining as a derived
complement. Its local CLI and projector do not activate a production transport
or establish a stable account identity. The strict bound reader below remains
unchanged.

The [aggregate transport](provider-allowance-aggregate.md) adds an optional
operator slot, explicit CLI publication/read and portal assembly selected by
`config/provider-allowances.json`. The selected Codex consumer uses its separate
configuration and slot; both consumers cannot be selected together. Neither
configuration is enabled by its source implementation. This work does not
establish a current provider account or activate collection.

A configured retained observation reader needs explicitly owned trusted
configuration identifying the selected producer and source,
its incarnation and binding generation, selected current account scope, and the
invalidation rule on source/account change. AccountRef is an opaque configured
reference, not necessarily a provider account ID. A selected current source is
weaker evidence than a verified stable account identity; the UI must retain that
distinction instead of merging reports across accounts.

A supported meter that omits provider updatedAt needs an explicit source-specific
freshness policy distinguishing retrieval time from provider update time. Retrieval
must not be labelled provider update time. Likewise an absent account ID must stay
absent; a configured current-source selection must not silently become a verified
provider identity. The present contract/validation needs a reviewed representation
of those semantics before an unattributed report can become a bound allowance.

Use a bounded sanitized retained document, a fixed maximum age, and the existing
providerallowance.read.v1 validator and account registry. The optional portal
consumer reads only its declared operator slot, without provider calls or
per-request commands. Reads must degrade stale current measures and invalidate
changed bindings while preserving the original reported observation and times.
Real upstream capture, reviewed runtime configuration and actual API/native/UI
acceptance remain separate from the synthetic transport fixtures.

The source assembly and its tests do not activate runtime configuration.
Existing authorized metadata may support source qualification. A new private
provider probe, credential, read boundary,
or held observer activation is a separate exact decision; this document grants
none. There is no evidence that a new credential or human-only step is inherently
required merely because a meter lacks an account ID. Raw observations and fixture
coverage are not live acceptance.
