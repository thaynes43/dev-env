# 07: access broker

**Status:** docs-only approval spike complete, Q-18 awaiting Tom's route choice (2026-10-08, [R-03](../research/R-03-claude-code-approvals.md)). Plan 02's corrected in-cluster acceptance passed; no laptop setup blocks progress (README decisions 44 and 45, Q-17). Steps 1 to 5 are built; H2 is deployed and verified. The v1 parity audit is complete ([R-04](../research/R-04-v1-capability-parity.md)); all twenty gaps below block cutover. Baseline references and the rescue-mount fix are deployed (#108, haynes-ops #3584/#3583); a fresh full fixture passed presence/permission and read-only Omni/API checks. P-11/P-12 remain open for their complete service/network evidence. Build keeper SSH minting (step 8/H5, Q-15 A, D-69). Q-18 blocks only the human approval implementation; capabilities beyond today's tier remain unavailable. A possible management UI changes neither Q-16 nor this priority order. [Issue #91](https://github.com/thaynes43/dev-env/issues/91) tracks the spike. Draft branches `agent/plan07-approvals-round3` (dev-env) and `agent/plan07-catalog-round3` (haynes-ops) retain reusable pieces; #90 and haynes-ops #3550 remain closed.
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

- [x] Step 6's **docs-only redesign spike** (2026-10-08, R-03, Q-18).
  Native hook schemas, managed controls, candidate forgery paths and plan 03's
  dependency cycle are recorded. Guarded approval remains unproven; an ordinary
  requester/coordinator relay is a soft gate. No human adapter, new guard,
  approval policy or break-glass grant ships from the spike.

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
  the nodes. D-69 defines the disabled-by-default backend. Until owner trust, standing
  policies and acceptance pass, Proxmox grants remain refused. General hw-ssh
  additionally needs P-19's copied-certificate/connection contract.
  The Proxmox code is built under D-69 with broker and keeper minting disabled
  by default. It includes immutable UID-bound jobs, a keeper-only durable journal,
  final live checks before SSH dispatch, cleanup recovery, typed private agentd
  files and PVE helper selection. API deduplication and quota follow the session
  UID, and cleanup cannot exec into a same-name replacement. This is partial
  progress: step 8 stays open until activation and the full hardware scope pass.
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
  6.12): the complete OPERATOR and hardware parity set in R-04, across repos for full
  sessions. Ops gets its actual remediation scope; v1 ops lacks hardware write keys.
  Nothing beyond v1 is approved, and no parity operation waits on Tom.
- [ ] H4. The API server's audit lines for `grant-*` to Loki. Nothing ships the audit
  log today (Talos writes it on each control-plane node).
- [ ] H5. Credential grants: the keeper's SSH CA as an ExternalSecret in
  `dev-env-system` (not the Proxmox operator token: Q-15 A mints over SSH); the CA's
  public key trusted by the Proxmox nodes, HaynesTower and PiKVM; a network policy for the
  keeper's egress on port 22 to the Proxmox nodes. v2's `dev-agents` never had the token or the key: profile
  `full` already leaves them out.
  The CredentialJob schema, exact broker/keeper RBAC and admission split, and
  empty keeper-only journal are prepared separately in haynes-ops #3589, before
  the new image pins. That prerequisite does not provision a CA, trust, egress
  or standing policies, and does not enable minting.
- [ ] The acceptance run below, and the break-glass half of S-12.

## V1 parity checklist (2026-10-08)

The complete audit is [R-04](../research/R-04-v1-capability-parity.md). Each gap
blocks cutover. Close a row only after its change deploys and its listed runtime
acceptance passes; permission checks alone do not cover admission or networking.

- [ ] P-01: complete OPERATOR catalog and standing short-lived GrantPolicies.
- [ ] P-02: precise ops standing grants; retain its distinct remediation scope.
- [ ] P-03: existing exec/proxy, controller and powerful-pod admission parity.
- [ ] P-04: existing runtime maintenance in the three v2 namespaces.
- [ ] P-05: existing named-SA/secret Job clones, including Recyclarr.
- [ ] P-06: exact observability PVC/StatefulSet rights, without snapshot writes.
- [ ] P-07: ops work-order ConfigMap create/update/patch.
- [ ] P-08: full/dev internal Traefik HTTPS and browser check.
- [ ] P-09: ops observability service ports and external destinations.
- [ ] P-10: grant CLI/MCP request/use/release and re-request after expiry.
- [ ] P-11: references restored/SecretSynced; actual read-only access awaits P-13.
- [ ] P-12: references restored/SecretSynced; fresh full presence/ADC permissions
  and Omni read passed. Complete GCP/Cloudflare service checks remain. Rescue
  credential exclusion is covered by controller tests; no runtime hold was needed.
