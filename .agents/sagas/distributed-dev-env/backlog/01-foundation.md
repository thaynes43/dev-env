# 01: foundation, task mode

**Status:** backlog
**Depends on:** Q-01 (build or adopt), Q-02 (language), Q-04 (sizes and cap), Q-05
(storage); spike S-7 for the clone path
**Parallel with:** nothing

## Goal

`agent-run -p "<task>"` from the v1 pod creates a session pod on a worker node. The
task runs on the static Claude token, opens its PR, and is suspended, rescued and
archived by the operator. An operator restart mid-task does not disturb it.

## In this repo

- CI: lint and tests (envtest for the operator), build, smoke-test and cosign-sign
  the agent image (`2.x`) and the operator image (`ghcr.io/thaynes43/dev-env-operator`),
  publishing from `main` only; one aggregate `… - Success` check; release-please;
  Renovate with the Dockerfile `customManagers` copied from haynes-ops.
- Agent image `2.0`: a copy of haynes-ops' `scripts/dev-env/Dockerfile` plus `tini`,
  `agentd`, the Codex standalone and `kubectl-cnpg` baked in (v1 downloads them at
  boot), and the `pve` and `hw-ssh` helpers. No code-server.
- agentd: config rendering (port of `dev-init.sh`), partial clone and worktree,
  start the agent in tmux, heartbeat, `agentd ctl status|rescue`.
- Operator: `AgentSession` CRD, pod and volume creation with the size class,
  placement and labels of DESIGN-001 section 7, suspend and archive with rescue
  (D-10), `POST/GET/DELETE /v1/sessions`, `GET /v1/fleet`, TokenReview auth.
- Keeper, minimal: mint the gh token into `dev-env-gh-token` every 40 minutes.
- `agent-run` v2: `-p`, `list`, `reap`, `fleet`.
- Tests that enforce DESIGN-001 5.1 (no owner reference to the Deployment; no delete
  of a Running session's pod outside drain and suspend).

## In haynes-ops (GitOps PRs; none touch `apps/dev/dev-env/app/resources/**`)

- Namespaces `dev-env-system` and `dev-agents`; the CRDs in their own Kustomization
  with `prune: disabled`.
- Operator and keeper HelmReleases, RBAC (DESIGN-001 6.11: cluster-wide read for
  agents, write verbs only by per-namespace RoleBindings that exclude both dev-env
  namespaces), CNPs (agent profile
  `full` = v1's allowlist verbatim, operator, keeper), ResourceQuota, LimitRange,
  PriorityClass `dev-env-agent`, a Kyverno policy requiring CPU limits in
  `dev-agents`.
- ExternalSecrets in `dev-agents` mirroring v1's; the empty keeper-owned Secrets.
- The shared CephFS volume (if Q-05 chose it), `prune: disabled`.
- Kyverno `verify-thaynes43-images`: add the `thaynes43/dev-env` workflow identity.
- Renovate: hold the v1 HelmRelease below `2.0.0`.
- CNPs of in-cluster MCP services that admit only the v1 pod (the haynesnetwork hop,
  the authoring services) also admit `dev-agents` session pods.

## Acceptance

- A task created from the v1 pod lands on talosw02 or talosw03 (or w01), runs with
  the M class limits, and opens a PR.
- `kubectl rollout restart deploy/dev-env-operator -n dev-env-system` during the task
  leaves the task's pod untouched (same pod UID, no restart).
- Reap of a session with an uncommitted file produces a bundle on the shared volume
  that restores the file.
- No session pod is ever scheduled on a control-plane node (checked with
  `kubectl get pods -n dev-agents -o wide`).
- From a session pod, `kubectl auth can-i` denies `pods/exec`, pod delete, Job create
  and Deployment patch in `dev-env-system` and `dev-agents`, and allows them in a
  listed namespace.
