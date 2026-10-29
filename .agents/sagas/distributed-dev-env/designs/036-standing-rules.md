# DESIGN-036: Standing rules on the Asks board

- **Status:** Proposed, 2026-10-29. Documentation only.
- **Decision:** [ADR-011](../adrs/011-standing-rules-as-policy-asks.md).
- **Tracking:** OpenProject #261, Epic #254 (Asks board), Saga #85.
- **Work order:** ASKS261-STANDING-RULES-DESIGN-20261029-01.
- **Source examined:** dev-env `c2afa8ce59cc7841f3d9f6b580d068b8e57037f9`;
  haynes-ops `854a7b24c9375650fbd3b7d0fc1c70f032116829` (read only).

## 1. Outcome and owner ruling

Tom approves a scoped rule once. An eligible agent can then request work
inside that rule without asking him again. Tom can see the rule and revoke
it. A rule supplies permission within its scope; each grant still has a
TTL, and other access controls and explicit holds still apply.

The source of the distinction is **Q-255-04, 2026-10-29, OpenProject #255
journal 2680**. Tom said: “It's situational, I may be asked to approve
binding changes that are beyond one request.” He also said: “here I do not
want to approve agents to always send emails just that I approve the one
that was drafted.” These excerpts omit the operational examples. Journal
2680 supersedes journal 2675, which was retracted in 2676.

| Approval kind | What Approve means | What can use it |
|---|---|---|
| One-off, DESIGN-034 v1 | Approves this exact action, target and content once, within its expiry. | The exact approved request. No policy is created. |
| Standing rule, this proposal | Approves this exact policy change for named requesters, actions, targets and limits. | Repeated matching requests, after installation and until revocation or explicit expiry. |

