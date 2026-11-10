# ADR-021: Grants that need Tom travel through the Asks board

- **Status:** Proposed for coordinator review. This ADR installs nothing,
  changes no live policy and approves no action. It is not accepted until the
  coordinator accepts it. No code merges before that.
- **Date:** 2026-11-10
- **Decider:** Tom Haynes, for the rulings quoted below. The mechanism that
  carries those rulings is proposed here for the coordinator to accept or
  send back.
- **Amends:** [ADR-010](010-agent-asks-and-approvals-board.md), the four
  sentences named below, and [DESIGN-034](../designs/034-agent-asks-and-approvals-board.md),
  the sentences named below and marked in that design. Approval Asks and
  info Asks keep the rules those sentences state.
- **Supersedes:** nothing whole. The 30-minute unanswered deny and the
  approval-page wording stay in force for every grant this path does not
  take, and for every deployment where the flag below is off.
- **Retains:** the Ask as the human record; set-once answer; portal-group
  admission; session redaction of issuer and subject; `AskScopeMaxTTL` for
  approval Asks; the broker as the only writer of AccessGrant status;
  standing GrantPolicies; delegate and maintenance decisions; attested
  policies.
- **Design:** [DESIGN-060](../designs/060-meatball-front-door.md) section 8
  (the adapter's contract) and section 10 row 2;
  [DESIGN-034](../designs/034-agent-asks-and-approvals-board.md) as amended
  with this ADR.
- **Tracking:** OpenProject [#525](https://work.haynesnetwork.com/work_packages/525)
  (parent #516). The Asks board this uses is OpenProject #264, live
  2026-11-10.

## Context

A grant that a GrantPolicy decides is approved in about a second. A grant
that no policy decides waits 30 minutes and is denied, and nothing tells
Tom. The broker's production notifier only logs
(`cmd/dev-env-operator/broker.go` lines 164-166,
`internal/broker/seams.go` lines 31-37). `--grant-approval-url` is the base
URL of an approval page (`cmd/dev-env-operator/main.go` line 230,
`internal/apiserver/server.go` lines 199-201). That page was never built.
`Decide` has no human HTTP caller. The operator API writes AccessGrant
specs only and has no status write (`internal/apiserver/grants.go` lines
28-33).

The Asks board is the other human path. A session creates an `info` or
`approval` Ask. Tom answers on the portal. The keeper can push "An agent
needs your answer". An approval Ask is a record. It grants no machine
action (ADR-010; DESIGN-034 section 7).

DESIGN-060 section 8 chose the board for grants that need Tom, and left
the adapter's contract to this ADR. Tom, 2026-11-10, by phone prompt in
coordinator session `dev-env-fable-coordinator-1109`: "Yes, use this list
(Recommended)" for which access is risky, and, asked how long a request
should wait: "I should always respond if I didn't I missed it in a way I
cannot get back to it. The UI should be my failsafe at missed requests but
it's super rare I'd say no to be honest, this is a home lab". The same day
he had said there needs to be a way he can handle approvals for risky
access. This ADR did not see those answers at their source. DESIGN-060
section 8 and section 11 quote them, and the work order for #525 repeats
the two sentences above.

## Decision

A grant that needs Tom travels through the Asks board, as DESIGN-060
section 8 chose. One broker flag, `--enable-grant-asks`, default off,
gates every runtime change below. Flag off, the broker's pending path is
unchanged: log, then deny at 30 minutes.

### 1. A third Ask kind, `grant`

`grant` joins `info` and `approval`. The operator's broker creates it, and
only the broker. `POST /v1/asks` stays a session route
(`internal/apiserver/asks.go` lines 73-76 and 87-100) and refuses
`type=grant` with 400, flag on or off. A session has no tool that creates
one. The MCP `ask` tool stays `info` or `approval`.

The broker creates the Ask from `Broker.pending`, after no GrantPolicy has approved
the grant and no delegate decision applies (`internal/broker/broker.go`
lines 268-289), and only when the flag is on. The Ask's context names the
requesting session. A label
`coordination.dev-env.haynesops.com/ask-origin=broker` marks it. The label
is not proof by itself: any principal that can create an Ask could set it.
The broker decides an answer only when all of these hold: the label is
`broker`; `requestId` is `grant-` plus `grant.uid`; `context.sessionUID`
equals the grant's requester session UID; and the live UID, digest, type,
target and TTL match section 2. `POST /v1/asks` refuses `type=grant` and
strips `ask-origin` from every session create, so a session cannot set the
label. Sessions have no direct Ask CRD rights (DESIGN-034 section 4). The
broker's ServiceAccount is the principal that creates an Ask with that
label, by writing the object, not by calling `POST /v1/asks`. A cluster
admin can still write one. That is the same trust as a cluster admin
writing AccessGrant status, which the broker already has to live with. The
request-id and session-UID checks stop a label copied onto some other
session's Ask from deciding this grant.

The scope is typed, on the Ask, not on the AccessGrant. AccessGrant spec
is unchanged. The fields are:

| Field | Meaning and bound |
|---|---|
| `grant.uid` | The AccessGrant's `metadata.uid`. The Ask field uses the bound `grantattest` already uses for a grant UID: at most 64 characters, `^[A-Za-z0-9]([A-Za-z0-9._:-]*[A-Za-z0-9])?$` (`internal/grantattest/reserved_decision.go` lines 85 and 1057-1058). The requester session UID is a stricter UUID (`api/v1alpha1/accessgrant_types.go` lines 239-244). This field is the grant object's UID, not that session UID. |
| `grant.scopeDigest` | `grantattest.ScopeDigest` of that object (`internal/grantattest/attest.go` lines 157-165). 43 base64url characters, `^[A-Za-z0-9_-]{43}$` (`internal/grantattest/reserved_decision.go` line 88). |
| `grant.type` | The grant's type: `kube`, `egress`, `credential`, `breakglass` or `maintenance`. |
| `grant.target` | One token. Kube and break-glass: `spec.kube.role`. Credential: `spec.credential.name`. Egress: `egress`. Maintenance: `maintenance`. 1 to 200 runes, the Ask text rules. |
| `grant.ttl` | The TTL the grant asked, equal to `spec.ttl`. The broker checks the duration; the CRD stores it as a duration string of at most 16 characters (`10m` through `8h`, the grant bounds in `internal/broker/checks.go` lines 17-22). |

`grant.detail` is optional display, 0 to 200 runes: comma-joined
namespaces, or `cluster` when a kube grant names none, or a single
destination summary for egress. It is not part of the binding. A mismatch
of detail does not refuse the grant. The digest does.

Approval `scope.action`, `scope.target` and `scope.expiresAt` stay as they
are and are absent on a grant Ask. Info Asks still have no scope and no
`grant` object.

The card's four parts come from these fields and from `spec.reason`, which
already exists (`api/v1alpha1/accessgrant_types.go` lines 214-220). The
agent writes only the why. The broker copies `spec.reason` into the Ask
`question` after the Ask text normalizer, at most 1000 runes. If that copy
fails the Ask text rules, the question is the fixed sentence "The session
asked for access. The reason could not be shown on the board." The grant
is still opened. The portal, for `type=grant` only, shows:

- **What:** type, target, and detail when set.
- **Why:** the question.
- **How long:** `grant.ttl`, on the button "Approve for <ttl>".
- **If you say no:** the fixed sentence "If you say no, this grant is
  denied and nothing is installed." It is portal text, not a field the
  agent can set.

The first slice has one approve button. It approves for the TTL asked.
`Decide` already treats a zero TTL as `spec.ttl` (`internal/broker/seams.go`
lines 222-225) and refuses a longer one (lines 227-228). The adapter passes
TTL 0. A shorter-TTL control is not in this slice.

The broker fills `context.workPackage` only when the requesting session has
exactly one distinct OpenProject id among its TaskClaims. A claim counts
when its source label
`coordination.dev-env.haynesops.com/source` equals
`taskclaim.SessionLabel` for that session's namespace, name and UID
(`internal/taskclaim/store.go` lines 33 and 62-63) and its `taskKey` is
`issue:<positive decimal>` (`api/coordination/v1alpha1/taskclaim_types.go`
lines 41-43). Several claims with the same id are one story. Zero claims,
more than one id, or a list that fails: the field is omitted and the Ask
is still created. The portal already links
`https://work.haynesnetwork.com/work_packages/<id>` when the field is set
(`web/portal/asks.go` lines 144-146). There is no work-package field on
AccessGrant or AgentSession. This ADR does not add one.

The create is idempotent. The request id is `grant-` plus the grant UID,
hyphens kept. The existing request-id rule is 8 to 64 characters from that
alphabet (`api/coordination/v1alpha1/ask_types.go` lines 68-71). A Kubernetes
object UID is a UUID, 36 characters, so the id is 42 and fits. A retry
returns the same Ask. A UID that does not fit that rule does not open an
Ask and does not deny the grant; the broker requeues. I have not seen a
grant UID in this repo that is not a UUID.

### 2. One use, then Decide

Tom answers on the existing portal route
`POST /v1/portal/asks/{id}/answer`. The portal reader is the only caller,
as today (`internal/apiserver/asks.go` lines 80-84). The answer is the
Ask's set-once write. `approved` is required, the same boolean an approval
Ask uses. Options are absent. A note is optional. An identical retry by
the same issuer and subject returns the stored answer and does not write
again.

The portal does not call `Decide`. The broker does, in process, when it
sees that Ask answered. `Decide` runs only when the live grant's UID equals
`grant.uid` and `grantattest.ScopeDigest` of the live object equals
`grant.scopeDigest`, the type and target match, and `spec.ttl` equals
`grant.ttl`. `Decision.By` is `authentik/<username>` as defined below.
`Decision.TTL` is 0.

That call is one use. `Decide` refuses a grant that is not pending
(`ErrNotPending`, `internal/broker/seams.go` lines 77-79 and 175-180). A
second call finds the grant already decided and returns `ErrNotPending`.
The broker does not create another grant and does not open another Ask.

**Username.** `By` is `authentik/` plus the `preferred_username` claim on
the UserInfo response the portal already fetches for the group check
(`web/portal/session/authorization.go` lines 95-124). That function keeps
`sub`, `portal_is_active` and `portal_is_admin`, and discards every other
field (lines 161-163). The portal starts retaining `preferred_username`
from that same body. It is not a new request and not a new token. The
portal puts it on the answer actor as `username`. Session-facing Ask
responses omit it, as they omit issuer and subject. The browser cannot
supply it. The pattern is `^[A-Za-z0-9][A-Za-z0-9._@+-]{0,62}$`, so a name
Authentik accepts, `@` and `+` included, still fits. A slash does not,
so the value cannot start a `policy/`, `human/` or `delegate/` prefix.
`authentik/<username>` therefore fits `approvedBy`'s max length of 253
(`api/v1alpha1/accessgrant_types.go` lines 436-440). Display name is not
the username. ADR-010 says the display name is presentation, not an
identity proof.

The broker copies that stored username. It does not accept a username from
a session. The trust is the one ADR-010 already gives the portal reader:
the operator stores the actor, and only that caller can write it. If the
claim is missing or fails the pattern, the portal does not submit the
answer, the Ask stays open, and the grant stays pending. If an answered
grant Ask has no usable username, the broker denies the grant
(`deniedBy` `broker`) and stops. It does not invent a name.

**When the answer is stored and the grant is not yet decided.** The answer
stays. The broker acts on the pre-check and on `Decide`:

| Result | What the broker does |
|---|---|
| Success | The grant leaves Pending. Stop. |
| A conflict or a read error, and the grant is still Pending, UID and digest still match | Retry on the next reconcile. |
| `ErrNotPending` | Stop. The grant has already ended, been released, or been deleted. Do not create a grant. |
| Pre-check mismatch, before `Decide` | The broker compares UID, digest, type, target and TTL to the Ask and does not call `Decide` unless they match. If the live UID matches and the grant is still Pending but the digest, type, target or TTL does not, it denies that grant (`deniedBy` `broker`, message that the scope no longer matches) and stops. If the UID does not match, it does not call `Decide` on the object that now has the name, and it stops. It never opens a replacement grant or a replacement Ask. |
| Any other `ErrRefused` | Deny the pending grant with the broker's reason and stop. A yes that the broker cannot apply does not sit pending forever, and it does not grant. |

`Decide` re-checks the same binding. Behind the flag, `decisionAllowed`
re-reads the Ask and returns `ErrRefused` when the label, request id,
session UID, UID, digest, type, target or TTL no longer match. A
`Decision` that carries only `By` does not pass that re-check. PR 5 of the
build plan needs this for credential, maintenance and reserved grants,
whose execution also re-reads the Ask. The pre-check and the re-check can
both see a mismatch if the object changes between them. Either one ends
the grant refused and does not create another.

A changed UID or digest ends refused. It never becomes a new grant.

`Decide` as it stands today refuses some of the risky types even for
`authentik/tom`. Those refusals are broker code, not AccessGrant spec, and
this path extends them behind the flag without a caller-supplied `By`
being enough:

- **Credential.** `decisionAllowed` refuses every approval
  (`internal/broker/seams.go` lines 209-211). Execution accepts only
  `approvedBy` of `policy/<name>` (`internal/broker/credentials.go` lines
  70-76 and 140-148). The pinned tests say a caller-supplied `By` stays
  refused, and that a future path stays default-off
  (`internal/broker/manual_credential_approval_test.go` lines 75-80).
  Behind the flag, `Decide` accepts an approval only when the broker has
  just read a matching grant Ask. A `Decision` with only `By` stays
  refused, so those tests stay as they are. Execution creates the
  credential job for that approval without looking up a policy, and only
  while the same Ask still binds the live UID and digest. No Ask, no job.
- **Maintenance.** `Decide` accepts a human approval only from
  `human/<namespace>/<serviceaccount>` plus `MaintenanceDigest`
  (`internal/broker/seams.go` lines 197-204,
  `internal/broker/maintenance_decision.go` lines 86-91). Once Active, a
  non-policy approval is not re-checked by the policy or delegate holds
  (`internal/broker/maintenance_attestation.go` lines 246-249,
  `internal/broker/delegates.go` lines 388-390). Behind the flag, the same
  Ask binding may approve a maintenance grant when `EnableMaintenanceGrants`
  is already on. The human ServiceAccount route is unchanged. The binding
  uses `grantattest.ScopeDigest`, which covers the maintenance envelope
  (`internal/grantattest/attest.go` lines 157-165), not a second digest.
- **Reserved kube** (`spec.kube.reserved`, `allNamespaces`, or
  `storageTargets`). `Decide` refuses it without a reserved delegate
  decision (`internal/broker/seams.go` lines 216-218). If status were
  forced Active anyway, the next reconcile fails the grant
  (`internal/broker/broker.go` lines 343-344). Behind the flag, an Ask
  binding may approve it, and that reconcile does not fail it while the
  Ask still binds. A delegate decision is unchanged.

Kube, egress and break-glass already accept `authentik/<username>` through
`Decide` (`internal/broker/envtest_test.go` uses `authentik/tom`). They
need no new acceptance rule. They still pass `check`
(`internal/broker/checks.go`).

The broker re-reads the Ask at execution for credential, maintenance and
reserved grants. If the Ask is gone or no longer binds, execution fails
closed the way a missing policy does. Info and approval Asks never enter
`Decide`.

### 3. Two clocks

The question and the access do not share a clock.

**The question.** A grant Ask has no `spec.expiresAt` and no
`scope.expiresAt`. `AskScopeMaxTTL` (24 hours, `internal/apiserver/apiv1/asks.go`
lines 129-132) stays the cap for approval Asks, together with the rule
that an approval Ask expires no later than its scope. `EffectiveState`,
`ValidateForAt` and the expiry sweeper (`internal/asks/sweeper.go` lines
99-109, `internal/apiserver/apiv1/asks.go` lines 311-338 and 470-494) do
not treat a grant Ask as expired because a clock passed. An answer is
accepted while the Ask is open, with no `now < expiresAt` check.
`ApprovalValid` stays false for a grant Ask. It is not a standing
approval. The session learns the access from the grant's phase, not from
`ApprovalValid`.

**The access.** The grant's own TTL, `spec.ttl`, is how long the access
lasts after approval. The broker already writes `status.expiresAt` and
ends an active grant there (`internal/broker/broker.go` lines 310-318 and
323-329). This ADR does not lengthen it. The minimum stays 10 minutes
(`MinTTL`).

**The 30-minute wait is not used for a grant this flag has selected for
Tom.** Today `pending` denies at `PendingTimeout` (30 minutes,
`internal/broker/broker.go` lines 67-69 and 244-247) and `decisionAllowed`
refuses `Decide` after the same wait (`internal/broker/seams.go` lines
181-182). With the flag on, both checks are skipped for a grant that no
policy and no delegate decision took, from the moment the broker selects
it, including the retries before the Ask exists. A failed Ask create does
not become a deny. The grant stays Pending and the broker retries.

The grant then ends only when Tom's answer is applied, when the session
releases it (`spec.release`, which `pending` already ends,
`internal/broker/broker.go` lines 241-242), or when the session is gone
(lines 210-214: an active grant is released, a pending one is denied,
message "the session ended").

These 30-minute checks stay as they are. They are not Tom's wait:

- an attestation is fresh only inside `PendingTimeout`
  (`internal/broker/attestation.go` lines 73-74);
- a delegate decision is fresh only inside it
  (`internal/broker/delegates.go` lines 173-174 and 264);
- the maintenance HTTP decision route refuses a grant older than it
  (`internal/apiserver/maintenance.go` line 54).

Flag off, the 30-minute deny and the `Decide` age check are unchanged for
every grant.

### 4. How many may be open

Grant Asks do not expire, so they do not use the 16 open slots an info or
approval Ask uses (`internal/asks/store.go` lines 30-37). They are counted
apart from those 16, and they still count toward the existing 512 total
per requester and 4096 per namespace. Closed records still count toward
those two, as DESIGN-034 section 2 says.

| Bound | Number | Why this number |
|---|---|---|
| Open grant Asks for one session | 3 | A session may have at most 3 grants waiting (`maxPendingGrants`, `internal/apiserver/grants.go` lines 39-41). One Ask per waiting grant. The fourth grant is already refused by that API, so a fourth Ask would have nothing to bind. |
| Open grant Asks in the namespace | 48 | 16, the existing open-Ask cap per requester, times 3, the pending-grant cap. Sixteen sessions can each hold the per-session maximum. 48 is 48/4096 of the namespace cap. |
| Total per requester, and per namespace | 512 and 4096, unchanged | A grant Ask is still an Ask. The lifetime caps stay the backstop. |

The count is an uncached list, the same pattern as the existing open
count, using the type label and the state label. An identical retry needs
no new slot. At the cap, create returns the existing `429 limit_exceeded`.
An unconfirmed count returns `503`. The broker then does not deny the
grant. It requeues and emits an event that the board is full. Ended-session
cards stay open until Tom dismisses them, and they count toward both caps.
That is what the caps are for.

### 5. When the asking session has ended

The grant ends with the session, as `pending`'s caller already does
(`internal/broker/broker.go` lines 210-214). `Decide` is not run: the
grant is not pending, and there is nothing to grant to.

The Ask stays open. It is not withdrawn and not expired. The board shows
the request (what, why, how long, and the fixed no-sentence), the session
name, the sentence "The session that asked has ended", the story link when
`context.workPackage` is set, and a Dismiss button. It does not show
Approve or Decline. Dismiss calls a new portal-reader route,
`POST /v1/portal/asks/{id}/dismiss`, which sets the Ask `withdrawn` and
does not call `Decide`. It is allowed only for a broker-origin grant Ask
whose session is gone or deleting, or whose grant has already ended.
Dismiss of a grant that is still pending for a live session is `409`.
That session releases the grant through the grant API; the broker then
withdraws the Ask. The session withdraw route refuses `type=grant`, so a
session cannot drop the card and leave the grant waiting.

Nothing on that card starts a session, a worker, or a grant. Starting the
step again is a coordinator start on the story, which this ADR does not
build.

The open list puts open grant Asks first, oldest first, and then other
open Asks newest first as DESIGN-034 section 3 already requires. A new
question does not push a grant Ask off the top of the board. The all-state
list stays newest first.

### 6. What this amends, and what is deleted when it ships

**ADR-010.** These sentences stay in force for `info` and `approval`, and
are amended as follows for `grant`:

| Sentence in ADR-010 | Amendment |
|---|---|
| "Make an **Ask** a first-class operator record for information or approval." | An Ask is also the record of a grant that needs Tom. The broker opens that kind; a session does not. |
| "An approval requires an explicit `approved: true` or `false` and exact scope. **There is no revocation in v1.** Its scope expires no later than 24 hours after Ask creation, and the Ask's answer deadline cannot exceed that scope expiry." | Unchanged for `approval`. A `grant` Ask has no scope expiry and no answer deadline. Its `approved` boolean is consumed by one `Decide`. It is not a standing approval, so the 24-hour cap does not apply to it. There is still no revocation of an approval Ask. |
| "An information answer is never approval. A saved approval grants no credential or proof of execution." | Unchanged for `info` and `approval`. A saved grant answer is the human decision the broker feeds to `Decide`. The Ask itself still grants nothing. The grant's phase is the access. `Decide` failing leaves the answer stored and the grant ungranted until section 2's rule ends it. |
| "Grant, budget and native-decision adapters need separate reviewed stories." | This ADR is the grant adapter's story (#525). Budget and native-decision adapters remain separate stories. |

**DESIGN-034.** The sentences and the amendment notes are marked in that
design with "Amended 2026-11-10 (ADR-021)". They are the type sentence
"Exactly `info` or `approval`"; the scope sentence that a scope is
required for approval and absent for info, with the 24-hour cap; the
expiry sentence that create uses `expiresIn` and that an approval expires
no later than its scope; the answer-time sentence `now < expiresAt`; the
admission sentence "16 open per requester, 512 total per requester and
4096 per namespace"; the caller sentence that a session creates as itself;
the route sentences for `POST /v1/asks` and for withdraw with no portal
withdraw; the section 7 sentences that an approval scope is capped at 24
hours and that the record grants no machine action; the section 8 card
sentence; the section 9 sweeper sentence that `EffectiveState` governs
expiry; and the section 12 sentence that each grant adapter needs its own
story. Q-255-04 is unchanged: an approval Ask is still a one-off inside
its expiry. This path does not spend an approval Ask.

**Deleted when the flag-on path has landed in source, in the last PR of
the build plan.** Not before, and not by this ADR.

- `LogNotifier` as the production notifier. The broker command wires it at
  `cmd/dev-env-operator/broker.go` lines 164-166. The type
  (`internal/broker/seams.go` lines 31-37) may remain in tests. The
  production command stops constructing it. Flag off, a grant that needs
  Tom is still denied at 30 minutes and still notifies nobody, which is
  what that log line does today. Flag on, the Ask is the notice, and the
  keeper's existing Ask push is the phone push. No new push credential.
- `--grant-approval-url` (`cmd/dev-env-operator/main.go` line 230) and the
  view field it fills (`internal/apiserver/grants.go` lines 663-664,
  `internal/apiserver/apiv1/grants.go` lines 177-179).
- Texts that point at an approval page that does not exist:
  `internal/apiserver/grants.go` line 262 ("Tom reads it on the approval
  page"); `internal/agentrun/mcp.go` lines 361 and 392;
  `internal/agentrun/grant.go` line 248; `internal/agentrun/help.go` lines
  920-922 and 997-998; the CRD comments generated from
  `api/v1alpha1/accessgrant_types.go` lines 107-113, 214-215 and 436-437;
  `internal/broker/seams.go` lines 20-22 and 49 ("from the approval page");
  `internal/broker/broker.go` lines 16-17 and 28; `docs/repo-layout.md` the
  broker line that says "Decide for the approval page". The same PR cuts
  the `request_access` tool description and `agent-run help grant` to
  about 40 words each, as #525 step 3. Wording that a human approves, and
  that a policy may approve first, stays. Wording that a page exists does
  not.

`approvedBy: chat/Tom` and `--grant-legacy-delegates` are DESIGN-060's
delete list. They are not this adapter. This ADR does not remove them.

## Build plan

All of this is behind `--enable-grant-asks`, default off, on the broker
command. The API and the portal ship the new kind in a form that does
nothing until the broker creates an Ask. Session create of `type=grant` is
refused even with the flag off. haynes-ops does not change in these PRs:
the coordinator pins the images, copies the Ask CRD before the operator
that serves it, and turns the flag on. The broker ServiceAccount will need
get, list, watch, create and status-update on Asks in `dev-agents`, and
get and list on TaskClaims if the story link is to work. That RBAC is not
a portal credential. The portal keeps the portal-reader token it already
uses for `POST /v1/portal/asks/{id}/answer`.

The PRs merge in this order. Each is releasable with the flag off.

1. **Wire and CRD.** `api/coordination/v1alpha1/ask_types.go`,
   `internal/apiserver/apiv1/asks.go`, generated `config/crd/` and
   `zz_generated.deepcopy.go`. This PR adds `answer.answeredBy.username`
   on the coordination Ask and on the wire actor, optional, pattern
   `^[A-Za-z0-9][A-Za-z0-9._@+-]{0,62}$`, omitted from session responses.
   A grant answer requires it; an info or approval answer omits it. Tests
   in `internal/apiserver/apiv1/asks_test.go` and the coordination
   validation suite: a grant Ask with the typed scope and no expiry is
   accepted; an approval Ask past 24 hours is still refused; an info Ask
   with a `grant` object is refused; a grant Ask with an expiry or an
   approval scope is refused; `ValidateForAt` accepts a grant answer with
   `approved` and a username that matches the pattern, and no clock check;
   a grant answer with a slash in the username, or with no username, is
   refused; an approval answer that carries `username` is refused; an
   approval still expires. The coordinator copies this Ask CRD into
   haynes-ops after this PR merges and before PR 4 or PR 6 is pinned.
   Either of those images stores `username`. An API server whose CRD has
   no such field drops it, and section 2 would then deny the grant.
2. **Store, sweeper, session create.** `internal/asks/store.go`,
   `internal/asks/sweeper.go`, `internal/apiserver/asks.go`. Envtest in
   `internal/asks/envtest_test.go`: the fourth open grant Ask for one
   session is 429; the 49th in the namespace is 429; a grant Ask does not
   consume one of the 16 info slots; the sweeper does not expire a grant
   Ask and still expires an info Ask; `POST /v1/asks` with `type=grant`
   is 400; a session create that sends `ask-origin=broker` stores no such
   label.
3. **Broker opens the Ask and holds the 30-minute deny.**
   `internal/broker/broker.go`, `cmd/dev-env-operator/broker.go` for the
   flag only. A broker unit test with a fake clock: flag off, denied at
   30 minutes and no Ask; flag on, still pending after 30 minutes with one
   Ask, and a second reconcile does not open a second Ask; a matching
   policy approves and opens no Ask; `spec.release` releases; a missing
   session denies. No envtest in this PR if the unit client can hold the
   Ask. If it cannot, one envtest in this package, alone.
4. **Broker applies the answer.** `internal/broker/seams.go` and a new
   `internal/broker/grant_ask.go`. Unit tests: one answer grants once and
   the repeat is `ErrNotPending`; a digest, type, target or TTL mismatch
   denies the pending grant and does not call `Decide`; a UID change does
   not call `Decide` on the replacement; a conflict retries; a transport
   error retries; another `ErrRefused` denies. `Decision.By` is
   `authentik/` plus the stored username. The test shows the pre-check
   outcome, not an `ErrRefused` the broker pretended `Decide` returned.
5. **Credential, maintenance and reserved execution.**
   `internal/broker/seams.go`, `internal/broker/credentials.go`,
   `internal/broker/broker.go` lines 343-344. The existing
   `TestManualCredentialApprovalIsRefusedToday` and
   `TestNonPolicyCredentialApprovalIsRefusedAtExecutionToday` stay green
   with the flag off and with no Ask. New tests, flag on, with a matching
   Ask: a credential approval creates one job; the same `By` without the
   Ask creates none; `decisionAllowed` re-reads the Ask and returns
   `ErrRefused` when the binding does not match; maintenance and reserved
   scope stay Active only while the Ask binds, and fail closed when it
   does not.
6. **Portal card, username, Dismiss.** `web/portal/session/authorization.go`,
   `web/portal/session/asks.go`, `web/portal/asks.go`,
   `internal/apiserver/portal_asks.go`. A portal test renders the four
   parts, Approve and Decline while the session exists, and, when the
   session is gone, the story link, Dismiss, and neither Approve nor
   Decline. A UserInfo body without `preferred_username` does not submit.
   A session response omits `username`. Dismiss does not appear as a
   `Decide` call.
7. **Texts and the production notifier.** The files in section 6. Tests:
   the `request_access` description and `grant` help are at most about 40
   words and do not contain "approval page"; `--grant-approval-url` is
   rejected by the parser as an unknown flag; the broker command does not
   construct `LogNotifier`; flag off, a pending grant is still denied at
   30 minutes.

No PR in this list turns the flag on in the cluster. No PR edits
GrantPolicies or haynes-ops.

## Alternatives considered

| Alternative | Why it loses |
|---|---|
| The portal calls `Decide` on the broker | `Decide` is an in-process method. The portal's only operator credential is the portal-reader token to the existing answer route. A broker HTTP client would be a new portal credential. The story stops rather than add one. |
| The operator API writes AccessGrant status | The API server has no status write (`internal/apiserver/grants.go` lines 28-33). The broker is the status writer. Splitting that would let a second process approve grants. |
| Put the OIDC subject in `approvedBy` | The grant view returns `approvedBy` to the session (`internal/apiserver/grants.go` lines 580-581). The subject is an identifier the session-facing Ask API is required to hide. `preferred_username` is a name already on the UserInfo body the portal fetches and throws away. |
| A new UserInfo scope or a new token, if the username claim is absent | That is a new credential or a new identity permission. If the live userinfo has no `preferred_username`, the build stops and reports. It does not fetch a wider profile. |
| Let a caller-supplied `authentik/tom` approve a credential | The pinned tests refuse exactly that, and they say the future path is default-off. The Ask binding is the verified decision. The string alone is not. |
| Give grant Asks the 16 open slots, or a 24-hour expiry | They do not expire, so they would fill the question quota, or they would turn a missed request into a no, which Tom refused. The caps are 3 and 48, taken from the pending-grant cap and the open-Ask cap. |
| Change the whole board to oldest first | Info and approval stay newest first, as DESIGN-034 section 3 ships them. Open grant Asks are placed ahead of them so a missed grant stays visible. |
| An AccessGrant spec or status field pointing at the Ask | The Ask already points at the grant. A spec change is the story's stop. A status field would be an AccessGrant CRD change this story's reach does not include. The broker finds its Ask by `requestId`, which is `grant-` plus the grant UID, as section 1 defines. |
| Approve reserved scope only through the delegate | Tom's phone is the path DESIGN-060 chose for risky access, and reserved scope is in that class. The delegate path stays for delegates. The phone path is an additional verified human decision, flag off by default. |

## Consequences

| Id | Consequence |
|---|---|
| C-55 | Good: a grant no policy decides can reach Tom's phone and stay on the board until he answers. A missed request is no longer a deny at 30 minutes while the flag is on. |
| C-56 | Good: the portal gains no credential. The broker gains Ask read and write, applied later in haynes-ops by the coordinator. AccessGrant spec is unchanged. |
| C-57 | Bad: with the flag on, a grant that waits on Tom waits until he answers, the session releases it, or the session ends. A forgotten live session holds its grant and one of its three slots. The board is the reminder. |
| C-58 | Bad: `preferred_username` becomes part of `approvedBy`, which sessions can read. It is a username, not the OIDC subject. Issuer and subject stay off the session view. |
| C-59 | Bad: credential, maintenance and reserved grants become approvable by this path when the flag is on. The same strings without an Ask stay refused. A wrong Ask binding fails closed. |
| C-60 | Neutral: approval Asks keep the 24-hour cap and grant nothing by themselves. Info Asks are unchanged. Flag off, production behaviour is unchanged until the last PR removes the log notifier and the approval-page texts; after that PR, flag off still denies at 30 minutes and still does not tell Tom. |

## What I could not determine

- Whether the live portal userinfo includes `preferred_username`. The
  portal permits other profile fields and discards them
  (`web/portal/session/authorization.go` lines 161-163). The provider
  blueprint is not in this repo. DESIGN-029's other Authentik clients use
  a `user_uuid` subject and are explicitly not the portal provider. The
  portal issuer string is
  `https://authentik.haynesnetwork.com/application/o/dev-env-v2-portal/`
  (`web/portal/session/production.go` line 37). I did not call userinfo.
- Whether Tom's username matches the pattern above. I did not look it up.
- The broker's live RBAC. It is in haynes-ops, not in this repo. The build
  plan names the rights the coordinator adds. A missing TaskClaim list
  omits the story link and still opens the Ask.
- A single story id on the session or the grant. There is none. The link
  depends on exactly one `issue:<id>` among that session's TaskClaims.
- Whether every requesting session has a TaskClaim. A session with none
  gets a card without a story link.
