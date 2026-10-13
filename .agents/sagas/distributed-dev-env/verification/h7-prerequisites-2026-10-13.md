# H7 read-only prerequisite checkpoint, 2026-10-13

**Status: audit complete; Q-33 pending. No H7 deployment or acceptance.**
Observed at 08:36 EDT (12:36 UTC), against dev-env
`2d70017d599212da3ecdc1b3e6554ac507dcaa62` and haynes-ops
`bf851754b3cc2b2d7953fd5a84c0aa4da2da46c2`.

The first H7 owner question is the phone identity exception in
[DESIGN-025 section 9](../designs/025-private-home-expiry.md#9-owner-overrides-hold-and-release-now).
[Q-33](../designs/025-private-home-expiry.md#18-h7-owner-identity-q-33) asks
whether to preserve Headlamp's shared backend or use Tom's personal Kubernetes
identity. No haynes-ops change is made while it is pending.

## Direct RBAC subjects in dev-agents

The listing includes wildcard and aggregated role rules. `yes` means a direct
RBAC allowance, not effective admission or a successful operation. The membership
of `system:masters` and Tom's personal username are not established by RBAC.
No impersonation, pod exec or trial workload was used to test access.

| Subject | AgentSession update | AgentSession patch | pods/exec create | pods/exec get | Job create |
|---|---|---|---|---|---|
| `Group:system:masters` | yes | yes | yes | yes | yes |
| `database/cloudnative-pg` | — | — | yes | yes | yes |
| `dev-agents/dev-env-agent` | — | — | yes | — | yes |
| `dev-env-system/dev-env-broker` | — | — | yes | — | — |
| `dev-env-system/dev-env-keeper` | — | — | yes | — | — |
| `dev-env-system/dev-env-operator` | yes | yes | yes | — | — |
| `dev/dev-env` | — | — | yes | — | yes |
| `flux-system/helm-controller` | yes | yes | yes | yes | yes |
| `flux-system/kustomize-controller` | yes | yes | yes | yes | yes |
| `frontend/headlamp` | yes | yes | yes | yes | yes |
| `kube-system/cronjob-controller` | — | — | — | — | yes |
| `kube-system/generic-garbage-collector` | yes | yes | — | yes | — |
| `kube-system/namespace-controller` | — | — | — | yes | — |
| `kube-system/reloader` | — | — | — | — | yes |
| `network/multus` | — | — | yes | — | — |
| `rook-ceph/rook-ceph-system` | — | — | yes | — | yes |
| `upgrade-agent/dev-env-ops` | — | — | yes | — | yes |
| `volsync-system/volsync` | — | — | — | — | yes |

AgentSession status-update subjects match the update subjects above. The live
identity admission guard is present; listing live Kyverno policies was forbidden,
so their installed state was not established by this audit. No bypass was used.

## Phone path and existing shelf workflows

Headlamp's Deployment uses `frontend/headlamp`, bound to `cluster-admin`.
Its configured kubeconfig uses the pod's ServiceAccount token, with no Headlamp
OIDC configuration. Authentik protects the ingress. This establishes the backend,
not whether Tom uses it on his phone or whether he is its only user.
Anyone using this backend would share any exception granted to that account.

No executable shelf-exec or archive-claim-mount workflow was found in the scanned
v1 scripts, resources, runbooks and workflows in these two repositories. This is
a scoped static finding; it does not establish every external or ad hoc workflow.
The prepared book-copy writer profiles bind the current shelf UID, spec, image and
mount inventory. A shelf change can invalidate that separate scope proof; preserve
original profiles and prepare a successor before using that workflow again.

## Remaining H7 prerequisites

- [Plan 11 F4](../backlog/11-project-workspaces.md): the remaining 34 Claude
  config names need actual names-only HOME evidence, verified client writes and
  fixtures before archive activation. None was present in the inspected current
  HOME. Preserve `unknown_entry` refusal rather than guessing a classification.
- Both cold resource measurements in [DESIGN-025 section 15](../designs/025-private-home-expiry.md#15-acceptance):
  2 GiB export, archive put and verify under hold/shelf limits; and the 16 MiB
  relay under operator limits, with `GOMEMLIMIT` near 200 MiB. Earlier warm local
  measurements do not satisfy these checks. Every Job needs a CPU limit.
- Archive guard after parity and owner-identity confirmation, before either
  archive claim is mounted. The first GitOps unit is the unmounted history PVC.
  Tom sets its bound PV to `Retain`; verify that before archive activation.
- Recheck live Reaping non-canary private Sessions immediately before the stop
  flag. The audit found none; ask about holds only if that later list is nonempty.
- Preserve the pickup brief's separate PR order: stop, archive, then history reads.
  Deletion remains gated on the merged H8 installed-read record and a separately
  authorized non-canary Session source. Canary originals cannot serve H8.

The operator was 2/2 at `sha-2221b83`, terminal recording enabled and the stop,
archive, read and delete flags off. The shelf was 2.9.1 with `RollingUpdate`,
only the shared rescue claim mounted and no archive claim. All five retained
canary originals and both failed Jobs remain evidence.

## Evidence

New read-only receipts remain on the pod PVC under
`/home/dev/work/orders/dev-env-h7-prereqs-1013/`. The refresh receipt is
`h7-readonly-refresh-20261013T123626Z-r1.json`, SHA-256
`abb12a660662e26f645bef7fd0d87d8ab7c5ba5c45ce91dfd396a0e7869f0bcf`.
The original source-scan receipt's mistaken textual conclusion is retained and
corrected by `h7-v1-parity-source-scan-20261013T121751Z-r2.json`.
No credential contents or provider transcripts were read or copied.
