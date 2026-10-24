# M6 same-connection recovery fence — source acceptance pending

Story [178](https://work.haynesnetwork.com/work_packages/178), under Epic 120 / Saga 85.
Fresh baseline `d4d21ef9d8f6682cabe1a45b78f5fd3726f50ef1` includes accepted
PR 529. Sole source implementer dev-env-1023-205407; one implementer, no children.
Root owns independent final-head native review, required CI and authorized merge.
UI, infra, #177/#160/#122/Future121 and V1 remain separately owned.

The external record is exactly ConfigMap
`dev-agents/dev-env-v2-portal-admission`; runtime has only GET on that resourceName.
No controller or per-session Kubernetes objects. Extra narrow RBAC is pending;
this PR grants/provisions/deploys nothing. Runtime assembly remains 503.

The contract replaces generation-only checking. Acquire and begin once. Before
session-table SQL, measure system identifier, active WAL timeline from
`pg_walfile_name(pg_current_wal_lsn())`, postmaster start, server address/port and
writable/non-recovery using the SAME `pgx.Tx`. Compare to one fresh linearizable
API GET, with resourceVersion absent (never 0), fixed trusted API CA/token and
fixed object path. Require exact pinned witness UID, resourceVersion and all
admission fields again immediately before and after COMMIT. Any uncertainty
returns unavailable and discards results; no automatic write retry.

Record data is a strict string schema: `schema=1`, `state=open|closed`,
`generation`, `system_identifier`, `timeline` (eight uppercase hex digits),
`postmaster_started_at` (UTC RFC3339Nano), `server_address` (canonical IP),
`server_port` (canonical decimal). Missing/error/closed/malformed/recreated or
mismatched witness denies. Recreated UID needs separate approved configuration
and closed recovery; observing a new object cannot silently admit it.

Recovery owner closes admission before restore, measures the actual new primary,
rotates AEAD key AND an independent fresh generation through the approved secret
path, and CAS-opens the exact incarnation. Never reuse generation or restore it
from backup. Logical restore on unchanged server and control-plane/witness restore
require explicit CLOSED admission; identity functions alone cannot detect those.
Runtime cannot edit the witness. No shared CNPG policy changes or production
failover are authorized. PostgreSQL role/function proof remains disposable/live
acceptance; do not add pg_monitor/admin speculatively.

First reviewable milestone: direct reader, same-transaction protocol, strict
negative/boundary fixtures and docs. Hosted disposable asynchronous failover must
lose an acknowledged DELETE with witness unchanged and show resurrected session
and callback denial before any observer. All unrun proof remains explicit.
Source success cannot release witness/DB/auth/isolation/Gate A or shared Authentik
logging holds.

Per-replica witness GET admission is a token bucket checked before a concurrency
slot and before the request. The ceiling is 30 GETs/second with burst 57, from
the measured 21 GETs per protected read and 9 per login, one read per second,
the existing one login per second, and login burst 4. Two replicas are 60
GETs/second and burst 114. Over budget sends no request and does not retry.
Non-positive, non-finite, or above-ceiling configuration is unavailable. Loopback
latency is not treated as live capacity.

The code client token URL is only `/application/o/token/`. Issuer HTTP
configuration rejects `revoke` and `introspect` paths. Rejecting `end-session`
as well remains optional and is not added; no new IdP call is introduced.
The hosted PostgreSQL go-test step sets `PORTAL_REQUIRE_DISPOSABLE_PROOF=1`
in that step's environment, so a missing database or failover input fails
the test process instead of skipping. A value set only on an earlier step
does not. The same acquired SQL connection, pre-commit witness, and
post-commit witness recheck are unchanged. Unauthenticated cookie misses can
exhaust the shared witness budget and make logout unavailable. A committed
write can still be reported unavailable when the post-commit read is over
budget. One fence per process, and two replicas for the 60/114 figure, are
activation assumptions, not enforced wiring.

Local evidence so far: official Go 1.27.2 archive checksum verified, compiler
binary SHA256 `548d3c32f83fa81152f543eb1526be7884cfe0526eb5e9ce29d3eca14786e7e0`.
Focused portal tests and vet pass; selected transaction/current-membership
`-race` passes. Local PostgreSQL explicitly skips without a disposable DB. The focused API RBAC
test passed against a locally provisioned disposable Kubernetes 1.35 API server. Hosted interim results are in [EVIDENCE.md](EVIDENCE.md); fresh final-head CI and
independent native review remain pending. A Go test parses this workflow with
the existing `sigs.k8s.io/yaml` module and requires the proof variable in the
go-test process environment. Valid YAML alone does not prove that placement.

References: PostgreSQL 16 [control/WAL functions](https://www.postgresql.org/docs/16/functions-admin.html)
and [LOCK privileges](https://www.postgresql.org/docs/16/sql-lock.html);
Kubernetes 1.35 [most-recent GET semantics](https://v1-35.docs.kubernetes.io/docs/reference/using-api/api-concepts/).
The API can internally serve a consistent GET through its supported cache;
there is no application cache, informer, object-file or resourceVersion=0 request.

Actual disposable proof and measured request costs: [EVIDENCE.md](EVIDENCE.md).
