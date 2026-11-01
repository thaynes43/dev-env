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
  convention. Claude reports cache reads apart from input. Codex input already
  includes them. The chart says that, and it does not add cache reads again.
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

Production has no allowance reader and no bound provider observation. The live
Usage page shows Claude, Codex, and Cursor as Not connected. Session tokens and
cost are not turned into a percent or a quota.

`MapProductAllowance` can project a decoded `providerallowance.read.v1`
document when a caller already has one. A provider with no source stays
Unknown, with the reason "No percent allowance reported" and no windows. That
is the Cursor result for a document that only carries Claude or Codex. Not
connected is the unconfigured availability, which is what the live page shows
today because it does not call the mapper. Neither state invents a Cursor quota.

The optional cached reader is a separate lane. This chart path does not
collect, fetch, or inject allowance, and it does not claim a live allowance.
