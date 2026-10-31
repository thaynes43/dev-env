# Architecture answer — reusable30-minute admin lease

The choice was recorded before implementation in OP233j4383. This continuation incorporates addenda10–16 and the required native documentation review. Source custody is now dev-env-1031-232038 after215024 exited; no live activation. Frozen PR801/cac49884 and its tests remain prototype evidence.

Choose ONE broker-owned issuer namespace, `dev-env-admin-issuance`. SessionNamespace still locates sessions, requester Pods, grants and installation. Ordinary identity, operator API audience and requester network stay unchanged. Both installed native identity and Kyverno exec guards match dev-agents callers; issuer admin identities are outside both matches. No per-lease GitOps, marker, lookup service or broker policy-mutation right.

Once-only proposed security/config deltas:

1. Protect the issuer as a native identity-guard TARGET namespace, including its Namespace object. Keep caller match and other Deny rules. Ordinary baseline/grant identities cannot create/delete/recreate/mint accounts, bind RBAC or run Pods there; the existing trusted workbench exception remains.
2. Create the issuer with standard restricted PSA, no exemption. Add a broker Role/RoleBinding there, which combines with existing root bind authority to grant EFFECTIVE ROOT-ISSUANCE authority: serviceaccounts get/create/delete; serviceaccounts/token create; subject dev-env-system/dev-env-broker. No namespace creation, Pods, exec or policy rights.
3. Adjust broker-guard once: grant-* SAs/tokens may use issuer; root CRBs bind exactly their own same-name issuer SA. Other catalog subjects stay dev-agents; network policy scope and non-root grants are unchanged. Kyverno caller match is unchanged.
4. For PR799's exact legacy-SA UID recheck, add API operator get ONLY on dev-agents/dev-env-agent through a named Role/RoleBinding. The pinned operator RBAC snapshot lacks this read; broker already has it. No other SA read, mint or mutation is added.
5. Publish the UNBOUND root role with apiGroups/resources/verbs [*] AND nonResourceURLs/verbs [*]. Install reviewed status CRD and compatible binary. Only after Tom's specific security approval configure existing matching actor/class lists and default-off `--enable-admin-leases --admin-issuer-namespace=dev-env-admin-issuance`. Enable only rd2; no RD3/storage extras.

Per lease, reuse the existing authenticated configured delegate, class and live reach/UID checks. An authorized delegate may act under persistent delegated authority without a new human message or per-lease phone approval. The eight inherited tomRef labels/comments/docs are corrected on802 in a separate commit after800 custody ended; wire name/shape, error codes and authority algorithms stay unchanged. Regression accepts eligible tree/legacy non-human delegates with a persistent-authority reference and refuses absent/invalid refs or an unauthorized actor. An assertion is never verified human consent.

Require approved TTL EXACTLY30m and fixed expiresAt=approvedAt+30m. Persist original issuer namespace/name and SA/CRB UIDs before mint; never adopt a replacement UID. Invalid/missing issuer or mismatched authority refuses issuance while the controller remains able to clean existing identities. Round remaining token lifetime DOWN; below600s do not mint. Validate returned expiry against the fixed deadline and recheck authority/UIDs before delivery. A normal same-Pod late refresh may retain an already-installed valid token only after those rechecks; it never extends actual JWT expiry. Late/overlong responses or Pod/UID/approval/binding mismatch revoke original authority.

Cleanup persists a no-reissue flag, confirms original CRB deletion first, then original SA, then requester token file through the original grant namespace and Pod UID fence. UID/ownership mismatch, API/finalizer or file-removal failure remains cleanup-pending with a visible error. Config changes never retarget cleanup. Finish old leases before changing controller watch namespace. No remint after release/expiry or cleanup start.

RBAC binds names, not UIDs. Root includes synthetic markers; reads bypass mutation admission. A trusted minting principal can recreate an SA behind a leftover CRB and read/list/watch; tests demonstrate this. Derived identities/tokens/RBAC/Secret copies, open exec, Jobs and persistent effects can survive original revocation. UID is an issuance/lifecycle fence, not root containment or universal rollback.

