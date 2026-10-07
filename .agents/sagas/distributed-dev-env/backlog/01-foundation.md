# 01: foundation, task mode

**Status:** in progress: KICKOFF B1 to B4 landed (#14, #15, #17, #18); steps 1 to 4
and 7 landed on 2026-10-06 (the CRD #24; pods and volumes #29, #30; the `/v1` API #31;
agentd #25, #27, #28; `agent-run` #34)
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
- [x] 2. Pods and volumes from the size class and `dev-env-templates`, placed per
  DESIGN-001 section 7, with the 5.1 tests (D-44, D-45), in two PRs.
  The pod gives agentd what D-40 to D-42 name: `AGENTD_SESSION`, `AGENTD_API_URL`,
  the projected token at `/var/run/secrets/dev-env/token` (audience
  `dev-env-operator`), and a termination grace period over 30 s.
  - [x] the templates, the pod and the volume, status, the operator's manager, and
    envtest proof that pods carry no owner reference to the operator and that a
    restart or a template change leaves a running pod untouched (#29);
  - [x] the finalizers: deleting or suspending a session never deletes its pod or
    volume before rescue; one guarded delete path (#30). Step 5 fills the seam:
    `rescued()` in `internal/controller/guard.go`, the archive that deletes the
    volume and lifts its finalizer, and `patch` on PVCs in the operator's RBAC.
- [x] 3. The `/v1` API (`sessions`, `fleet`) with TokenReview auth (#31; D-46).
  `internal/apiserver` serves `POST/GET /v1/sessions`, `GET/DELETE
  /v1/sessions/{name}`, the heartbeat route of D-41 and `GET /v1/fleet` on `:8443`
  from every replica; `internal/apiserver/apiv1` holds the wire types `agent-run`
  imports. Its envtest suite mints real tokens and reviews them on envtest's API
  server.
- [x] 4. agentd: config rendering, partial clone and worktree, tmux start,
  heartbeat, `ctl status|rescue` (DESIGN-001 3.6, D-40 to D-43), in three PRs:
  - [x] config rendering, the port of `dev-init.sh`: `agentd render` (#25);
  - [x] partial clone and worktree, the task in tmux, heartbeat, `agentd ctl status`
    (#27; D-41, D-42);
  - [x] `agentd ctl rescue`, the entry point step 5 extends with the bundle (#28;
    D-43).
- [ ] 5. Rescue to a bundle on the shared volume (D-10), then suspend and archive.
  The bundle extends `agentd ctl rescue` (D-43): it bundles the report's
  `unpushedRefs`, and archive trusts `cleanAndPushed`. In two PRs:
  - [x] the bundle: `agentd ctl rescue` writes one bundle per clone and a manifest
    to `rescue/<session>/<stamp>/` on the shared volume, checks them there, and
    with `--stop-agent` stops the CLI first (#32; D-48);
  - [ ] the operator: it runs that rescue by exec before a suspend deletes the pod,
    records the verdict in status, and archives a reaped session's volume only
    after a verified rescue; `patch` on PVCs in its RBAC.
- [ ] 6. The minimal keeper: mint the gh token every 40 minutes.
- [x] 7. `agent-run` v2: `-p`, `list`, `reap` and `fleet` (#34; D-50), plus `show`.
  `internal/agentrun` holds the commands; `make build` checks that the binary links
  no Kubernetes library. It finds the API in a session pod, mints a token for the
  pod's own ServiceAccount in any other pod (the v1 pod), and takes `--api-url` and
  `--token-file` elsewhere. Step 8 must get the API's CA to the v1 pod (D-50).
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
  with `prune: disabled`. Namespace `dev-tools` too (empty until plan 08). Done
  2026-10-06: haynes-ops #3468 (KICKOFF 8.3).
- The operator API's Service (`dev-env-operator`, port 8443) and its cert-manager
  Certificate, named for `dev-env-operator.dev-env-system.svc.cluster.local`; the
  operator's `--api-url` is that full name and `--client-service-accounts` adds the
  v1 pod (`dev/dev-env`); the CA reaches session pods through a templates mount and
  `AGENTD_API_CA_FILE`. For `agent-run` in the v1 pod, v1's `rbac.yaml` (outside
  `resources/**`) grants `create` on `serviceaccounts/token` for `resourceNames:
  [dev-env]` only, so the pod can mint its own token for audience `dev-env-operator`
  (D-46). The v1 pod also needs the API's CA as a file for `DEV_ENV_API_CA_FILE`
  (D-50): choose a way that does not restart the v1 pod, or hold that PR for Tom.
- Operator and keeper HelmReleases, RBAC (DESIGN-001 6.11: cluster-wide read for
  agents, v1's write verbs under the `dev-env-agent-guard` and
  `dev-env-identity-guard` admission policies and the Kyverno exec rule, nothing in
  the three dev-env namespaces), network policies (the web and platform tiers of D-24
  as `CiliumClusterwideNetworkPolicy` objects, the default-deny clusterwide policy for
  `dev-tools`, operator, keeper; done 2026-10-06, haynes-ops #3469, KICKOFF 8.5),
  PriorityClass `dev-env-agent` (-10, `preemptionPolicy: Never`), a Kyverno policy
  requiring CPU limits in `dev-agents` (done 2026-10-06, haynes-ops #3470, KICKOFF 8.6).
  No LimitRange here: it would fill every unset limit with its default, so a "no CPU
  limit" rule could never fire; the policy also carries the
  8 CPU / 24Gi ceiling (D-47). No ResourceQuota (D-21).
- Q-08's Kyverno LimitRange (50m default CPU request in every non-system namespace)
  is built in haynes-ops as a v1 fix, not by this plan. It went live on 2026-10-06
  (haynes-ops #3406, Kyverno `default-cpu-request`); at 03:34Z no Running pod in the
  cluster was BestEffort. Before phase 1 adds sessions, check that this still holds.
- ExternalSecrets in `dev-agents` mirroring v1's; the empty keeper-owned Secrets.
- The shared CephFS volume `dev-env-shared` on `ceph-filesystem`, `prune: disabled`.
- `dev-env-templates` with `gasha01-rbd` as the session volume class. The shared volume
  and the templates are done (2026-10-06, haynes-ops #3471, KICKOFF 8.7). The templates
  carry an all-zero placeholder image digest until KICKOFF B5 publishes
  `ghcr.io/thaynes43/dev-env:2.x.y`; B5's haynes-ops follow-up sets the real digest.
- The four config ConfigMaps in `dev-agents`, before the HelmReleases (KICKOFF 8.8a,
  D-49): `dev-env-config-claude` (`CLAUDE.md`, `mcp.json`, `agent-*.md`),
  `dev-env-config-codex` (`config.toml`, `AGENTS.header.md`),
  `dev-env-codex-requirements` (`requirements.toml`) and `dev-env-scripts`
  (`bashrc.sh`). The templates mount them at `/opt/dev-env/config/claude`,
  `/opt/dev-env/config/codex`, `/etc/codex` and `/opt/dev-env/scripts`; a session pod
  waits in `ContainerCreating` without them. The source is v1's
  `apps/dev/dev-env/app/resources/config/**` and `bashrc.sh`, copied into the v2 app
  `apps/dev-env-system/session-config/` and adapted (never editing v1's files), with a
  new `CLAUDE.md` for a session pod.
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
