# 07: access broker

**Status:** ready for the docs-only step 6 spike after plan 02's in-cluster CLI acceptance passed (2026-10-08), under Tom's Codex work order and workflow correction (README decisions 44 and 45, Q-17). No laptop setup or test blocks it. Steps 1 to 5 are built; H2 (the broker Deployment) is deployed and verified (2026-10-07). Resume with the approval spike inside the Claude Code app (Q-16), then the complete v1 capability parity check, then keeper SSH minting (step 8/H5, Q-15 A). A possible session-management web UI changes neither the approval ruling nor this priority order. The spike is in [issue #91](https://github.com/thaynes43/dev-env/issues/91). Draft code is on branch `agent/plan07-approvals-round3` (dev-env) and `agent/plan07-catalog-round3` (haynes-ops); PR #90 and haynes-ops #3550 remain closed.
**Depends on:** 01 (the baseline guard and egress tiers are in place); Q-07 (Tom
2026-10-06: credential grants, A)
**Parallel with:** 02 in the architecture; this run finishes 02 first (README
decisions 40, 44 and 45). Keep shared-code changes small and rebase before each push.

## Goal

Agents ask for more than the baseline and get it for a while: a namespace role, a
LAN or in-cluster destination, a credential, or break-glass. Tom approves from his
phone, or a standing policy in git approves at once. Everything is time-boxed and
audited, and the headlamp path is no longer needed. DESIGN-001 6.12, D-23 to D-27.

## Progress

One PR per step, in this order. Tick a step in the PR that lands it. The broker
runtime is deployed (H2), with its core catalog and RBAC (H1). Only standing
policies in git can approve requests until step 6 provides human approval. Plan 02
changes the same reconciler, API and agentd, so each step stays small and rebases
on main before it merges.

In this repo:

- [x] 1. The `AccessGrant` and `GrantPolicy` CRDs, with an envtest suite that proves
  each rule, and this list (D-54).
- [x] 2. `/v1/grants` in the operator's API: `POST`, `GET` (list and one), `DELETE`
  (release, by setting `spec.release`). The requester is the calling session; at
  most 3 pending per session; an identical request returns the pending or active
  grant it matches; the wire types in `apiv1`. The operator's `dev-agents` Role
  gains AccessGrant create, get, list and patch, and no status (D-56). haynes-ops H1
  grants `watch` with them, for step 5's backstop.
- [x] 3. The broker mode, kube grants: `dev-env-operator broker` with its own Lease;
  the policy match (break-glass and profile `ops` never match); the 30-minute
  timeout; the ServiceAccount, the bindings (RoleBindings per namespace, a
  ClusterRoleBinding for the cluster-wide roles) and a TokenRequest for the grant's
  ServiceAccount (not bound to the session's pod: the API server refuses that for a
  pod that runs as another ServiceAccount); revoke at expiry, on release and when
  the session ends. An envtest suite runs it under exactly the RBAC haynes-ops gives
  it: it binds catalog roles only, and a revoked grant's token is refused (D-61).
- [x] 4. Installing a kube grant (D-63): new session pods get a memory-backed
  `grants` volume and `KUBECONFIG`; `agentd ctl grant-install|grant-remove|grant-list|grant-use`
  keep private grant files and a kubeconfig with `default` and grant contexts. The
  broker installs by exec with the token on stdin, repeats for a new pod UID and
  fails/revokes after three attempts when an older pod lacks the grants directory.
  A broker upgrade never restarts a session. The kubeconfig names the API server by
  `KUBERNETES_SERVICE_HOST` and `_PORT`, because session pods use `ndots:1`.
- [x] 5. Egress grants (D-64): one CiliumNetworkPolicy per grant, selecting the
  session and excluding rescue hold pods; separate destination rules with the
  requested ports. The broker revokes on expiry/release/session end; the operator's
  separate backstop uses same-name reads and UID-precondition deletes at expiry
  when the broker is unavailable. Its CNP Role has `get, delete`, applied in H2
  before the new operator pin. Unit/fake-clock and envtest cases prove ownership,
  restart, finalizer delays, late creation and the exact RBAC.
- [ ] 6. **Superseded by Q-16 (2026-10-08): do not build as written.** Approvals move into the Claude Code app (design spike, issue #91); the Pushover and web page below are ruled out, and the approval surface is redesigned. The original text, for the parts that carry over (the request shape, the audit record, break-glass freshness): the approval page and Pushover: the broker's console port behind Authentik
  (Approve, Approve for less time, Deny, the request as a GrantPolicy snippet), a
  fresh login for break-glass, one Pushover message per request (high priority for
  break-glass).
- [ ] 7. `agent-run grant request|list|show|use|release` and `agent-run breakglass`;
  agentd's built-in `dev-env` MCP server with `request_access`, `grant_status` and
  `release_access`.
- [ ] 8. Credential grants (Q-07): the keeper mints the Proxmox token or signs an SSH
  certificate, installs it in the pod's `grants` volume, and removes it at expiry.
  The first check failed on 2026-10-07: the operator token cannot mint an expiring
  token for its own user (Proxmox answered 403 to the list and the create). Q-15 is
  ruled (Tom 2026-10-07, A), so this step is unblocked: the keeper signs itself an
  SSH certificate from its own CA, runs `sudo pvesh create
  /access/users/dev-env@pve/token/<grant> --expire <end> --privsep 0` on a Proxmox
  node, installs the token in the pod, and deletes it at expiry. No new Proxmox user,
  and the long-lived operator token is not needed by v2. The keeper needs port 22 to
  the nodes. Until the step is built, a Proxmox credential grant is refused (fails
  closed); hw-ssh certificates do not wait for it.
- [ ] 9. `POST /v1/fleet/nodes/{node}/evacuate` and `agent-run fleet evacuate`.
- [ ] 10. The end of break-glass: the broker sends Tom the audit list of what the
  grant created, from Loki. The forced refresh of both logins waits for plans 03 and
  04, which give the keeper the logins.

In haynes-ops:

- [x] H1 core. The CRD copies; the namespace/cluster grant role catalog; the broker's
  ServiceAccount and RBAC; the operator's AccessGrant rights.
- [ ] H1 break-glass catalog. Generate `-breakglass` from API discovery with a CI
  check before the human approval path can issue it.
- [x] H2 runtime. The broker Deployment, its network policies and the operator's
  CNP get/delete permission (haynes-ops #3542). Signed operator/broker
  `sha-763fe77` and agent `2.5.0` are deployed; existing sessions are preserved.
- [ ] H2 approval deployment (step 6). **Superseded by Q-16 (2026-10-08): do not deploy as written;** it follows the redesigned step 6. As first written: the Pushover ExternalSecret (v1's item
  `upgrade-gate`); the approval page's IngressRoute on an external host, with its
  Authentik blueprint.
- [ ] H3. The day-one GrantPolicy set, which approves nothing beyond v1 (DESIGN-001
  6.12): at most credential grants for haynes-ops sessions, since every v1 session
  holds both credentials today.
- [ ] H4. The API server's audit lines for `grant-*` to Loki. Nothing ships the audit
  log today (Talos writes it on each control-plane node).
- [ ] H5. Credential grants: the keeper's SSH CA as an ExternalSecret in
  `dev-env-system` (not the Proxmox operator token: Q-15 A mints over SSH); the CA's
  public key trusted by the Proxmox nodes and HaynesTower; a network policy for the
  keeper's egress on port 22 to the Proxmox nodes. v2's `dev-agents` never had the token or the key: profile
  `full` already leaves them out.
- [ ] The acceptance run below, and the break-glass half of S-12.

## Round 1 runtime verification (2026-10-07)

Code PRs #84 (D-63) and #86 (D-64) shipped in signed agent `2.5.0` and
operator/broker `sha-763fe77`. [haynes-ops #3542](https://github.com/thaynes43/haynes-ops/pull/3542)
deployed the broker and bounded scratch policies; [#3543](https://github.com/thaynes43/haynes-ops/pull/3543)
stopped it for the actual expiry test; [#3544](https://github.com/thaynes43/haynes-ops/pull/3544)
restored two replicas/PDB minimum 1 and pruned the fixtures.

One size-S idle local session proved baseline ConfigMap patch denial and blocked
HTTP egress; actual tmpfs with private grant files; kube context access only to the
scratch namespace; release invalidating the held token (401) and removing files;
and egress release removing access. For `grant-1007-232214-4f02`, the operator was
restarted after approval and the broker had zero pods through expiry. Its expiry
controller deleted the policy at 23:32:14.018Z; the exact CNP was NotFound and HTTP
was blocked while the audit phase stayed Active. The restored broker ended it
Expired before the session was reaped. The session's home PVC and scratch namespace
are gone; shared storage remains. Session/v1 UIDs and restart counts were unchanged
through every rollout. Ended grants remain as audit records. This does not complete
human approval, credential grants or the break-glass half of S-12.

## In this repo

- CRDs `AccessGrant` and `GrantPolicy`. Validation refuses any grant or policy that
  targets `dev-env-system`, `dev-agents` or `dev-tools`, and any GrantPolicy for type
  `breakglass` or roles `dev-env-grant-breakglass` and `dev-env-grant-secrets-read`.
  The broker re-checks the same rules.
- `POST/GET/DELETE /v1/grants` in the operator API. The requester comes from the
  caller's token, never the body. At most 3 pending requests per session; identical
  requests merge; unanswered requests are denied after 30 minutes.
- The broker mode of the operator binary (`dev-env-operator broker`): policy match,
  Pushover message with the approval link, the approval page (Approve, Approve for
  less time, Deny; show as a GrantPolicy snippet), materialise and revoke:
  - `kube`: ServiceAccount `grant-<id>`, bindings to a catalog role, a TokenRequest
    token with the grant's TTL installed by `agentd ctl grant-install` as kube
    context `grant-<id>`; at expiry delete the ServiceAccount and bindings;
  - `egress`: a CiliumNetworkPolicy selecting the session's label;
  - `breakglass`: a `kube` grant on `dev-env-grant-breakglass`, at most 1 h, Tom
    only, fresh Authentik login (5 minutes), Pushover at high priority; at expiry the
    keeper forces a refresh of both logins and the broker sends Tom the audit list of
    objects the grant created;
  - `credential` (Q-07, Tom 2026-10-06): the keeper installs a Proxmox API token for
    `dev-env@pve` that expires with the grant, or an hw-ssh certificate from its SSH
    CA valid for the grant's TTL, into the pod's tmpfs, and removes it at expiry.
    The operator token cannot mint expiring tokens (checked 2026-10-07), so Q-15
    (Tom 2026-10-07, A) has the keeper mint over SSH with `sudo pvesh create
    /access/users/dev-env@pve/token/<grant> --expire <end> --privsep 0`. Until that
    is built, Proxmox credential grants fail closed (refused, Tom told). The
    long-lived operator token never enters a session pod.
- The operator's backstop: delete expired grants' network policies if the broker is
  down; re-install active grants after a drain.
- agentd: `grant-install`, kube contexts, and the built-in `dev-env` MCP server's
  `request_access`, `grant_status` and `release_access` tools.
- `agent-run grant request|list|use|release` and `agent-run breakglass`.
- Tests: no path in the operator's ServiceAccount can bind a role; the broker can
  bind only catalog roles; an expired kube grant's token is refused by the API
  server; neither the operator nor the broker can write a
  `CiliumClusterwideNetworkPolicy`.

## In haynes-ops (GitOps PRs; none touch `apps/dev/dev-env/app/resources/**`)

- Broker Deployment and ServiceAccount in `dev-env-system`; its RBAC with `bind` on
  the catalog by `resourceNames`; its CNPs (traefik to the approval port only, the
  operator on 8443, egress to the API server and `api.pushover.net`).
- The grant role catalog: ClusterRoles `dev-env-grant-workloads`, `-storage`,
  `-secrets-read`, `-nodes` and `-breakglass` (DESIGN-001 6.12; no `edit`, `admin` or
  `cluster-admin`). `-breakglass` is generated from API discovery minus its exclusion
  list, with a CI check that regenerates it when the cluster gains an API group.
- `dev-env-identity-guard`, matching every `dev-agents` identity except the
  workbench, if plan 01 did not already ship it.
- The approval page's ingress on an external host behind Authentik, and its
  Authentik application.
- The day-one GrantPolicy set (nothing beyond v1), and the Pushover credential for
  the broker as an ExternalSecret.
- Ship the API server audit lines for `system:serviceaccount:dev-agents:grant-*` to
  Loki, if they are not shipped already.
- Credential grants (Q-07): the keeper's SSH CA public key trusted by the `dev-env`
  user on the Proxmox nodes and by root on HaynesTower (`TrustedUserCAKeys`; Unraid
  keeps its sshd config on the flash drive), set through hw-ssh with
  `declare-activity`; the Proxmox operator token and the hw-ssh key moved from the
  `dev-agents` Secrets to the keeper's namespace; profile `full` no longer mounts
  them. A GrantPolicy may approve credential grants for named repos (for example
  haynes-ops ops sessions), so routine Proxmox work does not ping Tom.

## Acceptance

- A session requests role `dev-env-grant-workloads` in one namespace for 15 minutes.
  Tom gets one Pushover message, approves on the page, the agent patches a
  Deployment's image there, and 15 minutes later the same command is refused.
- A request that matches a standing GrantPolicy is approved with no message and is
  recorded under the policy's name.
- An egress grant to a LAN address works for its TTL and is gone after.
- `agent-run breakglass` works only after Tom's fresh approval, and every action
  under it appears in the audit log as `grant-<id>`. Under it, a Secret read, a
  TokenRequest, a RoleBinding create and any write in the three dev-env namespaces
  are all refused.
- A grant request that targets a dev-env namespace is refused at creation.
- Under break-glass, creating a Flux Kustomization, an ExternalSecret, a pod that
  mounts a Secret, or deleting a CRD are all refused.
- `agent-run fleet evacuate <node>` moves a cordoned worker's sessions (rescue first)
  and tool instances; a `dev-env-grant-nodes` drain then completes. Evacuate is
  refused for a caller without a nodes or break-glass grant, and for a node that is
  not cordoned.
- A GrantPolicy for `dev-env-grant-workloads` with a wildcard namespace is refused
  at apply time.
- A Job with `serviceAccountName: headlamp` and exec into the headlamp pod are both
  refused for the agent ServiceAccount.
- A broker restart during an active grant changes nothing for the session.
- Every grant of the test run is listed by `agent-run grant list --all` with
  requester, approver and times.
