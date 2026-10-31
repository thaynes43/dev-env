# DESIGN042 S7 reserved decisions — source preparation

This source implements no live activation. Operator and broker class lists must
default empty. Publication, required tests, independent review, installation and
usable acceptance are separate states. The combined activation proposal remains
held. The existing grant API and broker remain the authority path.

`POST /v1/grants/{name}/decision` selects S7 explicitly with `reserved: true`.
`grant request --reserved` marks an immutable S7 request; `--all-namespaces`
selects the snapshot case, and repeated `--storage-target Kind/name@UID` names
exact storage targets. `grant decide --reserved --tom-ref <message>` uses the
existing decision route. `tomRef` is a required nonsecret decision reference
(for example, Tom's message or the configured delegated-authority reference).
An eligible non-human delegate can act under Tom's persistent configured authority
without a fresh human message or per-lease phone approval. The decision itself
remains fresh and bound to the grant; no authorization algorithm or wire field changes.
`ref` retains the request/reply correlation. The token supplies the actor identity.
The record says **agent assertion, not verified human consent**. It never creates
human provenance or a self-grant exception. Ordinary policy lookup failure never
selects reserved authority. Standing and attested approvals cannot approve S7.

The shared reach implementation owns live actor authentication, exact legacy
session/pod/ServiceAccount binding, requester ancestry and the designated reach
subset. S7 supplies only its explicitly enabled case and exact bounds. Both sides
must repeat the checks before application, on retry and while Active. The actual
legacy Claude lead and top-level GPU requester are required metadata-shaped test
cases; source fixtures do not establish usable access for those sessions.

| Case | Reviewed scope | Materialization and limits |
| --- | --- | --- |
| `rd2` | `breakglass`, `dev-env-grant-breakglass`, every API resource and verb cluster-wide, at most 1h | One per-grant ServiceAccount and ClusterRoleBinding. Root equivalent, including impersonation, ServiceAccount token minting and Secret reads. Never namespaces are not contained. Original-binding cleanup does not recall derived tokens, copied credentials, impersonated sessions or persistent effects. |
| `rd3a` | Secret `get/list`, 1–16 exact namespaces outside Never, at most 1h | Individual namespace RoleBindings, never a cluster binding. Read values cannot be recalled. |
| `rd3b` | `kube.allNamespaces: true`, reserved Secret-read role only, at most 1h | Live approval-time snapshot of namespace names and UIDs outside Never. The finite snapshot bound fails closed; it never truncates. Retry and Active checks refuse missing/replaced snapshot UIDs. Future namespaces remain excluded. No ClusterRoleBinding fallback. |
| `rd4-storage` | One namespace, 1–8 exact kind/name/UID targets | Proposed DELETE admission requires the caller's own Active, unexpired grant, its immutable UID, the authenticated ServiceAccount UID and the exact old object UID. Missing lookup, wrong reference, another grant, ended lease and same-name replacement refuse. Admission does not undo a deletion. |
| `rd4-hardware` | Operation and exact host/device/firmware plus `tomRef`, on the authenticated caller's own installed Active C4 lease | Separate hardware-intent audit operation. Keeper credential approval remains required. Credential authority is unchanged; this record enforces no command or target, reads no key and calls no hardware. |

The proposed default exclusion set for reserved namespaced scope and RD3 is:
`dev-env-system`, `dev-agents`, `dev-tools`, `dev-env-budgets`, `dev`, `default`,
`flux-system`, `kyverno`, `external-secrets`, `kube-system`, `kube-public`,
`kube-node-lease`, `cilium-secrets`. It is distinct from the older three-namespace broker identity fence. These are
agent-proposed defaults, not authenticated Tom decisions or immutable human-only
limits. Matching explicit `--reserved-excluded-namespaces` configuration can choose
a bounded exception; the canonical exclusion list is bound into each decision, and
a configuration change requires a new decision. No empty/error/lookup fallback
widens a bound. RD2 remains the explicit root exception.

Every reserved grant decision binds the approval or denial outcome, grant UID, requester session and Pod UIDs,
authenticated actor Pod and session UIDs, scope digest, approved TTL, case, bounds
revision, decision time and message reference. The effective digest also binds the
canonical namespace snapshot or storage targets. Grant UID, scope, TTL and actor
replacement require a new decision. Hardware intent uses its own set-once audit
record and cannot decide or extend the credential lease.

RD3b initial creation checks each namespace UID before and after the binding
write. Before minting any grant token, the broker persists a materialization
marker. A missing snapshot binding after that marker ends the grant instead of
creating new authority. Cleanup can remove its exact labelled object even if the
namespace was replaced, using the binding object's UID as a delete precondition.
This requires the held admission/RBAC packet to protect those labels and status,
and to keep grant token minting exclusive to the broker. Offline tests cover a
replacement during Create and recovery with a missing binding; they do not prove
live namespace-controller/admission behavior or contain a separate root actor.

Maintenance keeps its **30m default**. The reserved **1h maximum** is a separate
bound. Neither a decision freshness window nor an audit intent extends a grant.

The following runtime changes need a separately reviewed exact packet for Tom.
No literal live actor/namespace UID, image digest or configuration value is
invented here.

| Proposed change | Exact permission or configuration consequence |
| --- | --- |
| CRD delivery | Copy reviewed AccessGrant scope/status fields before enabling any binary or request that uses them; otherwise pruning loses binding evidence. |
| Issuer flags | Explicit matching `--reserved-decision-classes` and `--reserved-excluded-namespaces` on operator and broker. Empty/absent stays off. Existing delegate lists, designated reach subset and any exact legacy binding must match the reviewed reach source. |
| Namespace metadata | Operator and broker need `get/list` on core `namespaces` for the frozen UID snapshot and repeat checks. No Secret values are read by this lookup. |
| RD2 catalog | Proposed `dev-env-grant-breakglass` ClusterRole has API groups/resources/verbs `*`, bound only to the grant SA by the broker. Existing broker `bind` catalog and binding-shape admission must explicitly accommodate this root exception. This is a permission expansion, not Never containment. |
| RD3 issuance | Broker namespace RoleBindings only for exact reviewed names or the frozen snapshot. Snapshot and per-grant ownership checks must remain enforced by source and admission. |
| Grant ownership | Per-grant SA/bindings carry the immutable grant UID in addition to its name. Shape guards must protect those labels; replacement/collision cleanup uses UID preconditions. |
| Storage guard | Proposed Kyverno ValidatingPolicy on DELETE of PVCs, VolumeSnapshots and StatefulSets for `dev-agents/grant-*` callers; failure policy Fail. Kyverno needs metadata `get` on AccessGrants and ServiceAccounts in `dev-agents`. The policy selects the caller's own grant, never all grants as params. It requires protected broker status and operator-only set-once decision annotation. |
| Hardware audit | Operator needs metadata `get` on CredentialJobs in the configured policy namespace to verify the current pinned keeper installation receipt. Protect the new set-once hardware-intent annotation so only the operator can record it. No credential issuance, key access, hardware permission or keeper-policy change follows. |
| Existing residual authority | The storage catalog's non-delete permissions, workload exec, inherited default-SA rights and copied material need explicit disclosure in the final rendered packet. The proposed UID DELETE guard confines that operation only. |

The proposed storage fixture reuses Kyverno's existing resource lookup, JSON and
time libraries. Their interfaces are documented in the
[Kyverno CEL libraries reference](https://kyverno.io/docs/policy-types/cel-libraries/).
Offline CEL tests and the checksum-pinned Kyverno1.19.2 DELETE engine qualification use synthetic lookup results; they cannot prove webhook
installation, lookup RBAC or live enforcement. Synthetic CLI/API tests similarly
prove source behavior, not usable hardware or grant access.

Before a source merge: reconcile the final S5/S3 Secret wire/schema/digest contract
and the reach owner's released source; run meaningful positives, denials, lifecycle
and mutation checks; obtain independent review at the exact candidate head and
required hosted CI; use the guarded source merge path. No manual GitHub Approve is
substituted for review. No source result authorizes policies, flags, templates,
credentials, grants, network changes, rotation, hardware work or session restarts.