- [ ] P-13: both existing PVE API endpoints and all seven SSH network paths.
- [ ] P-14: Q-15 keeper PVE mint/install/revoke and durable recovery code built
  (#111), disabled by default. Owner CA/trust, policies and real test remain.
- [ ] P-15: typed agentd store and per-call pve selection built/tested (#111),
  including expiry, redaction, interrupted installs and flags. Signed agent 2.9.0
  is deployed (#3595); a fresh session passed empty-store/default-unavailable
  checks. Real grant/helper acceptance is required for closure.
- [ ] P-16: full hw-ssh user/sudo/root/raw/PTY behavior with short certificates.
- [ ] P-17: PiKVM trust/egress and standing full hardware grants across repos.
- [ ] P-18: owner CA/node trust and keeper journal/job/SSH deployment.
  The CRDs, exact RBAC/admission and journal inventory are deployed in
  haynes-ops #3589; CA/trust, SSH configuration/egress and activation remain.
- [ ] P-19: explicit general SSH certificate/connection revocation contract.
- [ ] P-20: actual read-only full MCP, browser and ops observability checks.

R-04 gives the change and acceptance evidence for every row. Secret API reads,
node drain, snapshots, broad workload creation and additional break-glass remain
unavailable until the approved human path ships; they do not replace parity work.

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

## Target implementation (historical step 6 superseded by Q-16)

- CRDs `AccessGrant` and `GrantPolicy`. Validation refuses any grant or policy that
  targets `dev-env-system`, `dev-agents` or `dev-tools`, and any GrantPolicy for type
  `breakglass` or roles `dev-env-grant-breakglass` and `dev-env-grant-secrets-read`.
  The broker re-checks the same rules.
- `POST/GET/DELETE /v1/grants` in the operator API. The requester comes from the
  caller's token, never the body. At most 3 pending requests per session; identical
  requests merge; unanswered requests are denied after 30 minutes.
- The broker mode of the operator binary (`dev-env-operator broker`): policy match,
  materialise and revoke. Human approval is redesigned under R-03/Q-18; do not
  build the old Pushover/web adapter:
  - `kube`: ServiceAccount `grant-<id>`, bindings to a catalog role, a TokenRequest
    token with the grant's TTL installed by `agentd ctl grant-install` as kube
    context `grant-<id>`; at expiry delete the ServiceAccount and bindings;
  - `egress`: a CiliumNetworkPolicy selecting the session's label;
  - `breakglass`: additional capability, unavailable until Q-18's Claude Code
    approval route and login-freshness test pass. The original Pushover/web
    mechanism is historical. At expiry the planned audit/refresh behavior still
    depends on keeper-owned logins;
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
  the catalog by `resourceNames`; its API/cluster network paths.
  The original approval-port and Pushover rules are superseded by Q-16.
- The grant role catalog: ClusterRoles `dev-env-grant-workloads`, `-storage`,
  `-secrets-read`, `-nodes` and `-breakglass` (DESIGN-001 6.12; no `edit`, `admin` or
  `cluster-admin`). `-breakglass` is generated from API discovery minus its exclusion
  list, with a CI check that regenerates it when the cluster gains an API group.
- `dev-env-identity-guard`, matching every `dev-agents` identity except the
  workbench, if plan 01 did not already ship it.
- The human adapter and its deployment only after Q-18 and R-03 acceptance;
  no Pushover/web approval ingress or credential.
- The day-one GrantPolicy set covering all v1 parity, and nothing beyond it.
- Ship the API server audit lines for `system:serviceaccount:dev-agents:grant-*` to
  Loki, if they are not shipped already.
- Credential grants (Q-07): the fresh keeper-owned SSH CA trusted on Proxmox,
  HaynesTower and PiKVM, with persistent host configuration and pinned host keys.
  The owner generates/stores the CA; neither v1 hardware private key nor its
  long-lived PVE operator token moves into the keeper or a v2 session. Full
  standing grants retain existing hardware scope across repos. Ops hardware
  writes are not v1 parity. D-69 covers the PVE backend; P-19 covers general SSH.

## Acceptance

**The original Pushover/web approval cases below are superseded by Q-16.**
R-03/Q-18 define their replacement. The parity checklist and R-04 closure tests
are mandatory; credential backend code with its feature off does not close them.

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
