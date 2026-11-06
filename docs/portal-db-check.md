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
| `FAIL auth` | 3 | the server refused the login (role, password or database) |
| `FAIL timeout` | 3 | the 5 s connect timeout, the 3 s query deadline or the 10 s total deadline expired |
| `FAIL query` | 3 | logged in, but the session is read-only or `SELECT 1` failed |

Exit code 2 means a usage error: `db-check` takes no other argument and no
flag. The output never contains a host, user, password, DSN, certificate
detail or driver error.

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

## Run it in the cluster: one-shot Job

Proposed spec. It is not created in haynes-ops; release and GitOps
belong to the coordinator.

- It needs a portal image built from a commit that contains `db-check`. The
  Deployment's current pin, `sha-ce6edc5`, predates it.
- It uses the Deployment's ServiceAccount, its labels for the existing
  network policy, and its existing config, Secret and CA volumes, unchanged.
  It adds no Secret, RBAC rule or network policy.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  generateName: dev-env-v2-portal-db-check-
  namespace: dev-agents
  labels:
    app.kubernetes.io/component: db-check
spec:
  backoffLimit: 0
  activeDeadlineSeconds: 60
  ttlSecondsAfterFinished: 3600
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
          image: ghcr.io/thaynes43/dev-env-portal:sha-<short>@sha256:<digest of an image with db-check>
          imagePullPolicy: IfNotPresent
          args: ["db-check"]
          # No container port: the portal Service targets the named port
          # "https", so this pod never becomes its endpoint. The probe on the
          # unused port keeps the pod never-Ready, so the PDB's healthy count
          # is unchanged.
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
            - name: config
              mountPath: /etc/dev-env-portal/config
              readOnly: true
            - name: secrets
              mountPath: /etc/dev-env-portal/secrets
              readOnly: true
            - name: ca
              mountPath: /etc/dev-env-portal/ca
              readOnly: true
            - name: tls
              mountPath: /etc/dev-env-portal/tls
              readOnly: true
            - name: tmp
              mountPath: /tmp
      # The Deployment's volumes, unchanged.
      volumes:
        - name: config
          projected:
            defaultMode: 288
            sources:
              - configMap:
                  name: dev-env-v2-portal-config
                  items:
                    - {key: origin, path: origin}
                    - {key: issuer, path: issuer}
                    - {key: authorization-url, path: authorization-url}
                    - {key: token-url, path: token-url}
                    - {key: userinfo-url, path: userinfo-url}
                    - {key: jwks-url, path: jwks-url}
                    - {key: openproject-base-url, path: openproject-base-url}
                    - {key: operator-api-url, path: operator-api-url}
                    - {key: auth-style, path: auth-style}
                    - {key: login-ttl, path: login-ttl}
                    - {key: idle-ttl, path: idle-ttl}
                    - {key: absolute-ttl, path: absolute-ttl}
                    - {key: fresh-for, path: fresh-for}
                    - {key: clock-skew, path: clock-skew}
                    - {key: pages-alertmanager.json, path: pages-alertmanager.json}
                    - {key: session-actions.json, path: session-actions.json}
              - secret:
                  name: dev-env-v2-portal-allowance-trust
                  items:
                    - {key: provider-allowances.json, path: provider-allowances.json}
        - name: secrets
          projected:
            sources:
              - secret:
                  name: dev-env-v2-portal-oidc
                  items:
                    - {key: OIDC_CLIENT_ID, path: OIDC_CLIENT_ID}
                    - {key: OIDC_CLIENT_SECRET, path: OIDC_CLIENT_SECRET}
              - secret:
                  name: dev-env-v2-portal-runtime
                  items:
                    - {key: DB_USERNAME, path: DB_USERNAME}
                    - {key: DB_PASSWORD, path: DB_PASSWORD}
                    - {key: SESSION_AEAD_KEY, path: SESSION_AEAD_KEY}
                    - {key: SESSION_GENERATION, path: SESSION_GENERATION}
              - secret:
                  name: dev-env-v2-portal-openproject
                  optional: true
                  items:
                    - {key: OPENPROJECT_TOKEN, path: OPENPROJECT_TOKEN}
              - serviceAccountToken:
                  path: OPERATOR_TOKEN
                  audience: dev-env-operator
                  expirationSeconds: 3600
        - name: ca
          projected:
            sources:
              - configMap:
                  name: dev-env-v2-portal-postgres-ca
                  items:
                    - {key: ca.crt, path: ca.crt}
              - configMap:
                  name: dev-env-v2-portal-issuer-ca
                  items:
                    - {key: ca.crt, path: issuer-ca.crt}
              - configMap:
                  name: dev-env-v2-portal-openproject-ca
                  items:
                    - {key: ca.crt, path: openproject-ca.crt}
              - configMap:
                  name: dev-env-api-ca
                  items:
                    - {key: ca.crt, path: operator-ca.crt}
        - name: tls
          secret:
            secretName: dev-env-v2-portal-tls
            items:
              - {key: tls.crt, path: tls.crt}
              - {key: tls.key, path: tls.key}
        - name: tmp
          emptyDir:
            sizeLimit: 16Mi
```

Read the result:

```sh
kubectl -n dev-agents logs job/<generated name>   # PASS, or FAIL <class>
kubectl -n dev-agents get job <generated name> -o jsonpath='{.status.succeeded}{.status.failed}'
```

**Notes**

- **Least privilege:** `db-check` reads only `secrets/DB_*` and
  `ca/ca.crt`. A narrower variant would mount only the
  `dev-env-v2-portal-runtime` DB keys and the Postgres CA ConfigMap.
- **Anti-affinity:** the Deployment's required anti-affinity matches the pod
  label. While the Job runs, a new portal replica cannot be scheduled on the
  Job's node, so run it outside a portal rollout.
- **Connections:** the Job uses one of the role's 8 connections. Two
  portal replicas use at most 4.

## Tests

```sh
GOTOOLCHAIN=local GOMAXPROCS=2 nice -n 19 go test -p 2 -run 'DBCheck|PortalCommand' ./web/portal/session/ ./cmd/dev-env-portal/
```

The tests use in-process fake Postgres servers on `net.Pipe` and a resolver
interceptor. They open no socket and contact no real database.
