# Explicit supported provider captures

These commands prepare private provider observations. They do not enable an
observer, schedule, operator route, portal reader or publication. The design is
[DESIGN-052](../.agents/sagas/distributed-dev-env/designs/052-supported-provider-collectors.md).

Use an explicitly qualified executable. Keep actual account artifacts out of git,
logs and public CI artifacts. A source document does not register its own account
as trusted. The aggregate's caller-owned registry is a separate reviewed input.
The unsalted email hash is private personal metadata, not anonymization. Keep
actual trust registries and operator configuration private too; never put their
values in public GitOps arguments/manifests. A private delivery mechanism requires
its own qualification before activation. Email spelling is preserved exactly;
a changed spelling requires requalification rather than silent identity merging.

## Codex official account and quota

```sh
/path/to/qualified/agent-run usage collect codex -o json \
  --executable /absolute/path/to/installed/codex --access-only-auth
```

First independently qualify that this HOME receives only access tokens, such as
the keeper-managed `codexauth` projection. A HOME containing a rotating refresh
token is forbidden, even for non-forced RPC reads: the official client may
refresh internally without a JSON-RPC refresh request. `--access-only-auth` is
an explicit caller acknowledgement of that prerequisite; it does not inspect
credential files or prove the projection. Missing/false acknowledgement refuses
before launching the child. Never create a second refresh-token owner.

This starts one official app-server child for bounded read-only methods, using
its normal existing authentication. It sends no thread, model, login, forced
refresh or reset-credit action. It refuses credential-refresh requests and
stops/reaps its own child/process group. Do not select another user's HOME or
copy credentials to make the read work. Expired access remains a blocker.

The sanitized `codex.app-server.account-capture.v1` output contains a private
provider-email hash, personal plan, two nullable quota windows and local
`receivedAt`. Missing percentages never become zero. Primary and secondary are
independent windows; neither a duration nor a plan name establishes a shared
pool. `providerObservedAt` stays null when the provider exposes no timestamp.
A successful RPC is a current collection of reported data, with an unknown
original provider measurement time.

Wrap this already received document in a capture-bundle entry with its original
`receivedAt` as both capture and receipt, an explicit producer/incarnation and
monotonic sequence. `usage capture -o json --input FILE --trust-file FILE` remains
pure local assembly. The account reference must match independently selected
trust. For this qualified personal source the account registry may explicitly
use `workspaceRef:null` and `accountScopeOnly:true`. This is not an invented
organization workspace or permission to merge another provider's allowance.
Legacy workspace-bound accounts retain their checks.

Read items expose optional `collectionFreshness`, calculated from original
capture time, receipt, maximum age and the serving clock. Existing `freshness`
continues to describe provider data. A recent reported percentage/reset may be
shown with last-checked/source information while strict current-capacity fields
stay unknown. Serving or reassembling old data never creates a provider refresh.

## Claude isolated passive callback

Create a dedicated private directory (mode 0700), then configure only an explicitly
qualified, legitimate invocation to run:

```sh
/path/to/qualified/agent-run usage collect claude-statusline \
  --state-dir /private/isolated/capture \
  --producer isolated-claude --incarnation invocation-1
```

The command reads one bounded callback from stdin. It does not configure Claude.
It discards every field except documented five-hour/seven-day percentage/reset
windows, including transcript paths, account data and session usage. It atomically
retains only the last two captures in `claude-captures.json`, mode 0600, with
monotonic sequence and original callback reception times. A conflicting source,
malformed/unreadable state, unsafe file, clock rollback or lock timeout refuses
without replacing the old capture. Null windows stay unknown; zero stays zero.
A callback is not an independent provider refresh. A timer replaying callback data
does not establish recurring provider acquisition.

The output artifact is directly accepted by `usage capture`. Account binding and
provider observation time remain separate. No global settings or active user
sessions are changed. A headless invocation is not assumed to run a UI statusline.

This command is separate from the session readings of DESIGN-059. There agentd
itself sets `statusLine` to the image's `claude-statusline-usage` helper in
Claude remote and local sessions, and sends the windows on the heartbeat: see
[Session allowance readings](operator-session-allowances.md).

## Claude structured SDK report

```sh
/path/to/qualified/agent-run usage collect claude-report -o json \
  --input /private/already-received-assistant-envelope.json \
  --received-at 2026-11-02T12:00:00Z
```

Supply the actual original local reception time. Formatting a retained input
does not refresh it; a missing, invalid or future receipt time refuses before
reading the input. This reads one already supplied official SDK assistant
`usage_report`; it does
not launch Claude, send `/usage`, resume a conversation or invoke a model. It
retains only server rate-limit rows and preserves their order, kind, group,
percentage, nullable ISO reset, model/surface scope, severity and active marker.
It drops session tokens/costs, message content and extra-usage billing. Null
limits mean unavailable; an empty list means the server reported no meters.
An inference-only credential may have no eligible structured report.

The resulting `claude.sdk.usage-capture.v1` can be placed in an anthropic capture
entry. No scope display label establishes a model identity, window duration,
account binding or shared entitlement. Each row remains independent. Missing or
null numeric/bool fields, numeric strings, duplicate keys, invalid reset dates,
aborted/error envelopes and session-only usage refuse.

## Remaining delivery boundaries

Cursor's CLI account/plan metadata alone is not a remaining-quota source. The
qualified normal interactive Usage display is supported as retained display
evidence below; its acquisition runner remains separate. Private RPC descriptors,
cookies and per-run billing are not substitutes.

A durable service needs a lifetime separate from this coding owner, supported
transports, schedule/backoff, bounded private retention, exact publisher/readers,
minimum slot permissions, signed artifacts and rollback. The existing aggregate
PUT is fenced to one live AgentSession UID. None of these capture commands turns
that into a deployment-owned publisher or extends any credential's expiry.
Actual recurring three-provider API/agent/UI acceptance remains separate.

The default-off [operator collector](operator-allowance-collector.md) supplies
the elected-leader schedule and durable reconciliation source path. It requires
its own exact host/private configuration, minimum named-slot permission and
recurring-use qualification. These standalone capture commands do not enable it.

The operator supports an explicit private configuration file with
`--aggregate-allowance-config-file=/private/mounted/selection.json` together with
`--enable-aggregate-allowance-reports`. It conflicts with inline configuration.
The private file must be regular, bounded and inaccessible to world; normal
projected-volume symlinks are supported. This only reads an already qualified
private mount at startup. It creates no Secret, slot, permission or collector.
The portal still needs its independently reviewed private trust configuration.

A sanitized version-pinned normal Cursor Usage capture uses
`cursor.cli.usage-capture.v1`. It retains the actual local acquisition interval,
rounded Included display, nullable account ref and literal month/day billing
label. The qualified renderer obtains that label from billing-cycle end, not
from the included-allowance reset. It is retained only in the advisory
`billingResetDisplayText` direct field. The quota reset precision remains unknown,
with no quota reset date, instant or countdown.
Generic period captions and API fallback zero do not become numeric entitlement
facts. A displayed Included zero remains unknown unless separately qualified,
and Auto is not a separate pool. The assembler supports caller-qualified account
scope with a genuinely null workspace; no cross-provider pool follows from the
same email. This adapter consumes an already captured display and does not launch
a CLI or establish recurrence.
