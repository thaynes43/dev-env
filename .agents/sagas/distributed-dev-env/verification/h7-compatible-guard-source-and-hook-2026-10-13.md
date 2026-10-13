# H7 compatible archive guard: source and registered hook — 2026-10-13

[haynes-ops #3902](https://github.com/thaynes43/haynes-ops/pull/3902) merged at
20:22:26 UTC as `650f3a04113d78b85f0d345a1944bc9fb8508d10`, from reviewed head
`e1577341ca95d280cc40d5c2a7e83c65cf566cbf`. It installs the compatible guard
under [ADR-007/Q-35](../adrs/007-preserve-v1-archive-trust-boundary.md) and the
[ratified contract](h7-compatible-guard-contract-2026-10-13.md). This is source
acceptance and observed webhook configuration, **not complete installed
admission or pre-mount baseline acceptance**.

## Source acceptance

The focused harness loads the archive policy and the complementary identity/exec
policies through the pinned Kyverno 1.19.1 compiler, matcher and lazy evaluator.
It supplies explicit AdmissionRequests, distinct old/new objects, actual CONNECT
options and synthetic resource lookups. It calls no live API, image or provider.
[Exact-head hosted run38461831857](https://github.com/thaynes43/haynes-ops/actions/runs/38461831857)
passed all 1,593 named tests/subtests, with no failures, skips, errors or missing
pass records. Controller history and maintenance, lookup errors/malformed
returns, owner removal and complementary borrowing cases passed.

Independent native Codex review found and corrected missing controller
maintenance allowances, DaemonSet history/dedup handling, missing-oldObject
status validation, an API construction error and workflow triggers. Subsequent
hosted failures exposed a policy-loader path error and ten incorrect lookup-count
assertions. The last ten requests already denied correctly; removing every owner
reference selects no parent GET. Only their count expectations changed, with
`allow=false` and every case preserved. Failed runs are retained and do not count
as acceptance. The final [independent R4 review](https://github.com/thaynes43/haynes-ops/pull/3902#issuecomment-6154449945)
has no finding. Required checks passed, and advisory findings have concrete
responses and are resolved.

Hosted compilation keeps the new dependency graph off the shared node. Exact
pinned cache actions preserve only module/build caches after completed tests,
keyed to OS, architecture, Go 1.26.6 and module hashes, without broad restores.
The serial suite retains `-count=1`; cached compilation never replaces evaluation.

The [R3 prerequisite audit](https://github.com/thaynes43/haynes-ops/pull/3902#issuecomment-6154385648)
traced admission resource.Get through pinned Kyverno source to its admission
controller's in-cluster dynamic client. The deployed
`kyverno/kyverno-admission-controller` ServiceAccount has the observed
`kyverno:admission-controller:view` binding to `view`. Effective rules and the
`system:aggregate-to-view` contributor allow GET for all required resources:

| API group | Resources |
|---|---|
| core | Pods, ReplicationControllers |
| apps | Deployments, ReplicaSets, StatefulSets, DaemonSets, ControllerRevisions |
| batch | Jobs, CronJobs |

Root independently checked binding UID
`3d8b57fb-be32-4fd3-a1d1-f5d516a8048c`. This establishes configured lookup
permissions, not a live admission lookup result. Kubernetes v1.35.5 primary
source also resolved the advisory's MarkPodsNotReady uncertainty: it copies the
existing Ready condition, changing status and transition time; the guard preserves
observedGeneration and the other copied fields.

## Observed deployment

Root reconciled `kyverno/kyverno-policies` through Flux. At 20:23:21 UTC it
reported Ready=True and Healthy=True for exact revision
`main@sha1:650f3a04113d78b85f0d345a1944bc9fb8508d10`. The generated
`kyverno-resource-validating-webhook-cfg`, UID
`052d322b-e958-4fc9-8ca6-d96e8d1db7fe`, resourceVersion `828461376`, contains:

| Field | Observed value |
|---|---|
| Hook | `vpol.validate.kyverno.svc-fail-finegrained-dev-env-archive-guard` |
| Service/path | `kyverno/kyverno-svc:443`, `/vpol/dev-env-archive-guard` |
| Failure/match policy | `Fail`, `Equivalent` |
| Timeout/review version | 10 seconds, `v1` |
| Side effects | `NoneOnDryRun` |
| Namespace | exact `dev-agents` namespace label and request condition |
| core rules | CREATE/UPDATE/CONNECT; Pods and ReplicationControllers including subresources |
| apps rules | CREATE/UPDATE; all five listed apps kinds including subresources |
| batch rules | CREATE/UPDATE; Jobs/CronJobs including subresources |
| Actor condition | authenticated ServiceAccounts except the eleven fixed trusted routes in the contract |

The condition preserves current v1, Headlamp, Flux, operator and infrastructure
administrator routes. It adds no rights or dynamic administrator exemption.
Human accounts retain existing RBAC; Q-33 preserves Tom's personal Kubernetes
account separately from Headlamp. The installed configuration supports no
isolation claim against trusted v1, human administrators, nodes, storage or
hypervisors, nor a workbench transitive-isolation claim.

Installation activity `act-202217-318848`, scoped to
`kyverno,dev-agents,dev-env-system`, ended early after these observations.
No v1 resources or permission bindings, retained canaries/Jobs, provider data,
credentials, history mounts or expiry flags were changed by this PR.

## Remaining evidence and next unit

Current v1 access cannot list dev-agents ReplicationControllers or read the
ValidatingPolicies, so its incomplete baseline and Forbidden reads are not empty
or successful results. Q-35 preserves that access. The next separate reviewed
GitOps unit is a dedicated, narrowly read-only metadata audit: complete bounded
workload listing, exact named policy/native binding/webhook reads, sanitized
collection and its own short-lived API credential. It receives no secret,
impersonation, workload-write, exec or archive-mount right. It is an untrusted
archive actor and changes no v1 grant. This API-authenticated audit is separate
from the later credential-free measurement Jobs. Do not request workstation
commands from the owner or borrow an administrator credential.
The coordinator proposes this reader within the authorized H7 prerequisite
evidence work; ADR-007's relayed ruling supplies no grant or privilege expansion.
Its precise source scope still needs independent review before deployment.

The audit must establish policy readiness, exact installed complementary guards,
complete paging and the Flux-owned shelf lineage; unexpected archive-bearing
roots/history or an incomplete kind blocks the pre-mount baseline. Actual API
webhook acceptance needs its own reviewed finite delivery plan. Source tests,
registered hooks and Flux health establish no actual installed request outcome.
The shelf image/mount must wait for these evidence gates. At 20:32:41 UTC none of
the five dev-agents Pods referenced the history claim; its same bound PV was still
Delete at 20:28:47 UTC. Owner-set Retain, exact
personal owner-override identity, F4, both cold resource measurements, later flags,
H8's approved non-canary read and deletion remain separate gates.

Local working receipts under `/home/dev/work/orders/dev-env-h7-archive-guard-1013/`
are corroboration; public PR/run links and this record preserve the cold-start
conclusion:

| Receipt | SHA-256 |
|---|---|
| `native-review-r4.json` | `8dd38070329a8c38ea95fcccd4588371de4715fdb068e648fac5b737f37755d7` |
| `pr3902-merged-r1.json` | `d3c9df72d6c11b3ff4b3f8e1d9425c5023825b914204fe1b6c3f8ea66a6d7f39` |
| `installed-flux-r1.json` | `453a5c2d460de3b9af1ef25d130f4fc3440a967bdeb7d6e4d484d6aff2333c2a` |
| `installed-archive-webhook-r1.json` | `a099c4a756532f9feaa98467cc78bee38b23dccf545fd9909be95af2957319fb` |
| `pv-retain-target-read-r2.json` | `5372c9b9d1d68042272a21752470a6e9d4df084e2b09c6a725dcee8cc6a317c7` |
| `history-unmounted-after-guard-r1.json` | `d3e61705d9e21ca52acb3056aa930a7e7bb1201e4187ba766e440b08bf22ff19` |
