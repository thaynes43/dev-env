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
source, but the central server does not mount them. The portal consumer is
wired and stays off until `config/current-source.json` is present. Route
activation, persistence, and exposure of a real report still require review
together. A missing account ID or update time does not itself require a new
credential or human login.

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
binding, or flag is installed. The operator may still return 404 while that
activation is held. This consumer does not widen the ACL.

`config/current-source.json` is optional and is not a deployed manifest. When
absent, the portal makes no current-source read. When present, it must name
the fixed slot `codex-current-source`, the publisher
`codex-get-usage-limits-observation-publisher`, one producer incarnation, a
positive source generation, and `maxAgeSeconds`. 1800 is the 30 minute
consumer limit. The ceiling is 86400 seconds. `attachCurrentSourceConfig` in
`web/portal/session/production.go` copies that selection onto the session's
existing pinned operator client and token path. It does not assign
`SessionSource.allowance` or dial during load.

After Authentik has accepted the request as `portal_is_active` and
`portal_is_admin`, one Sessions request, including Usage and a selected
session, GETs `/v1/provider-source-reports/codex-current-source` at most once.
The fetched bytes stay on that request. `NewCurrentSourceReader` projects
them. Tasks do not read the slot. A failed inventory does not read it either.
The final authorization check still runs afterward; a revocation does not
release the page. This selected path treats a non-null `providerUpdatedAt` as
unavailable. The generic constructor still accepts a non-null value that
`ValidateCurrentSource` accepts. A mismatched publisher, incarnation, or
generation, a stale retrieval, a passed reset, a malformed body, a redirect,
a body over 16 KiB, a 403, a 404, or a timeout stays on the allowance panel as
`Unknown · Current source unavailable`. The session inventory remains. Session
token counts are not turned into a quota. A bound allowance already stored on
the session is not merged into the selected projection and does not remain as
a fallback when the slot fails. Claude and Cursor stay disconnected. A
10,080 minute window still renders as seven days, not a calendar week. An
absent file leaves every provider disconnected.

Source tests use synthetic fixtures. They show the representation, the
default-off connection, and one authorized portal render of a synthetic slot.
They are not a live operator, a mounted route, or an accepted account
entitlement. OP323 remains open until a real retained report is served with
the route and grants activated.

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
