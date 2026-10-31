# DESIGN-043: reusable 30-minute Kubernetes admin lease

Status: Proposed, source only, DEFAULT OFF. OP233 owns this continuation.
The pre-implementation choice is preserved in [ARCHITECTURE-ANSWER.md](../verification/rd2-admin/ARCHITECTURE-ANSWER.md), mirrored in the existing shared OP233 handoff.
No deployment, merge or security activation is authorized. The previous static
compiler is preserved as prototype evidence in draft PR801 at
`cac49884af295a369a7d731f50e170f53e571e18`; its source tests are not an operational
workflow. This design replaces its per-lease guard edits with one reusable boundary.

## Choice and once-only security bundle

Issue each admin ServiceAccount in `dev-env-admin-issuance`, a dedicated broker
issuance namespace outside `dev-agents`. SessionNamespace still locates sessions,
requester Pods, AccessGrants and token installation. The requester keeps its ordinary
SA, operator API audience `dev-env-operator`, and Pod network identity. The issued
admin token uses Kubernetes' API audience; it is not an operator API credential.

The installed native `dev-env-identity-guard` and Kyverno CEL `dev-env-exec-guard`
both match `dev-agents` callers. This namespace choice handles both coherently:
issued admin principals do not match either. Ordinary callers keep their existing
Deny controls. This does not mean unchanged target protection was sufficient:
ordinary workload roles can create SAs outside protected namespaces. Therefore the
proposal extends the native guard's protected **target** namespace list once to
include the issuer, including writes to its Namespace object. Baseline and ordinary
grant callers cannot create, delete, recreate or mint issuer accounts, bind RBAC,
or create Pods there. The caller match and all other validations stay unchanged.
The trusted workbench exception remains the existing trust boundary.

The exact proposed source fixtures are:

- `internal/broker/testdata/proposed-haynes-ops/admin-lease/issuer.yaml`: empty
  namespace with PSA `enforce: restricted`, no exemption; Role with only
  `serviceaccounts` get/create/delete and `serviceaccounts/token` create; one
  RoleBinding to `dev-env-system/dev-env-broker`.
- `.../admin-lease/identity-guard.yaml`: native target namespace protection above.
  The installed snapshot remains byte unchanged in `testdata/identity-guard/`.
- `.../admin-lease/broker-guard.yaml`: grant-* SAs/TokenRequests may live in the
  issuer; network policies stay in dev-agents. A root ClusterRoleBinding must
  bind exactly its own same-name issuer SA. All other catalog subjects remain
  dev-agents, and no grant RoleBinding may target the issuer.
- `internal/broker/testdata/grant-breakglass-RESERVED-PROPOSAL.yaml`: unbound
  catalog root ClusterRole with `apiGroups/resources/verbs: ['*']` **and**
  `nonResourceURLs/verbs: ['*']`. No standing administrator subject.

Existing broker bind authority names this catalog role; no broad policy mutation,
namespace creation, impersonation or new broker workload/exec rights are added.
The once-only operational bundle must also install the generated AccessGrant CRD,
the reviewed binary and existing approved decision dependencies, then configure
`--enable-admin-leases --admin-issuer-namespace=dev-env-admin-issuance` on the broker.
Both default off/unconfigured. Invalid or unconfigured issuer settings refuse creation/mint, while the broker
continues running to clean original credentials. Missing namespace refuses
creation before mint. Existing reserved class lists and
actor designation must enable only the intended approved path. This is not
permission to enable RD3, storage, hardware, S7 extras or any standing GrantPolicy.

Dynamic per-lease guard mutation was rejected: it needs broader broker policy
rights, affects every session and recreates security GitOps/CI latency on a30m
clock. Static username/UID exemptions also need a security PR for every lease.
No new authorizer marker or parameter framework is introduced. Wildcard root
already authorizes every synthetic marker, so markers provide no independent fence.

## Authority and issuance

Use the existing authenticated S2 delegate modes, configured reserved class and
reach semantics from PR799 and PR800. No eligibility or decision algorithm changes
belong to this increment. An explicitly authorized delegate can make a fresh lease
decision under Tom's persistent delegated authority without a fresh human message
or per-lease phone approval. The protected decision binds grant/requester/session/
Pod/actor UIDs, scope, reference and TTL. It remains **agent assertion, not verified
human consent**. Agent-chosen commands stay within the approved task.

