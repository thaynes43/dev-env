# DESIGN-056: `dev-env-portal db-check`

- **Status:** Proposed, 2026-11-06.
- **Tracking:** [Story OP477](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/477).
- **Origin:** Tom, relayed: "More friction that we didn't have before. You
  need to smooth over the process." Checking the portal's database path today
  means reading pod logs or starting the whole portal.
- **Builds on:** [DESIGN-029](029-portal-foundations.md) (the portal and its
  Postgres store), `pages-source-check` in `cmd/dev-env-portal`.
- **How-to:** [docs/portal-db-check.md](../../../../docs/portal-db-check.md).

## 1. Goal and non-goals

One command answers one question: can this portal, with its mounted
production database settings, open a session to its database and run a
read-only query? It prints `PASS`, `FAIL <class>` or `INTERRUPTED`, and nothing else.

Non-goals: running the portal, its routes, OIDC, migrations or any store
write; reading or printing a DSN, host, user, password, certificate detail
or driver error; a new Secret, RBAC rule or network policy; creating the Job
(release and GitOps belong to the coordinator).

## 2. Behaviour

`dev-env-portal db-check` takes no other argument and no flag; anything else
is the usage error (exit 2), as for `pages-source-check`.

1. **Load.** It reads the files the server reads for its database, with the
   same readers and rules as `LoadProduction`: `secrets/DB_USERNAME` (must
   be `dev_env_v2_portal_runtime`), `secrets/DB_PASSWORD` (printable
   bearer text) and `ca/ca.crt` (at least one certificate). It reads no
   other file, no OIDC setting and no session key.
2. **Connect.** It builds the connection with the store's own code:
   `postgresRoots` (the pinned CA joins by key and subject with its validity
   window opened) and `postgresPoolConfig`. So the target is fixed:
   - host `postgres16-rw.database.svc.cluster.local`, port 5432;
   - database `dev_env_v2_portal`;
   - `sslmode=verify-full` with server-certificate verification, and
     `target_session_attrs=read-write`;
   - no fallback, and no ambient `PG*` files.

   It opens one connection, not a pool.
3. **Query.** In a read-only transaction it runs `SELECT 1`, checks the
   value, rolls back and closes. With the read-write check that pgx runs for
   `target_session_attrs=read-write`, the server receives exactly
   `show transaction_read_only`, `begin read only`, `SELECT 1` and
   `rollback`.
4. **Bounds.** The operation has a 10 s total deadline, from resolve
   through rollback. Inside it, the connect has the store's 5 s
   `connect_timeout` and the read-only transaction 3 s; the store's
   `statement_timeout` of 5 s also applies on the server.

   After a successful connect, the close has its own deadline of at most
   1 s. Within 1 s the check closes its one TCP connection. It waits for
   pgx's `CleanupDone`, not for `Close` to return or for `IsClosed`, and at
   four fifths of the bound it closes the dialed transport itself.

   pgx's own background cleanup may still be running when the line prints.
   It can no longer reach the network: its cancel dial is refused and its
   writes fail on the closed connection. It ends with the process.

   Two things would otherwise outlast the bound on a stalled peer:
   - after a deadline, pgx's `asyncClose` drains under its own 15 s
     deadline;
   - crypto/tls writes its close_notify alert under its own 5 s write
     deadline.

   Closing the dialed transport makes both fail at once. The check
   therefore returns within 11 s.

   The check opens exactly one connection. When a deadline or a caller
   cancel interrupts a statement, pgx abandons the connection in a background goroutine
   (`pgconn.asyncClose`). That goroutine first dials the server again to
   send a cancel request, then sends Terminate, drains and closes. The
   check's dialer refuses every dial after the first successful one, so that
   cancel dial never reaches the network, and pgx goes on to send Terminate
   on the original connection. The refusal happens after the result is
   known, so it changes neither the result nor the line.

   For the same reason, if the name resolved to several addresses, only the
   first address that accepts a TCP connection is checked. The cluster
   Service name resolves to one address. Tests pin all of this:
   - exactly one dial on every deadline path, with the refused cancel
     attempt counted;
   - the TCP connection closed within the bound (in the fixture pgx's
     cleanup has also finished by then; that is a test observation, not a
     production guarantee);
   - with two addresses, the first connection's class.

## 3. Output and exit codes

