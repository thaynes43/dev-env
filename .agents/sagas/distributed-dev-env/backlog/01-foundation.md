# 01: foundation, task mode

**Status:** in progress: KICKOFF B1 to B4 landed (#14, #15, #17, #18); step 1, the
`AgentSession` CRD, landed in #24 (2026-10-06)
**Depends on:** Q-01 (build), Q-02 (Go), Q-04 (requests and limits, no cap) and Q-05
(storage), all decided 2026-10-06; spikes S-7 (clone path), S-8 (gasha01 speed) and
S-12 (the guard)
**Parallel with:** nothing

## Goal

`agent-run -p "<task>"` from the v1 pod creates a session pod on a worker node. The
task runs on the static Claude token, opens its PR, and is suspended, rescued and
archived by the operator. An operator restart mid-task does not disturb it.

## Progress

The order of [KICKOFF section 4](../KICKOFF.md#4-then-plan-01-itself), one PR per
step. Tick a step in the PR that lands it.

- [x] 1. `AgentSession` types and the generated CRD, with an envtest suite (#24).
  The schema enforces D-39; `make test` runs the suite against kube-apiserver 1.35,
  the main cluster's minor.
- [ ] 2. Pods and volumes from the size class and `dev-env-templates`, placed per
  DESIGN-001 section 7, with the 5.1 tests.
  The pod gives agentd what D-40 to D-42 name: `AGENTD_SESSION`, `AGENTD_API_URL`,
  the projected token at `/var/run/secrets/dev-env/token` (audience
  `dev-env-operator`), and a termination grace period over 30 s.
- [ ] 3. The `/v1` API (`sessions`, `fleet`) with TokenReview auth.
- [x] 4. agentd: config rendering, partial clone and worktree, tmux start,
  heartbeat, `ctl status|rescue` (DESIGN-001 3.6, D-40 to D-43), in three PRs:
  - [x] config rendering, the port of `dev-init.sh`: `agentd render` (#25);
  - [x] partial clone and worktree, the task in tmux, heartbeat, `agentd ctl status`
    (#27; D-41, D-42);
  - [x] `agentd ctl rescue`, the entry point step 5 extends with the bundle (PR_C;
    D-43).
- [ ] 5. Rescue to a bundle on the shared volume (D-10), then suspend and archive.
  The bundle extends `agentd ctl rescue` (D-43): it bundles the report's
  `unpushedRefs`, and archive trusts `cleanAndPushed`.
- [ ] 6. The minimal keeper: mint the gh token every 40 minutes.
- [ ] 7. `agent-run` v2: `-p`, `list`, `reap` and `fleet`.
- [ ] 8. The haynes-ops PRs (KICKOFF section 4, item 8).
- [ ] 9. The first end-to-end run, then the acceptance checks below.

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
