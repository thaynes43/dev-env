# 07: access broker

**Status:** backlog
**Depends on:** 01 (the baseline guard and egress tiers are in place); Q-07 for the
credential grants only
**Parallel with:** 02

## Goal

Agents ask for more than the baseline and get it for a while: a namespace role, a
LAN or in-cluster destination, a credential, or break-glass. Tom approves from his
phone, or a standing policy in git approves at once. Everything is time-boxed and
audited, and the headlamp path is no longer needed. DESIGN-001 6.12, D-23 to D-27.

## In this repo

- CRDs `AccessGrant` and `GrantPolicy`, with a validation rule that refuses any
  GrantPolicy for type `breakglass` or role `cluster-admin`.
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
  - `breakglass`: a `kube` grant on `cluster-admin`, at most 1 h, Tom only, fresh
    Authentik login (5 minutes), Pushover at high priority;
  - `credential` (if Q-07 picks A): the keeper installs the Proxmox operator token or
    an hw-ssh certificate from its SSH CA into the pod's tmpfs, and removes it at
    expiry.
- The operator's backstop: delete expired grants' network policies if the broker is
  down; re-install active grants after a drain.
- agentd: `grant-install`, kube contexts, and the built-in `dev-env` MCP server's
  `request_access`, `grant_status` and `release_access` tools.
- `agent-run grant request|list|use|release` and `agent-run breakglass`.
- Tests: no path in the operator's ServiceAccount can bind a role; the broker can
  bind only catalog roles; an expired kube grant's token is refused by the API
  server.

## In haynes-ops (GitOps PRs; none touch `apps/dev/dev-env/app/resources/**`)

- Broker Deployment and ServiceAccount in `dev-env-system`; its RBAC with `bind` on
  the catalog by `resourceNames`; its CNPs (traefik to the approval port only, the
  operator on 8443, egress to the API server and `api.pushover.net`).
- The grant role catalog: ClusterRoles `dev-env-grant-*`.
- The approval page's ingress on an external host behind Authentik, and its
  Authentik application.
- The day-one GrantPolicy set (nothing beyond v1), and the Pushover credential for
  the broker as an ExternalSecret.
- Ship the API server audit lines for `system:serviceaccount:dev-agents:grant-*` to
  Loki, if they are not shipped already.
- Credential grants (Q-07 A only): the keeper's SSH CA public key trusted by the
  `dev-env` user on the Proxmox nodes (`TrustedUserCAKeys`), set through hw-ssh with
  `declare-activity`, and the operator token and hw-ssh key removed from profile
  `full`.

## Acceptance

- A session requests role `dev-env-grant-workloads` in one namespace for 15 minutes.
  Tom gets one Pushover message, approves on the page, the agent patches a
  Deployment's image there, and 15 minutes later the same command is refused.
- A request that matches a standing GrantPolicy is approved with no message and is
  recorded under the policy's name.
- An egress grant to a LAN address works for its TTL and is gone after.
- `agent-run breakglass` works only after Tom's fresh approval, and every action
  under it appears in the audit log as `grant-<id>`.
- A Job with `serviceAccountName: headlamp` and exec into the headlamp pod are both
  refused for the agent ServiceAccount.
- A broker restart during an active grant changes nothing for the session.
- Every grant of the test run is listed by `agent-run grant list --all` with
  requester, approver and times.
