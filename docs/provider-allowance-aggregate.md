# Aggregate allowance transport

This source connects the bounded collection's `providerallowance.read.v1`
snapshot to one operator slot, an explicit CLI publisher/read, and an optional
portal reader. It does not acquire provider data or enable recurring ingestion.
Source publication, installed configuration and real provider acceptance remain
separate evidence.

## Operator selection

Both `--enable-aggregate-allowance-reports` and
`--aggregate-allowance-config=<inline JSON>` are required. Without either, the
route stays off. A partial or malformed selection refuses startup without
echoing its contents. The API must be enabled and the Session namespace must be
`dev-agents`. The selection has exactly these fields:

| Field | Meaning |
| --- | --- |
| `slot`, `namespace`, `name` | Exactly `provider-allowances`, `dev-env-system`, `dev-env-provider-allowances` |
| `publisherSessionUID`, `publisherServiceAccount` | One selected live publisher; its current owned Pod and identity are checked |
| `readerServiceAccount`, `readerSessionUIDs` | One selected account and one through eight distinct Session UIDs |
| `portalNamespace`, `portalServiceAccount` | Existing `dev-agents/dev-env-v2-portal` reader identity |
| `maxAgeSeconds` | A positive bound of at most 1800 seconds; it does not enable the route |
| `contexts`, `registry` | Caller-owned provider/source/account binding and normalization rules, parsed by `ParseAggregateTrust` |

Trust is configured independently of the published report. A payload cannot
verify its own account, source, measures or freshness. Providers with no stored
observation remain explicit gaps. After an observation has been stored, retain
that history when acquisition or trust becomes unavailable, as described below. The separate Codex selected-source slot is
unchanged and is not used as an aggregate fallback.

A report carries exactly one status each for `cursor`, `anthropic` and
`openai`. It may also carry one optional `grok` status (OP192 S1): only
`unconfigured`, with no last-good record, no current freshness, no verified
binding and no `grok` item. Anything else for Grok is refused. A three-row
report may replace a stored four-row one and the reverse, so producers can
move between them in either direction. The served empty-slot document already
lists Grok as Not connected; no producer emits a Grok row until a later release.
An operator older than this rule refuses every update once a four-row report is
stored, so it must not be rolled back while one is stored (see
[DESIGN-030](../.agents/sagas/distributed-dev-env/designs/030-provider-allowance-contract.md),
OP192 S1).

The store updates only `report.json` in an existing ConfigMap. It never creates
or deletes that object. A live resourceVersion comparison protects updates;
unrelated data and metadata are preserved. Identical replay does not write or
refresh timestamps. A conflict refuses the write. An uncertain update is
confirmed only by reading the same bytes at a changed resourceVersion. Invalid
stored bytes block replacement rather than being silently repaired.

Publishers must preserve each provider's ordering history:

- Within one producer and incarnation, a new capture needs a higher sequence.
- A caller-selected new incarnation may restart its sequence, with strictly
  newer capture and receipt times. The payload cannot select its own incarnation.
- An account or workspace rebind needs a higher binding generation and strictly
  newer capture and receipt times, validated against caller-owned trust.
- A provider with a stored item cannot replace it with an itemless gap. Preserve
  its observation, original receipt and last-good record across failed attempts.

If acquisition stops or its context is removed or unverified, publish the held
item with unknown freshness, basis `none`, and unknown current measures whose
values have state `unknown` and no decimal. Keep its raw observation and receipt
unchanged. Set the source status binding to `unknown` without account/workspace
claims, preserve last-good, and record the actual last attempt. This update can
share a document with independent provider updates; it needs no slot reset.
A later trusted capture resumes ordinary ordering. A newly accepted capture
without provider time can advance last-good while its current values stay
unknown. Current known and uncapped values must match an original observed
measure's ID, state and value.

GET projects freshness at the serving clock while preserving original capture,
provider and receipt times. It never writes the report. A stale or passed-reset
measurement can remain last-reported history while its current value becomes
unavailable; reading or replaying it does not renew capacity.

## Explicit CLI operations

`agent-run usage --publish-file FILE -o json` reads one bounded regular file;
`FILE` may be `-` for stdin. It sends one PUT to `/v1/provider-allowances` through
the existing operator client. `agent-run usage --read-current -o json` sends one
GET to that path. Neither operation follows redirects or retries. A failed or
unknown publication is not permission to send a new intent. Refused report
contents are not echoed. Neither command contacts a provider.

These modes cannot be combined with each other, `--from-file` or
`--claude-statusline`. The no-option local preflight and the separate
`usage-source --read-current` contract remain available.

## Optional portal consumer

The optional private `config/provider-allowances.json` has exactly `route`,
`method`, `maxAgeSeconds`, `contexts` and `registry`. Route and method are fixed
to `/v1/provider-allowances` and `GET`; the age and trust rules match the operator
parser. Absence leaves the consumer off. A conflicting Codex current-source
configuration or malformed selection refuses configuration.

An authorized Sessions/Usage request reads the slot at most once, after a
successful inventory read, using the existing pinned operator origin and token
path. Redirects, malformed or oversized bodies, authorization failures and
unavailable reports produce unavailable allowance rows without substituting a
session's token accounting. Tasks do not read the slot. Existing final portal
authorization still applies.

## Validation and remaining acceptance

The assembly fixture uses two synthetic collection captures and the actual
CLI, API, store and portal projection seams. It checks replay, selected caller,
trust, original times, stale/reset degradation and explicit provider gaps.
It is not an authenticated provider observation or a live deployment.

Real source acquisition, same-account entitlement validation, two fresh
observations, reviewed runtime configuration and actual API/native/UI use remain
acceptance requirements. Private adapters, observers and ingestion activation
retain their existing holds. This source adds no credentials, grants, network
policy, template change or Session restart.

## Finite local capture assembly (source only)

[DESIGN-049](../.agents/sagas/distributed-dev-env/designs/049-finite-provider-capture-assembly.md)
defines `agent-run usage capture -o json --input FILE|- --trust-file FILE`.
It assembles explicit sanitized Cursor display, Claude passive window callback
and Codex current-source report inputs into one aggregate read document. The
separate trust file uses ParseAggregateTrust; provider payloads cannot supply
that trust. Original capture and receipt times remain distinct. These sources
supply no independent provider timestamp, so current values stay unknown.

The command performs no acquisition, provider call, schedule or implicit
publication. A caller may separately submit the result with `--publish-file`
only under the existing publisher configuration and authority. The server
independently verifies its own trust, including account binding, before a new
capture can enter the slot. Missing actual inputs remain acceptance gaps.
