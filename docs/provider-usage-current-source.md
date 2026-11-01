# Selected Codex source report

This slice adapts the already retained `get_usage_limits` task observation. It
does not call a provider. The source is explicitly selected; its stable account
identity and provider update time remain unavailable. It cannot enter the
verified account allowance path.

The source-specific schema is `codex.get_usage_limits.current_source.v1`.
The publisher selector is
`codex-get-usage-limits-observation-publisher`. A caller selects one producer
incarnation, positive source generation and maximum retrieval age. Neither a
publisher name in JSON nor that selection proves a provider account identity.
Changing the selected source or receiving an account-change signal requires a
new generation. An account change that the tool does not expose remains a
coverage limitation.

The sanitizer accepts the declared retained task observation format, rather
than arbitrary native tool output. `observedAt` in that format means retrieval
time. If `retrievedAt` is also present, the instants must agree. The sanitizer
preserves that instant; preparing or reading a snapshot never refreshes it.
Provider update time stays null. The output retains only source selection,
retrieval time, the reported used percentage, its derived remaining percentage, duration in minutes and reset
instant. Local automation ceilings, remaining points to those ceilings and
unknown banked credits are discarded.

The native tool reports `usedPercent`, `windowDurationMins` and `resetsAt`.
Remaining percentage is the derived complement of used percentage. The retained
handoff normalizes the duration name to `windowDurationMinutes`; it does not
establish a separately reported remaining value. The used percentage must lie
in 0 through 100. The sanitizer computes its exact decimal complement; any
supplied complement must agree and must never be silently repaired. The output
schema defines remaining as derived, and the display preserves that distinction. A duration of 10,080 minutes can
be displayed as seven days; it does not establish a calendar week. The exact
reset instant retains its timezone. Passing it invalidates the report; it does
not establish renewed capacity.

The production policy proposed here is a 30 minute retrieval age limit. This
is a consumer limit, not a claim about provider freshness. Future retrievals,
expired retrievals, invalid source selection and passed resets suppress the
current meter. Provider update time, when available, must not be later than
retrieval. The bound allowance schema and its validators remain unchanged.

## Retained artifact and transport

The accountable publisher is the authorized coordinator that already holds the
retained tool observation. The local sanitizer emits a bounded sanitized JSON
document. A publisher can retain it by atomic replacement of
`$XDG_STATE_HOME/dev-env-usage/codex-current-source.json`, with a default of
`$HOME/.local/state/dev-env-usage/codex-current-source.json`, in a private
directory and with file mode 0600. This is an operational artifact, not a public
fixture. No automatic publisher or periodic provider acquisition is enabled.

The current portal mounts no such artifact. Its operator client is a
`portal-reader`; the route allowlist in `internal/apiserver/server.go` does not
permit PublishedContext reads. PublishedContext payload scope also requires a
session or coordinator caller. Reusing its credentials under another identity
would bypass those boundaries. This slice adds no such access.

A concrete proposed continuation is one named retained report slot in the
existing operator service, `codex-current-source`, with an explicitly selected
publisher session UID/incarnation and source generation. The proposed exact
route is `/v1/provider-source-reports/codex-current-source`, with one authorized
publisher write and portal read. The fixed-slot handler and store now exist in
source, but the central server does not mount them. Operator configuration and
the portal consumer remain disconnected. Their
authorization, persistence, bounded retention and exposure of private report
data require review together before activation. A missing account ID or update
time does not itself require a new credential or human login.

The store updates one existing ConfigMap through a live resourceVersion CAS;
it never creates the object. Updates preserve its metadata and unrelated data.
The selected-source path refuses a non-null `providerUpdatedAt`, even when the
generic report schema accepts it. Undecodable stored bytes block later writes
rather than being repaired or replaced. Invalid or expired reads return no
current value without deleting the stored report or changing retrieval time.
An identical replay does not refresh the report. A lost update response is
confirmed only by a live read of the same bytes at a changed resourceVersion.
The handler remains disabled without all explicit source and caller fences;
maximum age alone cannot register or enable the route. No ConfigMap, Role,
binding, flag, or production reader is installed by this source preparation.

Consumer configuration would live in the portal's existing configuration root
and name that fixed slot, publisher identity, incarnation, generation and
`maxAgeSeconds: 1800`. The assembly point is `productSources` in
`web/portal/session/production.go`. A consumer must reject mismatched publishers,
source generations and content digests. It must not scan arbitrary contexts,
merge account reports, select by arrival order or use per-request subprocesses.
No mixed bound entitlement and selected-source report may silently win over the
other. An absent source remains disconnected.

Source tests and synthetic rendered reports establish the representation and
adapter only. Installed charts, an authorized retained transport, and one real
report rendered with its original retrieval time are separate acceptance
evidence. OP323 remains open until its actual consumer criteria are met.

## Local preparation

The local command emits only the sanitized report:

```sh
agent-run usage-source --codex-tool-report /declared/retained-observation.json \
  --incarnation selected-source-1 --generation 1 --max-age 30m -o json
```

`FILE|-` must be supplied explicitly. The command has no provider or operator
connection and does not install a publisher. It refuses symlinks, non-regular
file inputs and inputs over 16 KiB. The caller owns private artifact retention;
stdout must not be committed as a real account fixture.

`remainingPercentBasis` in the output is always
`derived_complement_of_used_percent`. The sanitizer accepts the native minute
field name or the retained handoff's normalized name and refuses disagreement
when both are supplied. A supplied remaining percentage or derivation marker
must agree; omission of the upstream remaining percentage is supported.
