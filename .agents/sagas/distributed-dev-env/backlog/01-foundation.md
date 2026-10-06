# 01: foundation, task mode

**Status:** in progress: KICKOFF B1, the Go skeleton, landed in #14 (2026-10-06)
**Depends on:** Q-01 (build), Q-02 (Go), Q-04 (requests and limits, no cap) and Q-05
(storage), all decided 2026-10-06; spikes S-7 (clone path), S-8 (gasha01 speed) and
S-12 (the guard)
**Parallel with:** nothing

## Goal

`agent-run -p "<task>"` from the v1 pod creates a session pod on a worker node. The
task runs on the static Claude token, opens its PR, and is suspended, rescued and
archived by the operator. An operator restart mid-task does not disturb it.

## In this repo

- Everything in Go: the operator, agentd and `agent-run`.
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
  placement and labels of DESIGN-001 section 7 (the scheduler places pods; a Pending
  session reports the scheduler's reason, D-21), session volumes on `gasha01-rbd`
  (D-22), suspend and archive with rescue (D-10), `POST/GET/DELETE /v1/sessions`,
  `GET /v1/fleet`, TokenReview auth. Agents run with no approval prompts (D-23).
- Keeper, minimal: mint the gh token into `dev-env-gh-token` every 40 minutes.
- `agent-run` v2: `-p`, `list`, `reap`, `fleet`.
- Tests that enforce DESIGN-001 5.1 (no owner reference to the Deployment; no delete
  of a Running session's pod outside drain and suspend).

## In haynes-ops (GitOps PRs; none touch `apps/dev/dev-env/app/resources/**`)

- Namespaces `dev-env-system` and `dev-agents`; the CRDs in their own Kustomization
  with `prune: disabled`.
- Namespace `dev-tools` too (empty until plan 08).
- Operator and keeper HelmReleases, RBAC (DESIGN-001 6.11: cluster-wide read for
  agents, v1's write verbs under the `dev-env-agent-guard` and
  `dev-env-identity-guard` admission policies and the Kyverno exec rule, nothing in
  the three dev-env namespaces), network policies (the web and platform tiers of D-24
  as `CiliumClusterwideNetworkPolicy` objects, the default-deny clusterwide policy for
  `dev-tools`, operator, keeper), LimitRange,
  PriorityClass `dev-env-agent` (-10, `preemptionPolicy: Never`), a Kyverno policy
  requiring CPU limits in `dev-agents`. No ResourceQuota (D-21).
- Q-08's Kyverno LimitRange (50m default CPU request in every non-system namespace)
  is built in haynes-ops as a v1 fix, not by this plan. It went live on 2026-10-06
  (haynes-ops #3406, Kyverno `default-cpu-request`); at 03:34Z no Running pod in the
  cluster was BestEffort. Before phase 1 adds sessions, check that this still holds.
- ExternalSecrets in `dev-agents` mirroring v1's; the empty keeper-owned Secrets.
- The shared CephFS volume `dev-env-shared` on `ceph-filesystem`, `prune: disabled`.
- `dev-env-templates` with `gasha01-rbd` as the session volume class.
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
- A session that does not fit stays Pending, and `agent-run` prints the scheduler's
  reason at once.
- The session's PVC is on `gasha01-rbd`; S-8's numbers are recorded.
- A session pod fetches an arbitrary public web page, and cannot reach a LAN
  address or an in-cluster service outside the platform tier.
- From a session pod, `pods/exec`, pod delete, Job create and Deployment patch are
  refused in `dev-env-system`, `dev-agents` and `dev-tools`, and allowed elsewhere
  within the guard (S-12's checks pass).
