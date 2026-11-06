# Portal database check

`dev-env-portal db-check` checks one thing: whether the portal can reach its
database with its mounted production settings. It opens one TLS session to
the portal database, runs a read-only `SELECT 1` and closes. It does not start
the portal, its routes, OIDC or migrations, and it never writes. Design:
[DESIGN-056](../.agents/sagas/distributed-dev-env/designs/056-portal-db-check.md);
story [OP477](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/477).

## What it prints

Exactly one line:

| Line | Exit code | Meaning |
| --- | --- | --- |
| `PASS` | 0 | connected, the session is read-write, `SELECT 1` returned 1 |
| `FAIL config` | 3 | `secrets/DB_USERNAME`, `secrets/DB_PASSWORD` or `ca/ca.crt` is missing or invalid, or an ambient `PG*` setting is present |
| `FAIL dns` | 3 | `postgres16-rw.database.svc.cluster.local` did not resolve |
| `FAIL refused` | 3 | the connection was refused or reset, or the network was unreachable |
| `FAIL tls` | 3 | the server refused TLS or its certificate failed verification against `ca/ca.crt` |
| `FAIL auth` | 3 | the TLS handshake succeeded, but the server ended the session before the login completed |
| `FAIL timeout` | 3 | the connect, query or total deadline expired |
| `FAIL query` | 3 | logged in, but the session is read-only or `SELECT 1` failed |
| `INTERRUPTED` | 4 | the check was cancelled from outside (SIGTERM, for example a node drain) before it finished |

`INTERRUPTED` is no verified result. It does not claim that no query ran,
only that the check did not finish; record it as not run. Only an expired
deadline is `FAIL timeout`.

`FAIL auth` does not only mean a bad password. It covers anything that ends
the session after the TLS handshake and before the login completes:

- a refused password or role (SQLSTATE class 28);
- a missing database (3D000);
- too many connections (53300);
- a server shutting down or starting up (57P01, 57P03);
- a dropped connection.

The check prints no SQLSTATE, so tell these apart from the database's logs.

Exit code 2 means a usage error: `db-check` takes no other argument and no
flag. The output never contains a host, user, password, DSN, certificate
detail or driver error.

**Bounds:** the operation has a 10 s total deadline, covering resolve,
connect, the read-only transaction and its rollback. Inside it:

- the connect has the store's 5 s `connect_timeout`;
- the read-only transaction has 3 s.

After a successful connect, the check closes its one TCP connection within
1 s; if the peer stalls, the check cuts the connection. So the check returns
within 11 s. The driver's own background cleanup may still be running when
the line prints. It can no longer reach the network (its cancel dial is
refused and its writes fail on the closed connection), and it ends with the
process. A failed connect is closed by the driver at once.

The check opens exactly one connection. If a deadline interrupts the query,
the driver would dial again to send a cancel request; the check refuses that
dial, so it never reaches the database.

## What it reads

It reads only these files under `/etc/dev-env-portal`, with the server's own
readers and rules:

- `secrets/DB_USERNAME`, which must be `dev_env_v2_portal_runtime`;
- `secrets/DB_PASSWORD`;
- `ca/ca.crt`.

The target is fixed in source:

- host `postgres16-rw.database.svc.cluster.local`, port 5432, database
  `dev_env_v2_portal`;
- `sslmode=verify-full`, verified against the pinned CA by key and subject,
  as the store does;
- `target_session_attrs=read-write`.

## Run it once in the cluster, through GitOps

The live run uses the normal GitOps route (coordinator decision, recorded on
OP477). It is not a direct `kubectl create`.

- **One fixed-name Job.** `dev-env-v2-portal-db-check-<yyyymmdd>`, named for
  the run date. It has no `ttlSecondsAfterFinished`, `backoffLimit: 0` and
  `activeDeadlineSeconds: 60`, and it runs the portal image by its signed
  digest.
  - **Image:** the image must be built from a commit that contains
    `db-check`. The Deployment's current pin, `sha-ce6edc5`, predates it.
    Verify the digest's signature as for any portal pin.
  - **Mounts:** only the existing keys `DB_USERNAME` and `DB_PASSWORD` of
    the existing Secret `dev-env-v2-portal-runtime`, and `ca.crt` of the
    existing ConfigMap `dev-env-v2-portal-postgres-ca`. No OIDC secret,
    session key, OpenProject token, TLS key or projected token.
  - **Identity:** the portal ServiceAccount, with
    `automountServiceAccountToken: false`.
  - **Network:** the pod label that the existing network policy selects. No
    new Secret, RBAC rule or network policy.
