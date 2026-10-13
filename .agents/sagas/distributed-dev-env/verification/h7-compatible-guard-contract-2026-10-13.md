# H7 compatible archive guard: implementation contract — 2026-10-13

**Current evidence route:** the separate reader failed before any baseline and
is held under Q-36. Use [the existing-access checkpoint](h7-reader-failure-and-existing-access-2026-10-13.md)
for the bounded Mac diagnostic and evidence work. No complete installed
acceptance or new reader access follows.

The coordinator ratified this implementation contract under
[ADR-007/Q-35](../adrs/007-preserve-v1-archive-trust-boundary.md). ADR-007 and
the measurement source prerequisites merged together in
[dev-env #262](https://github.com/thaynes43/dev-env/pull/262), `01c6bc99`.
This page preserves the ratified outcome. This is agent
implementation judgment within authorized H7 scope. **Guard source, deployment
and real webhook acceptance remain separate.** No archive mount, history
activation, v1 change or new RBAC grant follows from this contract.

The [source and registered hook checkpoint](h7-compatible-guard-source-and-hook-2026-10-13.md)
records the completed #3902 source unit and observed deployment. Complete
installed request and pre-mount baseline acceptance remain pending.

## Trusted actors

The guard protects both exact archive claims in `dev-agents`, including the
shelf before it mounts them. It denies protected access to untrusted
ServiceAccounts, including v2 agents/grants, the broker and the keeper.
Actor trust comes from the authenticated request username, never the workload's
selected ServiceAccount, labels or an arbitrary owner reference.

Preserve these verified current administrator routes as fixed exceptions:

| Exact ServiceAccount | Existing route preserved |
|---|---|
| `dev/dev-env`, `upgrade-agent/dev-env-ops` | Trusted v1 administrator access |
| `frontend/headlamp` | Existing administrator backend; no owner-override identity |
| `flux-system/kustomize-controller`, `flux-system/helm-controller` | Existing Flux administrator routes; kustomize reconciles the shelf |
| `dev-env-system/dev-env-operator` | Archive operator |
| `database/cloudnative-pg`, `network/multus` | Existing database/network infrastructure exec and/or Job access |
| `rook-ceph/rook-ceph-system`, `volsync-system/volsync` | Existing storage infrastructure exec and/or Job access |
| `kube-system/reloader` | Existing infrastructure Job access |

The five legacy infrastructure routes above are explicit trusted actors with
current broad exec and/or Job-create bindings in the H7 direct-RBAC receipt.
They are not newly granted rights. Do not exempt future identities dynamically
because someone grants them administrator RBAC. Human users keep existing
RBAC. System node, scheduler and controller identities are infrastructure,
not human users; their legitimate paths need explicit treatment.

Both archives remain reachable by these trusted actors and existing node,
storage, hypervisor and human administrator routes. The policy supplies no
isolation from them. Before claiming protection against known v2 agents/grants,
verify complementary identity/exec controls prevent their borrowing **each**
trusted identity. Missing proof narrows the claim; it does not authorize a v1
permission reduction. Preserve Q-33's personal Kubernetes account separately
from Headlamp and the later exact-user owner-override policy.

The existing identity guard exempts `dev-env-workbench`. The new archive policy
must refuse its direct protected requests, but cannot claim transitive borrowing
isolation for it. That application is not currently deployed; a future workbench
delivery must review this archive boundary, as DESIGN-024 already requires.

## Controller proof and resource coverage

Use the actual Kyverno 1.19.1 ValidatingPolicy engine, Fail/Deny, no background
or automatic controller-rule generation, and explicit namespace/resource rules.
Protect CONNECT exec/attach/port-forward, ephemeral containers, old and new
archive claims, shelf template changes and every standard workload podSpec
location, including ReplicationController and CronJob. ControllerRevision history
is actual JSON under `data.spec.template.spec`; it needs protection too.

Materialization exceptions require the exact authenticated stock controller,
same namespace, expected parent kind, current parent UID and required inherited
claim/ServiceAccount/template data. Lookups are bounded direct Gets. Missing,
malformed, recreated or denied/error parents cannot establish an exception.
Only documented generated fields may be normalized. Historical ReplicaSets and
revisions remain usable for legitimate rollout/rollback; their protected data
and owner chain cannot be introduced, rewritten or reparented by an untrusted
actor. A selected hash or owner reference is only a lookup hint.

The proof requires an audited clean activation baseline: no unverified existing
archive-bearing workload or revision, and a verified Flux-owned shelf lineage.
Then only trusted roots and authenticated controllers inheriting them can
introduce protected execution. Metadata is not a creator record. An unexpected
baseline blocks acceptance; no provenance stamp or automatic cleanup repairs it.

Check status/scale and other subresource semantics against the actual API
strategy. Unchanged execution alone does not establish unchanged owner metadata.
Protect parent UID/owner chains and shelf identification across updates, while
preserving legitimate controller/node maintenance. A pure shelf restart ignores
only `kubectl.kubernetes.io/restartedAt`; unrelated spec, template metadata,
owner or finalizer changes do not enter that exception. DELETE/eviction creates
no new reader and retains existing behavior.

## Validation and installed acceptance

The offline harness must load the **actual policy expressions** through the
pinned Kyverno compiler/evaluator. Supply explicit AdmissionRequests, distinct
old/new objects and mock Get responses, including NotFound/null, Forbidden,
transport errors and malformed objects. Verify trusted requests bypass unsafe
lookups, all untrusted paths fail closed, legitimate historical controllers
pass, forged/recreated/reparented chains fail and nonarchive work is unaffected.
Ordinary Kyverno CLI fixtures do not establish CEL CONNECT or distinct old/new.

Prefer hosted compilation for the new dependency graph. Any necessary local
check uses the shared lock, `nice -n 19`, `GOMAXPROCS=2`, package/test concurrency
1 and count 1. No CPU burners, stress tools or wide/looped tests on shared nodes.
The harness has no API, image or provider side effects.

After reviewed source and required checks, GitOps deployment needs an independent
installed receipt: exact policy/webhook rules, namespace filters, failure policy,
Ready state, controller bindings and unchanged retained runtime. A forbidden
policy read is not an empty listing or successful acceptance. An authorized
redacted administrator receipt may supply missing read evidence; credential
borrowing and token reads are not the verification path. Actual webhook requests
require a separate reviewed fixture/delivery plan, preserving the unused history
claim and existing canaries. Source evaluation proves no installed request result.

[Issue #261's public ratification](https://github.com/thaynes43/dev-env/issues/261#issuecomment-6153207334)
also preserves the outcome. Copies under
`/home/dev/work/orders/dev-env-h7-archive-guard-1013/` are local working notes:

| Working note | SHA-256 |
|---|---|
| `compatible-guard-proposal.md` | `2c949d957c145799c261795f7efc664264df265330d6143caee2bb75f0bcfd46` |
| `root-guard-ratification-r1.md` | `a542b5a805dd1bb7f1c46cca617deba7e1353e49e0c12145a1767ec0db51c20b` |

The compatible source unit
targets only the new policy, its app resource entry and focused synthetic tests
with necessary CI wiring. Shelf image/mount, owner identity/Retain, F4, both cold
measurements, flags, H8's non-canary read and deletion remain later gates.