| Output | Exit | Meaning |
| --- | --- | --- |
| `PASS` | 0 | connected, the read-write check passed, `SELECT 1` returned 1, and the read-only transaction rolled back successfully |
| `FAIL config` | 3 | a mounted file is missing or invalid, or an ambient `PG*` setting is present |
| `FAIL dns` | 3 | the fixed host did not resolve |
| `FAIL refused` | 3 | the TCP connection was refused or reset, or the network was unreachable |
| `FAIL tls` | 3 | TLS was refused or the server certificate failed verification |
| `FAIL auth` | 3 | after a good TLS handshake, the server ended the session before the connect, including the read-write check, completed |
| `FAIL timeout` | 3 | a connect, query or total deadline expired |
| `FAIL query` | 3 | logged in, but the session is read-only; or `BEGIN`, `SELECT 1` or the rollback failed with a server error or a dropped connection; or `SELECT 1` returned another value |
| `INTERRUPTED` | 4 | the caller cancelled the check (SIGTERM, for example during a node drain) before it finished |

A usage error (an extra argument or flag) prints nothing on stdout,
`portal usage unavailable` on stderr, and exits 2. If the one line cannot be
written to stdout, the command exits 1 (`cmd/dev-env-portal/main.go`), which
is no result.

`INTERRUPTED` is no verified database result. It does not claim that no
query ran, only that the check did not finish.

**The verdict boundary** (coordinator ruling, 2026-11-06). The class is
decided when the operation ends, before the close.

- `PASS` is decided only when the read-only transaction finishes with a
  successful rollback. A caller cancel that ends the rollback, even after a
  good `SELECT 1`, is `INTERRUPTED`.
- A `FAIL` decided at `BEGIN` or at `SELECT 1` survives a caller cancel
  during the rollback or the close.
- A cancel during the close never changes the class.
- Only an operation that ended because the caller cancelled is
  `INTERRUPTED`.

The class is fixed when `dbCheckEnd` classifies the ending error. It reads
the context's state at that moment, so there is a microsecond window
between a non-server error and its classification.

- A caller cancel in that window turns that `FAIL` into `INTERRUPTED`. The
  `FAIL` can be dns, refused, tls, auth or query: for example a DNS
  not-found, a refused connection, an x509 failure, a drop after TLS, or a
  dropped query.
- A deadline expiring in the same window gives `FAIL timeout` instead.
- `PASS` never passes through `dbCheckEnd`, so the result stays fail-safe:
  at worst no result, never a false `PASS`.

This is documented rather than changed.

A config fault is found before any network and stays `FAIL config` even
then. The command's context is SIGTERM-aware, and a cancelled context is
told apart from an expired deadline: only a deadline is `FAIL timeout`.

`auth` covers any end of the session after a good TLS handshake and before
the connect, including pgx's read-write check (`show transaction_read_only`),
completes, not only a bad password:

- a refused password or role (SQLSTATE class 28);
- a missing database (3D000);
- too many connections (53300);
- a server shutting down or starting up (57P01, 57P03);
- a dropped connection.

The class comes from the phase that failed and the error's type, never from
error text. The check records whether the name lookup, the dial and the TLS
handshake completed: pgx calls its `AfterNetConnect` hook only after TLS.
That gives the class without parsing driver messages. No error, address or
value is ever printed.

## 4. Running it in the cluster

The live run uses the normal GitOps route (coordinator decision, recorded on
OP477), not a direct `kubectl create`. The full manifests and the record and
cleanup steps are in the how-to.

- **One fixed-name Job,** `dev-env-v2-portal-db-check-<yyyymmdd>`, with
  `backoffLimit: 0`, `activeDeadlineSeconds: 60`, no
  `ttlSecondsAfterFinished`, and the portal image by its signed digest.
- **Its own small Flux Kustomization,** with `wait: false` and no health
  checks. It depends only on the portal's network policy, and the portal
  Kustomization does not depend on it. A failed check cannot block or gate
  the portal.
- **No TTL, so the run cannot repeat.** Nothing deletes the Job, so Flux
  never re-creates it, and its one line and status stay until they are
  recorded.
  - The result is read with bounded `kubectl wait` steps and recorded
    first.
  - The cleanup is a follow-up PR that removes the manifest; Flux prunes
    the Job.
  - A later run is a new manifest with a new date, since a Job's spec
    cannot change.
- **Mounts.** Only what `db-check` reads: the existing keys `DB_USERNAME`
  and `DB_PASSWORD` of the existing Secret `dev-env-v2-portal-runtime`, and
  `ca.crt` of the existing ConfigMap `dev-env-v2-portal-postgres-ca`. No OIDC
  secret, session key, OpenProject token, TLS key or projected token.

  The portal ServiceAccount stays, with `automountServiceAccountToken:
  false`. A test parses the how-to's YAML and pins the mounted files, the
  single-shot shape and the Kustomization's independence from the portal.
