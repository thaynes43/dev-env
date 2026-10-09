# R-04: v1 capability parity

**Audited 2026-10-08.** Q-16 requires v2 to retain today's capabilities before
cutover. The audit is complete; parity is not. The blocking checklist lives in
[backlog/07](../backlog/07-access-broker.md#v1-parity-checklist-2026-10-08).

**Baseline corrected later on 2026-10-08 (D-70).** Tom pointed out that agents
already use the Headlamp pod. Parity includes their effective access through
accepted pod exec, Headlamp ServiceAccount workloads and self-merged GitOps,
alongside their direct OPERATOR permissions. Comparing only the OPERATOR role
incorrectly classified existing Kubernetes powers as enhancements.

Headlamp's technical reach is not a standing authorization to use it. Historical
Headlamp work required Tom's live directive for the task or access scope; an
equivalent route must preserve that requirement. Standing policies cover the
already preapproved direct OPERATOR and accepted credential scopes, not blanket
Headlamp cluster-admin access. A directive that already authorizes the task need
not be requested again; this correction adds no new approval step.

**Target clarified 2026-10-09 UTC (D-71).** Tom wants Headlamp retired once parity
with guardrails is reached. The target is to replace its task scope with tested,
guarded access. Keeping the old path is only a migration fallback. Guardrails may
change the mechanism while preserving accepted tasks and owner requirements;
conditional Headlamp retirement is part of the target, not an optional claim of
new Kubernetes power.

## Evidence and limits

Read-only audits compared v1's live ServiceAccounts, RBAC, admission and network
policies with v2's deployed configuration, helper scripts and grant code. The
source baseline was dev-env `2a268806`; haynes-ops was read at `4d6ec72e` for
Kubernetes and `700f487d` for hardware. Later unrelated main changes do not stand
in for runtime verification.

Both `dev/dev-env` and `upgrade-agent/dev-env-ops` bind `dev-env-operator`.
The interactive v1 identity additionally has database PVC deletion and scoped
observability storage rights. The remediation identity additionally writes its
work-order ConfigMaps. All v2 profiles currently use `dev-agents/dev-env-agent`,
with profile `full` the default. There are **zero live GrantPolicies**. Only the
shelf runs in `dev-agents`; this audit did not create a session or a grant.

The follow-up read-only check found live ClusterRoleBinding `headlamp-admin`
binding `frontend/headlamp` to `cluster-admin`. The running Headlamp pod uses
that ServiceAccount and mounts its projected credential; its GitOps init builds
its kubeconfig from that identity. The role permits all API resources and verbs;
admission controls and existing operating rules still apply. The query was at
2026-10-08 23:55:40 UTC. Source: [Headlamp configuration](https://github.com/thaynes43/haynes-ops/blob/8ae3a2ff2f15156ee2df243b1cbd55403748cb05/kubernetes/main/apps/frontend/headlamp/app/helmrelease.yaml#L41)
and [v1 exec RBAC](https://github.com/thaynes43/haynes-ops/blob/8ae3a2ff2f15156ee2df243b1cbd55403748cb05/kubernetes/main/apps/dev/dev-env/app/rbac.yaml#L81).
Existing DESIGN-001 sections 6.11 and 6.12 already described Headlamp exec and
ServiceAccount Jobs; haynes-ops' `.agents/reports/pushover-triage-2026-09-25.md` records a
node drain through that route. No credential contents were read and no privileged
operation was performed for this correction. These declarations establish the
available identity; they do not claim a new end-to-end Headlamp exec test.

Targeted `kubectl auth can-i --subresource=exec` checks confirmed v1's exec,
proxy, Job, rollout, Flux and ExternalSecret operations and its scoped storage
rights. These checks prove authorization, not admission or network reachability.
The parity closure tests below must exercise both. No hardware write, token mint,
secret-value read, broad test loop or running-session restart was performed.

The Kubernetes source comparison includes haynes-ops
[`dev-env/app/rbac.yaml`](https://github.com/thaynes43/haynes-ops/blob/4d6ec72e/kubernetes/main/apps/dev/dev-env/app/rbac.yaml),
[`cloudnative-pg/app/dev-env-rbac.yaml`](https://github.com/thaynes43/haynes-ops/blob/4d6ec72e/kubernetes/main/apps/database/cloudnative-pg/app/dev-env-rbac.yaml),
[`kube-prometheus-stack/app/dev-env-rbac.yaml`](https://github.com/thaynes43/haynes-ops/blob/4d6ec72e/kubernetes/main/apps/observability/kube-prometheus-stack/app/dev-env-rbac.yaml),
[`dev-env-ops/app/rbac.yaml`](https://github.com/thaynes43/haynes-ops/blob/4d6ec72e/kubernetes/main/apps/upgrade-agent/dev-env-ops/app/rbac.yaml),
[`dev-env-system/rbac/app`](https://github.com/thaynes43/haynes-ops/tree/4d6ec72e/kubernetes/main/apps/dev-env-system/rbac/app)
and [`dev-env-exec-guard.yaml`](https://github.com/thaynes43/haynes-ops/blob/4d6ec72e/kubernetes/main/apps/kyverno/policies/app/dev-env-exec-guard.yaml).
The existing named-SA/secret Job path is documented in
[`arr-quality-profiles.md`](https://github.com/thaynes43/haynes-ops/blob/4d6ec72e/.agents/runbooks/arr-quality-profiles.md).

## What is already covered

- Broad Kubernetes reads excluding the Secret API are copied into v2's baseline.
  A new API group missing from both versions is not a v2 regression.
- Runtime pod deletion, rollout and Flux/ExternalSecret/CronJob patches retain
  their verbs outside protected v2 namespaces. Their admission restrictions still
  need the maintenance-parity check below.
- `database` PVC deletion is already bound to v2's baseline with the original
  delete-only scope. Do not replace it with the broader storage catalog role.
- The same ten MCP servers are configured. Policies admit their named cluster
  transports and the API/operator ports. A real read-only MCP smoke is still due.
- Bot git access and self-merging GitOps remain the deployment path. Renovate and
  Authentik blueprint wiring retain that path, as Q-16 requires.
- v2's `pve` and `hw-ssh` scripts preserve v1's command behavior. Credential
  selection and hardware network reachability are still missing.

## Blocking gaps and closure evidence

Every row is mandatory before cutover. A planned feature that has not shipped is
still a gap; an authorization check alone cannot close it.

| Id | Gap | Change and acceptance evidence |
|---|---|---|
| P-01 | No standing policies; the catalog lacks complete direct OPERATOR and effective Headlamp task scope. | Add precise parity roles, validation/binding allowances and short-lived policies, or retain an equivalent accepted route. Exercise direct runtime verbs and representative Headlamp-equivalent operations under the existing owner rules, including expiry and release for grants. Do not equate narrow OPERATOR rights with the full baseline or enable broad roles wholesale. |
| P-02 | All standing grants reject profile `ops`. | Permit precise remediation parity scopes. Prove auto-approval for those scopes and denial for capabilities beyond v1. Separate the two v1 identities' additional permissions. |
| P-03 | Exec/proxy and effective Headlamp task scope are blocked by the catalog and admission. | Supply a guarded replacement for accepted Headlamp tasks; retain the old route only as a migration fallback until equivalence is proved. Preserve existing controller, Traefik and CNPG operations. Verify authorization, admission and bounded read-only execution. Direct Secret reads through the session's baseline identity remain absent; owner-directed Secret access through Headlamp is already within effective v1 scope. Preserve that owner requirement and do not read values in acceptance artifacts. D-71 requires retiring Headlamp after guarded parity and caller migration. |
| P-04 | Guards block maintenance in `dev-env-system`, `dev-agents` and `dev-tools`. | Preserve the existing v1 maintenance capability without a new human gate. Test admission for its targeted runtime writes. Session lifecycle API calls alone are not equivalent to platform maintenance. |
| P-05 | Job admission allows only default-SA jobs and a narrow secret suffix. | Preserve existing job-clone operations, including Recyclarr's named SA and mounted secret. Use server dry-run and one bounded CPU-limited fixture; do not grant general workload creation. |
| P-06 | v1's observability PVC create/delete and StatefulSet delete are absent. | Add the exact namespaced parity scope and closure test. VolumeSnapshot writes are beyond this v1 scope and must not hitchhike on it. |
| P-07 | The remediation work-order ConfigMap writes are absent. | Supply its exact `upgrade-agent` create/update/patch scope to the appropriate persona. Verify an isolated work-order fixture; do not use broad workload writes. |
| P-08 | Full/dev cannot reach internal Traefik HTTPS. | Restore the internal ingress port alongside current named MCP transport rules. Run the existing browser's read-only internal-app check. |
| P-09 | Ops lacks its observability and external network paths. | Restore the bounded Prometheus/Alertmanager/Loki and HTTP service paths and current ops external destinations. Prove read-only queries and external fetches in an ops session. |
| P-10 | Grant CLI and built-in access MCP are unbuilt (step 7). | Implement request/list/show/use/release plus re-request after expiry. An identical active request returns the existing grant; it does not extend its TTL. Prove unattended standing-grant use and renewal without prompting Tom. |
| P-11 | The Proxmox read token was incorrectly excluded from the baseline. | Restore its existing 1Password references for full and accepted ops profiles; verify ExternalSecret readiness and a read-only helper call after P-13. Never copy the long-lived operator token into a session. |
| P-12 | Full's existing Omni Reader, GCP ADC and Cloudflare DNS credentials were incorrectly excluded under Q-07. | Restore isolated references/mounts for full, keeping narrower dev unchanged. Verify field presence without printing values and bounded read-only service checks. Rescue holds must retain GitHub but omit other profile credential mounts. No Omni operator key is added. |
| P-13 | Session hardware network paths are absent. | Provide baseline read or standing egress for dashboard HTTPS, direct-node PVE fallback on 8006 and all seven existing SSH targets on 22. Prove both API endpoints and every SSH target. Keep addresses in their existing private configuration. |
| P-14 | Proxmox credential requests fail closed: no keeper mint/install/revoke backend. | Build Q-15 A with pinned native SSH, fixed-expiry tokens, durable recovery and verified provider deletion. Test crashes at persistence boundaries, ambiguous remote results, replacement pods, expiry and cleanup failure; then a declared bounded real mint/install/revoke. See D-69. |
| P-15 | Agentd installation and `pve` credential selection support kube grants only. | Add UID-fenced typed private files; preserve credential directories during kubeconfig rewrites. `pve` reads the live unexpired operator grant per invocation while `--ro` retains the reader fallback. Test kube/PVE coexistence, stale cleanup, expiry, secret redaction and all flags/generic CRUD behavior. |
| P-16 | General hw-ssh CA trust, principals and certificate selection are unbuilt. | Preserve the existing dev-env user and complete sudo allowlist on Proxmox, root on HaynesTower and PiKVM, raw targets, `pve-all`, stdin and permitted interactive/PTY use. Restrict only keeper minting certificates to a fixed command. Verify each existing command class with bounded safe fixtures. |
| P-17 | H5 omitted PiKVM and standing hardware scope was narrowed to one repo. | Include all existing targets and full sessions across repos. Preserve PiKVM's declared rw/write/ro workflow and the existing owner permission requirement for ATX, Wake-on-LAN and console keystrokes. The v1 remediation executor lacks hardware write keys, so do not call automatic ops hardware writes parity. |
| P-18 | SSH CA provisioning, keeper trust/egress and cleanup authority are unbuilt. | Owner creates/stores the fresh CA; nodes trust it with pinned host keys. Ship keeper mounts, named journal and exact job RBAC, SSH egress, broker receipts and standing policies. Keeper owns credentials; broker alone owns AccessGrant status. No v1 private key or rotating refresh token is reused. |
| P-19 | SSH certificate revocation and existing-connection behavior need a contract. | Record the copied-certificate/connection limits, choose the contract, then test it without cutting v1's command/PTY capability. Deleting client files is not server revocation, and certificate expiry does not terminate an established SSH connection. |
| P-20 | Configured MCP transport has not been verified in a restored full session. | Run read-only calls through each relevant configured service, with no secrets or returned transcripts in artifacts. Include internal browser access and ops observability checks above. |

P-11/P-12 are implementation regressions being corrected. The other rows include
missing planned work and actual guard/network losses. None is waived by the audit.

## Baseline restoration (2026-10-08)

The rescue-mount fix (#108) shipped first as signed operator `sha-d4c48bc` through
haynes-ops #3584. The operator Kustomization depends on templates, so combining
both changes in one commit would have applied the GCP mount before the fix.
After rollout verification, haynes-ops #3583 restored the references. All four
ExternalSecrets are Ready/SecretSynced; secrets/templates applied `4f1b9c52`.

Idle full S session `dev-env-1008-142013` verified environment presence without
values and readable service-account ADC JSON at read-only `/etc/gcp` mode 0440.
Bounded Omni Reader and own operator API calls exited zero with output discarded.
It sent no prompt, was rescued as CleanAndPushed and removed with its home volume.
V1 and shelf UIDs/readiness/all restart counts stayed unchanged; the declaration
was ended. No natural hold pod was needed. No PVE LAN check or Cloudflare/GCP API
mutation/token exchange was performed. P-11/P-12 remain unchecked for their full
closure evidence; their missing-reference regressions are fixed.

## Proxmox backend progress (2026-10-08)

Dev-env [#111](https://github.com/thaynes43/dev-env/pull/111), main `eeb15e3`, built
the disabled backend: immutable session/job identities, broker cleanup receipts,
keeper pinned SSH and private journal recovery, typed agentd files and PVE
selection. All ten affected packages passed together; the final recovery fixes
passed focused regressions and full CI. Independent and Claude reviews have no
remaining findings. The signed operator publish is run `37798469467`; signed
agent 2.9.0 follows release #110.

Haynes-ops [#3589](https://github.com/thaynes43/haynes-ops/pull/3589), `da983541`,
applied the generated schemas, exact broker/keeper roles and admission policy,
and keeper-only journal inventory before runtime pins. Both schema copies match
the generated main files byte for byte apart from provenance comments. The CRD
is Established, requester.sessionUID is present, admission has no type warnings
and the four prerequisite Flux targets are Ready. Live component impersonation
was unavailable: runtime RBAC evidence covers declarations, with authorization
and admission exercised by the exact-role envtests. No Secret values were read.
V1 and shelf identities and all restart counts were preserved; the declaration
was ended.

Haynes-ops [#3595](https://github.com/thaynes43/haynes-ops/pull/3595), `d7b845a6`,
deployed those signed runtime pins. Operator/broker/keeper are updated, Ready and
available 2/2, 2/2 and 1/1; Helm and all five Flux targets are Ready. Keeper GitHub
readiness passed with minting off, no CA mount and zero jobs.

Idle full S session `dev-env-1008-152312` stayed on 2.8.0 through the rollout with
the same pod UID and zero restarts, marked Outdated without a restart. Fresh
`dev-env-1008-153405` ran exact agent 2.9.0 at revision `2.9.0-5030ab2483`; its
grants volume is memory-backed, typed listing was empty and Proxmox availability
exited 4 without a provider command. Both fixtures were rescued CleanAndPushed,
archived and removed with their pods/home PVCs. V1's UID and all restart counts
remain unchanged. The shelf completed its planned 2.7.0-to-2.9.0 replacement and
is Ready with zero restarts. The declaration was ended.

CA storage was subsequently confirmed by the owner (Q-19, 2026-10-08
America/New_York). P-14/P-15/P-18 remain open for CA projection/owner node trust,
standing policy and real
hardware/network activation and acceptance. No token was minted, no node trust
changed, and general hw-ssh remains unbuilt under P-19's pending contract.

## Scope that must remain distinct

Q-07 removes only the Proxmox **operator** token and long-lived hardware SSH key
from sessions. It does not remove the Proxmox reader, Omni Reader, GCP ADC or
Cloudflare DNS access. General SSH certificates need the existing hardware
command scope; a forced command suitable for the keeper minter would cut parity
if applied to ordinary hw-ssh grants.

Secret API reads, node cordon/drain, snapshots and broad workload operations are
outside the direct OPERATOR identity, but already reachable through the accepted
Headlamp cluster-admin path. Calling them additional effective powers was wrong.
Direct broker grants for them would replace a detour; audit records, expiry and
per-session attribution improve how existing access is managed. They are not
evidence of a new capability requiring a new approval gate. Existing rules for
owner-directed or disruptive work still apply. No blanket direct cluster-admin
grant is authorized by this correction.

No concrete additional Kubernetes power has been identified beyond that effective
v1 scope. A proposed human approval route must name its actual new capability or
workflow before asking Tom to choose an implementation. Q-18's original premise
is withdrawn; no route has been selected. Do not reintroduce the old Pushover/web
approval tests or make an unbuilt replacement approval path block existing tasks.

SSH validity is checked at authentication. A server revocation list can reject a
copied certificate on a new login, but does not terminate an existing channel.
These limits require an explicit general hw-ssh contract before enabling that
backend. [OpenSSH certificate documentation](https://man.openbsd.org/ssh-keygen#CERTIFICATES),
[sshd RevokedKeys](https://man.openbsd.org/sshd_config#RevokedKeys).

The current `pve` helper uses `curl -k`; v1 has no verified private-CA contract.
Any TLS verification migration must cover both existing API endpoints. Do not
claim that a copied helper already verifies their private CA.

## Resume and safeguards

First restore the omitted baseline references after [the rescue-mount fix (#108)](https://github.com/thaynes43/dev-env/pull/108) ships.
Then build the Proxmox backend under D-69 with the feature disabled until owner
trust and standing-policy acceptance pass. Complete the remaining parity rows
before requesting cutover. D-70 corrects the Headlamp baseline; any future approval
proposal must preserve that scope and Q-16's Claude Code app requirement.

All tests run at low parallelism under `nice -n 19`, `GOMAXPROCS=2`, `go test -p 2`,
one local suite at a time. No stress tools, burners or looped suites. Every
cluster fixture has a CPU limit. Deploy through GitOps, declare disruptive work,
compare v1 and all session pod UIDs and init/regular restart counts, and remove
fixtures/end the declaration when finished. Never edit v1's mounted resources.
