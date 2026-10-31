# DESIGN-043: RD2 minimal admin lease

Status: **PROPOSED, source only, default off, activation held**. 2026-10-31.
Canonical work: OpenProject #233, existing access epic; one Codex writer, exact-head
Claude lead review. No live policies, roles, flags, credentials or sessions change.
This does not supersede an Accepted ADR. DESIGN-042 is the separate proposal in
PR #782; #799/#800 own legacy reach/reserved decisions, not this draft.

## 1. Proposed decision

Recommend **option (c), an explicit static exemption for one exact grant username
AND ServiceAccount UID**, in both the native identity guard and Kyverno exec guard.
Keep their Deny rules for baseline and every other grant identity. Use the existing
break-glass grant, per-grant SA, ClusterRoleBinding, token installation and revoke.
The role includes wildcard resource rules **and** wildcard non-resource URLs.
Grant admission exemption is root-equivalent trust, not a sandbox.

This recommendation deliberately chooses static admission configuration. A native
VAP cannot GET an arbitrary AccessGrant. The exemption itself does **not** prove
current Active state or evaluate a clock. An immutable activation record, fresh
broker checks, short token expiry and deletion of the original SA/binding provide
that lifecycle. Lookup failure during proposal compilation yields no exemption;
there is no request-time lookup in option (c). An already installed exemption is
not removed merely because a future lookup fails. These are material limits.

The source helper here is an **unwired design prototype**, controlled by the
zero-value-false `enableRD2AdminProposal` argument. It reads through a supplied
Reader and returns CEL or literal `false`; it performs no writes. There is no new
runtime CLI flag and no production caller. It does not implement #799/#800, validate
a reserved decision, or publish/update a guard. A later bounded source story must
wire the authorized transaction, binding persistence, token cap and cleanup before
activation. This draft is ready for a design choice, not ready to enable access.

## 2. Exact binding and approval

Persist one immutable record, with all values taken from authoritative live reads
in the authorized decision transaction:

| Field | Required equality |
| --- | --- |
| Grant | `dev-agents/<grant-name>`, exact AccessGrant UID and scope digest |
| Original identity | SA namespace/name and exact SA UID; binding subject names that SA alone |
| Requester | session name and UID, current Running Pod UID; installed token destination is that Pod UID |
| Authority | `status.phase=Active`, no deletion/ended timestamp, `spec.release=false`, `now < status.expiresAt` |
| Time | approval instant, explicit 30-minute TTL and fixed expiry; no silent renewal |
| Decision | authenticated actor SA/session/Pod UIDs, reach mode, message ref, reserved RD2 revision and recorded decision digest |

For an SA token Kubernetes supplies its SA UID in **`request.userInfo.uid`**. Match
both username and this UID. RBAC subjects alone use the SA name and would therefore
also match a recreated SA; the admission exception must not. Do not refresh the
record's SA UID from a same-name replacement. **The SA-name prefix alone does NOT
prove approved current authority.** The prototype checks the first five rows from
read objects; it relies on the caller's approved immutable record and does not
validate the decision row. A nonempty `approvedBy` string is not proof of consent.

The existing grant TokenRequest has no Pod `BoundObjectRef`. Requester session/Pod
binding is broker issuance, installation and recheck state, **not** a cryptographic
restriction on where the grant token is used. A copied original token can be used
from another API-reachable client until it expires or the original SA is revoked.
Admission sees its original SA UID, not the physical client pod's identity.

**A delegate's or actor's assertion is NOT verified human identity.** An
agent-written `tomRef` or `ref`, even syntactically valid, is provenance only.
TokenReview authenticates the delegate; it does not authenticate the alleged human
sender of an upstream chat. Before deciding, the authorized delegate must verify the
supported message channel's actual sender provenance and scope, and bind its stable
reference to grant UID, digest, requester UID/Pod UID, actor UIDs, TTL and target.
A replay, changed scope, replaced UID, stale message or ambiguous sender refuses.
Do not copy a private message body into this public repository.

