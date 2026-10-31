# Alertmanager snapshot Pages PoC

`agent-run pages list --source alertmanager --from-file snapshot.json` reads a caller-provided Alertmanager API v2 alert array. `pages show FINGERPRINT --source alertmanager --from-file snapshot.json` selects one record. List supports `--search TEXT` (case-insensitive name, summary, description or fingerprint) and `--status active|suppressed|unprocessed`. Either command accepts `-o json` or `-o html`. Input defaults to stdin. The HTML is a local export; it is not an authenticated portal, deployment, collector, or notification record.

An envelope may instead supply `{"observedAt":"2026-10-28T00:00:00Z","source":"caller description","alerts":[]}`. The RFC 3339 observation time and plain-text source are caller assertions, not authenticated provenance. Bare arrays report both as Unknown. Do not put credentials or sensitive information in input or the source description. Source descriptions cannot contain URLs. Links are accepted only for absolute HTTP(S) URLs without userinfo; only Prometheus query keys `g0.expr` and `g0.tab` are retained; all other query keys and fragments are removed. Alert text and labels remain caller-supplied and may contain sensitive data; the output is not a secret scrubber.

Each output carries the same selected normalized records and coverage: a current snapshot cannot establish notification or delivery history, disposition, or recurrence. End times never imply resolution. A valid empty array is a successful observation of zero current alerts; a filtered result with no matches says only that no alerts are in that result. Missing, unreadable, malformed, or over-limit input returns an Unavailable result and a failure exit code. A missing fingerprint in `show` returns a not-found exit code.

Limits: 2 MiB input, 1,000 alerts, 8,192 bytes per string or key, 1,000 object members, 4,000 array elements at any level, and nesting depth 16. Violations fail the whole snapshot rather than silently dropping records or fields. Duplicate fingerprints fail closed, including identical duplicates. Duplicate JSON members and case variants of recognized structural fields are rejected; label keys remain case-sensitive. Unknown API fields are ignored after these bounds are checked; recognized fields must have the expected types. Supported states are active, suppressed and unprocessed. Timestamps, when supplied, must be RFC 3339. The snapshot is supplied offline; no network request or alert mutation occurs.

## Authenticated portal source (source-only)

The default-off portal route `/portal/projects/dev-env/environments/internal/pages` performs a fresh bounded Alertmanager read only after current session authentication and existing current group-based metadata authorization. It rechecks session and authorization before release. The private optional `config/pages-alertmanager.json` requires exactly `source_url`, which must equal `http://alertmanager-operated.observability.svc.cluster.local:9093/api/v2/alerts`. Absence disables the source and shows “Pages not enabled”. Enabling access to alert labels and bodies for the currently authorized group requires a separate owner decision. The client makes one GET per authorized view, with five-second timeout, no proxy or redirects, and the same 2 MiB input limit. Errors render Unavailable without cached data. The page uses a server observation time and cannot establish notification history, delivery, disposition or recurrence. Source merge and test do not establish runtime access or installation; portal network authorization and deployment remain separate owner decisions.

## Offline V1 responder orders (2026-10-31, OP324)

Supply a separate snapshot with existing ConfigMap read access:

```sh
# Take the capture time AFTER the read, so no order can be newer than it.
# Preserve it when reusing this file.
kubectl get cm -n upgrade-agent upgrade-work-orders -o json > orders.json
orders_observed_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
agent-run pages list --source alertmanager --from-file snapshot.json \
  --orders-from-file orders.json --orders-observed-at "$orders_observed_at" -o json
```

`pages show FINGERPRINT` accepts the same flags. Alert input can still use stdin;
the orders flag requires a separate file. Capture time is a caller assertion,
not verified provenance. Never replace an old capture time with the time of
reading the file. An order `updated` up to 30 seconds after the capture time is
accepted (producer clock skew); anything later makes the whole file Invalid.
The CLI evaluates freshness against its clock with a five-minute
maximum age. It makes no network request. Omit the orders flag to leave the
response source Unconfigured.

Each alert carries the shared response projection in JSON, text and HTML.
The production V1 source retains only actual responder data keys and status,
created/updated Unix-second strings. The source includes ConfigMap
resourceVersion when present. The known responder keys are `rem-responder-<sig8>`, `esc-responder-<sig8>`,
`esc-rem-<sig8>` and the stale-lane watchdog's `esc-rem-watchdog-<sig8>`. Unrelated
work lanes and digest bookkeeping do not supply responder evidence. Any other key
under a responder prefix is counted as ignored (`IgnoredKeys`), never trusted,
and cannot invalidate the other orders; its value must still be a bounded JSON
object. No order reason, note, diagnosis, alert context
or signature reaches the output. The producer scripts and one read-only live
shape inspection found no explicit alert fingerprint/start identity or incident
hash. All current responder records therefore remain Unjoined; a valid empty
responder store is Empty. Neither a reused key nor a refire inherits an old
attempt. Lookalike identity fields are ignored pending an actual producer
contract. Order/owner/state remain literal Unknown without verified joins.

Missing/unreadable order files yield Unavailable; malformed files yield Invalid,
however old their capture time (Invalid is decided before freshness). Both return
a failure exit while keeping the alert snapshot. A capture time later than the
CLI clock is Invalid and also returns the failure exit (not a usage error: the
flag syntax is valid). An old capture time is Stale and exits 0. None means
resolution.
Syntax errors in flags return a usage error. Limits: 2 MiB total ConfigMap JSON,
1,000 data keys, 64 KiB per encoded order/string, 16 nesting levels and 1,000
members/elements per object/array. Duplicate JSON members, case aliases of
consumed fields, invalid types/status/timestamps or order events after capture
are refused as a whole; errors do not echo input. Synthetic fixtures match the
real mixed store shape; real order contents are never committed.

Before any real join can succeed, the V1 producer must write an explicit alert
identity into order JSON. That haynes-ops responder change is proposed only;
this slice does not make it. A portal or operator reader would need read access
to `upgrade-agent` ConfigMaps, which requires a separate approved access decision.
Those readers remain Unconfigured. No RBAC, activation or runtime acceptance
follows from this offline adapter; canonical claim wiring also remains open.