At PR800 d13f2782, `internal/apiserver/grant_reserved.go:18` checks only
`ValidRef(req.TomRef)`; line19 incorrectly says "fresh Tom message". The CLI's
`internal/agentrun/grant_reserved.go:103` likewise checks only syntax. The pure
core checks reference syntax, not human identity, message age or a new phone
approval. This is a provenance-label/documentation correction, not a policy delta.
The wire name `tomRef` stays for compatibility. PR800's owner owns that correction
and its regression coverage; this branch does not change those files. Final
qualification must reconcile the actual reviewed dependency head. No current live
policy is broadened by this documentation.

The new path requires an Active, unreleased, undeleted RD2 reserved decision with
approved TTL exactly30m and `expiresAt == approvedAt +30m`. Existing live actor and
requester checks run before creation and again before and after mint. A legacy
approval alone cannot enter this path. The old source-only reserved materializer
remains held; it is not the selected operational workflow.

Before any TokenRequest, status records the original issuer namespace/name,
ServiceAccount UID and ClusterRoleBinding UID. A known UID is never replaced or
recreated, including after configuration changes or restart. Before a first UID
receipt, crash recovery may read an exact grant-UID/managed-by-labelled object; no
token can yet have been delivered. Root can forge those labels, as discussed below.
SAs disable automount. The binding points to that account alone.

Mint rereads the grant, original SA and binding from the API. Remaining TTL is
rounded down; less than600s refuses instead of applying the old600s floor. The
actual returned expiry must be future and no later than the fixed approval end.
A late API response or concurrent release/identity change prevents delivery. A
mint refusal enters cleanup, except a normal same-Pod late refresh at the600s
floor may retain its still-valid installed token after live authority/UID checks.
It requeues at the fixed deadline and never extends actual JWT expiry. Installation uses the existing requester Pod UID
fence and records actual token expiry. No token is logged, stored in Kubernetes
Secrets or published in source artifacts. A copied token is usable elsewhere while
valid: TokenRequest cannot bind it to a Pod running under a different SA.

## Revocation and failure

Release, expiry, loss of authority or configuration mismatch starts cleanup with a
durable no-reissue flag. Cleanup uses the original status namespace/name and UID,
never current issuer configuration. It confirms original CRB deletion first, then
original SA deletion, then token-file removal through the original grant's session
namespace and the current controlled Pod's UID fence. Delete preconditions use the
read UID. Ownership or UID mismatch, finalizer delay, API failure or failed token
file removal leaves Active cleanup-pending with a retained error; it never reports
ended or adopts a replacement. Retry only continues cleanup. Missing objects are
idempotent success. A missing requester Pod has no remaining local file; otherwise
an unavailable installer cannot claim successful file cleanup.

A changed SessionNamespace does not retarget cleanup invoked for an old grant.
Changing a controller's watch namespace does not by itself schedule old grants;
operational reconfiguration must finish old leases before changing its watch scope.
No remint occurs after release/expiry or once cleanup begins. Disabling the feature
does not disable cleanup of already recorded credentials.

## Limits and evidence

RBAC authorizes namespace/name, never SA UID. Original UID checks are issuance and
lifecycle fences, not perfect containment of trusted root. Admission never sees
GET/LIST/WATCH. A principal already able to create/mint an issuer account can
recreate a name behind a leftover root CRB and read/list/watch Secrets. The envtest
explicitly demonstrates that access, then demonstrates CRB-first revocation while
refusing to delete or adopt the replacement UID. The original token fails once its
SA is gone; a separately signed local expired-token fixture verifies token expiry.

A root holder can mint other tokens, make identities/RBAC, impersonate, copy Secrets
and change persistent resources or guard configuration. Those effects, existing
exec streams and running Jobs can outlive this lease. The envtest demonstrates a
derived identity/token remains useful after the original authority is removed.
There is no universal rollback; deleting the original CRB/SA revokes only that
original authority, subject to API authentication/authorization cache lag. No
measured live cache bound or open-exec/Job termination is claimed.

The requester223503 already matches platform API egress443/6443 per the supplied
preflight; this is metadata evidence, not a socket trial. No Cilium, LAN/world egress,
template, PSA exemption or permanent GPU model is proposed. Standard target PSA and
all other admission still apply. Native admission and RBAC envtests cannot prove
live Kyverno filtering/evaluation, PSA configuration, Cilium reachability, real
hardware utility, actual message provenance, multi-server cache delays or usable
acceptance. Tests make no destructive storage requests.