The intended first case is legacy Claude lead `dev-env-1029-132039` (depth 1,
`full`, shared baseline SA) deciding for top-level GPU session
`haynes-ops-1029-223503` (depth 0, parent `dev-env-system/dev-env-human`). These
metadata-shaped names identify the case, not current UID evidence. Do not guess
session versus Pod UIDs from unlabeled input. No live identity lookup was performed
by this draft author; the final activation packet must fill fresh exact UIDs.

S2 mode (a) is requester attestation under expressly configured trust, including
eligible initial maintenance in S6. A top-level requester/attestation is not by
itself authority to self-approve RD2 or an extension. Mode (b) is a **different**
authenticated allowlisted delegate. S2 on this pinned main refuses reserved
break-glass decisions; actual legacy eligibility and designated policy reach are
separate default-off dependencies in #799, and reserved RD2 decisions in #800.
A tree-only delegate does not reach this unrelated GPU session. The later packet
must match operator/broker `--grant-delegates`, its explicit
`--grant-policy-delegates` subset and the exact legacy SA/session/Pod UID tuple in
`--grant-legacy-delegates`; only reserved class `rd2` may be enabled. Self,
requester-descendant and maintenance-participant refusals remain. No reparenting,
active identity migration, new coordinator session or human-token fallback is
part of this proposal. HTTP 202 records a decision; only reconciled Active state
qualifies the original lease. An authenticated Tom message triggers that bounded
process; it does not turn the delegate into a human principal.

Within mode (b), S2 form 1 is a configured coordinator host with its own configured
SA, Pod name/UID and host annotation; form 2 is an allowlisted S4-admitted named
coordinator session, with its exact live controlling session/Pod and sole holder.
The intended legacy actor is proposed form 3 from #799, using the exact shared-SA
UID plus session/Pod UID tuple, not blanket shared-SA eligibility. Every form still
requires configured authority, live identity, separation and permitted reach;
reserved RD2 needs #800's explicit class decision rather than an ordinary standing
GrantPolicy. Neither an arbitrary peer nor a requester claiming Tom approved can
substitute for that authenticated actor.

## 3. Admission feasibility and failure semantics

All choices must preserve existing validation for nonapproved users. All lookups
that are actually performed must fail closed. No `failurePolicy: Ignore`, missing
param Allow, broad prefix exemption or exception selected by requester labels.

| Choice | Feasibility and error handling | Revoke/cache behavior | Disposition |
| --- | --- | --- | --- |
| (a) VAP `paramKind` / `paramRef` | Broker could write one parameter per Active grant, keyed by exact SA UID, and delete it on revoke; require `parameterNotFoundAction: Deny`, `failurePolicy: Fail`, binding Deny. However `paramRef.name`/selector is static, cannot interpolate request UID or GET the grant. Multiple selected params are ANDed, not an OR allowlist. Naively adding a binding per grant does not implement any-approved-grant semantics. Parameter resolution happens before CEL match conditions: a missing param on this wildcard guard can deny unrelated cluster writes and prevent recovery through normal APIs. A singleton registry with empty state, separate recovery scope and a lifecycle controller is additional design, not a minimal per-grant patch. | Param/grant/SA changes are not atomic. Admission informers can continue to use an old param after deletion; no proven fixed upper bound. Native VAP has no trustworthy wall clock supplied here: storing `expiresAt` alone does not enforce expiry. Broker deletion and token expiry are still necessary. | Do not recommend a naive param implementation. No claim that missing-param tests are performed here. |
| (b) Kyverno context `apiCall` | A ClusterPolicy can GET an AccessGrant by exact name; check grant UID, requester binding, Active/unreleased state, exact SA UID from a broker-owned record and current expiry. Current exec guard is a CEL ValidatingPolicy, not a ClusterPolicy; a context `apiCall` is not a drop-in field. Use Enforce/Deny and Fail at both policy and generated webhook, propagate missing/read/substitution errors as refusal, never a default-success precondition. Existing identity VAP cannot share this lookup result, so it still needs a reviewed static exemption plus the Kyverno gate. | Lookup can race a revoke after the read. Resource/global-context caches and webhook configuration propagation must be measured; no immediate consistency guarantee. A webhook excluded by namespace/resource filters never sees the request, regardless of Fail. Kyverno currently skips some controller namespaces; assuming coverage would be unsafe. | Feasible only with broader engine/coverage qualification, not selected for this minimal draft. |
| (c) Static exact username + UID | Both guards retain Fail/Deny and exclude one literal authenticated identity. The compiler must read/validate the exact Active binding before rendering; any missing/error/mismatch returns `false` and invalidates earlier proposed output. No admission lookup exists, so a request-time lookup outage cannot be described as being denied by this option. | Broker deletes original binding then SA on release/expiry/recheck failure; tokens of that SA lose validity. Static exemption removal is separate cleanup and may lag. Stale exemption cannot bless a recreated SA UID. SA authentication, RBAC and admission/webhook caches have propagation windows with no verified bound in this draft. Token expiry is a separate deadline. | **Recommend**, with these explicit limits and the required lifecycle wiring still held. |

