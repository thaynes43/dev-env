# Pages MCP source contract

`agent-run pages-mcp` is a separate stdio MCP server offering only `pages_list` and `pages_get`. It is disabled without an explicit private source configuration; it does not register itself, expose an endpoint, or grant access. The current parent cannot consume arbitrary local stdio. A parent-visible service, authentication, authority, network access and Gatus activation require separate approval and verification.

The optional `--source-config FILE` is an owner-controlled local JSON object with only boolean keys `alertmanager` and `gatus`; omitted keys are false. The file must be a regular, private file owned by the current user. It contains no URLs, credentials or principal selectors. Invalid configuration stops startup. Tool arguments cannot select a URL, method, endpoint, credential, file or principal. A disabled source or invalid call performs no backend request.

The only source operations are a Grafana MCP `grafana_api_request` for GET `/api/datasources/proxy/uid/alertmanager/api/v2/alerts` at the fixed Grafana MCP endpoint, and a fixed HTTPS GET of Gatus `/api/v1/endpoints/statuses`. There are no redirects, environment proxies, cookies, caller headers, retries or alternate routes. Grafana accepts only a standard MCP result with one text content item containing a JSON array; other envelopes are Unavailable. This is a synthetic source contract: the actual Grafana response envelope and access are not verified here.

Both tools fetch fresh snapshots; capture time is not provider freshness. `pages_list` selects a source, optional local text filter, and limit from 1 to 100 (default 20). `pages_get` selects an exact key and optional SHA-256 content revision. Revisions hash the full normalized content, excluding capture time. A changed revision reports `ChangedSinceList`; an absent key reports `NotPresentInCurrentSnapshot`, not a historical absence. Empty and Unavailable remain distinct. Responses identify current alerts or current endpoint checks, not notification/delivery history, complete retention, incident duration or recurrence. The server drops upstream errors, Gatus host/condition/error fields and links; free text is untrusted data and uses heuristic redaction, not an instruction or a guarantee of secret removal. Limits are bounded source defaults, not SLOs.

Each Alertmanager call opens an MCP session; this source leaves it to normal server expiry. It does not issue a session deletion request.

As of 2026-10-30 (OP313), Alertmanager items also carry `response`, the shared
portal/MCP projection of existing responder order and canonical claim evidence.
`response.identity` contains the fingerprint and original `startsAt`; a refire
changes that identity and the content revision. The legacy `key` still selects
a fingerprint in the current snapshot, so callers must check the start identity
and use `expectedRevision` to detect a changed observation. The projection has
order/claim source conditions and timestamps, an existing order attempt and
canonical reference, owner, state/time and safe evidence link. It has no create
or execution method. Production has no order or claim adapters or configuration
to enable them: joined fields stay `Unknown`, and controls remain disconnected.
Only synthetic fixtures exercise joined states. Gatus checks are unchanged.
