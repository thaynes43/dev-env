# DESIGN-031: Pages lookup (durable Pushover page history, MVP)

Date: 2026-10-28. OpenProject Story #210 (Epic #95), "Architecture: durable
Pushover page history and source navigation for the dev-env toolkit". Tom approved
overnight Claude dev work at 2026-10-28T03:21:36Z ("Build the dev-env tools we
talked about like ability lookup pages and schedule reminders"). That approval
overrides #210's backlog-only wording for this bounded MVP only. It is not
security approval: this design adds no credential, grant, endpoint, RBAC or
network path.

**KEEPER PAGES ONLY, not all Pushover history.** Recorded or sent (accepted by
Pushover) does not mean delivered or read. Every other sender in section 3 is NOT
covered, and pages from before recording began are Unknown.

## 1. Goal

An agent can look up recent pages, meaning Pushover notifications that reached
Tom, with: date and time with timezone; sender and source; title and body;
page or incident ids; source links and provenance. Coverage is the smallest real
retained subset, stated honestly. Anything not recorded is Unknown, never "no page".

## 2. Provider facts

- Pushover has no readable history, feed or webhook for the sending application
  (https://pushover.net/api). Messages are stored per device, not viewable from
  the website, and deleted once verified delivered or after 21 days.
- A 200 answer with `status` 1 and a `request` id means "received and queued"
  (https://pushover.net/api). It is not delivery. Receipts exist only for
  priority 2 and cannot be listed (https://pushover.net/api/receipts). The keeper
  uses priority 0 and 1, so there is no receipt.
- The Open Client API reads messages only for a device it registers, with an
  account login and a device secret (https://pushover.net/api/client). That is a
  new credential, so it is out.
- Alertmanager has no queryable notification history. `/api/v2/alerts` shows
  current alerts only. Its notification log is internal deduplication state (from
  the project's source, not from the pages fetched for this note). A `webhook_config`
  can post a payload with `groupKey`, `status`, `receiver`, labels, annotations,
  `alerts[]` and `externalURL` (https://prometheus.io/docs/alerting/latest/configuration/).
  A webhook sees an attempt to notify, not whether Pushover accepted it.

## 3. Sender inventory

Read from source and manifests only; no Secret was read. Credential names only.
"An ExternalSecret" supplies each; values never leave it.

| Sender | Where | Credential reference names | Format | Route | Covered |
|---|---|---|---|---|---|
| dev-env keeper notifier | dev-env `internal/keeper/notifier.go` (`PushoverSender`, `send`); `cmd/dev-env-keeper/main.go` (notify-test) | files `/etc/dev-env-keeper/pushover/token` and `user`, from an ExternalSecret (`DEV_ENV_PUSHOVER_TOKEN`, `PUSHOVER_USER_KEY`) | Fixed title and message. Priority 1 for NeedsLogin, 0 for resolved and the 7-day warning. Link `https://claude.ai/code`. | Direct to api.pushover.net | **Yes** (notify-test excluded: a manual test notice) |
| dev-env broker | `internal/broker/seams.go` `LogNotifier`; `cmd/dev-env-operator/broker.go` | none | Not a sender: it logs that a grant waits | none today | n/a; plan 07 step 6 may add one |
| Alertmanager `pushover`, `pushover-daytime` | haynes-ops `observability/kube-prometheus-stack/app/externalsecret.yaml` receivers | `ALERTMANAGER_PUSHOVER_TOKEN`, `PUSHOVER_USER_KEY` | HTML message; title `[FIRING:n] alertname`; priority 1 firing, 0 resolved; `send_resolved` | critical goes to `pushover`; three Plex canary alerts to `pushover-daytime` (09:00-21:00 ET); all else to `null` | No |
| Gatus | `observability/gatus/app/resources/config.yaml`, `buddy.yaml`, about 20 per-app `gatus.yaml` | `GATUS_PUSHOVER_TOKEN`, `PUSHOVER_USER_KEY` | Gatus built-in text | Direct | No |
| upgrade-gate (health-gate) | `upgrade-agent/health-gate/app/resources/page.sh` | `PUSHOVER_TOKEN`, `PUSHOVER_USER_KEY` | Title `[upgrade-gate] sev check/comp`, body clamped to 1000 chars, priority 1 if critical | Direct curl | No |
| alert-responder | `upgrade-agent/alert-responder/app/resources/respond.sh` | same two names | Title `[responder] ...`, priority 0, capped per hour | Direct curl | No |
| dev-env-ops | `upgrade-agent/dev-env-ops/app/resources/` `ops-digest.sh`, `order-status.sh`, `session-launch.sh`, `work-order-watch.sh` | same two names | Title `[dev-env-ops] ...`, priority -1, 0 or 1 | Direct curl | No |
| v1 dev-env auth-watch | `dev/dev-env/app/resources/auth-check.sh` | same two names | `page()` helper, priority 0 | Direct curl | No |
| haynesnetwork outbox | `frontend/haynesnetwork/app/helmrelease.yaml` (code in another repo, not read) | `PUSHOVER_APP_TOKEN`, `PUSHOVER_USER_KEY` | Batch notifications | Direct | No |
| scribe-notes (vexa) | `ai/vexa/app/resources/scribe_notes.py` | `PUSHOVER_TOKEN`, `PUSHOVER_USER_KEY` (optional, inert until set) | Form POST | Direct | No |
| Home Assistant | not referenced in haynes-ops | unknown | unknown | unknown | Unknown |

The operator's network policy allows api.pushover.net, but no operator code sends.

## 4. Reuse

- `ConfigMapNoticeStore` already holds the keeper's deduplication state in
  `dev-env-keeper-notices` under a Role limited to get, update and patch on that
  one name. A second key, `pages.json`, needs no new RBAC.
- The ConfigMap is readable by every session already (the workbench read role and
  the pod identity read ConfigMaps), so lookup widens no access.
- Not reused: the inbox, PublishedContext and shelf archive are session-identity
  stores; a new CRD or store would need haynes-ops RBAC and CRD installs.
- There was no record-then-send path. The keeper recorded dedupe state after sending.

## 5. Decision: record-then-send in the keeper (option a)

`internal/pages` is a pure package (standard library, no I/O). The keeper's
notifier writes a record before each send and the outcome after. `agent-run
pages list|show` reads the ConfigMap JSON from stdin or `--from-file`; it stays
standard-library only and holds no Kubernetes client:

    kubectl -n dev-env-system get cm dev-env-keeper-notices -o json | agent-run pages list

No operator route and no RBAC request in this MVP.

### Record (schema version 1, key `pages.json`)

`key` (idempotency key), `correlation`, `sender`, `source`, `provenance` (the
build that sent it), `title`, `body`, `truncated`, `priority`, `url`, `urlTitle`,
`recordedAt` and `lastAttemptAt` (UTC; shown in UTC and America/New_York),
`state`, `attempts`, `sentCount`, `ambiguousCount`, `requestId`, `httpStatus`.
Document fields: `recordingSince`, `droppedCount`, `oldestRetainedAt`, and after a
reset `lostRecords` and `resetAt` (below).

Keys: `critical:<provider>:<incident opened>`, `resolved:<provider>:<incident opened>`,
`warning:<provider>:<local day>:lapse-<lapse date>`. The correlation is the
incident id, or `<provider>-lapse-<lapse date>`.

### States, exactly

| State | Means |
|---|---|
| recorded | Written before the send. The outcome was never stored: a crash, or a send in flight. |
| sent | Pushover answered HTTP 200 with status 1. It queued the page. Only the `request` id is stored. |
| failed | A definite 4xx (429 included), or no request was made (credentials missing). |
| ambiguous | A timeout, a transport error, a 5xx, or a 2xx that did not confirm. The page may have reached Tom. |

The word "delivered" is not a state. With several attempts the state is the best
evidence: sent, then ambiguous, then failed. A later refusal does not undo an
earlier acceptance, and a retry in flight does not erase `ambiguous` or `sent`.
A retry of the same key adds an attempt to the same record. The retry rules
toward Pushover are exactly what they were; the record adds no send.

### Flow

1. `Begin`: add the record (or an attempt), and store it. Loading and writing share
   a 1 second budget. A slow or failing store never blocks the send: it adds at most
   about 1 second of latency before the request, then the page is sent. If the record
   was loaded, it stays in memory and is stored on a later pass; if it could not be
   loaded, that page is Unknown in lookups. After a failed store call the keeper makes
   no further store call for a minute, so a stuck store delays no later send.
2. Send, with exactly the form fields it had before the record existed (a test
   fails on any extra field). On a 2xx the answer body is read up to
   4 KiB for `status` and `request` only. The error body of a refusal is still not read.
3. `Finish`: store the outcome, with its own budget of 2 seconds. If that save fails the stored record stays
   `recorded` (what a crash leaves) and the next pass stores it.

### Bounds, privacy and redaction

- 100 records and 256 KiB, oldest dropped first; `droppedCount` and
  `oldestRetainedAt` say so. The document is read back at most 1 MiB.
- Title 250, body 1024, link 512 characters (Pushover's limits), cut on
  characters, with `truncated` set.
- No field can hold a credential: the Pushover token and user key, request
  headers and response bodies are never stored. The request id must look like an id.
- A link with `user:pass@`, a non-https scheme, or a query or fragment key
  matching token, key, secret, auth, sig or password is stored as `REDACTED`.
  Free text is scanned the same way: unsafe links, `name=value`, `name: value`
  and JSON `"name":"value"` pairs with such a name, and bearer or basic values
  become `REDACTED`. The guard runs again when a record is read, so a record
  edited by hand is also shown redacted.
- The text guard is a heuristic. It does not catch a bare token with no name
  (`ghp_...`), a value with spaces after `name:`, a non-string JSON value, a
  secret in a name that lacks token, key, secret, auth, sig and password, or an
  encoded blob. It also over-redacts: a name such as `design:` or `monkey:` matches
  `sig` or `key`. The keeper's texts are fixed and hold none of these; a sender added later
  must not rely on the guard to make a free-form body safe.
- A damaged `pages.json` would stop recording for good, so the keeper resets it: it
  starts an empty record, sets `resetAt`, and adds a best-effort count of the records
  it held (and any earlier `lostRecords`) to `lostRecords`. The coverage then says
  "damaged and reset at ..., N earlier records lost". A document of another schema
  version is not damage and is left alone: nothing is recorded and the page is sent.
  A reader that cannot parse the record reports Unknown, not an empty history.

### Lookup and coverage

`agent-run pages list [--sender S] [--state S] [--since D] [--limit N] [-o json]`
and `pages show <key>`. Every answer starts with the banner "KEEPER PAGES ONLY -
not all Pushover history" and prints the coverage: keeper pages only, since the
first keeper image with this code (the record's `recordingSince` once it exists),
every sender in section 3 marked NOT covered (the CLI list includes keeper notify-test), older pages Unknown, and a page
missing from the record Unknown, not "no page". Damaged input is an error, never
an empty history. An error answer prints the banner and the coverage on stderr too.
`list` exits 0 and shows "newest N of M matching" whenever more records match than
`--limit`; `show` exits 4 for a key with no record.

### Not built, preserved

Pages metrics (counters by sender and state) and claims (who took a page) are
future requirements. `state`, the attempt counters and the schema version let
both be added without a migration. No workflow engine. `internal/pages` can also
back the broker's Notifier when plan 07 step 6 adds a real send.

## 6. What this does not do

- It records only after a keeper image that contains this code is deployed. The
  merge is source. The deploy is a separate haynes-ops release step, not part of this change.
- A crash between the stored write-ahead and the send leaves `recorded` for a page
  that was never sent. That is why `recorded` means "outcome unknown", not "sent".
- Pushover has no idempotency key, so a repeat after lost state can still send
  twice. The record shows it as extra attempts on one key.

## 7. Follow-up: option (b), the Alertmanager webhook adapter

This is the next coverage step. It covers the critical and Plex canary pages,
which are the largest share. Nothing here is done or approved.

How the responders learn about alerts today (haynes-ops, read only):

- alert-responder is a CronJob on `*/10 * * * *` that polls Alertmanager
  `/api/v2/alerts` for current critical alerts. It is not a webhook receiver.
- health-gate and dev-env-ops reach Alertmanager (port 9093) for verification,
  and dev-env-ops works from work orders.
- The only Alertmanager `webhook_configs` receiver is the heartbeat to an
  external dead-man's-switch. No webhook path to an in-cluster responder exists.

So there is no cheaper recording point already in place. The cheapest future one
is a second integration on the same receivers: a `webhook_configs` entry beside
`pushover_configs` in `pushover` and `pushover-daytime`, posting to an in-cluster
adapter that writes the same record format.

It needs exact approval for each of:

1. the Alertmanager receiver change (haynes-ops, the Alertmanager config template);
2. the adapter deployment (a small in-cluster receiver);
3. a CiliumNetworkPolicy path from Alertmanager to the adapter, and from the
   adapter to its store;
4. the store: a bounded store the adapter may write and agents may read, with
   its own RBAC or CRD.

Limits to state when it lands: a webhook records that Alertmanager attempted to
notify, in parallel with Pushover, so it cannot say Pushover accepted the page.
Gatus and the shell senders would still be uncovered, and each would need its own
change.

## 8. Acceptance

Source: `internal/pages` tests (ring bounds and markers, idempotent replay, every
state transition, clamping, redaction, no secret field, time rendering across
DST, coverage text); keeper tests (write-ahead order, outcome classes, retry
without a duplicate, an unusable record changes no send, requests identical with
and without the record); CLI tests (Unknown, banner, coverage, filters, JSON).
Installed and accepted states are not claimed: they wait for the keeper release.

## 9. Alertmanager snapshot PoC (2026-10-28)

A separate, explicit `--source alertmanager` path accepts a caller-supplied current API v2 snapshot for offline `pages list` and `pages show`, with text, JSON, and local HTML output. It does not alter the keeper source or its coverage. The snapshot is not notification or delivery history, and cannot establish disposition or recurrence; those remain Unknown. Observation time and source are caller assertions, not verified provenance. Input failure is Unavailable, not an empty successful observation. Bounds, filtering, link handling and usage are documented in [the PoC guide](../../../../docs/alertmanager-pages-poc.md). This is source and offline test evidence only, not deployed or accepted portal integration.

## 10. Default-off authenticated current snapshot route

The source-only portal extension adds an authenticated current Alertmanager Pages view. The existing current group/session authorization gates the fixed source GET. The separately held decision to enable this source for the authorized group covers access to alert content; no pinned-subject policy is added. Session and metadata authorization are checked again before release. The client has no proxy, redirects or credentials, reads one fresh bounded current snapshot, and never falls back to a cached snapshot. Empty and Unavailable are distinct; notification/delivery history, disposition and recurrence remain Unknown. The private configuration contract and source limits are documented in the [PoC guide](../../../../docs/alertmanager-pages-poc.md). The earlier source proposal for a pinned issuer/subject Pages scope is superseded by this group-based contract (2026-10-28). A separate runtime network gate, data access decision and installation remain unapproved; this source change does not claim a working live portal route.

## 11. Gatus current-check snapshot parser (2026-10-28)

A source-only, unwired `internal/pages` parser accepts a caller-supplied Gatus
endpoint status array, observation time and source reference. It keeps the
latest current check by timestamp for each stable key, preserving Unknown versus
explicit false and zero; malformed, conflicting or over-limit input is
Unavailable. Its defaults bound input to 2 MiB, 1,000 endpoints, 4,000 total
results and nesting depth 16; these are parser limits, not production SLOs.

The snapshot does not establish notification or delivery history, retention
completeness, incident duration or recurrence. Gatus notification history
remains not covered by the keeper record. There is no Gatus client, collector,
portal route or parent-visible connector in this source slice, and no runtime
installation or acceptance is claimed. The synthetic test contract and further
limits are recorded in [the source context](../../../context/gatus-status-adapter-20261028.md).

## 12. Read-only Pages MCP source contract (2026-10-28)

A separate, default-disabled stdio mode proposes exactly two current-snapshot
tools: list and exact-key get. It uses fixed source operations, bounded
normalization and content revisions; it does not record notification history,
register a parent connector, or grant access. The current parent cannot consume
arbitrary local stdio. Remote transport, authentication, source access and live
acceptance require separate decisions. See [the source contract](../../../../docs/pages-mcp-readonly.md).

## 13. Existing order and canonical claim join (2026-10-30, OP313)

OP223 journal 3558 defines this source-only increment. A shared response
projection in `internal/pages` joins the exact alert fingerprint and original
`startsAt` string to an existing V1 order attempt, then to an existing TaskClaim
or OpenProject work package. Portal detail and MCP list/get carry that same
projection. Source interfaces only read; they cannot create claims, dispatch,
retry or execute. Two readers obtain the same existing order and owner.

The read-only source reference is haynes-ops at
`0cfb60c2d55a4c13b7ca72f2dc78685ec7ff464b`:
`.agents/runbooks/alert-responder.md`, `.agents/runbooks/agentic-remediation.md`,
`kubernetes/main/apps/upgrade-agent/health-gate/app/resources/remediate.sh`, and
`dev-env-ops/app/resources/{work-order-watch,order-status}.sh` under that same
`upgrade-agent` directory. No runtime store was read. Fix round (2026-10-31) also
read `alert-responder/app/resources/respond.sh` at haynes-ops
`a036fda0c24b146c52ee199d1f9924d29006523e`: its `rem_watchdog` marks a stale
`rem-*` order `failed` (setting `note` and `updated`) and calls `escalate.sh
rem-watchdog`, which files `esc-rem-watchdog-<sig8>`.

V1 uses two different keys. The diagnosis incident hash is the first 12 hex
characters of SHA-256 of `fingerprint|startsAt`. The work-order ConfigMap
`upgrade-agent/upgrade-work-orders` has a data key such as
`rem-responder-<sig8>` whose value is a single JSON-encoded object. Only `status`,
`created` and `updated` (Unix seconds stored as strings) are needed here, along
with the actual data key. That order key can be reused and overwritten after a
terminal attempt. It is not the incident hash. This projection never synthesizes
an order key from labels or a fingerprint. Its source must evidence the exact
alert-to-order association and the order's `created` value. A later reader must
establish that association explicitly; the current store alone does not provide
a universal fingerprint/start join. Missing association stays Unknown.
Direct `esc-responder-<sig8>` and promoted `esc-rem-<sig8>` keys use the same
order subset (`health-gate/app/resources/escalate.sh`); they are accepted only
with an explicit incident binding too.

`ResponderOrderSource` supplies a bounded order observation and its explicit
alert identity binding. `CanonicalClaimSource` supplies the matching order key
and creation timestamp, canonical reference (kind plus stable ID), owner and
explicit state evidence with a timestamp and safe HTTPS evidence link. Each
observation includes source attribution and observation time. TaskClaim has no
lifecycle status: its mere existence cannot establish responding or resolution.
Adapters must supply explicit workflow evidence; arbitrary order notes are not
parsed into state. Neither adapter has a production implementation in this slice.

The caller supplies an evaluation time and a positive maximum observation age.
Unconfigured, unavailable, empty, stale, invalid, unjoined and mismatched
observations remain distinct reasons for Unknown. No wall clock or cached
fallback is used. Fresh observations must cover their event timestamps, claim
state evidence must not predate the order attempt's `created` time, and both
bindings must agree.

V1 records only the order statuses `pending`, `claimed`, `done`, `failed` and
`escalated` (haynes-ops `kubernetes/main/apps/upgrade-agent/dev-env-ops/app/resources/work-order-watch.sh` status
flow and `order-status.sh`). `order-status.sh <key> working` is a heartbeat: it
bumps `updated` and keeps the recorded status, so `working` is never a status.
An order with any other status, empty included, is Invalid. A configured claim
source is looked up only after the order joins; until then its condition is
NotChecked, shown as "Not checked (order not joined)", never Unconfigured.
Claimed/responding require explicit canonical evidence and a V1 `claimed`
order. Needs-user requires explicit canonical evidence and an `escalated` or
`failed` order. Resolved requires explicit canonical evidence and a `done`
order. A claim state outside claimed, responding, needs-user and resolved is
Invalid. A supported claim state that disagrees with the order status is
Mismatched (fix of 2026-10-31): the projection withholds the owner and claim
reference with the state, because a contradicted claim confirms neither. The
portal shows "Mismatched (claim and order disagree)". Pending, failed,
escalated, missing current alerts and old terminal records alone never mean
resolved. A refire has a different start identity even if its order key is
reused. Unknown fields are literal `Unknown`; unsafe evidence is never linked.

The projected state is the responder's recorded workflow state, labelled
"Response state" in the portal and `responseState`/`responseStateAt` in MCP. It
is never the alert's own status. The current snapshot lists only active,
suppressed and unprocessed alerts, so a `resolved` response state beside a
listed alert means the responder recorded a resolution while Alertmanager still
lists the alert. The projection does not hide it: it cannot observe the alert's
end (an absent alert is not a historical absence), so gating on that would mean
`resolved` never shows. The two fields stay side by side instead.

Evidence links are rendered as the portal `href` and returned as the MCP
`evidenceURL`, byte for byte. They require `https://`, Go's canonical form
(`url.Parse(v).String() == v`), no credentials, port, query or fragment, and a
character set that html/template neither escapes nor normalizes in an `href`:
letters, digits and `-._~/%:@,;=!$*`. So spaces, quotes, angle brackets,
backticks, backslashes, `&`, `+` and parentheses are rejected raw. A
percent-decoded path also rejects spaces, quotes, angle brackets, backticks,
backslashes, control and invisible formatting characters and `.`/`..` segments
(an encoded `&` or `+` stays inert in the link). The
host must be a lowercase DNS name with at least two labels whose last label
starts with a letter: that rejects every IP literal a browser would parse
(dotted, short, hex, octal or decimal IPv4 and bracketed IPv6), single-label
names and `.localhost`. The existing redaction guard still applies. An
additional exact-origin allowlist applies (OP316 below).

The MCP content revision hashes the normalized items without the capture time
or the response's `orderObservedAt` and `claimObservedAt`. A re-observing
adapter therefore keeps the revision while the evidence is unchanged;
conditions, sources and event times still count as content.

Every Unknown field stays the literal `Unknown` rather than being omitted, so an
Unconfigured response keeps UI/MCP parity and costs about 525 bytes per alert in
MCP JSON (513 for the object including OP322's claimOwner, measured with a
16-hex fingerprint and a nanosecond start; `TestUnconfiguredResponseSize` bounds
it at 520). Against the 2 MiB normalized `all` bound that is the whole budget at
about 3,990 alerts; a
snapshot over the bound fails closed as Unavailable and is never truncated. A
listed page of at most 100 items adds about 52 KB. This slice documents the
cost and does not trim.

The source-only OP316 prerequisites (2026-10-31) replace the per-alert
interfaces with `FindOrders(ctx, identities)` and `FindClaims(ctx, lookups)`.
`ProjectResponses` deduplicates the exact fingerprint/start identities and makes
at most one order batch call, then at most one claim batch call for joined
order attempts. Each `ClaimLookup` includes the identity and key/created pair.
Portal workspace renders and MCP `pages_list`/`pages_get` use this same snapshot
projection before grouping, filtering or limiting. `ProjectResponse` is the
single-identity wrapper. Invalid identities and invalid evaluation policy never
reach a source. Empty snapshots make no calls; a snapshot with no joined orders
makes no claim call. Conditions remain per identity: a missing batch-map entry
is Unavailable (no observation), while an available observation with a nil
record is Empty. A batch error makes that source Unavailable; neither errors
nor extra map entries create associations. Claim evidence remains NotChecked
for identities whose order did not join, including when other identities join.

`ResponseSources.EvidenceOrigins` is the caller-controlled list of exact
HTTPS origins, without a path or trailing slash. Nil/empty denies every link,
keeping evidence and the unconfirmed claim fields Unknown (Invalid). Syntax,
redaction and byte-identity checks above still apply alongside origin membership.
Matching uses the literal `https://` plus host, never a prefix, suffix, wildcard
or normalized form. Subdomains require their own entry. Existing lowercase DNS
and no-port restrictions remain: uppercase hosts/schemes, explicit `:443` and
other ports are rejected even if listed. Non-origin entries (paths, queries,
fragments, credentials) cannot authorize a link. The allowlist is a source-only
assembly config, not an MCP argument or extension to `--source-config`.

These two prerequisites are implemented and synthetically tested, not production
adapter acceptance. A real adapter still needs separately authorized read-only
wiring and evidence of usable behavior.

OP322 adds `claimOwner {namespace,name,uid}` to the response projection and to
the canonical batch evidence input. After the existing exact incident/order/
claim/evidence checks pass, a TaskClaim owner is copied from that same batch.
It is never parsed from the free-text owner, inferred from TTL, or resolved by
name. Non-TaskClaim, absent or invalid owner metadata stays literal Unknown in
all three fields. The claim reference retains namespace/name/UID, so its UID
can be compared directly with the negotiated session detail (DESIGN-010).
Stale, unjoined and mismatched evidence withholds the owner along with the
claim. No extra list/get or production reader activation follows.

Maintenance is the second consumer slice (2026-10-31): negotiated grant detail
reuses the authenticated requester session UID and existing claim scope/list, as
specified in DESIGN-010. It does not infer an envelope claim or approval.

The remaining OP322 consumers have two bounded follow-up slices:

1. Upgrade/Renovate: define the explicit binding from repository/PR/exact head
   and existing upgrade order attempt to TaskClaim UID, then add a read-only
   projection. Test contenders, changed heads, blocked outcomes and Unknown
   evidence. Keep canonical work identity stable across head changes; an old
   head's evidence cannot confirm the new head. Do not infer success from
   process exit or activate an executor.
2. Portal UI: consume the negotiated session projection and Page claim owner,
   display exact identities and Unknown/stale custody, and link by UID only
   within existing read authority. Coordinate with OP326's session detail;
   test presentation parity and old-operator omission. No `web/portal` edits
   are included in this slice. Reader activation and installed/end-to-end
   acceptance require their own authorized rollout and evidence afterward.

Production entry points continue to use unconfigured response sources,
`UnconfiguredStore` and `DisconnectedPagesControls`. No reader, endpoint, CRD,
claim registry, access path, control or automation is activated. Synthetic table
tests cover all states and failure distinctions, reused order attempts, refires,
two callers, evidence sanitation and portal/MCP parity. Source and tested are
separate from installed or accepted; this story claims only the former two.

## 14. Offline V1 responder order adapter (2026-10-31, OP324 source slice)

`ParseV1ResponderOrders` parses the actual `kubectl get cm -o json` shape for
`upgrade-agent/upgrade-work-orders`. It retains only actual responder data keys
and `status`, `created`, `updated`, with the ConfigMap resourceVersion as source
attribution. `Observations` returns this safe subset keyed by the actual data
key; no signature, labels, free text or derived key establishes an association.
The batch `ResponderOrderSource` reports Unjoined for a nonempty responder store,
Empty for an observed store with no responder records, Invalid for malformed
input and Unavailable for a failed file read. Invalid is decided before freshness,
so a malformed file reads Invalid however old its capture time. Otherwise
observation freshness remains a separate Stale check. Existing claim mismatch checks remain unchanged.

The producer scripts were inspected at haynes-ops main
`a036fda0c24b146c52ee199d1f9924d29006523e`: health-gate's `remediate.sh` and
`escalate.sh`, the alert-responder's `respond.sh`, and dev-env-ops'
`work-order-watch.sh` and `order-status.sh`.
One read-only live ConfigMap shape read on 2026-10-31 confirmed single-encoded
JSON object strings, responder entries mixed with unrelated work orders and a
digest bookkeeping entry. Neither those producers nor the observed JSON has
an explicit fingerprint, startsAt or incident-hash identity field. The `alert`,
`sig`, reason and notes are context, not identity evidence. No real order
contents are committed. Synthetic fixtures include the unrelated entries;
their needed-field types do not impose responder types on unrelated orders.
No speculative future identity schema is accepted by this adapter.

The offline Alertmanager `pages list|show` path accepts
`--orders-from-file FILE --orders-observed-at RFC3339`. The file is the caller's
ConfigMap snapshot; the capture time is an explicit caller assertion, never
inferred from file mtime, order timestamps or the time of reading the file.
The evaluation clock checks a five-minute maximum observation age. The orders
file cannot share stdin with the alert snapshot. Omitting the flag leaves
response sources Unconfigured. Text, JSON and HTML carry the same shared
response projection. Malformed or unreadable order input returns a failure
exit while preserving the alert snapshot and its Unknown response reason.
Bounds are 2 MiB per ConfigMap JSON, 1,000 data keys, 64 KiB per encoded order,
16 nesting levels and 1,000 members/elements per object/array; duplicate members,
case aliases of consumed fields, non-string data, invalid status/times and
future order event times are refused without echoing input. Reused keys and
refires cannot inherit evidence: without a producer binding every alert remains
Unknown, even beside a terminal order.

Known responder keys are `rem-responder-<sig8>`, `esc-responder-<sig8>`,
`esc-rem-<sig8>` and `esc-rem-watchdog-<sig8>` (respond.sh's watchdog escalation).
Any other key under a responder prefix is unrelated: it is counted
(`IgnoredKeys`) and ignored, never failing the store, though its value must still
be bounded JSON. An order `updated` may be up to 30 seconds after the capture
time (producer clock skew, or a write between the read and the capture); the
documented recipe takes the capture time after the read. A future capture time
is Invalid and the CLI exits 1, like a malformed file.

Exact remaining dependencies:

1. The V1 producer must write an explicit alert identity into the order JSON
   before any real join can succeed. That is a haynes-ops responder change,
   proposed only and not made here. Its exact identity schema and attempt
   binding must be reviewed before this adapter can consume it.
2. A portal or operator reader would need read access to `upgrade-agent`
   ConfigMaps, which is a separate approved access decision.

The portal and operator remain Unconfigured. This slice adds no network call,
RBAC, CRD, reader activation or cluster mutation. Canonical claim source wiring
and installed, usable/end-to-end acceptance remain open. Source and synthetic
validation do not complete OP324's runtime acceptance.