These semantics follow the [native VAP reference](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/)
and [Kubernetes dispatcher](https://github.com/kubernetes/apiserver/blob/master/pkg/admission/plugin/policy/validating/dispatcher.go).
Kyverno's [external-data](https://kyverno.io/docs/policy-types/cluster-policy/external-data-sources/)
and [ValidatingPolicy](https://kyverno.io/docs/policy-types/validating-policy/)
references describe the distinct engines; neither is an installed-cluster proof.

### Exact guard treatment

The supplied 21:00 preflight reports live bindings Deny. The verbatim identity
copy remains untouched. The proposed copy changes **only** the identity match
condition, preserving all existing validations byte for byte. For the approved
exact tuple it exempts identity rules 1 (dev-env namespaces), 1b (controller exec),
2 (grant control namespaces), 3 (SA choice and elevated workloads), 4 (added Secret
references), 5 (host/local PV) and 6 plus following Flux/ESO restrictions. This is
an honest exemption from those controls, not a narrowly capable admin role.
For unapproved grants and `dev-agents/dev-env-agent`, those rules still Deny.
The agent guard matches only the baseline username and needs no change; the
approved grant username does not match it. Broker guard applies to the broker,
not the leased actor, and already catalogs `dev-env-grant-breakglass`; preserve
its own-name SA/CRB ownership conditions and check live bind RBAC before activation.

Kyverno's proposed `dev-env-exec-guard` uses the **same exact username AND UID**
exemption, with Fail/Deny unchanged. This admits controller/privileged-pod exec
where that webhook is reached; the native guard exemption handles controller
namespaces Kyverno does not see. No exemption applies to a different UID or to
other grant identities. Other admission policies still apply, including image and
Pod Security checks. Native PSA exemptions are distinct configuration and do not
disappear with a ClusterRoleBinding. The supplied history reports only
`kube-system` and the broker **actor username** exempt from PSA; a grant SA is not
the broker. A privileged Job accepted as a Job can still have its child Pod refused
by PSA. Initially use the existing PSA-exempt namespace when such a task is approved;
privileged Pods in other enforced namespaces require an explicit reviewed PSA or
namespace-policy decision. Full RBAC does permit changing namespace labels/policies;
such a change is persistent root activity, not automatic lease cleanup. This draft
does not add any PSA exemption or promise Mac `system:masters` admission parity.

## 4. Exact role and network

Propose `dev-env-grant-breakglass`, bound cluster-wide to exactly
`ServiceAccount dev-agents/<grant-name>` in ClusterRoleBinding `<grant-name>`:

```yaml
rules:
  - apiGroups: ['*']
    resources: ['*']
    verbs: ['*']
  - nonResourceURLs: ['*']
    verbs: ['*']
```

The second rule deliberately covers **all** non-resource endpoints and methods,
including `/api`, `/api/*`, `/apis`, `/apis/*`, `/version`, `/openapi/*`, `/healthz`,
`/livez`, `/readyz`, their subpaths, `/metrics`, `/logs` and `/logs/*`. This provides
the full admin non-resource surface instead of depending on ambient authenticated
discovery. It is broader than needed for ordinary discovery; that breadth is
intentional in an effective-root lease. Wildcards include future resources and
endpoints. It is not membership in `system:masters`, nor the built-in role object.
Inventory every existing binding before creating/changing this shared logical role;
unknown older break-glass leases must not gain new authority implicitly.

| Client | API endpoint, CA, port and policy |
| --- | --- |
| Session pod | Actual agentd kubeconfig server is `https://<KUBERNETES_SERVICE_HOST>:<KUBERNETES_SERVICE_PORT>` from `GrantsFromEnv`, using the Service address, not search-expanded short DNS. Expected Service port is TCP 443, with API backend TCP 6443; exact numeric Service host/port values were not read and must be confirmed as nonsecret metadata later. The equivalent fully qualified Service DNS is `kubernetes.default.svc.cluster.local`. Use projected cluster CA `/var/run/secrets/kubernetes.io/serviceaccount/ca.crt`, without reading it here or disabling TLS verification. Broker TokenRequest uses the Kubernetes API audience, not the separate operator audience; verify actual configured audience from nonsecret metadata and the returned expiry during later qualification. `dev-env-session-platform` permits `toEntities: kube-apiserver` TCP 443 and 6443. Supplied 21:07 metadata preflight says actual GPU session `app.kubernetes.io/name=dev-env-session` matches the selector; this is policy proof, not a tested socket or token. Keep existing kubeconfig `/run/dev-env/grants/kubeconfig` and token file in grants tmpfs; credential values remain unread. |
| Mac | A different authenticated principal (`haynes-m5max-local-agent`, supplied preflight says `system:masters`) uses its existing external API endpoint and trusted cluster CA. **Exact Mac server hostname and port are not supplied/verified by this draft**; no credential file is opened to guess them. It cannot use cluster DNS as a general substitute. Mac traffic is not session-pod Cilium egress. Its current successful access does not prove pod reachability or make a lease token its identity. No Mac credential copy or CLI action is required. |

No Cilium delta is proposed for the actual GPU requester: existing API egress
already applies. Other selector/profile shapes need their own metadata check. No
LAN/world broadening, SSH, host-key, storage, API proxy or network grant is included.

## 5. Activation transaction, revoke and residual authority

A later reviewed implementation must serialize one lease's transaction:

1. Validate authenticated message/delegate decision and exact UID/digest/TTL
   envelope through #799/#800; create the broker-owned SA with automount off and
   bind only the reviewed admin role. Freeze its original UID in the activation
   record. Active approval status may exist before installation; current broker
   ordering already permits that. A future RD2 installation gate must withhold
   the token while the reviewed guards are pending. Partial failure means no
   usable installed admin token.
2. Validate current requester and grant state, render/review/install both exact
   admission exemptions through the existing GitOps delivery owner and reviewed
   PR/Flux route. Do not give the broker admission-policy mutation rights. Each
   new original SA UID requires a new rendered guard diff; the existing static
   exception cannot be inherited. This adds GitOps propagation latency to issuing
   a lease, consumes part of its fixed deadline, and does not provide instant
   message-to-token delivery. The exact delivery owner's approved in-scope action
   belongs in the later activation record, not a new broad broker entitlement.
3. Mint an API-audience token for at most the remaining fixed 30-minute deadline;
   check the actual TokenRequest expiration returned by the server. Never rely
   on a requested lifetime alone or mint a longer renewal. Current
   `tokenSeconds` rounds up and clamps to at least 600 seconds: for example a
   five-minute remainder requests ten minutes. That existing path is **not** a
   hard RD2 deadline. The future RD2 path must round down, refuse issuing when
   less than the API minimum remains, and refuse installation if the returned
   expiry exceeds the fixed deadline. A token already minted too long requires
   original-SA revocation; withholding only its file is insufficient. Install over existing
   stdin-only, Pod-UID-bound delivery. An extension requires a new explicit
   authenticated decision; it is not a token refresh that resets the lease clock.
4. On release, expiry, requester/actor replacement, lost actor eligibility or
   failed authority recheck, delete the original owned ClusterRoleBinding and SA
   with UID preconditions; remove the installed file. Remove both static
   exemptions separately and record cleanup evidence. API failure is unfinished
   cleanup, never successful revocation. A stale exemption is not authority
   without the original live SA and RBAC, but report it truthfully.

Current broker Active delegated rechecks are ordinarily at one-minute intervals;
cleanup adds API/authentication/RBAC/webhook cache latency. No finite early-revoke
bound has been measured here. A lookup failure at activation refuses issuance;
an outage after issuance is not magically an immediate Deny of an existing static
exemption. The 30-minute expiry must be enforced on the original token; a stopped
broker must not extend it. [Kubernetes SA token validation](https://kubernetes.io/docs/reference/access-authn-authz/service-accounts-admin/)
checks original identity and token lifetime, with cache/propagation caveats.

**Residual risks:** deleting the original SA/binding does not close an already
established exec stream, stop its remote process, undo in-flight requests, stop
running Jobs, or undo written objects. Root can mint tokens for **other** SAs,
create RBAC/identities, impersonate principals, read/copy Secrets, change admission
or network controls, or create privileged persistent workloads. Those derived
credentials, bindings, copied data and effects can outlive the lease indefinitely.
No automatic cleanup of arbitrary admin-created resources is promised. Root can
also tamper with the proposed guard/exemption itself. Trust and audit are the
boundary; TTL does not make unrestricted root perfectly revocable. The grant is
cluster-wide, including the dev-env namespaces; the RD3 thirteen-name proposal is
not an RD2 containment guarantee. RD3 and storage engineering are out of scope.

Rollback: stop new issuance (leave all RD2 knobs off), revoke the exact original
SA/binding/token file, restore the pinned original identity and exec guards, then
remove the role only after confirming no other bindings depend on it. Preserve
AccessGrant audit/status and cleanup failure evidence. Do not roll back #799/#800,
restart sessions or claim rollback removes derived authority. No rollback is run
by this draft.

## 6. Evidence and remaining engineering

Fixtures are under `internal/broker/testdata/proposed-haynes-ops/rd2/`, explicitly
non-authoritative and not recursively loaded by older proposal tests. The static
literals are synthetic. The existing verbatim live copy stays unchanged.

Envtest runs a real kube-apiserver/etcd, loads the proposed native guard and admin
role, and authenticates the positive case using a synthetic real TokenRequest
whose SA UID matches the compiled snapshot. That positive snapshot is read from
real envtest AccessGrant/session/Pod/SA objects with exact generated UIDs; Active
status is set by the test as a broker stand-in, not via a proven approval route. It checks admission of a privileged
Job, Secret-referencing Pod, target-namespace RBAC create and dev-env namespace
write; controller exec must reach the downstream upgrade handler. Negative users
receive the same broad test RBAC, so their expected 403 must identify this guard.
It checks unapproved grant, baseline SA, same username/different UID and another
existing grant identity. It also deletes the real envtest SA, observes the old
token become Unauthorized, recreates that SA name and mints a new real token;
with the old static exemption and name-based RBAC still present, the new UID
remains denied. Revoked, expired, absent-grant and failed-read snapshots
render `false`; after that rendered guard converges, all those operations Deny.
These are **static-render transitions**, not missing-param or request-time lookup
tests. Unit tests also cover default off and grant/requester/Pod UID replacement.

Envtest cannot prove Kyverno evaluation/webhook filtering, PSA configuration on
real control planes, Cilium routing, real node privilege, running Job behavior,
a successful exec stream, real message provenance, delegate eligibility from
#799/#800, installed source/image freshness, a 30-minute wall-clock token expiry,
or bounded multi-API-server revocation/cache timing. Existing negative cases for
other grant identities remain mandatory. This proposal claims source/test evidence
only: no installed, deployed, usable or accepted state.

Before any later single activation decision, the accountable owner must supply:
exact fresh UIDs and message binding; qualified #799/#800 source/images; the
immutable activation-record implementation and default-off production gate; a
reviewed per-grant GitOps edits for both static guards; the installation gate,
capped-token and UID-owned revoke
wiring/tests; exact rendered haynes-ops diffs including PSA implications; broker
catalog/bind and no-unknown-role-binding evidence; network/CA/audience metadata;
and approved installed positive/denial/expiry/cleanup evidence. These are explicit
engineering/qualification gaps, not an activation request from this draft.