Both target Pods have the shared handoff mount. Their agentd2.45/86cef5bb install/remove protocol file is byte-identical to current source. The pinned Linux CLI/MCP helper is available as `clients/agent-run-linux-amd64`, sourceb29d493, SHA256 `ac8411ee90641050320072329422b0fab9b66c18296aa726f162d53846ed3436`,15919679bytes; CLIENT-COMPATIBILITY.md records successful version/help and protocol checks; no default-client/MCP/HOME/pairing change or restart. This is artifact availability, not live use. Recommend the30m default for first separately approved READ-ONLY qualification; do not promise10m or15m usability. Existing privileged-Pod exec differs from creating one; target PSA still applies, with no new exemption.

Read-only metadata22:28Z found no catalog root CRBs or breakglass leases. Refresh before rollout. Protect namespace and constrain root subjects while issuance is off, then issuer/RBAC/unbound role, schema/binary and finally approved configuration. Guard-first rollout intentionally refuses legacy dev-agents root subjects; do not broaden it. A newly found legacy root lease is a rollout hold for authorized cleanup.

Evidence remains distinct: unit/fake-clock lifecycle tests, real local envtest TokenRequest/native RBAC/admission, locally signed expired-JWT rejection, original-SA token invalidation, and later live cessation/cleanup. No30m wall-clock, live Kyverno/PSA/Cilium or usable acceptance is claimed. Requester223503's supplied selector preflight matches existing API443/6443 egress; no network change. PR800's reviewed8260 tree equals actual mergea290233; only our source changes sit above it. Final exact-head CI/native review and the explicit activation decision remain separate gates.


## Permanent broker authority and rollback boundary (ADDENDUM16)

The broker's permanent ability to create issuer ServiceAccounts, bind the wildcard
root role and mint their tokens is EFFECTIVE ROOT-ISSUANCE AUTHORITY. The existing
broker ClusterRole permits `bind` on `dev-env-grant-breakglass`
(haynes-ops `kubernetes/main/apps/dev-env-system/rbac/app/broker.yaml:32-33`)
and ClusterRoleBinding create/delete. That existing allowance plus the issuer Role
and broker-guard delta place issued root subjects outside both guards. The small
SA/token Role is therefore not low privilege. The 30-minute path is enforced by
application checks in a trusted broker.
A compromised broker, or an approved root lease, can derive persistent authority.
This is the intended homelab trust model; no new containment machinery is proposed.

Flag OFF stops normal issuance but does NOT remove the permanent RBAC capability.
Rollback must first close normal issuance and revoke all ORIGINAL leases, confirming
CRB deletion before SA/token and requester-file cleanup, while keeping the new
broker/schema and required cleanup rights available. Do not downgrade/prune original
UID status before cleanup is complete. Any cleanup error remains a visible hold.
Then remove/revert issuer RoleBinding/Role and the root-role bind allowance; remove
the unbound root catalog role and restore guard/config deltas with dependency checks.
Do not restore the legacy dev-agents root-subject allowance while leaving root bind
and the wildcard role available: that would retain root issuance through the old
namespace. Keep target namespace protection until issuer contents are safely gone.
The named-SA metadata read can remain only if another authorized legacy feature
still needs it. Restore exact prior pins/config only after compatible cleanup.

Derived tokens/identities/RBAC, copied Secrets, open exec, created workloads and
persistent effects are NOT automatically undone by original lease cleanup or this
rollback. Audit/removal of such effects is separate authorized work, not an automatic
promise. This is disclosure and an ordered rollback plan, not a new approval gate.


## Required review dispositions and publication (R2-R4)

F1 accepted source trust disposition: RD2 is true root. The issued principal can write and exec
in dev-env-system, dev-agents, dev-tools and the issuer. The dev-env namespace fence
does NOT apply to RD2 root; ordinary grants retain it. This is deliberate proposed
scope under persistent delegated authority, not a claim of runtime acceptance.
F2 accepted source trust disposition: existing tree mode lets an eligible delegate approve root
for its own spawned child. This is retained under configured persistent authority;
self/requester-descendant refusals are unchanged. The exact designated legacy actor
can also reach the approved top-level requester through existing reserved authority.
Both consequences must be visible to Tom's later security decision.
Issued admin SAs can exec privileged-SA pods (Kyverno caller does not match) and
controller-namespace pods (native caller does not match). Envtest proves the native
half, not live Kyverno, PSA or Cilium.