- **Its own small Flux Kustomization.**
  - It has `wait: false` and no health checks.
  - It depends only on the portal's network policy Kustomization.
  - The portal Kustomization does not depend on it.

  So a failed check cannot block or gate the portal.
- **No TTL, so the run cannot repeat.** Nothing deletes the Job, so Flux has
  nothing to re-create, and its single line and status stay readable until
  they are recorded.
  - Do not delete the Job by hand while its manifest is in git: Flux would
    re-create it and probe again.
  - The cleanup is a follow-up PR that removes the manifest, and Flux prunes
    the Job.
  - A Job's spec cannot change, so a later run is a new manifest with a new
    date.

A test (`TestDBCheckDocJobMountsOnlyWhatItReads`) parses the YAML below. It
pins the mounted files to the three `db-check` reads, the single-shot shape
and the Kustomization's independence from the portal.

The Job, for example
`kubernetes/main/apps/dev-env-system/portal-db-check/app/job.yaml` in
haynes-ops:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  # One fixed name per run; replace the date with the run date.
  name: dev-env-v2-portal-db-check-20261106
  namespace: dev-agents
  labels:
    app.kubernetes.io/component: db-check
spec:
  backoffLimit: 0
  activeDeadlineSeconds: 60
  template:
    metadata:
      labels:
        # Required: the existing CiliumNetworkPolicy dev-env-v2-portal selects
        # this label for DNS and postgres16:5432 egress.
        app.kubernetes.io/name: dev-env-v2-portal
        app.kubernetes.io/component: db-check
    spec:
      restartPolicy: Never
      serviceAccountName: dev-env-v2-portal
      automountServiceAccountToken: false
      enableServiceLinks: false
      dnsPolicy: ClusterFirst
      dnsConfig:
        options:
          - name: ndots
            value: "1"
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        fsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: db-check
          # The signed digest of a portal image that contains db-check.
          image: ghcr.io/thaynes43/dev-env-portal:sha-<short>@sha256:<signed digest>
          imagePullPolicy: IfNotPresent
          args: ["db-check"]
          # No container port: the portal Service targets the named port
          # "https", so this pod never becomes its endpoint. The probe on the
          # unused port keeps the pod never-Ready, so the PDB's healthy count
          # is unchanged (see "The portal PDB" below).
          readinessProbe:
            tcpSocket:
              port: 8443
            periodSeconds: 5
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              cpu: 100m
              memory: 64Mi
          volumeMounts:
            - name: db-secrets
              mountPath: /etc/dev-env-portal/secrets
              readOnly: true
            - name: postgres-ca
              mountPath: /etc/dev-env-portal/ca
              readOnly: true
      volumes:
        - name: db-secrets
          secret:
            secretName: dev-env-v2-portal-runtime
            defaultMode: 288
            items:
              - key: DB_USERNAME
                path: DB_USERNAME
              - key: DB_PASSWORD
                path: DB_PASSWORD
        - name: postgres-ca
          configMap:
            name: dev-env-v2-portal-postgres-ca
            defaultMode: 288
            items:
              - key: ca.crt
                path: ca.crt
```

Its own Flux Kustomization, for example
`kubernetes/main/apps/dev-env-system/portal-db-check/ks.yaml`, with a
`kustomization.yaml` in `app/` that lists `job.yaml`:

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: dev-env-v2-portal-db-check
  namespace: dev-env-system
spec:
  # Only the egress policy the Job relies on. The portal Kustomization does
  # not depend on this one, and this one does not gate on the Job.
  dependsOn:
    - name: dev-env-v2-portal-policy
      namespace: dev-env-system
  path: ./kubernetes/main/apps/dev-env-system/portal-db-check/app
  prune: true
  wait: false
  sourceRef:
    kind: GitRepository
    name: haynes-ops
    namespace: flux-system
  interval: 30m
  retryInterval: 1m
  timeout: 2m
```

### Preflight, immediately before the Job run

