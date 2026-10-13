# H7 read-only prerequisite checkpoint, 2026-10-13

**Status: read-only checkpoint; Q-33 answered. No H7 deployment or acceptance.**
Dev-env source: `2d70017d599212da3ecdc1b3e6554ac507dcaa62`.
The original audit used haynes-ops
`25f09cf42bf0708ceb4d08442ea2d7afe15e6ace`: RBAC at 08:11 EDT (12:11 UTC),
runtime state at 08:14 EDT (12:14 UTC), and the source scan at 08:15 EDT
(12:15 UTC), corrected at 08:17 EDT (12:17 UTC).

The 08:36 EDT (12:36 UTC) refresh used haynes-ops
`bf851754b3cc2b2d7953fd5a84c0aa4da2da46c2`. It rechecked RBAC and Headlamp,
and scanned the three changed manifests. It did not reobserve the operator,
shelf or Session runtime state.

The first H7 owner question is the phone identity exception in
[DESIGN-025 section 9](../designs/025-private-home-expiry.md#9-owner-overrides-hold-and-release-now).
[Q-33](../designs/025-private-home-expiry.md#18-h7-owner-identity-q-33) is answered
directly in this chat: preserve Tom's personal Kubernetes account permanently;
Headlamp retires after full v2 adoption. The original native prompt never arrived.
This settles the access choice, not the exact authenticated username or native
phone-prompt acceptance. Q-34's workstation request is withdrawn after Tom said
he cannot perform it from his phone and asked the coordinator to continue.
Discover the username from accessible read-only evidence. The unmounted history
PVC can proceed; keep any identity-dependent policy pending until verified.

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

The 17:06 UTC personal-identity audit used haynes-ops
`3030615481190860a210a14eadffae7fc28c49d5`. Its cluster inspection record from
2026-07-29 establishes an exact historical personal Kubernetes username with
`system:masters`. Current Omni metadata has the same account as Admin, last
active 2026-10-10. Live Kubernetes RBAC has no personal User subject; it does not
prove current personal authentication. This is a strong candidate for guard
preparation, not installed access acceptance. Its account email stays in local
evidence; any guard needs a reviewed private identity parameter.

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
  HOME. The pinned 2.1.296 code audit found conversation messages persisted under
  `remote-control/fork-seeds`, so that name cannot be assumed to be runtime-only.
  Code candidates for `plans`, `tasks` and user `agent-memory` also lack the
  observed-name/type prerequisite. All 34 remain unclassified. Preserve
  `unknown_entry` refusal rather than guessing a classification.
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

In the 08:14 EDT (12:14 UTC) runtime snapshot, the operator was 2/2 at
`sha-2221b83`, terminal recording enabled and the stop,
archive, read and delete flags off. The shelf was 2.9.1 with `RollingUpdate`,
only the shared rescue claim mounted and no archive claim. The snapshot contained
all five retained canary originals and both failed Jobs; preserve them as evidence.

## Evidence

New read-only receipts remain on the pod PVC under
`/home/dev/work/orders/dev-env-h7-prereqs-1013/`. The refresh receipt is
`h7-readonly-refresh-20261013T123626Z-r1.json`, SHA-256
`abb12a660662e26f645bef7fd0d87d8ab7c5ba5c45ce91dfd396a0e7869f0bcf`.
The original source-scan receipt's mistaken textual conclusion is retained and
corrected by `h7-v1-parity-source-scan-20261013T121751Z-r2.json`.
No credential contents or provider transcripts were read or copied.

The bounded F4 audit is under
`/home/dev/work/orders/dev-env-f4-evidence-1013/`:
`f4-pinned-code-evidence-and-candidates-r1.json` and `audit-manifest-r1.json`.
The official 2.1.296 artifact's registry integrity was verified; the retained
binary is non-executable. No provider session or runtime change was used for it.

The personal-identity audit is under
`/home/dev/work/orders/dev-env-h7-personal-lookup-1013/`:
`personal-identity-findings-r1.json`, SHA-256
`0457ded016daf2012e57d96e3c99022aff0f99147e3d096c5faaa0da91968319`.
No current personal kubeconfig, credentials or authentication attempt was used.
