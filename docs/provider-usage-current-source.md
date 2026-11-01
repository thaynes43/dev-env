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
source. The central server can mount the exact route only when an injected
handler is explicitly enabled and fully configured. The deployed process
leaves that handler nil. The operator entrypoint below can construct it
only from an explicit valid configuration, and that configuration is not
set. The optional portal consumer stays off unless
`config/current-source.json` is present; no such config is mounted in production.
Their authorization, persistence, bounded retention and exposure of private
report data require review together before activation. A missing account ID or
update time does not itself require a new credential or human login.

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
or binding is installed. The operator flags below default off and are not
set in a deployment. The operator may still return 404 while that
activation is held. This consumer does not widen the ACL.

`config/current-source.json` is optional and is not a deployed manifest. When
absent, the portal makes no current-source read. When present, it must name
the fixed slot `codex-current-source`, the publisher
`codex-get-usage-limits-observation-publisher`, one producer incarnation, a
positive source generation, and a positive `maxAgeSeconds`. This selected
transport enforces a 30 minute maximum (1800 seconds) and accepts a shorter
positive age. The generic current-source validator's 24 hour ceiling is
unchanged. `attachCurrentSourceConfig` in
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

## Operator entrypoint

The operator process can build `apiserver.ProviderSourceReports` only when
both of these are set. With the enable flag false and the selection empty,
the handler stays nil. Enabling without a selection, or supplying a
selection while disabled, refuses startup. Construction
does not read, write, list, or watch the slot. Maximum age alone does not
enable the route. The flags do not turn on the API and do not turn on the
portal reader. A bad setting refuses process start with the fixed sentence
`provider source configuration was refused`. That sentence does not include
the submitted text.

```text
--enable-provider-source-reports
--provider-source-config=<json>
```

`--enable-provider-source-reports` defaults false.
`--provider-source-config` defaults empty and is inline JSON, not a file
path. The API must be on (`--api-bind-address` is not `0`) and
`--session-namespace` must be `dev-agents`. The JSON object has exactly
these fields:

```json
{"slot":"codex-current-source","publisherRef":"codex-get-usage-limits-observation-publisher","producerIncarnation":"codex-retained-tool-source-1","sourceGeneration":1,"maxAgeSeconds":1800}
```

`slot`, `publisherRef`, `producerIncarnation`, and `sourceGeneration` must
match those values. `sourceGeneration` is the integer 1. `maxAgeSeconds` is
an integer from 1 through 1800. A shorter positive age is accepted. Zero,
a negative age, and any age above 1800, including 86400, are refused.
Unknown fields, duplicate keys, a missing field, and a non-integer age are
refused. An `enabled` field inside the JSON is unknown and is refused.

These are fixed. They are not fields of the JSON:

- route `GET` and `PUT` `/v1/provider-source-reports/codex-current-source`
- ConfigMap `dev-env-system/dev-env-provider-source-codex-current`
- data key `report.json`
- publisher session `dev-agents/dev-env-1027-010058`, UID `84ddc461-fd07-47ab-a2c1-72910898bac4`, ServiceAccount `dev-env-workbench`
- portal ServiceAccount `dev-agents/dev-env-v2-portal`, GET only, and only when `--enable-portal-reader` is also set
- report ceiling 16 KiB, with `providerUpdatedAt` null

The publisher check uses that session UID and the pod that exists at the
request. It does not pin a historical pod UID. The store writer is the
manager client. The store reader and the caller reader are the manager API
reader. The store still only gets and CAS-updates an existing object. This
entrypoint does not create the ConfigMap, install RBAC, collect usage, or
call a provider. It does not establish a provider account identity, and it
does not mean ingestion is complete.

## Local preparation

By default, the command emits only the sanitized report:

```sh
agent-run usage-source --codex-tool-report /declared/retained-observation.json \
  --incarnation selected-source-1 --generation 1 --max-age 30m -o json
```

`FILE|-` must be supplied explicitly. The default path reads only that input
and makes no operator, provider or credential request. It refuses symlinks,
non-regular file inputs and inputs over 16 KiB. The caller owns private artifact
retention;
stdout must not be committed as a real account fixture.

`remainingPercentBasis` in the output is always
`derived_complement_of_used_percent`. The sanitizer accepts the native minute
field name or the retained handoff's normalized name and refuses disagreement
when both are supplied. A supplied remaining percentage or derivation marker
must agree; omission of the upstream remaining percentage is supported.

The pure `providerallowance.CodexRawToolReport` library adapter accepts the
nested official-tool envelope separately. It requires an unattributed
`accountId: null` and a primary Codex window. Preferred
`rateLimitsByLimitId.codex` and legacy `rateLimits` must agree when both are
present. A different limit, a secondary window, unknown or duplicate fields,
and conflicting plan or credit metadata are refused. Plan and credit fields
are discarded after comparison; they do not become allowance or entitlement.

The caller supplies the original observation time. The adapter preserves it,
converts Unix reset seconds to UTC RFC3339, and uses the existing builder to
derive remaining percentage and validate freshness. It never authenticates
the input, discovers an account, refreshes retrieval time, or infers a calendar
week from the window duration. Provider update time stays null.

The command accepts this nested envelope through
`--codex-raw-tool-report FILE|- --observed-at RFC3339`. Raw and flattened modes
are mutually exclusive. Raw input requires the original retrieval time; it is
never replaced with the clock, file mtime or reset time. Both paths use the
same selection and freshness checks. A manual first report does not establish
automatic ingestion or other-provider coverage.

### Explicit fixed-slot publication

`--publish` is optional and defaults to false. After validating and sanitizing
one report, it uses the existing configured operator client for one
`PUT /v1/provider-source-reports/codex-current-source`. The request body is the
canonical JSON without the formatting newline. Redirects are refused; failed
or uncertain publication is not retried. A 204 prints only
`published codex-current-source status=204`. Error output contains fixed
status categories, without input, credentials, server bodies or locations.

The command adds no endpoint or identity override. Existing client environment
configuration and authentication discovery still apply when publication is
requested; those may read a projected token or use the existing connection
setup. The server still checks the selected live Session, owned Pod and
ServiceAccount and the configured persistent store. This client cannot enable
the route, create storage or grant publication rights.

This is explicit input preparation and optional submission. It does not fetch
a provider report, install a collector, schedule recurrence or bind an account.
A first manual agent publish and continued ingestion need separate evidence.
The source change does not activate the operator route or portal config.
