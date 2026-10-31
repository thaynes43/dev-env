# DESIGN-042: Homelab operator access activation bundle

- **Status:** Proposed, 2026-10-31. Source preparation only. No activation approval,
  policy, flag, template, grant, credential provisioning or security/network change.
- **Canonical work:** OpenProject [OP233](https://work.haynesnetwork.com/work_packages/233),
  Epic [112](https://work.haynesnetwork.com/work_packages/112), Saga 85.
  Integration story [333](https://work.haynesnetwork.com/work_packages/333) owns this
  correction; it creates no competing queue. OP304 auto-review is deferred and is
  not a prerequisite.
- **Custody:** Codex `dev-env-1031-171057` integrates the released design from
  [PR782](https://github.com/thaynes43/dev-env/pull/782) at
  `e11aaaa48188902116f2f3742f1bb0bb6d4899ee`. Claude `dev-env-1029-132039` is
  review-only. S2/S6 belong to `dev-env-1031-163602`; S3 to Grok with Codex
  `dev-env-1031-165730`; S5 to its separate Grok writer. No source-branch takeover.
- **Evidence:** section 2 pins the actual source heads and their limits. Earlier
  live observations in PR782 were made at 13:50–14:23Z on October 31; they are
  dated observations, not a current runtime acceptance claim.
- **ADR bookkeeping:** [ADR-013](https://github.com/thaynes43/dev-env/blob/2984528d78e54f3dfd77cf12755f2f389a075dff/.agents/sagas/distributed-dev-env/adrs/013-attested-maintenance-approval.md) is Accepted and unchanged. S6 adds ADR-014; S2 adds
  ADR-015. A later accepted decision records the reach/reserved-class changes
  proposed here; do not reuse those numbers or amend an Accepted ADR.

`DES/` means haynes-ops `kubernetes/main/apps/dev-env-system/`. D001 means
[DESIGN-001](001-dev-env-v2.md). An **authenticated actor** records its own
TokenReview-verified decision. An **agent assertion** reports someone else's
consent; a message reference does not authenticate that person.

## 0. One recommended proposal for Tom's exact review

Approve one concrete activation packet after its missing source and rendered
configuration have been independently reviewed. Q-042-01 and Q-042-02 are one
consolidated decision, not separate partial approvals. This document does not
submit or consume that decision. New security/access scope remains held for Tom.
Tracks below are delivery dependencies inside that packet, not permission to
activate a convenient subset before the complete proposal is reviewed.

The recommendation retains the existing grant framework, agent-chosen commands,
message approvals, per-grant identities, TTL, release and audit. It adds no service,
per-command gate or OP304 dependency. The complete deliverable must serve existing
sessions, including top-level ones; current code restrictions are not product intent.

1. **Eligibility:** `full`, `workbench` and future `coordinator` sessions in any repo
   may request the classes in section 5, within the approved policy scope.
2. **Mode (a):** a requester records its own attestation under a policy Tom approved.
   This is an agent assertion, with no independent approver authentication.
3. **Mode (b):** a separate allowlisted, live pod-bound delegate records its own
   decision, labelled `delegate/<namespace>/<serviceaccount>`, never `human/`.
   Tree reach stays the default. The proposed explicit policy-reach subset in 6.6
   lets a designated delegate serve existing top-level sessions within the same
   policies or enabled reserved cases. **That subset is missing source.** It is a
   necessary prerequisite, not an accepted limitation or a PR791 feature.
4. **C2 namespace operator:** permit exec, restart and Jobs in the named app
   namespaces plus `network`, with a per-grant list of named Secrets for new mounts.
   **Proposed retained exposure, requiring Tom's review:** exec can read existing
   workload Secrets/tokens; a created Job can select **any ServiceAccount in that
   namespace**, including one with no running pod. Its authority may extend outside
   the namespace. The current source does not enforce the example's `default` SA.
   Keeping that behavior is proposed, not accepted. Restricting it would require an
   explicit revised proposal and source, not a silent permission reduction.
5. **Hardware:** C4 leases the existing hw-ssh key and Proxmox operator credential
   for at most 1h. Their effective powers include root/root-equivalent operations.
   File removal cannot recall a copy, stop a process or undo an external operation.
6. **GPU:** S6 restricts mode (a) maintenance to an explicit depth-0 session whose
   immediate creator SA is listed. Mode (b), including extensions, needs a separate
   delegate and the exact maintenance envelope. No fixed command list is introduced.
7. **Reserved cases:** after Tom's fresh message, S7 lets an agent execute RD2–RD4.
   **P7 as proposed is root-equivalent**, not a confined namespace grant. Every verb
   on every API resource allows impersonation, privileged-SA TokenRequests and
   Secret get/list in Never namespaces. Admission does not intercept reads, and
   impersonated identities bypass a guard keyed to `grant-*`. This is a proposed
   exceptional scope for Tom's explicit decision, **not accepted risk**. Its original
   identity's TTL does not revoke credentials or identities obtained through it.
   Sections 7.1 and A3 state the resulting limits rather than promise containment.

### Trust roots and the actual lead

The trust boundary follows identities, not session depth: **every pod using the
workbench SA** has its bound exec rights, including workbench children. The human
SA and configured client identities (including legacy `dev/dev-env`) also have
bootstrap authority. A depth-0 workbench session is a supported S4 launch route;
it is not the only workbench pod able to use SA-level exec rights. Such roots can
exec into a delegate pod and act with its token. Separation checks therefore
constrain ordinary callers, not a compromised root. Bare-pod/TokenRequest authority
must be inventoried too; counting AgentSessions is not a bound on all token holders.

This is disclosed residual scope pending Tom's review. Narrowing workbench exec
only into coordinator pods is technically compatible with general app exec. It is
not ruled out by Tom's requirements. The proposal defers it to preserve the existing
workbench management path and keep this source increment bounded; that is an
operational/scope recommendation, not a manufactured user ruling.

Actual Claude `132039` remains a full legacy session, not a provisioned named
coordinator. Actual GPU `haynes-ops-1029-223503` has parent
`dev-env-system/dev-env-human`, depth **0**. Neither is reparented or restarted.
P6 remains held. A new named approval session after an approved P6 is a proposed
identity/template route, not a description of the running Claude session.

### Delivery dependencies inside the packet

| Track | Concrete change | Required source and configuration before activation |
|---|---|---|
| T0 | C1 app, C3, C4 and widened node policies under mode (a) | Attested-only P1 shapes, using already installed D-88 machinery; no `delegated` fields. No S2 CRD assumption and no automatic activation from this document. |
| T1 | A new named approval session, launched by the existing workbench route | Merged S4 plus reviewed P6, template-hold exception and proof no active session restarts. Existing `132039` keeps its current identity. |
| T2 | Delegated kube/egress/maintenance and infra C1, including existing top-level callers | Corrected S2, S3, missing 6.6 source, a separately provisioned delegate, matching operator/broker lists, P3, operator policy-read RBAC. Add P1 `delegated` blocks **only after** S2's CRD copy is installed. |
| T3 | C2 exec, Jobs and named Secrets | Integrated S5 after S2; T2; P4/P5; explicit review of any-SA exposure; `kube-namespace-operator` delegated-only. |
| T4 | Exact-envelope GPU mode (a) | S1 plus S6 released/pinned, reviewed creator list, `maintenance-card-c`, attested-maintenance flag. Mode (b)/extensions require T2. |
| T5 | RD2–RD4 message execution | Missing S7, T2 with 6.6, reserved bounds and flags, P7 root exception, RD3 expansion and RD4 UID guard. |

T0 has no new R1/R2 dependency if it uses only installed attested fields. All other
tracks copy the CRD and pin the binaries that implement their fields before applying
policies that use them. No older CRD is expected to accept `delegated`. A rollback
removes dependent policy fields and disables issuance before downgrading the CRD or
binary. Do not roll back past S6 with attested maintenance enabled.

### Rollback by persistent delta

Disable new issuance first, release affected grants and prove cleanup/executor stop
before removing their enforcement or downgrading source. Never restart active
agent sessions as a rollback. Preserve audit objects and exact before/after pins.

| Delta | Concrete rollback and limit |
|---|---|
| S1 | After maintenance executors stop, set `--enable-attested-maintenance=false`; restore the prior reviewed binary pin only after the remaining dependencies permit it. |
| S2 | Empty `--grant-delegates` on operator/broker after affected leases end; remove delegated policy blocks before schema/binary downgrade. |
| S2 policy reach | Empty the proposed `--grant-policy-delegates` subset on both services; verify Active grants that lose reach terminate. Tree-only behavior remains; revoke/clean leases before source rollback. |
| S3 | Restore the previous CLI/MCP pin; this does not cancel recorded decisions or leases. |
| S4 | Stop new named-coordinator admission through the held profile/list configuration. Wait for affected sessions to end safely before a classifier/template downgrade; preserve their heartbeat/rescue support and restart none. |
| S5 | Remove the C2 issuing policy, release leases and remove owned Secret-use Roles/bindings; restore previous source/schema only after all Secret-field dependents are removed. Copies and workload tokens persist as disclosed. |
| S6 | Disable attested maintenance and prove executor stop before resetting creators or downgrading past S6. Never restore broader creator eligibility while the flag remains enabled. |
| S7 | Empty reserved-case flags, release original grants and verify owned cleanup before removing fields/source. P7-derived authority and hardware effects are not reversed. |
| R1 CRDs | Remove policies/requests relying on new fields and disable their issuers before restoring the previous CRD; do not silently prune Active grant evidence. |
| R2 pins | Restore recorded prior operator/broker/client image digests only after the dependent issuers and leases are stopped; never roll an active session pod. |
| P1 policies | Delete each new policy or restore its exact previous shape; verify delegated lease recheck/cleanup. Widened node/credential effects already performed remain. |
| P2 flags/lists | Restore exact prior flags and delegate/creator lists on API/broker after issuance is disabled; verify policy/list loss revocation and maintenance stop. |
| P3 decision annotation guard | Disable delegated writes and finish pending/Active decisions before restoring the maintenance-only annotation guard; never open an unguarded write interval. |
| P4 role/catalog/broker rights | Remove C2 issuance and leases, then remove owned use Roles/bindings and the namespace role/catalog entry; restore broker RBAC/shape guard together. |
| P5 admission guards | After C2/storage leases end, restore the previous **fail-closed** identity/delete guard: new Secret references and storage deletes refuse again. Do not remove a guard while its grant role is still usable. |
| P6 identity/template | Stop new admission; wait for the new approval session's safe end and remove only its proposed profile/SA/bindings/guard match. Restore exact previous template revision without draining/restarting existing sessions. |
| P7 root catalog | Disable RD2 issuance, remove original grant bindings/SA, then remove catalog/broker bind rights and role. Copied Secrets, minted credentials, alternate identities and persistent effects require separately authorized containment; deleting P7 does not revoke them. |

## 1. Outcome and controlling requirements

Tom's October 31 corrections require agent-executable internal capabilities, broad
homelab utility, approval by messages to the requester, and normal Prowlarr work.
His October 26 OP214 requirement is a streamlined route for agents to ask a
coordinator or user for cluster/online access, with mobile user approval.

Acceptance is the required workflow, not today's implementation restriction:

- No internal class is human-only or requires Tom's Mac/token after valid message
  approval. Agents choose commands within the approved capability envelope.
- Ordinary delegated authority can be approved by an appropriately scoped agent.
  Reserved cases still cite Tom's exact message.
- Reuse standing/attested policies where they cover the work; do not add a gate to
  existing routine access merely because a new source path exists.
- Preserve scope, identity, expiry, release and refusal proof. Keep provider refresh
  ownership with the keeper and keep credentials out of transcripts and artifacts.
- Preserve active GPU/Haynesnetwork sessions. Source, tested, installed and usable
  acceptance are distinct. The successful emergency MAM GitOps repair is not
  namespace-grant parity.

[Plan 07](../backlog/07-access-broker.md), Q-16/D-70 and
[ADR-003](../adrs/003-session-coordination-private-repositories.md) remain the
requirements context. This proposal does not replace them or create another queue.

## 2. Exact source and evidence inventory

Snapshot: 2026-10-31; source-head refresh at 17:22Z. Reverify moving heads before delivery;
never carry an approval from one SHA to another. The private handoff retains the
latest read-back. A passed local test is not hosted integration or deployment.

| Component and owner | Exact source/dependency | Evidence and remaining gate |
|---|---|---|
| S1 attested maintenance | PR781 merged `2984528d78e54f3dfd77cf12755f2f389a075dff` | Default-off source; no A1 utility acceptance. |
| S4 named coordinator | PR787 reviewed `a62bf62e32fe466453de6c41a5ca23ec3970cdab`, merged `b82e3b2954d08a22abc6ed9d61039a40f3cbd1e2` | Independent review approved; required CI green. P6 still held; merge provisions no identity. |
| S2 / owner 163602 | Draft PR791 current `78e6b3feccd95515a0405b7415d19b3c56743b8f` (includes S6 merge `1195633`); independently reviewed earlier `83103c69f41a389d26d2d2d8df1abecd7f1b08c6`, branch `agent/dev-env-1031-163602`; replaces released draft #786 `0678aee` | Full local API PASS 68.267s; real TokenRequest/TokenReview envtest PASS 7.419s, with synthetic Pod materialization. Broker 25.860s, operator 0.097s, schema 8.157s passed. Independent Claude review of `83103c6` **REQUEST CHANGES** for decision-slot lockout; current `78e6b3f` still documents it rather than correcting it. Combined local API 68.239s / broker 26.280s / operator 0.099s passed per owner receipt. Exact-head final CI/review and source correction required. Tree-only source. |
| S6 / owner 163602 | PR790 reviewed `aa55b7a63843d17fb9eb50dacfb20a01d9b84881`, merged `119563303ea42b33963b19953c5b264a6b92aac9` at 17:14:39Z; branch `agent/dev-env-1031-163602-s6`; replaces draft789 `40ae62e0ef2d8826e7da536fb853ccdc76ad84a4` | Independent Claude review APPROVE at this head; broker/operator and creator-removal mutation passed. Hosted required CI including Test passed; merged source only. No activation. |
| S3 / Grok primary, Codex 165730 support | Draft PR792 `3e1f1d39de6442460e20a1b38f0387449d437079`; supplied base `68c398b9c12cc9b93df658d72b68aedf1f067180`; Grok source `49dc659` | Local package/vet/static CLI build and six meaningful mutants PASS. Hosted merge-result Build/Lint fail because moving S2 lacks S5 `CreateGrantRequest.Secrets`/`Grant.Secrets`. Keep draft; integrate after S2/S5, rerun hosted CI and exact-head review. |
| S5 / separate Grok writer | PR785 `fe3b62fcbcf5d8f7ae8f288d9e3fe52f09a166cd`; local-only correction ACP `ef4f0d17-c002-42e5-9d12-7dc52e6b9e31`, workbench workspace | Existing source green, but not integrated with S2. Local patch corrects same-object digest nonmutation test and discloses any-SA Job exposure. Patch SHA256 `992378f1cfa009828cacb785e96a545ad2bae0880f3bef31179d49cd2fa372d1`; no integrated/tested/pushed-head claim. |
| S2 reach follow-up (6.6) | **Absent** from PR791 and merged S4; request to existing S2 owner after disjoint follow-up custody | Source design only. No default/flag/schema exists for policy reach today. |
| S7 reserved classes | **Absent** | No reserved flag, `tomRef`, `targets` or `allNamespaces` server implementation is claimed. S3 must extend its contract after S7; PR792 is not S7 proof. |

The current S2 head's author/native source review reports no blockers, but the
independent Claude slot-lockout finding remains unresolved by that documentation
change. Do not treat either review as permission to bypass the acceptance defect.

Source links: [S2](https://github.com/thaynes43/dev-env/pull/791),
[S6](https://github.com/thaynes43/dev-env/pull/790),
[S3](https://github.com/thaynes43/dev-env/pull/792),
[S5](https://github.com/thaynes43/dev-env/pull/785).
The S2 owner's local combined S2/S6 work is not substituted for a remote head.

## 3. Existing machinery and workflow limits

- D-88 attestation binds grant UID, requesting session UID, Pod UID, scope digest
  and TTL. The broker checks live identity and records `policy/<name>`; it is an
  agent assertion. A policy-less request waits for timeout, not automatic approval.
- S2 adds the authenticated decision route and broker checks. HTTP **202** records a
  Pending decision; broker reconciliation later produces **Active**. Neither a 202
  nor a TokenReview test proves an installed executor or useful app operation.
- S4's exported `NamedCoordinatorLaunchAllowed` / `NamedCoordinatorSessionEligible`
  admits a human/configured-client depth-0 launch, or a depth-0 workbench session
  launching a named coordinator at depth 1. Ordinary callers cannot launch it.
  Holder checks include reserved/live sessions and resume safety; they are broader
  than the old phrase “one Running session.” Cross-replica launch races still need
  their recorded acceptance evidence; a process-local mutex alone proves nothing.
- The current maintenance human route remains a fallback, not the selected required
  route. S1/S6 cover only creator-eligible initial leases. They do not cover every
  top-level session, C2, infra C1, extensions or reserved cases.
- Current tree-only S2 serves descendants, not unrelated existing top-level sessions.
  Launching a new child under a delegate is useful for new bounded work, but does
  not grant the original GPU session an extension or migrate an active session's
  identity. It is not the complete answer to the reach gap.

## 4. Boundaries retained and corrected

D-19 runtime guards and GitOps remain. RD1 uses existing bot PR, review, delivery
receipt, squash merge and Flux. An agent-written message ref is not machine-verified
Tom consent. DESIGN-039's pending stronger consent mechanism and OP304 auto-review
are not prerequisites or substitutes for Tom's exact scope review here.

Never-list exclusions apply to ordinary namespaced grants and RD3. They do **not**
confine P7's proposed effective root authority, existing workbench exec, compromised
workload SAs, node operations, or GitOps changes. A NoExecute taint can reach storage
workloads and bypass PDBs. Do not label storage isolation “enforced” on that basis.

## 5. Capability classes

| Class | Mechanism and requested scope | Maximum TTL | Approval |
|---|---|---|---|
| C1 app | kube `dev-env-grant-workloads`; explicit app namespaces | 3h | (a) or (b) |
| C1 infrastructure | same role; explicit infrastructure namespaces | 8h | (b) only |
| C2 namespace operator | kube `dev-env-grant-namespace-operator`; namespaces and 1–16 named Secrets | 1h | (b) initially |
| C3 cluster API | egress endpoints/labels and explicit ports | 3h | (a) or (b) |
| C4 LAN/host | egress destinations/ports, plus hw-ssh or Proxmox credential | egress 3h; SSH and credentials 1h | egress (a)/(b); credentials (a) |
| C5 maintenance | node UID, GPU identity, image digest, runtime, capabilities, resources and executor identities | 1h | initial (a) with S6, or (b); extensions (b) |

### 5.1 C2: named Secret use and effective powers

S5's role has pods get/list/watch/delete, exec create, logs get; deployments and
StatefulSets get/list/watch/patch; Jobs get/list/watch/create/delete; configmaps and
Events get/list/watch. It has no direct Secret get/list rule.

Each grant's `secrets` list is in ScopeDigest. Policies enumerate namespaces and
an optional deny list, not every allowed Secret name. The broker uses `kubeInside`
for delegated matching too. S5 must restore its server request/view fields, extend
the standing-policy validation to `has(self.delegated)`, regenerate CRDs, and test
against corrected S2 before S3 is rebased.

The broker creates a per-grant `<grant>-secret-use` Role with `use` on exactly the
named Secrets and binds it only to that grant SA. Kubernetes does not check Secret
use during Pod creation. Admission must therefore run the per-name authorizer check
for each reference using the **requesting grant identity**, fail closed if no own
Role exists, and stay within CEL cost limits for all 16 names. Missing grant,
ended grant, another grant's names, an unlisted name and a denied name must refuse.
A param-only VAP that matches no own object cannot provide that guarantee.

Jobs/Pods created by grant identities carry `activeDeadlineSeconds` at most 3600;
Job cleanup uses `ttlSecondsAfterFinished` at most 3600. The current fixed cap also
binds C1 Jobs. Per-grant expiry removes bindings, SA and Secret-use Roles; the
workload deadline bounds continuing workloads. An open exec stream, a running
command's effects, printed/copied values and borrowed workload tokens are not
recalled by deleting a RoleBinding.

**Residual authority:** named Secret use bounds new direct references, not every
Secret an agent can obtain. Exec sees existing env/mounts and SA tokens. A created
Job can choose any SA in the granted namespace, including an unused privileged SA;
that SA's effective cluster scope is part of Tom's C2 review. The A2 example uses
`default` voluntarily. No namespace-only containment or default-SA enforcement is
claimed. Do not log values while proving access.

## 6. Approval and identity contract

| Property | Mode (a) | Mode (b) |
|---|---|---|
| Recorder | requester via `grant attest` | different allowlisted actor via `grant decide` |
| Provenance | agent assertion | authenticated delegate; relayed Tom consent remains an agent assertion |
| Binding | grant UID, session UID, Pod UID, ScopeDigest, TTL | grant UID/scope/TTL, recorded delegate Pod/session UIDs, required message ref; requester live UID checked; proposed policy binding in 6.6 |
| Record | `policy/<name>` | `delegate/<ns>/<sa>`; never human provenance |
| Eligible types | kube, egress, credential; creator-eligible initial maintenance | kube, egress, maintenance/extensions; S7 separately adds reserved cases |

### 6.1 Request and decision flow

1. Request with `--no-wait`, then read grant name, UID, digest and TTL.
2. Send those exact values, purpose and risk through supported coordinator/user
   messaging. Verify sender provenance and scope. Arbitrary peer/repository/tool
   claims and silence are not approval.
3. On an actual in-scope answer, attest under an opted-in policy or have the separate
   delegate run `grant decide <name> --grant-uid <uid> --digest <digest> --approve
   --ref <message-id>`. `--approve` is explicit. S7's proposed reserved contract also
   requires a Tom message ref; that option is not claimed in PR792.
4. Expect 202/Pending, then independently observe Active or a refusal. A new TTL or
   digest needs a new decision. A renewal is a new grant, not a reused statement.

### 6.2 S6 creator eligibility

The immediate `spec.parent` must be a listed SA **and** the `dev-env.haynesops.com/depth` label must be exactly
**`0`**. Unset `--attested-maintenance-creators` follows the configured human SA;
explicitly empty means none, with at most 16 valid unique `namespace/name` entries.
A child, absent/malformed parent, omitted depth, unlisted creator or replaced
requester fails closed. Approval and the Active lease/executor path recheck the
creator. Removing it starts executor termination; cleanup waits for stop proof.
The exact `223503` shape is the positive fixture. Root-ancestor human provenance is
not enough. Mode (b) stays separate.

### 6.3 S2 current tree route and required slot correction

Refer to section 0 for the single trust-root statement. Both operator and broker
must have the same `--grant-delegates` list. A coordinator host must retain its exact
configured host/pod/SA binding. A session delegate must retain S4 launch eligibility,
controller/session UID, admission label, live Running Pod UID and sole holder.

Tree reach walks the requester's recorded session ancestry; a host reaches only
its direct task children. Self, ancestor, sibling and unrelated grants refuse in
this default mode. SA-ref parents do not establish a delegate ancestor merely
because two top-level sessions have the same creator.

**Blocking source acceptance:** before writing the set-once annotation, S2 must
live-read policies in the configured policy namespace and require coverage for
**approvals and denials**. An uncovered or otherwise unauthorized decision must
return 403 with no annotation/status mutation, leaving a subsequent authorized
human or delegate decision usable. PR791 at `83103c6` does not do this: the broker
refuses later while the annotation locks the decision slot until timeout. Merely
documenting that behavior is not completion. The real-token envtest must create
its policy **before** the decision and test this refusal/recovery path.

The operator needs policy-namespace configuration and get/list GrantPolicy RBAC in
haynes-ops, held until the approved packet. The broker still independently rechecks
coverage and live identities before apply, on each retry and while Active. Policy
removal/narrowing, allowlist removal, actor replacement and loss of reach must end
a lease. Current S2 normally rechecks Active delegated leases every minute;
cleanup/stop has its own latency, and API failure is not an immediate-revocation
guarantee. Transient read failures must not be reported as successful checks. The
independent review also requests assessment of Forbidden reads during Active and
coordinator-host grant read scope. No fix is claimed until a tested reviewed SHA.

### 6.4 P6: proposed new delegate identity, no active migration

Recommend a new `dev-agents/dev-env-coordinator-access` session identity with the
existing `coordinator` profile shape (`full` plus named SA), existing agent bindings
and guard coverage. The name **replaces**, rather than supplements, the earlier proposed
`dev-env-coordinator-lead`: it describes an approval session instead of implying
that actual `132039` changes identity. Only the access identity is proposed for the
initial P6 configuration. It is separate from actual `132039` and `223503`. A depth-0 workbench session can launch it through S4's real handler;
TokenReview then classifies its own projected pod-bound token. With the **missing 6.6 policy-reach subset implemented and explicitly enabled**,
this serves the unrelated existing GPU and legacy Claude sessions. It cannot approve the launching
workbench session's grant, because it is that requester's descendant. For that
factory's requests, select a separate non-descendant delegate launched through an
**existing configured nonhuman client** (S4's depth-0 client shape), or another
independent depth-0 workbench factory. The final packet must identify and prove
that actual agent-operated launcher route; a supported source branch is not proof
of available credentials. If neither exists, nonhuman bootstrap is an explicit
activation prerequisite, not a reason to use Tom's token or exempt the descendant
refusal. No credential provisioning is authorized here. No Tom Mac step, borrowed
human token or active-session restart counts as satisfying this route.

P6 remains a held haynes-ops proposal: SA/bindings, agent-guard match, templates and
exact delegate flags all require Tom's review. Inventory every subject that can
exec, create bare Pods as that SA or create TokenRequests. Those subjects are trust
roots even if S4 admits only one session holder. If a delegate itself needs a
mode-(b)-only grant, the workbench factory must launch a **different** named delegate
with separately approved allowlist entry; no self-decision exception is introduced.
Do not provision a second identity automatically or mislabel legacy `132039` as one.

### 6.5 Scope escalation

A missing policy/case must report a concrete refusal, not wait as if approval were
possible. S3 notices are client evidence, not a complete server policy oracle.
Standing scope changes use RD1. Routine in-policy decisions stay with the scoped
delegate; reserved decisions cite Tom. Source readiness is not access approval.

### 6.6 Minimal missing source: designated policy reach

**Proposed, absent, default off.** Add `--grant-policy-delegates=<ns>/<sa>,...` to
operator and broker as a subset of `--grant-delegates`, using its existing bounded
parser. Empty preserves S2 tree-only behavior. It creates no new resource, policy
framework, requester identity or endpoint. This is new access scope pending Tom's
exact review, not permission to configure it now.

For a designated identity, replace only the tree-membership requirement with
existing exact delegated-policy coverage (or an enabled S7 case). Preserve all
other actor, scope, TTL and live-state checks. This permits the separate approval
session to decide requests from existing top-level `223503`, legacy `132039`, and
workbench/v1-created sessions; it does not reparent or migrate them. A sibling or
unrelated requester is eligible **only** for that explicit subset and covering
scope. Default tree delegates continue to refuse both.

In both modes refuse the requester's own session/Pod/account, a delegate that is a
**descendant of the requester**, and an actor named as maintenance coordinator or
worker. Walk ancestry with verified live objects; missing/replaced/ambiguous
ancestry fails closed. A new child cannot approve its parent's grant. Trust roots
able to take another actor's token remain the residual exposure in section 0.

Perform identical reach and policy checks before the operator writes the decision,
at broker apply/retry and while Active. Bind the recorded decision to the reviewed
policy name/UID and effective scope digest as well as the grant/actor UIDs and TTL;
for S7 bind its exact reserved case/bounds revision instead. Removing the policy,
replacing its UID, narrowing it, removing either delegate-list entry or changing
actor/requester identity revokes the lease through its normal cleanup/stop path.
No implicit widening on a policy lookup failure or broker downgrade is allowed.

**Handoff request:** existing S2 owner 163602 owns feasibility/implementation after
its current correction, on a separately declared follow-up branch/story. Integration
owner changes no S2 source. This request is not acknowledgement or proof that the
source exists. S7 must reuse this reach seam rather than silently invent a second
actor route. See section 9.3 for paths, permissions and required evidence.

## 7. Reserved decisions and external controls

### 7.1 S7 reserved cases: absent source

Reuse `POST /v1/grants/{name}/decision`, `reserved: true`, a mandatory nonsecret
`tomRef`, and default-empty `--reserved-decision-classes`. These names specify the
proposed contract; they are not existing flags/fields. Each decision binds grant
UID, requester/actor UIDs, exact scope and TTL; storage additionally binds object
UIDs. No standing or attested policy approves a reserved case. Tom's message is
recorded as an agent assertion alongside the authenticated delegate identity.

| Case | Concrete proposed scope | Execution and honest limit |
|---|---|---|
| RD2 | P7 `dev-env-grant-breakglass`: every API resource/verb, cluster-wide, TTL ≤1h; per-grant SA/ClusterRoleBinding | Root-equivalent exception. `impersonate`, `serviceaccounts/token`, Secret get/list, bind/escalate bypass the claimed identity confinement. Never namespace exclusion is **not** an effective RD2 guarantee. Kyverno/admission may reject some original-identity writes; they do not contain all alternate identities. |
| RD3a | Secret get/list in 1–16 explicitly named namespaces outside Never, TTL ≤1h | Namespaced RoleBindings, not cluster-wide binding. Values read cannot be recalled. |
| RD3b | `kube.allNamespaces: true`, reserved Secret-read role only, TTL ≤1h | Broker snapshots all namespaces except Never at approval and records the list; one RoleBinding per namespace. Future namespaces excluded. Recheck snapshot/ownership on retries; never fall back to a ClusterRoleBinding. |
| RD4 storage | one namespace, `dev-env-grant-storage`, up to 8 exact kind/name/UID targets | P5 deletion guard uses the caller's own Active grant and exact object UID, fail closed when missing. Same-name replacements refuse. Deletion is irreversible. |
| RD4 hardware | operation plus exact host/device/firmware target and `tomRef`, recorded on the C4 lease | Audit intent only, not target/command enforcement. Existing credential is the actual authority. S7 needs a separate audit path because S2 rejects credential decisions; keeper still requires its policy approval. |

P7's original ClusterRoleBinding and SA are removed on expiry/release. That stops
new requests using the original identity; it does **not** revoke impersonated
sessions, tokens minted for other SAs, copied Never-namespace credentials or
persistent objects it created. No bounded-revocation claim covers those derived
powers. Tom must see this exact scope and decide whether to approve it or require a
bounded catalog instead. The recommendation here preserves the broad homelab
break-glass capability with that explicit exception; the decision is pending.
A bounded alternative would need a separately reviewed explicit API/verb catalog,
exclude identity minting/escalation and all-namespace Secret reads, and prove its
indirect workload/proxy/node powers too. It is not silently substituted here.

### 7.2 RD1 uses the existing delivery workflow

Bot PR → review and required checks → pinned delivery guard/receipt → squash merge
→ Flux. Record Tom's authority-change message ref against the **exact** PR head and
rendered authority diff. Closing before merge cancels; a revert narrows after merge
but does not undo effects. GitHub admin/rulesets remain external constraints, not
RD2–RD4 caller tests. DESIGN-039 stronger consent and OP304 are deferred.

### 7.3 External ceremonies and remaining limits

Provider browser consent, third-party 2FA/captcha, GitHub admin/App/ruleset changes
and password-manager item creation/rotation need their existing external actors.
They are not justification for making internal grant classes human-only. The
keeper remains the only provider-token refresher. Control-plane maintenance and
profile `ops` remain outside this packet. Existing baseline CNPG re-clone scope is
unchanged. No new credential ceremony is executed by this design story.

## 8. Issuance, expiry and revocation

A request names its role/destinations/ports, namespaces, Secret names or immutable
maintenance/storage targets, exact TTL, purpose and restoration plan. None are
inferred from an app/node name or old approval. On release/expiry remove the grant's
bindings/SA, Secret-use Roles, CNP or installed credential files, as appropriate.
Maintenance cleanup waits for executor termination proof. Keep the audit record;
do not put secret values in proof. Open exec streams, external effects, copied
keys/tokens and P7-derived powers have the explicit limits above.

Concurrency recommendations (not current general broker enforcement): C1/C3 two;
C2 one per namespace; C4 one per credential; C5 one per target; reserved one per
class. A safe refusal is not progress acceptance. Competing Pending maintenance
reservations and the unauthorized set-once slot are different problems; the latter
is a required S2 correction, not closed by an availability follow-up.

## 9. Exact prerequisite and implementation matrix

### 9.1 P1 policy shapes

All requester blocks use `[full, workbench, coordinator]`, no repo restriction.
At T0 use **attested only**, omitting delegated fields entirely. After T2's CRD,
binaries and reach source are installed, add the delegated blocks shown below.

| Policy | Final blocks | Scope | maxTTL |
|---|---|---|---|
| `kube-workloads` | attested, delegated | workloads role, app namespaces | 3h |
| `kube-workloads-infra` | delegated only | workloads role, infrastructure namespaces | 8h |
| `kube-namespace-operator` | delegated only | namespace-operator role, app namespaces and `network`; explicit deny list | 1h |
| `egress-cluster-services` | attested, delegated | explicit app endpoint selectors and up to 16 explicit ports | 3h |
| `kube-nodes-attested` | attested, delegated | nodes role, cluster-wide including storage reach | 3h |
| `lan-ssh` | attested, delegated | current eight `lan-ssh-tier` hosts plus existing PiKVM CIDR, port 22 | 1h |
| `lan-pve-api` | attested, delegated | existing pvedash and five node hosts, ports 443/8006 | 3h |
| `credential-hw-ssh-attested` | attested | existing `hw-ssh` credential | 1h |
| `credential-proxmox-attested` | attested | existing `proxmox` credential | 1h |
| `maintenance-card-c` | attested, delegated | exact #781 envelope, node UID/Card C/image digest/runtime/capabilities/resource bounds | 1h |

Before Tom's exact review, freeze the rendered endpoint labels/ports (including
empty-selector semantics), literal existing host/CIDR list, Secret deny list, full
image digest, live node UID, delegate identities and all policy/RBAC diffs. These
are configuration prerequisites, not permission to guess or apply values. A packet
with those values unresolved is not activation-ready.

### 9.2 Namespace bands

- **App:** `ai`, `downloads`, `frontend`, `home-automation`, `media`, `observability`,
  `office`, `photos`.
- **Infrastructure:** `arc-runners`, `arc-systems`, `cert-manager`, `k8tz`, `network`,
  `upgrade-agent`; C1 delegated-only, C2 only `network`.
- **Storage/data:** `ceph-csi`, `database`, `openebs-system`, `rook-ceph`,
  `storage-util`, `volsync-system`; RD4 for new storage grant deletes.
- **Never for ordinary namespaced grants and RD3:** `dev-env-system`, `dev-agents`,
  `dev-tools`, `dev-env-budgets`, `dev`, `default`, `flux-system`, `kyverno`,
  `external-secrets`, `kube-system`, `kube-public`, `kube-node-lease`, `cilium-secrets`.

P7 effective root, inherited workload SA powers, workbench exec, node operations
and the GitOps authority route are the explicit exceptions/limits, not promises of
namespace isolation.

### 9.3 Owned source and permission dependencies

| Item | Exact source or GitOps work | Owner/request and proof needed |
|---|---|---|
| S2 slot correction | `internal/apiserver/grant_decision.go`, Server/operator policy-namespace wiring; shared broker matcher; real-token fixtures | Existing owner 163602, PR791. Live policy coverage before annotation for approve/deny; 403/no mutation then authorized decision succeeds; two-replica CAS still yields one record; broker rechecks retained. |
| S2 reach follow-up | API/broker reach helpers and decision binding, `cmd/dev-env-operator` subset flag parsing; API/broker tests | Request to 163602 for separate disjoint custody after current fix. Tree default, policy subset validation, self/descendant/participant refusals, UID/digest binding and active revocation. No source-ready claim. |
| S6 | PR790 creator rule and integration with S2 pending path | 163602; preserve delegate → human → attested order plus S6 creator check. Reverify final combined head and actual TokenRequest/TokenReview envtest. |
| S5 | server/view `secrets`, digest, schema, `kubeInside` delegated matching, per-grant use Role; local Grok correction | Existing Grok owner. Rebase after corrected S2; generated schema, all five fail-closed cases, same-object nonmutation mutant, any-SA residual exposure and required CI. |
| S3 | PR792 client commits on final S2/S5 contract | Grok / Codex 165730; retarget/rebase after dependencies. Local six-mutant proof retained; hosted Build/Lint/Test and exact-head review required. S7 fields need later explicit client work. |
| S7 | AccessGrant/decision wire/schema/digest; API reserved gate; broker materialize/revoke; RD3 expansion; RD4 target guard and hardware audit | **Unassigned missing source**, feasibility request to existing S2/S5 owners through coordinator before custody. Separate bounded follow-up(s), no duplicate writer or invented source readiness. |
| R1/R2 | CRD copies before new-field policies; released operator/broker/client pins | Held haynes-ops delivery after source/CI/review. CRD acceptance and binary versions recorded independently. |
| P2/P3 | matching operator/broker delegate and policy-reach lists, S6 creators, reserved flags; set-once annotation guard for kube/egress/maintenance | Held security/access review. Verify exact subjects and list equality, no annotation forge/overwrite; flags absent/empty fail closed. |
| Policy reads | operator get/list GrantPolicies only in configured policy namespace | New permission needed by slot correction; held haynes-ops RBAC diff. Broker keeps its independent live reads. Forbidden read must not consume a slot or silently authorize. |
| Delegate host reads | broker named-Pod get in a configured host namespace outside `dev-agents` | Only if such a host is selected. Explicit named-resource permission and reviewed binding; no cluster-wide Pod-read assumption. New P6 session in `dev-agents` avoids that extra dependency. |
| P4/P5 | namespace role/catalog and broker bind/use/Role shape guard; identity guard per-name Secret use/deadline; storage own-Active-grant UID DELETE guard | Held haynes-ops source after S5/S7. Test RBAC escalation constraints and absence of own params/Role. Secret-use is not a native Pod permission. |
| P6 | new named approval SA, bindings/guard/profile; supported workbench launch; no restart/drain of active sessions | Template hold unchanged. Tom's exact exception plus rendered-diff/continuity proof; do not retrofit actual `132039`. |
| P7 | explicit root ClusterRole/catalog/broker binding path and admission compatibility | Missing held GitOps source. Tom must review root/credential/expiry residuals, not a false identity-guard guarantee. |

These are requests and prerequisites, not a new backlog. Canonical progress stays
on OP233 and its linked owned stories. Native review is mandatory; automated Claude
advisory is optional, and every emitted finding must be fixed or answered.

## 10. Acceptance: source, installed and usable remain separate

Run CPU-bounded suites, at most two workers/packages and one suite at a time. No
load/stress loops. Source-only fixtures use synthetic Secrets/targets and no live
credential reads. Runtime proof requires a later authorized execution story.

### A1: GPU and top-level message route

Source proof: exact `223503` parent SA/depth-0 positive; child, missing parent/depth,
unlisted creator, stale requester and creator-removal negatives; executor stop proof.
The real-token path mints actual API-server TokenRequest tokens and validates
TokenReview for workbench launch → new named delegate → child grant → 202/Pending
→ broker Active → release. Synthetic Pod materialization is explicitly disclosed.
Test the duplicate-holder/two-replica launch race, ordinary launch refusal and S4
eligibility seam independently from decision CAS.

After 6.6, additionally use an existing top-level-shaped requester with no delegate
ancestor: C2, infra C1, exact GPU maintenance/extension and enabled reserved cases
must work through a separate policy-reach delegate. Same digest without that subset
must refuse. The delegate's own grant and a requester-created descendant delegate
must refuse even with policy reach. No human credential fallback counts as this pass.

Installed/usable proof later: preserve original session/Pod UIDs and restart counts;
request the exact Card C/node UID/full image/runtime/capabilities/resources envelope,
receive a real in-scope message, attest or delegate as eligible, observe Active and
a bounded executor, run read-only `nvidia-smi`, then release and prove termination,
Pod/CNP cleanup and later refusal. No load or power-write probe follows from this
proposal. Existing observed 350 W is not permission to write it.

### A2: namespace-operator MAM workflow

Source preconditions: corrected S2 plus reach source, integrated S5/P4/P5, separate
new named delegate, and exact `kube-namespace-operator` policy. The requester may be
an existing top-level session; it need not become a descendant or restart.

Request downloads namespace, Secrets `myanonamouse-secret` and `prowlarr-secret`,
TTL 30m. Delegate approves the exact UID/digest/ref; observe Active and the per-name
Role, then `grant use`. The example Job voluntarily selects `default`, caps runtime
and cleanup at 3600s, gets `MAM_ID_PROWLARR` and `PROWLARR__AUTH__APIKEY` by references,
and makes the same status-only Prowlarr GET/PUT operation as the emergency repair.
Never print either value.

Refuse unlisted/denied Secret, another namespace, baseline/C1 caller, missing own
grant, ended grant, or a Secret allowed only by another grant. Confirm all 16 names
fit admission cost. Test direct references separately from exec/any-SA exposure;
a synthetic privileged unused SA demonstrates that exposure without touching a real
credential. Open exec/borrowed-token limitations remain disclosed. Release removes
the SA, RoleBindings and Secret-use Roles; later requests fail.

Run real MAM acceptance only on the next authorized cookie apply/idempotent repair,
not an indexer test solely for proof (MAM can rotate the cookie on use). Emergency
haynes-ops #4501 merged `e6f80c51` and cleanup #4502 `01706241` succeeded through
GitOps; OP319 closure does not satisfy this grant path.

### A3: reserved callers, scopes and cleanup

For **every RD2–RD4 recorded decision**, independently test: mode (a) refused;
unlisted caller refused; requester itself refused; requester's descendant as
delegate refused; missing `tomRef` refused. Also test wrong grant UID/digest/TTL,
expired decision, replay, missing enabled case and removed delegate. Default-tree
sibling/unrelated request refuses; designated policy reach may admit those within
bounds. An SA-ref creator shared by top-level sessions is not tree reach.
RD4 hardware audit requires those caller checks too; its underlying C4 attestation
alone never counts as the reserved record. No external GitHub control substitutes
for these internal caller tests.

| Case | Positive proof | Scope/refusal and expiry proof |
|---|---|---|
| RD1 | exact-head bot PR, message ref, delivery receipt, review, merge/Flux | external repo rules plus exact diff review; close/revert semantics. This is a different route from the reserved API tests. |
| RD2 | synthetic app write through root grant; envtest/SAR verifies effective impersonate, TokenRequest and Never Secret-read permissions without reading real Secrets | malformed/unbound reserved record and TTL >1h refuse. Do **not** expect identity guard to block all alternate identities/reads. Original SA/binding gone on release; derived-identity/persistent-effect limits explicitly demonstrated with synthetic fixtures, not claimed revoked. |
| RD3a | synthetic Secret read in one named allowed namespace | Never/unnamed namespace refused; binding removed and later reads refuse. |
| RD3b | synthetic Secret read in frozen expanded list | Never/future namespace refused, no all-namespace binding, all owned bindings removed. |
| RD4 storage | one synthetic UID-bound delete | new UID at same name, other object/namespace, absent or ended own grant refuse; release before delete cancels; binding cleanup proven. |
| RD4 hardware | authenticated delegate audit record on valid C4 lease with exact operation/target/Tom ref | record checks tested; target/command enforcement is **absent**. Credential file cleanup proven; copied key/process/external effects remain. |

### A4: decision slot, revocation and dependency regressions

Before annotation, uncovered approve **and deny**, policy-read Forbidden, an
invalid maintenance target, and other unauthorized decisions must leave no record.
Then prove an otherwise authorized decision can succeed without release/re-request
or timeout. Existing authorized conservative target-reservation behavior needs its
own progress test; OP332 is not proof that the unauthorized-slot defect is fixed.

After a valid decision, test policy removal/narrowing/replacement UID, reach-list
removal, actor Pod/session replacement, requester UID change, suspended/reaped
actor and stale digest at apply and while Active. Ensure transient reads retry,
permanent lost authority ends access, and maintenance waits for termination proof.
Two-replica decisions give one record and one 409. Rebase S3 on actual S5 wire fields
and rerun hosted CI; its standalone local PASS does not waive the merge-result fail.

## 11. One pending decision packet

Q-042-01 asks Tom to review the **exact final rendered packet and reviewed source
heads**, including designated policy reach for existing sessions, creator scope,
C2 any-SA/cross-namespace exposure, workbench trust-root access, hardware powers,
and RD2 root/derived-credential residuals. None is labelled accepted. Q-042-02 asks
for only the P6 template exception, with verified continuity of active sessions.
They are delivered together through the coordinator once the concrete prerequisites
are resolved. There is no default approval, one-time approval reuse or human-token
workaround. Missing source/evidence and unresolved exact configuration remain
explicitly pending; do not solicit an abstract risk waiver in place of that packet.

## 12. Non-goals and stop boundary

No new access framework, approval service, per-command gates, bulk tracker/history
inspection, OP304 prerequisite, live activation, credentials, session migration,
GPU load/power changes or security/network writes. This source-preparation story
ends with a reviewed proposal, exact dependency handoff and canonical checkpoint.
It does not automatically start the missing-source stories or activate any track.