Source acceptance: targeted/full affected suites, lint/generated/layout, exact-head
CI, self-review plus existing independent native review; draft source publication
only. The final activation packet follows those receipts and identifies any unmet
dependency honestly. Tom's later specific security activation approval is required.

Read-only source cross-check: haynes-ops `2edd85ad1f4aa5013b3a57fee6d23e47e07026cf`
contains the same installed native/broker guard snapshots (SHA256
`e705571cfbb4b5ee787e9d743bf8a1a949a97af36080757cf4fe6b5b9c898adc` and
`1279b4cd286867ce71a1ffa44b9d1e7d9097466f16efac9cf4523290eeba2bfe`).
`kubernetes/main/apps/kyverno/policies/app/dev-env-exec-guard.yaml:54` onward
contains the identical dev-agents caller condition with Fail/Deny. The platform
selector is `.../dev-env-system/network/app/session-platform.yaml:24`, and its
API ports are lines73/75. These are source checks plus the supplied requester
preflight, not fresh live admission or connectivity acceptance.


Client qualification is separate from controller readiness. Both target sessions
report agentd2.45.0 at86cef5bb; its entire protocol/grants.go is byte-identical to
this source. The broker's grant-install/remove argv, stdin token and Pod-UID fence
are unchanged; no issuer namespace enters that wire. The final pinned Linux
agent-run helper is published only as an artifact in the existing shared handoff's
`clients/agent-run-linux-amd64`; `CLIENT-COMPATIBILITY.md` records its exact source,
SHA256, version/help checks and the protocol comparison. It is available to these
two sessions without changing their default client, MCP configuration, HOME,
pairing or Pod. Artifact availability is not proof of live installation or use.
The historical side-by-side2.74 helper predates this new reserved API/CLI path.

For the first separately approved READ-ONLY qualification, recommend this path's
30m default. An exact10m lease loses time before TokenRequest and would be under
the600s floor; neither10m nor15m usability is promised. No expiry is extended.
Read-only Kubernetes metadata/discovery and, within the approved task, exec into
an already-admitted GPU Pod do not require creating a privileged Pod. Creating
one remains subject to standard PSA; no new PSA exemption is proposed. Agent
commands remain chosen within the approved task, without per-command approvals.
Real locally expired-JWT rejection, simulated broker expiry, no-mint unit tests,
and original-SA token invalidation are distinct receipts. Actual live use and
cessation/cleanup require the later authorized test; no30m wall-clock trial has
run here.


Early-review disposition: the calibrated late-refresh defect is fixed and tested
through install/reconcile. Same-Pod valid authority survives minimum refusal;
requester Pod, SA UID, binding-subject and approval mismatches revoke immediately.
The suggestion to admit legacy dev-agents root subjects is not adopted. Read-only
metadata at2026-10-31 22:28Z found zero catalog root CRBs and zero breakglass leases.
Refresh that observation before rollout. Install target namespace protection and
issuer-only root-subject guard while issuance is off, then issuer/RBAC/unbound
role, compatible schema/binary and finally approved configuration. During this
interval legacy dev-agents root binding creation intentionally refuses; ordinary
non-root grants keep their subjects and behavior. A later legacy root lease is a
rollout hold for its authorized cleanup, not a reason to broaden the guard.


Dependency reconciliation: PR800's exact reviewed8260e7b tree is byte-identical to
its actual main merge a290233b3e45b587c151ee0e027d6d3ed529cd86. The native
source review approves8260 conditional on its exact CI; the merged source and
final-green receipts are retained by owner214008 under the existing S7 handoff.
This branch rebases only its own issuer change onto that actual merge. The
provenance-label follow-up remains API/CLI/wire comments/help and documentation:
`internal/apiserver/grant_reserved.go`, `internal/agentrun/{help,grant_reserved,mcp,mcp_reserved}.go`,
`internal/apiserver/apiv1/{grant_reserved,grants}.go`, and `docs/reserved-grants.md`.
It is a wording correction under existing delegated authority, not a fresh-human
or eligibility requirement. Those separate owned files are unchanged here.