- **Label.** The pod carries `app.kubernetes.io/name: dev-env-v2-portal`.
  That is the only way the existing `CiliumNetworkPolicy` lets it reach
  Postgres and DNS, so no policy change is needed.
- **Service.** The same label is in the portal Service's selector, but the
  Service's `targetPort` is the named port `https`. The Job pod declares no
  container port, so no endpoint is created for it.
- **PDB.** The portal PodDisruptionBudget selects the same label, so it
  also selects the Job pod. Source:
  - haynes-ops `kubernetes/main/apps/dev-env-system/portal/app/pdb.yaml`
    at `3e92686c`;
  - an integer `spec.minAvailable: 1` and selector
    `app.kubernetes.io/name: dev-env-v2-portal`;
  - live on 2026-11-06: currentHealthy 2, desiredHealthy 1, expectedPods 2,
    disruptionsAllowed 1.

  This is harmless, for two reasons:
  - With an integer `minAvailable`, the disruption controller does not need
    a scalable controller for each selected pod. That requirement applies
    only to `maxUnavailable` or a percentage `minAvailable`. So the Job pod,
    which has none, does not make the PDB fail to sync.
  - The `readinessProbe` on the unused port 8443 keeps the Job pod
    never-Ready. While it exists, expectedPods rises by one, but
    currentHealthy, desiredHealthy and disruptionsAllowed do not change.

  Recheck the PDB's form immediately before the Job run (the how-to's
  preflight). If it ever uses `maxUnavailable` or a percentage, the Job pod would put it in `SyncFailed` with
  `disruptionsAllowed: 0` until the Job is pruned. Then the dedicated-label
  follow-up must come first.
- **Anti-affinity.** The Deployment's required anti-affinity matches the
  label, and required anti-affinity is honoured both ways:
  - the Job cannot be scheduled on a node that already runs a portal
    replica;
  - a new replica cannot be scheduled on the Job's node.

  With four workers and two replicas it fits. If it cannot be scheduled,
  the Job ends `DeadlineExceeded` after 60 s with no output line. That is
  recorded as "not run", never as a db-check failure.
- **Follow-up, not in this change:** the check could get its own pod label,
  with a small policy allowing only DNS and Postgres egress and no shared
  Service, PDB or anti-affinity.
- **Connections.** The role's `connectionLimit` is 8. Two replicas use at
  most 4, and the check adds one.

## 5. Tests

All tests run fixture-only, with in-process fake Postgres servers on
`net.Pipe`. No socket is opened and no real address is reached; a resolver
interceptor fails any DNS query.

- **Success.** The fake server checks the TLS server name and the startup
  user and database.
- **Expired pinned CA.** A pinned CA past its `NotAfter` still verifies, as
  production does.
- **TLS:** an unknown CA, a wrong hostname, or a server that refuses TLS.
- **Each other class:**
  - auth: 28P01, 28000, 3D000, 53300, 57P01 and 57P03;
  - timeout;
  - refused;
  - dns;
  - query: an error from `SELECT 1`, a read-only session;
  - config: each missing or invalid file, an ambient `PGSSLROOTCERT`.
- **Caller cancel** (`TestDBCheckCallerCancelIsInterruptedNotTimeout`): a
  cancel that ends the connect or the query is `INTERRUPTED`.
- **The verdict boundary** (`TestDBCheckDecidedResultSurvivesACallerCancel`):
  - a cancel during the close keeps `FAIL query` and `PASS`;
  - a cancel during the rollback keeps a `FAIL query` decided at
    `SELECT 1`;
  - a cancel that ends the rollback after a good `SELECT 1` is
    `INTERRUPTED`;

  Each case keeps one wire connection and a finished cleanup.
- **Query failures** (`TestDBCheckRollbackAndValueAreQueryFailures`): a
  rollback server error after a good `SELECT 1`, and a wrong `SELECT 1`
  value, are `FAIL query`. Each sends exactly the four statements, over one
  wire connection, with cleanup finished.
- **Bounded close:** a stalled close after a pass, a query deadline and the
  total deadline each return within their bounds with one sanitized line,
  and none dials a second connection.
- **The how-to's manifests:**
  - the Job has a fixed name and mounts exactly the three files `db-check`
    reads, with no TTL and no retry;
  - its Kustomization is independent of the portal and does not wait.
- **Sanitization:** the output of every case is exactly one of the lines
  above.
- **No-write:** the server sees exactly the four statements, over the simple
  protocol only.
- **Command:** arguments are refused before any work; exit codes are as
  above; the server path is unchanged.