Recheck the portal PDB right before the PR with the Job merges. It must
still have an integer `minAvailable` and no `maxUnavailable` (see "The
portal PDB" under Notes):

```sh
kubectl -n dev-agents get pdb dev-env-v2-portal \
  -o jsonpath='{.spec.minAvailable}|{.spec.maxUnavailable}|{.spec.selector.matchLabels}{"\n"}'
# expect: 1||{"app.kubernetes.io/name":"dev-env-v2-portal"}
```

If it shows a percentage or a `maxUnavailable`, do not run the Job: do the
dedicated-label follow-up first. Also confirm no portal rollout is in
progress.

### Record the result, then clean up

After the PR with the Job merges, read the result with bounded waits. First
make sure Flux has created the Job: `kubectl wait` on a Job that does not
exist yet fails at once, and that would record a premature `NOT RUN`.

```sh
job=dev-env-v2-portal-db-check-20261106   # the manifest's name

# 1. Flux applies it: reconcile once, then wait at most 5 min for the Job.
flux -n dev-env-system reconcile kustomization dev-env-v2-portal-db-check --with-source --timeout=5m
if ! kubectl -n dev-agents wait --for=create --timeout=5m "job/$job"; then
  echo "$job NOT APPLIED (Flux did not create the Job; nothing ran)"; exit 1
fi

# 2. At most 90 s for Complete, then at most 5 s more for Failed.
kubectl -n dev-agents wait --for=condition=complete --timeout=90s "job/$job" \
  || kubectl -n dev-agents wait --for=condition=failed --timeout=5s "job/$job"

line=$(kubectl -n dev-agents logs "job/$job" --tail=1 2>/dev/null)
status=$(kubectl -n dev-agents get "job/$job" -o jsonpath='{.status.conditions[*].reason}')
case "$line" in
  "PASS"|"FAIL config"|"FAIL dns"|"FAIL refused"|"FAIL tls"|"FAIL auth"|"FAIL timeout"|"FAIL query") result="$line" ;;
  "INTERRUPTED") result="NOT RUN (interrupted; no verified result)" ;;
  "") result="NOT RUN (no output; $status)" ;;
  *)  result="UNEXPECTED OUTPUT (not recorded)" ;;
esac
echo "$job $result"
```

Record these in the tracker story or handoff before any cleanup:

- the Job name;
- the image digest;
- the time;
- `$result`.

`$result` is sanitized by construction: one of the eight result lines, or
`NOT RUN` (no output, or `INTERRUPTED`). `NOT APPLIED` means Flux did not create the Job; nothing ran, so
nothing is recorded as a check result. Never paste other log text. Then open the follow-up PR that
removes the Job's manifest and Kustomization; Flux prunes the Job and its
pod.

**No output and `INTERRUPTED` are "not run", not a db-check result.**

- `NOT RUN` means the check printed no line, or printed `INTERRUPTED`, so
  the database path is untested. Record it as not run, never as `PASS` or a
  db-check `FAIL`.
- `INTERRUPTED` means the run was cut short by its parent, for example
  SIGTERM during a node drain, and gives no usable database verdict. It is
  not proof that no query ran.
- The usual cause is that the pod never started: Kubernetes ends the Job
  with `DeadlineExceeded` after 60 s.
- A likely reason is scheduling. The Deployment's required anti-affinity
  matches the pod label, and that rule works both ways:
  - the Job cannot run on a node that already runs a portal replica;
  - a new portal replica cannot run on the Job's node.

  With four workers and two replicas it fits. Do not merge the Job during a
  portal rollout.

## Notes

- **The portal PDB.** It selects the same label, so it also selects the Job
  pod. Source:
  - haynes-ops `kubernetes/main/apps/dev-env-system/portal/app/pdb.yaml`
    at `3e92686c`;
  - an integer `spec.minAvailable: 1` and selector
    `app.kubernetes.io/name: dev-env-v2-portal`;
  - live on 2026-11-06: currentHealthy 2, desiredHealthy 1, expectedPods 2,
    disruptionsAllowed 1.

  This is harmless, for two reasons:
  - With an integer `minAvailable`, the disruption controller does not need
    a scalable controller for the Job pod. That requirement applies only to
    `maxUnavailable` or a percentage `minAvailable`.
  - The never-Ready pod adds one to expectedPods but changes neither
    currentHealthy, desiredHealthy nor disruptionsAllowed.

  **Recheck the PDB before use.** If it ever uses `maxUnavailable` or a
  percentage, the Job pod would put it in `SyncFailed` with
  `disruptionsAllowed: 0` until the Job is pruned. Then the dedicated-label
  follow-up below must come first.

- **Connections:** the Job uses one of the role's 8 connections. Two portal
  replicas use at most 4.
- **Follow-up, not in this change:** a cleaner long-term setup would give the
  check its own pod label, with a small policy allowing only DNS and
  Postgres egress. The check would then no longer share the portal's label,
  Service selector, PDB and anti-affinity.

## Tests

```sh
GOTOOLCHAIN=local GOMAXPROCS=2 nice -n 19 go test -p 2 -run 'DBCheck|PortalCommand' ./web/portal/session/ ./cmd/dev-env-portal/
```

The tests use in-process fake Postgres servers on `net.Pipe` and a resolver
interceptor. They open no socket and contact no real database.
