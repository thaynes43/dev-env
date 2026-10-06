# 07: access broker

**Status:** backlog
**Depends on:** 01 (the baseline guard and egress tiers are in place); Q-07 (Tom
2026-10-06: credential grants, A)
**Parallel with:** 02

## Goal

Agents ask for more than the baseline and get it for a while: a namespace role, a
LAN or in-cluster destination, a credential, or break-glass. Tom approves from his
phone, or a standing policy in git approves at once. Everything is time-boxed and
audited, and the headlamp path is no longer needed. DESIGN-001 6.12, D-23 to D-27.

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
    First check that the operator token can mint expiring tokens for its own user;
    if not, the keeper installs the operator token itself and the approval page says
    that a copied value outlives the grant.
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
