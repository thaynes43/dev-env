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

The source-only portal extension adds an authenticated current Alertmanager Pages view. A separate exact issuer/subject scope is required before the fixed source GET; existing metadata permission does not itself authorize alert content. Session and metadata authorization are checked again before release. The client has no proxy, redirects or credentials, reads one fresh bounded current snapshot, and never falls back to a cached snapshot. Empty and Unavailable are distinct; notification/delivery history, disposition and recurrence remain Unknown. The private configuration contract and source limits are documented in the [PoC guide](../../../../docs/alertmanager-pages-poc.md). A separate runtime network gate and installation remain unapproved; this source change does not claim a working live portal route.
