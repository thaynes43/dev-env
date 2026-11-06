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
read-only query? It prints `PASS` or `FAIL <class>` and nothing else.

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
4. **Bounds.** It has a 5 s connect timeout (the store's `connect_timeout`),
   a 3 s query deadline and a 10 s total deadline. The store's
   `statement_timeout` of 5 s also applies on the server.

## 3. Output and exit codes

| Output | Exit | Meaning |
| --- | --- | --- |
| `PASS` | 0 | connected, the read-write check passed, `SELECT 1` returned 1 |
| `FAIL config` | 3 | a mounted file is missing or invalid, or an ambient `PG*` setting is present |
| `FAIL dns` | 3 | the fixed host did not resolve |
| `FAIL refused` | 3 | the TCP connection was refused or reset, or the network was unreachable |
| `FAIL tls` | 3 | TLS was refused or the server certificate failed verification |
| `FAIL auth` | 3 | the server refused the login (role, password or database) |
| `FAIL timeout` | 3 | a connect, query or total deadline expired |
| `FAIL query` | 3 | logged in, but the session is read-only or `SELECT 1` failed |

The class comes from the phase that failed and the error's type, never from
error text. The check records whether the name lookup, the dial and the TLS
handshake completed: pgx calls its `AfterNetConnect` hook only after TLS.
That gives the class without parsing driver messages. No error, address or
value is ever printed.

## 4. Running it in the cluster

A one-shot Job in `dev-agents`:

- the pinned portal image digest, `args: ["db-check"]`;
- the portal ServiceAccount;
- the Deployment's existing projected config, Secret and CA volumes;
- `backoffLimit: 0`, `activeDeadlineSeconds: 60`, `ttlSecondsAfterFinished`
  and small CPU and memory limits.

The full spec is in the how-to.

- **Label.** The pod carries `app.kubernetes.io/name: dev-env-v2-portal`.
  That is the only way the existing `CiliumNetworkPolicy` lets it reach
  Postgres and DNS, so no policy change is needed.
- **Service.** The same label is in the portal Service's selector, but the
  Service's `targetPort` is the named port `https`. The Job pod declares no
  container port, so no endpoint is created for it.
- **PDB.** A `readinessProbe` on the unused port 8443 keeps the pod
  never-Ready, so the PodDisruptionBudget's healthy count is unchanged.
- **Anti-affinity.** The Deployment's required anti-affinity matches the
  label. While the Job runs, a new portal replica cannot be scheduled on the
  Job's node, so run it outside a portal rollout.
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
- **Each other class:** auth (28P01), timeout, refused, dns, query (an error
  from `SELECT 1`, a read-only session) and config (each missing or invalid
  file, an ambient `PGSSLROOTCERT`).
- **Sanitization:** the output of every case is exactly one of the lines
  above.
- **No-write:** the server sees exactly the four statements, over the simple
  protocol only.
- **Command:** arguments are refused before any work; exit codes are as
  above; the server path is unchanged.