This is the standing-rule follow-up described by the lead in journal
2680. [DESIGN-034 §13](034-agent-asks-and-approvals-board.md#13-questions-for-tom)
records the dated ruling. Its v1 API remains unchanged by this document.
Information answers remain information. No Ask starts a generic executor.

## 2. What the code supports today

| Source | Observed contract | Design consequence |
|---|---|---|
| [AccessGrant and GrantPolicy, D-54](../../../../api/v1alpha1/accessgrant_types.go) | Grants live beside Sessions. Their specs are immutable except one-way `release`. Policies live in `dev-env-system`, written in haynes-ops and applied by Flux. Policies match profiles, repositories and optional agent kinds. | Reuse the policy/grant split. A repository name is not a project or session group. New subject kinds need their own trusted resolver. |
| [Grant handlers, D-56](../../../../internal/apiserver/grants.go) | Only an authenticated Session creates a grant for itself. The server derives requester fields and UID. Identical pending/active scopes merge; TTL and reason are absent from that merge key. Release sets `spec.release`; the broker performs cleanup. | Do not let a browser or agent supply a requester identity. A repeated grant request does not prove a new approval, use or TTL extension. |
| [Policy matcher, D-61](../../../../internal/broker/match.go) | All populated requester lists must match. No requester lists means no match. Policy matches are tried in name order; deleting policies are skipped. `ops`, break-glass and Secrets-read do not qualify. | Preserve these refusals. Display overlapping policies. Do not turn ordering into a permission priority or deny rule. |
| [Broker lifecycle](../../../../internal/broker/broker.go), [watches](../../../../internal/broker/manager.go) | Pending grants use a cached policy list. A policy change queues pending grants. Active kube/egress grants proceed without rereading the policy. `approvedBy` stores `policy/<name>`, with no policy UID or revision. | Policy deletion alone cannot promise immediate revocation. Add exact rule provenance and an authoritative admission barrier. |
| [Credential execution](../../../../internal/broker/credentials.go) | Before creating the first CredentialJob, the broker rereads the exact approving policy and checks its match. That is not a general continuous policy-revocation check. | Do not claim all credential use is stopped by deleting a policy. Credential rules need separate qualification. |
| [D-88 attestation](../../../../internal/grantattest/attest.go), [API write](../../../../internal/apiserver/grant_attestation.go), [broker checks](../../../../internal/broker/attestation.go) | A Session claims it obtained approval for one grant UID, scope digest and TTL. This is explicitly not verified human identity. Attested policies use nested requesters and empty legacy requester lists so older brokers match nobody. | Never convert attestation into Tom's approval of a rule. Reuse the rollback refusal pattern, not its human-identity claim. |
| [Egress builder, D-64](../../../../internal/egress/policy.go), [revoke](../../../../internal/broker/kube.go) | A grant creates a scoped CiliumNetworkPolicy. All listed ports apply to all listed destinations. Cleanup checks ownership; kube cleanup deletes bindings and the grant ServiceAccount. | Preview the entire permission. Preserve ownership and UID checks. Removing an allow policy does not override some other allow policy. |
| [Broker RBAC copy](../../../../internal/broker/testdata/haynes-ops/broker.yaml), [role catalog copy](../../../../internal/broker/testdata/haynes-ops/grant-catalog.yaml) | Policy rights are get/list/watch. Bind rights name a fixed role catalog. The workloads role is broad and does not grant `pods/exec`. | No existing broker policy writer or pod-specific exec role can be reused unchanged. |
| [DESIGN-029 groups](029-portal-foundations.md#group-based-action-scopes-2026-10-29), [DESIGN-034 attribution](034-agent-asks-and-approvals-board.md#3-api-and-caller-classes) | Fresh UserInfo checks the same request-private principal and active/admin group claims. Resource scopes remain separate. Asks store the principal for private audit; Session views omit issuer/subject. | Reuse group checks, CSRF and identity projection. Do not add personal-ID allowlists or infer standing-rule administration from portal login. |

The haynes-ops read at the pinned revision confirmed these deployment
facts, independently of the test copies:

- `kubernetes/main/apps/dev-env-system/rbac/app/broker.yaml` gives the
  broker only get/list/watch on `grantpolicies`.
- `kubernetes/main/apps/dev-env-system/grant-policies/app/kustomization.yaml`
  includes `policies.yaml`. Flux owns desired policy configuration.
- `kubernetes/main/apps/dev-env-system/broker/ks.yaml` depends on the CRDs,
  RBAC and network Kustomizations. `broker/app/helmrelease.yaml` runs the
  broker separately from the operator, with leader election.

These are source observations, not a live cluster acceptance. The
[D-54/D-56/D-61/D-64/D-88 record](001-dev-env-v2.md#612-access-no-prompts-in-the-pod-control-at-the-platform)
and [plan 07](../backlog/07-access-broker.md) supply the earlier rationale.

## 3. The rule record

Propose an operator-owned `StandingRule` in the protected coordination
store. Its spec is immutable. Its status records consent and installation;
it is not another writable copy of `GrantPolicy`. A broker-owned admission
record holds the runtime barrier described in §6. Sessions and the portal
have API access only, with no direct CRD or cluster-configuration writes.

| Field | Proposed meaning and limit |
|---|---|
| Identity | Server rule ID and UID, immutable revision, originating Ask UID, requester Session UID and schema version. Never reuse a revoked rule identity. |
| `who` | Exactly one primary kind: `profile`, `session_group` or `project`, with one exact operator-resolved reference. Optional extra profile/repository constraints narrow it by intersection. No wildcard, empty selector or “any agent.” At most 8 narrowing values. |
| `action` | One registered action kind and adapter version. It describes enforceable permission, such as a catalog kube role or scoped egress. Kube consent also binds the resolved role permission digest and immutable catalog revision. Prose cannot define enforcement. |
| `target` | Typed scope for that adapter: named namespaces and role, or explicit destinations/endpoints and ports. Resource-specific actions bind a trusted resource identity, and its allowed replacement behavior. Never accept a supplied shell command as a policy target. |
| `limits` | Required maximum grant TTL, at most the existing type cap; propose 1h by default. Required maximum concurrent grants, propose 1, bounded 1–8 per rule. Any other advertised limit must have an enforcement check. Missing limits are not unlimited. |
| `reviewAt`, `expiresAt` | At least one future UTC date. A review date prompts review; it does not silently revoke or demand approval on each use. Explicit expiry stops new grants. Propose a review reminder after 90 days when no expiry is chosen (Q-261-03). |
| `reason` | Plain text, 1–1000 runes, explaining why repeated permission is needed. Required separately from an individual grant reason. |
| Consent | Private BFF-derived actor, approval time, exact canonical rule digest, group-policy version/check time and stable operation ID. A display name is not identity proof. |
| Installation | Compiler version, exact generated policy digest, GitOps PR/head/merge commit, expected policy name/UID/generation, broker capability and confirmation time. Unknown or drifted evidence never means active. |

The server computes the digest over the schema/adapter versions, resolved
`who`, action, target, limits, dates and reason. Approval binds that digest
and Ask UID. Canonicalization sorts sets and makes defaults explicit. A
changed material field creates a new revision and a new Ask; answer notes
cannot amend the rule.

An explicit expiry also bounds each grant's end time. Do not issue a
grant if the remaining rule lifetime is below the grant type's minimum
TTL. Expiry closes the admission barrier and initiates the same issued-grant
cleanup as revocation; it does not depend on a sweeper running on time.
Review reminders do not extend an expiry or automatically renew consent.

A named ClusterRole is mutable in today's catalog. Its expansion would
widen existing bindings as well as future grants, even if the GrantPolicy
digest stayed the same. Kube activation therefore also requires an
immutable, versioned role permission set and an admission guard preventing
in-place expansion or aggregation drift. Consent, installation and grants
bind that role revision, UID and resolved permission digest. A new permission
set uses a new catalog revision and new consent. A check at grant admission
alone does not protect existing bindings. The current mutable role catalog
does not meet this condition; its schema/bind allowlist changes need a
separate reviewed capability slice before any kube rule is enabled.

Resolve membership from authenticated Session data and operator-controlled
configuration. A profile is the effective profile used to build that
Session, not a name asserted by the requester. A project needs the trusted
catalog identity and current membership. A session group needs a stable
group UID and operator-owned membership, not a mutable label or parent
string. Recheck eligibility for each admission, including replacement
Session UIDs and profile changes. Unavailable membership denies admission.

The first build should support exact profiles, with optional repository
narrowing. It must reject project/group activation until those resolvers
are installed and qualified. Do not translate a project into one repository
and claim they are equivalent.

Propose limits of 16 pending rule proposals per requester and 256 retained
rules in the first installation. Reuse the Ask text/encoding bounds. Page
lists at 20 by default, at most 100. Keep at most 8 outstanding grant
admissions in a rule's runtime record. Keep audit events in separate,
bounded immutable records; refuse new admissions when audit capacity cannot
be confirmed. Revocation always retains a reserved audit slot and needs no
new proposal slot. A later retention story must qualify archival before
deleting records or increasing these bounds.

### Refuse broader or unsupported permission

Reject “agents may send email”: no bounded who, recipient set, content
class, rate/size limit or qualified mail adapter exists. Approval of one
draft's exact content does not repair those omissions. An agent may
propose a new typed adapter through a separate design story.

Likewise, “profile `sandbox-example` may exec into service `sample-app`”
needs an exact workload target and an exec adapter that enforces it. The
current kube catalog offers neither pod selectors nor exec-specific roles.
Do not substitute the workloads role or namespace-wide admin. Bind a future
adapter to the reviewed workload identity and container; document pod-roll
behavior and the fact that an interactive shell can reach data available
inside that container. Until then, label this proposal Unsupported and
disable Approve. No real operational target is reproduced here.

The initial compiler may support scoped egress and named kube roles only
after their immutable catalog path qualifies. Every compiled permission
must fit the approved rule. Keep break-glass,
Secrets-read, `ops` and the protected namespaces excluded. Defer cluster-wide
node rules, all credential rules, mail and pod exec to separate bounded
stories. Do not silently loosen existing schema or admission guards.

## 4. Propose and approve a rule

Extend the Ask discriminant with **`type: standing_rule`**. Keep `approval`
as the one-off kind and `info` as information. This is a versioned follow-up,
not a reinterpretation of v1 data. The new type carries a typed `rule`
proposal; it cannot carry the one-off `scope` in its place. Use the existing
Ask creation idempotency and server-derived requester. A new MCP tool or
explicit typed option returns immediately with the Ask ID and status.
It cannot answer or apply the rule.

An agent checks applicable active rules first. If a match exists, it asks
for a grant and supplies the rule reference for server verification. It
does not ask Tom to approve the same permission again. No match may prompt
a new one-off or standing-rule Ask, as the work requires. Pending,
uninstalled or Unknown rules are not permission. Do not fall back to a
duplicate human prompt while the matching proposal is awaiting installation.

Propose an immutable typed `ruleRef` on the grant request/spec, naming the
rule UID/revision. It is new wire/schema work. For a supplied reference,
validate current rule eligibility before the existing scope-merge path.
Merge only the same rule revision and exact permission/limit binding;
never return a revoked or different-rule grant as usable. Reject a failed
reference without falling back to another policy or attestation. Legacy
requests retain their separate existing semantics and overlapping authority.

The portal shows two clearly distinct approval forms:

| Form | Required plain text | Primary controls |
|---|---|---|
| One-off approval | **“Approves this one action”**, followed by the exact action, target, content and expiry. | Approve this action / Deny |
| Standing rule | **“Creates a standing rule until you revoke it”**, followed by who, permission, target, limits and review date. If it has an expiry, add “It ends on [date] if you do not revoke it sooner.” | Approve this rule / Deny |

The rule form also says: “Matching requests will not ask you again.
The rule takes effect after its policy change is installed.” Show the
compiled permission preview before the control. No preselected approval,
hidden target, free-text override, approval through a notification link,
or inference from a note. Unsupported adapters cannot be approved.

Reuse the same-origin BFF, encrypted browser session, CSRF/Origin checks,
bounded forms and fresh group checks in DESIGN-029/034. Bind the receipt
to the same private session, Ask UID, rule digest and operation ID. Recheck
before dispatch. The operator accepts the human assertion only from its
admitted BFF identity. Agents, coordinators, relays and agent attestations
cannot answer or forge the actor. Rule administration needs an explicitly
enabled resource capability and the owner choice in Q-261-01.

The rule's answer deadline is separate from its policy lifetime. An open
proposal can expire as an Ask. Once answered, consent stays as history;
the rule's own dates, revocation and installation determine permission.
Do not run the v1 24h one-off validity helper on a standing rule. Deny
creates no activatable policy. Identical response-loss retries return the
same answer/intent; changed content conflicts. Read status after an
ambiguous result. No new key, policy or consent is inferred from a timeout.

## 5. Approval to GitOps policy

Choose an **operator-written GitOps PR**, not broker-owned policy objects.
The broker has no policy write authority today. Its separate identity
protects that boundary. Flux already owns these policies; a second live
writer would cause drift and need a new trust model.

After consent, a narrow, model-free PR writer takes the immutable rule
digest and produces only the allowed policy diff in haynes-ops. Its future
GitHub authority must be reviewed and limited to this delivery path. Do
not add a GitHub credential to the general operator or broker. This design
creates no writer, credential, repository permission or automatic merge.

1. Compile the resolved rule using an allowlisted adapter. Save the exact
   manifest digest, source base, Ask UID and rule revision in a durable
   installation intent. Exclude raw human identity and private Ask prose
   from public Git/PR text. Refuse publication if the approved scope cannot
   be represented safely in the chosen repository.
2. Open one PR for that intent, using a stable marker to reconcile lost
   responses. Propose only the policy and its rule reference. A base conflict
   requires revalidation of the same meaning; a semantic change needs a new
   Ask. Never regenerate wider scope to make CI pass.
3. Tom approves the policy change through the portal once. The authorized
   GitOps merger checks that consent still covers the exact diff and is
   neither revoked nor expired, that required CI passed, and that no hold
   applies. The PR records this consent. Review and merge are deployment
   controls, not a second per-grant approval. Who may perform that merge is
   an open delivery choice, Q-261-04; no auto-merge is authorized here.
4. Flux applies the reviewed policy. Confirm the applied policy UID,
   generation and content digest with an authoritative read. For kube,
   confirm the guarded role revision and resolved permissions too. The broker
   verifies the matching rule revision, consent, epoch and its own support.
   Only then may the rule show Active. A merged PR or Ready Flux condition
   alone is not enough.
5. Each later grant records exact rule ID/UID/revision, policy UID/digest,
   epoch, authenticated Session UID, scope digest, TTL and admission ID.
   Keep `approvedBy: policy/<name>` for existing consumers; it is not the
   whole provenance record. Never mutate an agent attestation to look like
   this verified portal consent.

Propose a new `GrantPolicy.spec.standingRule` variant. Put requester
selectors inside that variant; keep legacy `profiles`, `repos` and `agents`
empty, as the attested-policy rollback pattern does. It also references
rule UID/revision, approved digest and epoch. Schema must forbid combining
it with `attested` or the legacy requester fields. Updated brokers use the
new evaluator; older brokers see no named requester and match nobody.
Install CRDs before writers so unknown fields cannot be silently pruned.
Qualify downgrade behavior on real stored objects, not only a helper test.

Do not put new safety limits only in annotations on an ordinary matching
policy: an old broker would still approve it. Missing rule records,
unreadable consent, digest drift, unsupported adapters, expired rules or
unconfirmed policy installation refuse admission. Unrelated legacy
policies keep their existing behavior; this proposal does not migrate or
broaden them automatically. The portal identifies them as legacy policies,
not managed standing rules, until a separate reviewed migration.

## 6. List, revoke and audit

Add **Standing rules** beside Asks in the portal shell. Show Pending
approval, Approved / awaiting policy, Installation failed, Active, Review
due, Expired and Revoked, with source timestamps. Keep the historical Ask
answer visible independently. Show who, action, target, limits, dates,
reason, consent display name, policy revision/PR, last admission and linked
grants. List revoked rules too. Do not turn unavailable evidence into zero
grants or Active. A Session may read the safe projection of its applicable
rules, without raw approver identity or other Sessions' private content.

Proposed operator routes, all default off:

| Route | Caller / effect |
|---|---|
| `GET /v1/standing-rules?mine=true` and `GET /v1/standing-rules/{id}` | Authenticated Session; applicable safe projection only. No scope expansion through a cursor. |
| `GET /v1/portal/standing-rules` and `GET /v1/portal/standing-rules/{id}` | Admitted BFF with fresh human group/resource checks; bounded list and detail. |
| `POST /v1/portal/standing-rules/{id}/revoke` | BFF only, after the revoke-authority ruling. Exact UID/revision, expected digest, reason and stable operation ID; private actor derived at the BFF. |

The form says **“Revoke this rule”** and identifies the affected grants.
It uses the same fresh identity, CSRF and receipt protections as approval.
No GET changes authority. Logout or group removal stops future portal
operations; it does not erase or retroactively revoke a rule.

### Immediate effect for new grants

A GitOps deletion alone is too slow. Add a broker-owned **per-rule
admission record** with a monotonic revoked flag and epoch. This runtime
record can stop access; it cannot create policy or exceed installed scope.
Use narrow CRD/status rights and admission guards installed through GitOps.
The operator saves an immutable authenticated revoke intent. The broker
commits its audit reference and revoked flag by compare-and-swap on the
same record used to admit grants.

Every admission must commit its grant UID, scope digest and rule revision
in that record before `AccessGrant` can become Active. That CAS is the
issuance point. A revocation CAS and an issuance CAS cannot both win from
the same version. After revocation wins, no later admission under this
rule can succeed. Cached matches are candidates only. A fresh read alone,
followed by an unrelated status write, is not sufficient serialization.
Broker failover must use the durable record, not an in-memory mutex or a
leader Lease alone.

The bounded admission entries also enforce concurrency across replicas.
Each entry remains until its grant has a confirmed terminal/cleanup fact;
Unknown consumes its slot. A grant admitted just before revocation is an
already-issued grant, even if its Active status write is still pending.
Recovery must follow its saved admission, prevent duplicate materialization
and route it to cleanup when revoked. Never let a stale worker recreate
authority after cleanup; fence grant execution/reinstallation and cleanup
with the same admission generation. Qualify this protocol before enabling
any managed rule.

Do not mark cleanup confirmed while an older execution worker or its
external requests may still complete. Require durable completion/drain
evidence for that generation, then a final cleanup/read-back. Uncertain
worker ownership stays Unknown and retains its admission slot. A fresh
check followed by an unfenced resource create cannot satisfy this contract.

The revoke API reports success only after authoritative confirmation of
the barrier commit. “Revoked for new grants” means that commit, without a
wait for PR merge, Flux or an informer refresh. A saved intent awaiting the
broker is **Revocation pending**; a lost response is **Unknown** until
reconciled by the same operation ID. Do not claim instant physical cleanup
or a confirmed revoke when the broker is unavailable.

Once confirmed, the writer opens a GitOps PR to remove the policy, or
reconciles the existing removal intent. Approval awaiting installation is
also revocable: the barrier prevents a late Flux apply from activating it.
A recreated policy with the same name cannot clear the tombstone. Restoring
an old PR or editing prose cannot reactivate a revoked rule. A new rule
requires new consent and a new identity.

### Grants already issued

**Proposed default, subject to Q-261-02: release every grant issued under
the revoked rule.** Admission history and grant provenance identify the
exact set, including a grant whose status write was interrupted. Mark
cleanup requested durably and enqueue those grants immediately. Reuse the
one-way release and broker-owned cleanup paths; do not delete the audit
records or transfer them to another matching policy.

| Issued permission | Revocation effect and limit |
|---|---|
| Kube | Delete owned bindings and the grant ServiceAccount, then confirm cleanup. This blocks subsequent authenticated requests with that identity. An existing exec stream or action already started may continue; do not claim it was killed or undone. |
| Egress | Remove the grant-owned CiliumNetworkPolicy with ownership/UID checks. Show control-plane cleanup separately from observed network enforcement. Existing connections and another policy's allows prevent a blanket “disconnected” claim. |
| Credential | Outside the first build. Future adapters must qualify remote revocation and cleanup. A copied static key cannot be revoked by removing a tmpfs lease. |
| External action, such as mail | Outside the first build. Revocation cannot unsend an action or substitute for a per-action cancellation contract. |

Show counts of cleanup requested, confirmed and Unknown, with grant IDs
and remaining TTLs. Cleanup failure remains visible and blocks fresh use
of that admission. Do not restart Sessions or terminate a pod to simulate
revocation. Other independent policies and baseline rights may still allow
the same work; show overlaps. Revoking one rule is not a global deny.

### Durable audit and recovery

Record proposal, explicit approval/denial, PR intent, merge, installed
digest, admission, revoke intent, barrier commit and cleanup outcomes.
Each event binds the rule/Ask UID, revision/digest, policy epoch, operation
ID, server time and authenticated actor or service. Store human principals
only in the protected audit, using DESIGN-034's Session projection. Logs,
pushes, public PRs and metrics carry opaque IDs and fixed reason codes,
not identities, tokens or rule prose. Notices are hints, not authority.

CAS/idempotency and authoritative read-back must reconcile crashes before
or after each write. Save the immutable revoke intent and actor first;
the barrier CAS embeds its verified event reference/digest and commit time.
That is the durable deny audit, even if later event export fails. Do not
claim an atomic transaction across the intent and a separate event object.
Revocation does not wait for inbox delivery. F1 may later deliver a safe
status hint, never a portable permission token.

Retain revoked tombstones and audit until a separately qualified retention
extension preserves their denial effect. Do not reuse the broker's 90-day
ended-grant deletion as the rule-retention policy. A restore could resurrect
an old active record. Require an externally managed GitOps policy epoch
and a closed admission gate after restore, until authorized reconciliation
of consent and revocations. Missing/old epochs refuse use. Reject rollback
to a broker that can bypass these checks. No restore or rollback may
silently resurrect permission.

## 7. One-offs, Q-255-01 and DESIGN-034 F2

**One-off approval can never become a rule.** Enforce the Ask type and
typed payload as immutable in schema and store. The policy compiler accepts
only an approved `standing_rule` Ask and its exact rule digest. Rewording
question/answer text, repeated approvals, an inbox copy, a tracker comment
or a `grant attest` reference cannot supply that type. A “propose a rule”
shortcut creates a new Ask without copying consent.

DESIGN-034 v1 remains a one-off record with at most 24h scope expiry and
no revocation or generic one-use executor. The ruling defines once-only
human intent; it does not claim a source adapter already enforces
consumption. Any future action adapter must reserve its exact use and
preserve Unknown without replay. It cannot use this rule mechanism to
skip the one-off content binding.

**Q-255-01 is still open and deferred to F2:** may any currently admitted
portal-group member revoke an unused approval given by another member?
F2 is the separate revocation/audit increment for one-off approvals. Its
answer is not permission for agents to revoke, and does not silently settle
standing-rule administration. The lead must explicitly cover both scopes
when obtaining the ruling, or record a separate standing-rule ruling.
Recommend the same current group for both, with revoker/time/reason kept.
No personal-ID pin is proposed.

Share the BFF actor checks, audit event format, CSRF, operation receipts,
expiry/restore semantics and race tests with F2. Keep the effects distinct:
one-off revoke prevents an unused action reservation; rule revoke prevents
future admissions and initiates the chosen issued-grant cleanup. Neither
can undo an action already performed. F2's one-off action-consumption
adapter is not required to build the rule-specific grant barrier, but the
shared audit and revoke-authority contract is a prerequisite to enabling it.

DESIGN-034 already calls its Needs attention work **F3**. The work order's
“F3 standing rules” label is tracking shorthand for #261, not a reassignment
of that existing lane. The build slices below use SR labels only as design
references. OpenProject remains the sole work queue.

## 8. Build stories and acceptance

These are proposed slices, not newly claimed stories. Search OpenProject
under Epic #254 before creating each one. Record scope, owner, paths,
prerequisites, budget, checks, artifacts and stop conditions before pickup.
Do not start a build from this docs work order. Each source slice should
fit one bounded session, about two hours with a 90-minute checkpoint,
concurrency one unless its isolation and combined budget are approved.
Token/API costs are Unknown. Split a slice that will not fit.

| Slice | Ready prerequisites / serial order | Source acceptance |
|---|---|---|
| SR1 — rule wire/store and shared audit | ADR-011/design reviewed; existing Ask backend; F2 audit/authority contract and Q-255-01/Q-261-01 | Immutable `standing_rule` type/spec/digest; bounded records/events; private actor projection; route caller checks; CAS/identical retry; one-off-to-rule refusal. Real API-server schema tests. No policy writer or active rule. |
| SR2 — broker admission/revoke protocol | SR1; Q-261-02 and date semantics settled | Durable per-rule CAS barrier, concurrency bound, immutable ruleRef and exact grant provenance; scope merge cannot bypass revoke or limits. No admission after confirmed revoke, issued-grant cleanup and no stale execution/reinstall. Qualify crash/failover/restore/epoch behavior and overlapping policies. Missing evidence fails closed. |
| SR3 — profile compiler and GitOps PR writer | SR1/SR2 contracts fixed; writer authority, safe publication and Q-261-04 settled | Only qualified kube/egress scopes compile; legacy requester fields empty; nested rule policy schema and old-broker refusal; exact consent/diff check; one PR per intent; no merge/apply rights. Installation read-back and drift refusal. Kube stays unsupported until immutable versioned roles, permission digests and guards qualify for existing bindings too. Separate custody for writer, broker and Flux. |
| SR4 — portal and agent tools | SR1; SR2/SR3 contracts fixed | Distinct approval copy, exact scope preview, nonblocking proposal/status, active-rule lookup, list/revoke/audit views and truthful pending/Unknown/cleanup states. CSRF/group removal/stale UID/forged actor tests. Mobile/desktop QA. No repeat approval for a matching active rule. |
| SR5 — trusted project/group resolvers | SR1–SR4; separately reviewed membership sources | Exact stable project/group identity, membership from trusted configuration, intersection semantics, deletion/recreation/changed-profile refusal and isolated scope tests. Reject activation until installed. Not required to accept profile-only SR4. |
| SR6 — GitOps installation | Required source CI/review, signed images, schema and rollback qualification, owner decisions and runtime authorization | Separate haynes-ops PRs install CRDs/guards and narrow rights before writers; install broker enforcement before any managed policy. Default-off flags enabled only in authorized steps. Writer cannot merge, broker cannot write policy, agents cannot apply cluster configuration. Preserve all running Sessions. |
| SR7 — usable acceptance | SR6 and separately authorized disposable fixtures | Tom approves a synthetic rule once from the phone; two distinct matching grants succeed without another question; wrong who/target/limits are refused. Revoke while racing a grant; no admission after commit; issued grants show confirmed/Unknown cleanup honestly. Late PR apply, stale cache, restart, response loss and restored records cannot reactivate. Retain exact evidence. |

SR2 and SR3 need explicit smaller protocol/schema stories if their contracts
exceed one session. Build them serially where they touch the same schema or
broker state. SR4 may develop against the fixed contract in isolation;
integration waits for its dependencies. Follow-on pod exec, credential or
external-action adapters each need their own story and owner scope.

Use fake clocks and deterministic interleavings for races. No CPU burners,
stress, wide test loops or local image builds. At most `DEV_ENV_CPU_LIMIT`
test workers, capped at two for Go, one suite at a time. Cluster fixtures
need explicit authorization and CPU limits. Never use real credentials,
live mail or valuable Sessions as fixtures.

| Evidence level | Required claim |
|---|---|
| Source | Reviewed code, schema, generated CRDs, meaningful tests and required `CI - Success` on the exact head. Passing source tests does not install rules. |
| Published | Signed images/artifacts and their source commits/digests. Publication does not prove rollout. |
| Installed | Reviewed haynes-ops merge, applied schema/guards/RBAC, effective identities/flags, running image digests and refusal checks. No usable claim from a pin alone. |
| Usable / accepted | SR7's phone approval, repeated matching use, denial, revoke race and issued-grant outcome evidence, accepted by the authorized owner. Record limitations explicitly. |

This docs story ends at one ready PR with CI started. The lead owns its
independent review, final required CI result and merge. This worker neither
installs nor accepts the feature and does not start the build slices.

## 9. Open questions, not rulings

**Q-261-NN records are recommendations only. They have not been delivered.**
This work order explicitly leaves delivery to the lead. Do not treat a
recommended option as consent. Q-255-01 remains the canonical pending
one-off revocation question; the relationship is in §7.

| ID | Question / recommendation | Alternative and gate |
|---|---|---|
| Q-261-01 | May the same current portal group approve standing rules, with an explicitly configured rule-admin capability? Recommend yes, using fresh group checks and the exact approved resource scope. | A separately managed policy-admin group. Settle before enabling standing-rule approval. Revoke authority also needs explicit coverage under Q-255-01 or a distinct ruling. |
| Q-261-02 | Should revoking a rule also release every grant already issued under it? Recommend yes, with cleanup progress and existing-stream/copied-credential limits shown. | Stop new admissions and let existing grants reach TTL. Settle before enabling revoke; no silent default. |
| Q-261-03 | Should review dates be reminders, with only an explicit expiry ending permission? Recommend yes; default review after 90 days, no repeated per-use question. | Make review due a hard stop. Settle date/copy semantics before enabling any rule; expiry never auto-renews. |
| Q-261-04 | May a separate authorized delivery merger merge the exact consent-bound GitOps PR after required checks, without another approval prompt? Recommend yes for policy-only diffs, after that service's narrow authority and gates are reviewed. The PR writer remains unable to merge. | Tom merges each policy PR. Gate the writer/merge integration; this design and the portal answer grant no GitHub or cluster write authority. |

No question here changes Tom's Q-255-04 ruling or authorizes a runtime
change. No question is sent from this docs session.