Immutable publication plan: after exact-head CI and native independent approval,
use the authorized normal pinned source merge and publish.yml. Record actual merge
commit M and verify its
production tree against reviewed83c43484. Publish/cosign-verify
ghcr.io/thaynes43/dev-env-operator:sha-<M-first7>, then seal BOTH operator/broker pins
as sha-<M-first7>@sha256:<observed-published-digest>. Never use latest, overwrite a
tag, or pretend candidate83 is a different future squash merge.
The digest remains UNKNOWN until publication: PR builds do not publish. Retain the
workflow run, exact source, immutable tag, OCI digest and successful cosign
verification. Signed publication does not authorize a haynes-ops pin or deployment.
The normal release-please/versioned publish-agent.yml workflow publishes the signed
agent image containing agent-run. For the scoped client, verify that image digest
and workflow identity, then extract its agent-run and retain its checksum/source
receipt for side-by-side use. Do not substitute unsigned execution or change trust,
PATH, MCP, HOME, pairing or active sessions. If signed delivery is unavailable,
record the precise gap and keep qualification held. The previously built unsigned
helper is offline compatibility evidence only, not the selected execution artifact.

The authority granted is EXACTLY 30 minutes of FULL CLUSTER ADMIN. The first
qualification actions are planned READ-ONLY, with early release; the credential
itself is not read-only and is not confined to a GPU UUID or command list.
These are PROPOSED
invocations, never executed here. Each actor uses its own authenticated environment;
there is no Mac approval command. CLIENT denotes the explicitly pinned scoped
agent-run extracted from the verified signed publication, not a PATH replacement.
GRANT_NAME/GRANT_UID/SCOPE_DIGEST come from fresh request/show responses. REQUEST_REF
is current platform correlation; AUTHORITY_REF is legitimate persistent authority.

```text
Requester haynes-ops-1029-223503:
 CLIENT grant request --type breakglass --role dev-env-grant-breakglass --reserved --ttl 30m --reason 'Bounded read-only Kubernetes admin qualification' --no-wait -o json
Eligible coordinator dev-env-1029-132039:
 CLIENT grant show GRANT_NAME -o json
 CLIENT grant decide GRANT_NAME --approve --reserved --grant-uid GRANT_UID --digest SCOPE_DIGEST --ref REQUEST_REF --tom-ref AUTHORITY_REF --ttl 30m --reason 'Read-only task under configured delegated authority'
Requester, after Active AND installation:
 CLIENT grant show GRANT_NAME -o json
 CLIENT grant use GRANT_NAME
 kubectl auth whoami
 kubectl auth can-i --list
 kubectl get --raw=/version
 kubectl get namespaces -o name
 kubectl get nodes -o name
 kubectl get pods -n dev-agents -o name
 CLIENT grant release GRANT_NAME
 CLIENT grant use default
```

This is a read-only task envelope, not per-command approval. No Secret reads,
workload/RBAC/policy/hardware/storage mutation, privileged Pod or executor manifest
is proposed. Existing privileged-Pod exec differs from creating one; PSA applies.

Rollback metadata checks: zero non-ended grants with status.adminCredential; zero
CRBs referring to dev-env-grant-breakglass; original SA UIDs absent; requester file
and selected context removed. Retain new broker/schema/cleanup rights until complete.
Then remove issuer RoleBinding/Role and empty namespace, remove root bind/catalog as
appropriate, only then revert guard/config deltas. Never remove target protection
behind a leftover binding or restore legacy root subjects while root bind/catalog
remain. Derived authority and effects are not automatically undone.

Optional LOW risks retained, not silently fixed: R5 CleanupStarted persistence
conflict can permit a later bounded rechecked remint, so overrun revoke is not a
universal durable guarantee across that conflict; R6 clock skew can falsely revoke
(fail-closed); R7 original-object label drift blocks cleanup with visible failure.
R5-R7 are retained trust/availability/cleanup risks, not fixed by this documentation
commit. The INFO persistent-reference reuse case was not added; no new replay gate
is claimed. Transient mint errors fail closed; privileged status-edit and config
change limits remain as described in the native review. No optional code cycle is
started after the frozen candidate.
