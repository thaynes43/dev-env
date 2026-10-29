# DESIGN-034: An Asks and approvals board for agents

- **Status:** Proposed; documentation only, 2026-10-29.
- **Decision:** [ADR-009](../adrs/009-agent-asks-and-approvals-board.md).
- **Tracking:** [Story #255](https://work.haynesnetwork.com/work_packages/255),
  [Epic #254](https://work.haynesnetwork.com/work_packages/254), Saga #85.
- **Source read:** `origin/main` at `18b55268ab6ec143abe1802a9020c3e4c284582a`.
- **Delivery boundary:** one ready design PR; the lead reviews and merges.
  No code or runtime action here. Pending choices remain for Tom's review.

The lead published the v1 build contract in [Epic #254](https://work.haynesnetwork.com/work_packages/254)
journal 2539 after this worker's dispatch. It defines the shared wire types
and existing lanes #256–#259. Sections below preserve that v1 surface and
name stronger follow-up requirements separately. Epic journal 2555 flags
the delivery, lifecycle and scope gaps for the lead. This document neither
authorizes nor pauses another lane. Proposed fields and safeguards require
wire/store review before implementation; they are not claims about v1 code.

## 1. Result and current seams

An agent posts a question and gets its ID and link at once. Tom opens the
Asks board, reads its context and answers. The answer waits in the agent's
inbox until that original Session reads and acknowledges it. The agent can
work on independent tasks meanwhile. A dependent action stays held.

This is a question thread with one final answer. A follow-up is another Ask
linked to the first. Ordinary conversation stays in the existing session
message and history views. The board does not add a second chat transport.

| Existing source | Contract to preserve or extend |
|---|---|
| [DESIGN-029, October 29 group ruling](029-portal-foundations.md#group-based-action-scopes-2026-10-29) | Current UserInfo for the request's principal admits the portal group. Send/history targets remain exact name/UID lists in both BFF and operator. The work order reports Tom's live acceptance; this docs task performs no runtime check. |
| [`server.go`](../../../../internal/apiserver/server.go) | Dedicated portal conversation and send routes; `/v1/inbox` and `/ack`; task budgets; child decision and own decision-authority routes. Portal reader has a narrow route allowlist. New routes need explicit caller admission. |
| [`inbox_types.go`](../../../../api/coordination/v1alpha1/inbox_types.go), [DESIGN-014](014-durable-offline-inbox.md) | Immutable messages, original Session senders, recipient UID fences and explicit read ACKs. A portal answer cannot use today's sender schema unchanged. Projectless Sessions also need an explicit operator-origin delivery scope. |
| [DESIGN-010](010-atomic-task-claims.md), [DESIGN-024](024-coordination-record-retention.md) | Protected coordination group, uncached checks, deterministic identity, bounded records, archive before deletion. A claim or ACK grants no new authority. |
| [`decisions.go`](../../../../internal/apiserver/decisions.go), [`grant_attestation.go`](../../../../internal/apiserver/grant_attestation.go) | Exact child decision reservation/confirmation is not generic human authority. A grant attestation is explicitly an agent claim; the broker still decides. |
| [`mcp.go`](../../../../internal/agentrun/mcp.go) | `agent-run mcp` is a stdio server. Its three access tools request, inspect and release grants. It has no Ask or inbox-consumption tool today. |
| [Portal runtime](../../../../docs/portal-session-runtime.md), [`commands.go`](../../../../web/portal/session/commands.go), [`authorization.go`](../../../../web/portal/session/authorization.go) | Same-origin forms, CSRF, opaque encrypted browser sessions, fresh UserInfo and request-private session binding. Send's opaque `actorRef` is insufficient to name a human approver. |
| [DESIGN-031](031-pages-lookup.md), [`notifier.go`](../../../../internal/keeper/notifier.go), [`internal/pages`](../../../../internal/pages/pages.go) | Keeper notification sender and bounded page record. Sent means Pushover accepted a push, not phone delivery or human reading. The broadly readable page ConfigMap cannot hold Ask text or approval identity. |
| [DESIGN-033](033-pages-controls.md) | Desired state and applied state are distinct. A saved answer, a valid approval and a completed action need distinct evidence here too. |

## 2. Ask record and bounds

Add a namespaced `Ask` in `coordination.dev-env.haynesops.com`, stored in the
`dev-agents` namespace, as journal 2539 specifies. The separate API group,
not a broadly readable ConfigMap, protects its content. No Session owner
reference: a reap must not erase it.
The immutable spec is the request. Only the operator writes its bounded
status through compare-and-swap. Workers receive no direct CRD rights.
Treat audience and requester provenance not present in the v1 wire type
as server-held store metadata, not caller-controlled extension fields.

| Field | Meaning and proposed bound |
|---|---|
| Version, ID and key | Version 1 contract. ID is server-generated `ask-` plus 16 lowercase hex digits. Creation carries a stable `requestId`, unique per original requester UID. Atomically bind that key to one ID and the full immutable intent; collision must not overwrite an Ask. A new request ID is a new question. |
| Type | Exactly `info` or `approval`. |
| Audience | Server-selected portal project/environment, initially `dev-env/internal`. A project link is separate from this answer audience. An agent cannot select a broader audience. |
| Question | UTF-8 plaintext, 1–1000 runes. |
| Options | 0–6 choices. Each has a unique ID matching `^[a-z0-9-]{1,24}$`, a label of 1–80 runes and a description of 0–300 runes. `allowFreeText` states whether an info answer may use text. |
| Recommendation | `recommended` is an existing option ID or empty. The option description gives its reason. Never preselect approval in the form. |
| Context | Server-filled requester Session name/UID, optional OpenProject `workPackage` ID, and 0–5 HTTPS links of at most 300 characters. Validate links as below. A PR is a validated GitHub link; exact SHA and limits belong in approval scope. Typed references are a possible later schema improvement. |
| Requester | Namespace, Session name and UID, creating Pod UID, authenticated service actor, provider and creation time derived by the operator. DNS/UID bounds follow the existing Session identity types. Link the work item and claim when available; never acquire ownership by asking. |
| Scope | Required for approval; absent for info. V1: `action` and `target`, each 1–200 runes, and `expiresAt` in RFC 3339, no more than 7 days ahead. These descriptions must name exact action, target, material limits and one-time scope. They are record-only; section 7 proposes typed enforcement later. |
| Deadlines | Ask `expiresAt`: default 24 hours, maximum 7 days, strictly future at creation. Approval scope has its own `expiresAt`, strictly future at creation and at answering, within the 7-day ceiling. An answer cannot extend either expiry. |
| Answer | Set once: `optionId`, text of 0–2000 runes, approval-only `approved`, `answeredBy` (issuer, subject, displayName) and server `answeredAt`. Validate choice/free-text rules and an explicit approval boolean; text cannot imply approval. DisplayName is untrusted presentation; issuer/subject is identity. Never store access tokens or cookies. |
| Delivery | V1 `delivery.inboxMessage` and `delivery.deliveredAt` identify confirmed inbox storage. They do not prove read/ACK. |
| Proposed follow-up status/audit | Monotonic revision, answer digest, durable delivery intent/message UID, ACK identity, group-policy evidence, revocation and consumption facts. At most 16 semantic transition entries of at most 1 KiB; saturating retry counters. These additions need shared wire review. |

Require valid UTF-8. Reject NUL, unsafe controls and bidi override controls;
permit newline and tab in prose. V1 string lengths above count runes;
also propose byte limits for both input and canonical encoded JSON:
request at most 32 KiB, spec at most
24 KiB, full Ask at most 64 KiB, nesting at most 8. Reject duplicate keys,
case aliases, unknown fields, null in required fields and trailing JSON.
For identity, cap issuer at 2 KiB, subject at 256 bytes and displayName at
256 runes. Accept only configured HTTPS origins for OpenProject, GitHub
and this portal; refuse userinfo, credential-like query parameters and
unsafe schemes. Never fetch a supplied link. Render validated links as
escaped anchors and all other prose as text. These are validation
recommendations for the lane review, not extra v1 wire fields.
Reject an info Ask with neither options nor free text. Reject an answer
whose option ID is absent from the Ask, info text when free text is disabled,
or an approval field on an info answer. Approval requires an explicit
boolean, not an omitted value decoded as false. Bound a proposed creation
`requestId` to 16–128 URL-safe ASCII bytes.
Do not truncate an action, question or answer into a valid record. Schema
limits and API validation must agree, including multibyte input.

Proposed admission limits: 16 open Asks per requester, 512 retained Asks per
requester UID and 4096 retained Asks per audience. The retained requester
bound is a lifetime bound while that UID exists. Closed records still
count. Serialize admission across replicas with an audience-scoped CAS
reservation, not a per-process count. Unknown reservations remain counted
until reconciled. Existing identical retries and closing, answering,
revoking, reading or acknowledging records remain available at the cap.
Return 429 with a fixed reason; never discard an old question to admit one.
Expose counts and an 80% warning without question text or principal labels.

### States

V1 stores `open`, `answered`, `expired` and `withdrawn`. Its delivery field
and an explicit InboxReadAck can project the six-state lifecycle below.
Do not add new enum values in one lane alone. `delivered` and
`acknowledged` are lifecycle evidence, not v1 stored-state spellings.

| State | Exact evidence |
|---|---|
| `open` | Request is stored; no final answer. |
| `answered` | One human answer is durably confirmed. Crash-safe delivery also requires a durable intent in the store; v1 must not claim that guarantee before reconciliation is implemented. |
| `delivered` | The exact answer InboxMessage is durably stored and its UID confirmed. This does not mean an agent read it. |
| `acknowledged` | The original requester explicitly created the matching InboxReadAck. This does not mean it acted or completed work. |
| `expired` | No answer won before Ask `expiresAt`. |
| `withdrawn` | The original requester withdrew the open question. |

Normal progression is `open → answered → delivered → acknowledged`.
Only stored state `open` can become `expired` or `withdrawn`. Answer, withdraw and expiry
race through the same revision CAS. Check the server clock at that write;
an Ask past `expiresAt` is already unanswerable before a sweeper marks it.
No reopening, answer replacement or scope edit. Correct one with a new Ask
and a context link. An answer retry never advances state by itself.

The completed design adds separate approval validity: `not-approved`,
`active`, `expired`, `revoked`, or `consumed`. A denied Ask still delivers its answer. An
acknowledged Ask can have an expired or revoked approval. Passing the answer
deadline after an accepted answer does not erase it. Passing approval
expiry makes it unusable even if a status reconciler has not run.

## 3. API and caller classes

The v1 routes follow journal 2539 and are behind `--enable-asks`, default
off. Follow-up routes are labelled below. Retain audience-bound
TokenReview, TLS, rotating token-file reads and current Session/Pod checks.
Never expose the operator token to the browser. Disabled routes are absent.

| Caller | Allowed operations |
|---|---|
| Session agent | Create an Ask as itself; list and read its own Asks; withdraw an open Ask; read and ACK its own answer inbox. No supplied requester, answer, human principal or approval. |
| Portal reader | Read bounded Ask summaries/detail for configured portal audiences. No answer, withdrawal, ACK, approval consumption or general inbox access. |
| Portal answerer | Operation role on v1's portal-reader BFF caller for final answers, after human group authorization. Revocation is a follow-up. It grants no general coordinator, grant, budget or Session mutation authority. |

The new answer operation uses the existing portal-reader caller class in
v1. Explicitly add only the two portal Ask routes to its route allowlist;
metadata reading alone must not admit generic mutation. Its projected
identity must be unavailable to agent pods. A later split into a dedicated
answerer service identity can reduce that trust, but is not a v1 prerequisite.
The BFF attests the authenticated OIDC principal over its fixed verified
TLS channel. The operator does not classify that principal as its existing
Kubernetes Human caller.

V1 forwards the BFF-derived actor principal, not a browser field. Its
source contract trusts the BFF to perform the live group check. Recommend
a reviewed extension for an internal envelope containing exact issuer/subject,
group-policy ID and revision, positive current membership, authorization
time, Ask ID/revision, body digest and request ID. Maximum age: 30 seconds;
no future authorization time accepted. Bind it to that one authenticated
portal-reader request. No forwarding endpoint, agent-provided header, plain
`actorRef`, quoted user message or tracker comment can supply this envelope.
The answerer service is trusted to attest correctly; compromising it can
forge an answer. That trust and its network/credential isolation need
independent review and negative tests before approval activation. Record
the group-policy revision alongside principal/time before claiming the
full audit contract; v1's principal-only wire shape does not contain it.

| Route | Caller and behavior |
|---|---|
| `POST /v1/asks` | Session only. Stable key and immutable request. Confirmed new record: 201; identical retry: 200; changed intent under the same requester/key: 409. |
| `GET /v1/asks?mine=true&limit=...&cursor=...` | Session only; its own records, newest first. No reader scope over other agents' Asks. |
| `GET /v1/asks/{id}` | Owner Session only in v1. Includes its stored answer and delivery facts. GET never ACKs. |
| `POST /v1/asks/{id}/withdraw` | Owner Session, open only. Propose an expected-revision precondition and stable operation key in the shared contract. |
| `GET /v1/portal/asks?state=open\|all&limit=...&cursor=...` | Portal reader only, scoped to configured audiences. A selected Ask comes from this read; v1 adds no portal detail route. |
| `POST /v1/portal/asks/{id}/answer` | Portal reader carrying server-derived actor principal. Set-once CAS while open and unexpired. Approval requires explicit `approved: true` or `false`; a choice label or note cannot approve. Recommend expected revision, request/scope digest and stable operation ID for safe form retry. |
| Follow-up `POST /v1/portal/asks/{id}/revoke` | Fresh human group check, expected revision and stable key. Any admitted answerer may revoke; preserve original answer plus revoker/time/reason. Not in v1. |
| Follow-up `POST /v1/asks/{id}/consume` | Later typed action adapter only; exact requester UID, scope, executor, target precondition, expiry and one-use CAS. Not enabled for the first board. |
| Follow-up `GET /v1/asks/{id}/wait?after=REV&timeout=SECONDS` | Own Session; maximum 25 seconds, default 0; one wait per Session, 32 per replica. Timeout returns current state with no side effect. V1 needs no wait route. |
| Existing `/v1/inbox/{message}` and `/ack` | Original recipient reads the answer and explicitly ACKs it. Extend the narrow origin decoder; preserve current recipient UID and Pod checks. |

List order is newest creation time first, then full ID. V1 caps `limit`
at 100. Recommend default 20 and a 256 KiB encoded response ceiling, with
a continuation cursor when the byte cap ends a page early. Portal forms
need full bounded records; plain list summaries can omit answer prose.
Propose opaque authenticated cursors bound to caller scope, filters,
last key and a first-page time cutoff; lifetime 15 minutes, at most 512
bytes. Newly created Asks wait for refresh. A state-filtered list can change
between pages; it is not a snapshot or authoritative total. Include
`observedAt`, coverage and a continuation marker. Invalid/stale cursors
require a fresh list, not a wider query. Empty, partial and unavailable
are different results. Return no partial private payload on a failed read.

For safe retries, extend mutation contracts with a stable operation ID and
digest before confirming. Creation already has v1's `requestId`.
Answer/withdraw/revoke retries must match the original actor, operation,
Ask and material intent; return the prior confirmed result before testing
the old revision. Another actor with the same answer gets 409, not someone
else's receipt. At most one semantic answer, withdrawal, revocation and
consumption exists; no unbounded idempotency-key table per Ask.

An unconfirmed write or lost response is 503/Unknown. Read by the original
ID to reconcile; creation may explicitly retry the identical `requestId`
and intent when its generated ID is unknown. Only a confirmed identical
retry can establish success. Never blindly retry a v1 answer POST without
the operation-identity extension.
Never invent another key or change scope after a timeout. Stale revision
is 409, overdue answer is 410, invalid input 400/422 and a hard bound 429.
Unauthorized private reads use uniform 404 without metadata. Authentication
failure stays 401. Logs carry IDs, fixed reasons, revisions and outcome;
question, answer, raw principal and authentication material stay out.

## 4. Storage and retention

Recommend the coordination CRDs. An Ask belongs to the operator and must
remain readable when the portal or its database is unavailable. This keeps
requester identity, inbox and archive custody together. Portal PostgreSQL
already has narrow browser-session and immutable receipt privileges. It
does not give the operator a transaction across a SQL answer and a CRD
InboxMessage. Putting Asks there would still need a durable outbox and
would add a database client, migration and recovery authority to the operator.

The proposed completion adds immutable Ask spec plus bounded CAS status,
not editable free-form comments. One answer write atomically stores its principal, digest and
outbox intent in that same Ask. Final answer and revocation fields are
write-once. Delivery and notification attempts retain only fixed counters,
last time and reason. Intermediate retries do not consume audit slots.
Status writes use uncached confirmation; no informer cache proves a human
answer committed. A leader reconciles pending deliveries in bounded pages.

This needs new narrow Ask create/get/list and status-write permissions in
GitOps. Session callers use the operator API, not Kubernetes CRDs. Portal
reader and answerer receive no direct CRD access. It adds no worker grant.

Extend DESIGN-024's archive registry, validators, read fallback, metrics,
release rules and delete allowlist explicitly. Existing retention code
knows only its existing kinds; it must keep unknown records. Proposed Ask
release requires all of these:

- An uncached read proves the original requester UID absent.
- The Ask is closed, or first becomes expired/withdrawn. No active approval
  or pending action reservation remains.
- Each associated operator-origin inbox message is ACKed or its recipient
  UID is proved absent. Its complete identity must match this Ask/answer.

Treat the operator-origin answer as dependent on its Ask and recipient,
not a fictitious sender Session. Archive the full Ask revision, answer,
revocation and consumption evidence, message and ACK before deleting any
member. Verify their manifest digests and cross-references under the shelf
lock. Use the same 30-day grace after first proved release, fresh checks,
UID/resourceVersion delete preconditions, grace reset on restored presence,
and indefinite private archive as DESIGN-024. Unknown proof releases nothing.
Live requester means keep its records, even after expiry or ACK. Archived
read failure is Unavailable; an archived approval is always unusable.

Keep the board usable before retention activation by enforcing its bounds.
No new deleter runs until schema, archive reads and GitOps grants are
qualified and separately enabled. A shelf outage cannot erase a live Ask.
Archive custody and long-term growth inherit DESIGN-024's limits; a new
shorter deletion policy needs a later owner decision.

An etcd restore may restore an old approval or consumption bit. Before
re-enabling future action adapters after restore, change a GitOps-managed
approval-policy epoch held outside that restored ledger. Old approvals are
invalid under the new epoch. Prove this recovery rule before machine
consumption ships. Portal PostgreSQL's accepted lost-revocation window for
browser sessions does not waive this approval requirement.

## 5. Delivery and acknowledgement

Extend InboxMessage with a discriminated **operator Ask answer** origin.
It binds Ask ID/UID, immutable answer ID/digest, operator actor, attested
human principal reference and exact recipient Session namespace/name/UID.
It claims no sender provider invocation. Original Session-origin messages
retain their current immutable schema and saved-project rules.

For an ordinary managed Session with no saved catalog snapshot, explicitly
allow a recipient-only coordination scope for this operator origin, bound
to the Ask audience and original recipient UID. Do not invent a project
snapshot or use the broadly readable ConfigMap group. Existing project
inbox readers/coordinators gain no content right over this new scope.
Legacy readers that cannot decode it return Unavailable. Deploy compatible
schema, stores, API/CLI decoders and archive readers before emitting it.

Keep the answer body within the inbox text bound of 16 KiB; propose a
12 KiB encoded ceiling including the full v1 `answer` object. A later
version may add Ask/answer IDs, scope digest, validity and a canonical
status link. Fetch full scope from the Ask. No transcript or raw identity
token enters a message. A body copied to ordinary chat is data, not
approval authority.

V1's inbox body is JSON with exactly `askId`, `state` and `answer`. Keep
that shape for its clients. Put immutable origin/digest/recipient evidence
in the InboxMessage metadata/spec extension; introduce a versioned body
only with compatible reader qualification. Derive an internal answer ID
from the confirmed set-once answer when the wire has none. The bounds
above apply to the whole body, including its escaped answer and identity.

The durable outbox key is the Ask UID plus answer ID and event kind.
Create the corresponding message deterministically, then confirm immutable
intent and UID before marking `delivered`. A crash after message creation
but before status update must find that same message. Identical storage
retries are safe; no prompt injection or external action is retried.
At an inbox cap, retain the answer and pending intent, show “Answer saved;
delivery pending,” and alert on age. Reserve capacity for one answer and
one revocation message per admitted Ask in a bounded system-message quota;
agent messages cannot consume it. Close/read/ACK still work at the cap.

If the original requester is suspended, keep the message for its next
authenticated read on the same Session UID. If it is absent or replaced,
retain the answer in the board and report “Requester unavailable.” Never
deliver to a new UID or restart a Session to deliver. Initial delivery
checks the requester UID; an already confirmed immutable message remains
valid history after that UID disappears.

The agent lists, reads, validates Ask/answer IDs, records the answer in its
own durable handoff, then ACKs the exact message UID. Reading or calling
`ask_status` never ACKs. Reconcile the matching ACK to `acknowledged`.
An ACK may arrive before the delivery status update. Reconcile from both
confirmed message and ACK identities without losing that ACK.
An ACK proves the recipient's API acknowledgement; it proves neither
model comprehension nor execution. Revocation gets its own deterministic
message and ACK fact and does not undo the original answer ACK.

```mermaid
sequenceDiagram
    participant A as Agent
    participant O as Operator and Ask store
    participant P as Portal BFF
    participant T as Tom
    participant I as Durable inbox
    A->>O: ask(key, question, exact scope if approval)
    O-->>A: Ask ID and link, state open
    Note over A: Continue independent work
    O-->>T: Bounded push with portal link
    T->>P: Open Ask and submit answer
    P->>P: Current session, Origin, CSRF, fresh group check
    P->>O: Answer, principal assertion, expected revision
    O->>O: CAS answer and durable delivery intent
    O-->>P: Answer saved
    P-->>T: Saved; delivery pending
    O->>I: Confirm deterministic answer message
    O->>O: Mark delivered with message UID
    A->>I: List and read own inbox at a safe checkpoint
    I-->>A: Answer envelope
    A->>A: Validate and save local checkpoint
    A->>I: ACK exact message UID
    I-->>O: Confirm matching ACK
    O->>O: Mark acknowledged
```

The notification arrow is optional. Push failure never changes the answer
path. A human can discover the Ask on the board without a push.

## 6. MCP and harness use

Extend `agent-run mcp`; preserve its existing grant tools. No provider API
key, inference call or provider-specific login is needed to post an Ask.
The operator derives Session identity from the existing projected token.

| Tool | Contract |
|---|---|
| `ask` | Typed request, caller-stable key, question/options/recommendation/context and approval scope if needed. Returns ID, revision, link and deadlines immediately. No wait parameter or human answer tool. |
| `ask_status` | Read own Ask by ID, including answer, delivery and approval validity. A follow-up may add bounded `wait_seconds` 0–25 and `after_revision`; v1 returns immediately. No ACK or consumption side effect. |
| `list_my_asks` | One bounded page of own Asks and a cursor; state/type filters are optional follow-up fields. Includes answered and unacknowledged records. No all-session scan, implicit continuation or automatic ACK. |
| `withdraw` | Own open Ask; expected revision and stable operation key need the shared-contract extension. Cannot erase an answer or revoke on a human's behalf. |

Also expose narrow `inbox_list`, `inbox_read` and `inbox_ack` wrappers over
the existing CLI/API operations for harnesses without a shell. These are
explicit reads and ACKs, not an `approve` or relayed `answer` tool. The
work item's earlier `list_answers` name maps to a `list_my_asks` state
filter; it does not introduce another answer store.

Claude Code, Codex and another MCP-capable harness use the same local
stdio command through their managed configuration. Instructions tell them
to check pending answers at turn start, before a held action and before a
handoff. A submitted Ask must include its ID and next safe step in that
handoff. A new tool configuration reaches a session at its next supported idle
configuration reload or normal lifecycle transition. Do not rewrite
running clients or restart a Session to install it. Codex native question delivery is not a prerequisite for these tools.

Storage alone does not wake a busy model. The first version guarantees
retrieval on those checkpoints, not immediate unsolicited native turns.
An automatic wake or injection adapter would need a separate provider
contract and one-time delivery proof. A wait times out cleanly; it never
starts an unbounded poll loop. If all remaining work depends on the answer,
save “Waiting for answer” and stop that bounded work.

Session-less native hosts or external agents need a separately reviewed
identity/recipient adapter. Until then they cannot borrow a Session UID,
copy a pod token or approve through a relay. “Any agent” means any harness
with a supported authenticated requester and inbox, not anonymous callers.

## 7. Approval authority, expiry and revocation

An approval Ask must describe one exact proposed action. V1's `action` and
`target` prose must state its material limits; neither a wildcard nor
“approve my plan” is sufficient. If it cannot fit precisely in those
bounded fields, link the reviewed artifact and keep execution held until
a typed scope can bind its exact content digest. Before machine
consumption, add typed action names and a canonical scope schema, never a
general shell executor. Include exact resource name/UID, environment and operation;
for a PR, repository/number and head SHA; for a budget, ledger UID and
numeric currency/token/time/concurrency limits; for access, grant UID,
role, namespaces, endpoints and TTL. Include target-state preconditions
and any one-time attempt limit. Missing or unknown limits cannot mean
unlimited. A changed action or target needs a new Ask.

The initial board records the v1 action/target/expiry description and has
no generic executor. It is not a complete revocable approvals service
until the audit and revocation follow-up ships. Add canonical scope
digests, at most 8 KiB and 16 targets, in the later enforcement contract.
Each machine-consumable action kind
needs its own validator and enforcement adapter. An unknown kind stays
record-only and cannot be consumed. Render the full exact scope before
the approval button. A shortened label is not the scope reviewed.

Only a current human principal admitted by the portal group may answer.
The BFF verifies `portal_is_active` and `portal_is_admin` for that exact
request-private session before rendering and immediately before committing
an answer. Preserve session revocation, token digest, expiry and subject
checks around UserInfo. No per-person allowlist replaces the group.
Issuer/subject is audit identity, not an authorization pin.

The full follow-up audit must store principal, admitted group-policy ID/revision and check time, answer
time, Ask/requester UID, full canonical scope/digest, chosen validity,
approval-policy epoch and request ID. V1 supplies only its option, text
and explicit approval choice from the browser; identity is server-derived.
Proposed revision/digest/operation fields need shared wire review. Agents and
coordinators have no answerer credentials or endpoint rights. An agent
cannot approve its own Ask or another agent's Ask. A trusted coordination
relay may link the Ask but cannot synthesize its human answer.

Membership is checked at answer and revoke time. A stored approval records
membership at that time. Later logout or group removal does not rewrite
that historical fact or automatically revoke its bounded approval. It
denies later portal operations. Explicit revocation and expiry end unused
approval; any future requirement for immediate recall on group removal
needs an IdP-backed revocation adapter and qualification. Do not claim one
exists. Keep approval TTL short and make that limit visible.

A later consuming adapter must reread current Ask validity and target
state and CAS-reserve its single use before dispatch. Denial, revocation,
expiry and reservation race on the same authoritative Ask revision. No
offline page, inbox copy, stale cache or agent assertion can authorize use.
An uncertain reservation stays unusable pending reconciliation. Never
replay a privileged action after Unknown merely because approval exists.
Record reservation, executor and applied/failed/unknown result separately.

Revocation before reservation prevents use. Reservation is the one-use
authorization boundary. Revocation after it records cancellation requested;
the adapter must try to stop a pending dispatch, but cannot promise to win
that external race. An action may already start or finish. The board shows whether cancellation was
confirmed, too late or Unknown. It cannot promise to undo an external
effect, remove a copied credential or refund a spent budget. Revocation
is durable even if its inbox notice fails. A new approval requires a new
Ask and does not replenish a consumed attempt.

| Existing approval | Later mapping; existing enforcement remains authoritative |
|---|---|
| Access grants and agent attestations | An Ask links the exact AccessGrant UID and current scope digest/TTL. A future broker adapter validates human approval directly. An agent `grant attest` record stays a claim; it cannot be relabelled a portal answer. Grant policy, install, use, expiry and revoke remain broker facts. |
| Task budget extension | An Ask names the ledger UID, revision and exact added time/tokens/cost/concurrency. A future budget service adapter consumes it once through its existing ledger CAS. A human answer cannot edit a ledger or silently admit more children. |
| Managed-child decision / decision-authority | Link an Ask to the exact decision ID, task Session/Pod/thread and writer generation. A future adapter must retain existing Reserved/Confirmed authority and budget fences. A portal approval or parent relay alone does not confirm that native decision. |

## 8. Portal board and Needs attention

Add **Asks** to the existing shared desktop and mobile shell. Default to
open Asks. Offer narrow filters for info/approval, requester and closed
state. Propose `/portal/projects/dev-env/environments/internal/asks` with
`?ask=<id>` for selection, and same-origin `POST .../asks/{id}/answer`
for the BFF form. Keep filters and selection in canonical URLs. A card shows question,
requester, linked work item/PR, recommendation, deadline and age. Unavailable
counts never render as zero.

Needs attention includes every visible open Ask. Its Session says
**Waiting for you** with the Ask question and link; retain liveness, task
outcome and other attention reasons beside it. This is a pending human
dependency, not proof the whole agent is blocked. Multiple Asks show a
count and each question. An absent Session's Ask still appears on the board.
An answered Ask moves to delivery status, not back to “Waiting for you.”
This cross-page projection is F3. The first #258 increment keeps its count
on the Asks board only, under lead journal 2553.

The selected Ask shows its exact scope, validated context links and the
requester's compatible conversation, through the existing projection.
Fetch conversation on demand, with separate history authority. Missing
history cannot prevent reading or denying an Ask. Approval stays explicit
with full scope visible. Info choices allow a text answer. No answer is
preselected; approve and deny have equally clear labels. A follow-up uses
ordinary session messaging or a linked new Ask, not an edit of the answer.

The form POSTs only to the same-origin BFF. Use the current encrypted
session, `__Host-` cookie, exact Origin, strict form keys, bounded body,
constant-time CSRF comparison and request-private principal reference.
Reauthenticate and check live UserInfo immediately before dispatch, with
the same handle, principal and token digest. GET, push links and browser
prefetch have no answer side effect. An expired session requires login and
a fresh form; do not queue approval offline.

Bind form receipts to that browser session and request ID in the existing
private receipt pattern. The common Ask answer is visible to its audience;
another principal cannot read the first principal's private form receipt.
A stale revision shows the newer answer. A lost response says “Answer
unconfirmed” and offers a status read. No automatic resubmit or new request
ID. Saved, delivery pending, delivered, acknowledged and action result are
separate lines. Expired, revoked or consumed approval remains prominent.

Use a single column at phone width, full-width choices, touch-sized
controls, keyboard focus and labelled text fields. Retain list position
on Back. Put approval scope above the submit control, without horizontal
scroll. Escape prose as text; no active Markdown, remote images, raw tool
output, thinking or executable attachment. No JavaScript or background
polling is required for the first version. Refresh shows current facts.

## 9. Notifications

Recommend the keeper's existing Pushover sender and `internal/pages`
outcome vocabulary, with a new bounded Ask-notice job. Add an explicit
operator-to-keeper metadata adapter and its reviewed identity/network
policy; the keeper has no general Ask text reader today. Do not distribute
its notification credentials to agents or the portal.

Use a fixed title and body such as “An agent needs your answer.” Link to
the configured HTTPS portal origin and exact Ask route. Include no
question text, PR title, principal, token or signed approval in the push
or broadly readable page record. A link always opens authenticated GET.
Record the correlation as an Ask ID and semantic event.

Proposed caps: one new-Ask push per Ask, at most one push per requester
per five minutes and six per recipient per hour. Persist reservation and
rate state across keeper replicas/restarts. Coalesce excess into one board
digest within those caps; the durable board retains all questions. No
urgent flag supplied by an agent can bypass a cap or quiet hours. Record
suppressed/deferred notification state separately from Ask delivery.

Pushover has no sender idempotency contract in the current design. A lost
send response is `ambiguous`, not sent or delivered. Do not automatically
repeat an ambiguous Ask push. A definite pre-send failure may retry once
after five minutes, within the caps and only while the Ask is open. Record
before/after the attempt using DESIGN-031's bounded history. Ask, answer
and inbox delivery do not depend on notification success. Keepers' current
login notices and their emergency behavior stay unchanged.

Quiet hours remain Q-255-02. Proposed Ask priority is 0 with no emergency
repeat. Until Tom answers, the notification story stays disabled; the
board can be built and tested without live pushes.

## 10. Chat history T4: eligible Sessions, not per-UID lists

Recommend a default-off history mode named `eligible-registered` for the
configured portal resource. Initially that resource remains
`dev-env/internal`; no cross-project or all-namespace expansion follows.
Every Session in that resource is eligible when all of these hold:

- It belongs to an operator-managed namespace and the resource's declared
  repository/project scope, including explicitly covered projectless
  `dev-env` Sessions.
- Its original Session UID and current controlled Pod are freshly proved,
  Running and non-deleting, as the current conversation route requires.
- Its agentd has a compatible registered-conversation reader, and initial
  and current registrations agree on the original Session UID, conversation
  and worktree. The current registration binds the current Pod; the first
  launch retains its original Pod identity. The supported provider adapter
  validates its transcript format.
- The current portal principal passes the same live group check before
  fetch and before response release. The configured history resource scope
  admits this Session. Summary visibility alone grants nothing.

Select eligibility from operator-owned identity and resource policy, not
an agent label, a supplied filesystem path or a reusable name. The BFF and
operator must agree on the mode. Refuse ambiguous combinations with exact
target lists; preserve the existing exact mode until this one is reviewed
and activated. Send keeps its separate target list. A new provider becomes
eligible only after adapter qualification, not merely a provider enum.

An older/unregistered Session, missing current pod, legacy thread, changed
worktree or unsupported provider shows a fixed Unavailable reason. Do not
scan homes, adopt an unregistered file, read raw logs or fall back to archive
or inbox as transcript history. Suspended and archived transcript access
needs its own contract; the Ask/answer itself stays readable meanwhile.
No existing Session is restarted or rebound to make it eligible.

Keep current plaintext roles, redaction, no thinking/tool bodies, 60 turns,
4 KiB per turn and 96 KiB total rendered text. Preserve bounded fetches,
coverage and source times. Current cursors bind the entire transcript
snapshot and owner; appends invalidate them and the BFF currently fetches
only the newest page. T4 eligibility does not claim stable growing-history
pagination. If added later, it needs its own bounded cursor contract.

Trade-off: any admitted portal-group member can read any eligible Session
in this resource, including future compatible Sessions without another
UID configuration edit. This fits Tom's group direction and removes
manual target churn, but increases transcript exposure. Redaction cannot
guarantee removal of secrets or private user text. Group membership,
resource policy, private responses, no-store caching and content-free
access audit remain essential. Group removal takes effect on the next
fresh check; it cannot retract text already shown. Extra projects and
archive access need separate review. Q-255-03 holds this eligibility change.

## 11. Migration and outage behavior

After live acceptance, use the board for routine information questions and
bounded approval requests across compatible harnesses. Update shared
instructions through their GitOps source. Do not merely remove the current
phone-question rule before delivery, human identity and real agent ACK
have been accepted. Do not translate a harness security prompt into a
board approval or bypass the harness's own enforcement.

When the board is down, first reconcile any uncertain creation or answer
using its stable key/ID. Preserve that ID in OpenProject and the local
handoff. Continue independent authorized work. For an urgent question,
use a supported native phone prompt only when its delivery is verified,
one question at a time. Record the fallback answer's actual source and
scope in the canonical work item. Such a record is not a forged portal
principal answer. Leave the old Ask open for reconciliation or explicitly
withdraw it once reachable; do not spend the same one-use approval twice.

Codex currently has no verified native phone fallback. Keep its dependent
action held and hand the pending question to the lead's verified route.
No guessed notification channel, credential repair, tracker comment or
ordinary chat relay substitutes for action-specific human approval.
Information already authorized in the session remains usable; an outage
does not reset existing permission or require reapproval of completed work.

## 12. Build stories and acceptance

The canonical queue already contains lanes #256–#259. Reuse those items;
do not create a competing A1–A11 backlog. Their owners and budgets remain
as recorded by the lead. Journal 2539 gives merge order: wire types first,
backend next, then MCP, portal and notifications. Source scaffolding may
proceed in isolated files under those work orders; runtime acceptance
waits for the combined result.

The table maps that order and the remaining bounded follow-ups. New rows
need search-first filing under #254 before pickup, with owner, custody,
budget and handoff. The lead grooms them after reviewing this design;
this PR grants no pickup or parallel dispatch. Estimate each new source
follow-up at two hours with one worker and a 90-minute checkpoint; usage
cost and quota are unknown. Split at that checkpoint if it will not fit.
Tests use at most `DEV_ENV_CPU_LIMIT` workers, one suite at a time. No
burners, stress or wide test loops. Every item reports source/test evidence
separately from installed and usable/accepted state.

| Story | Dependency | Outcome and acceptance |
|---|---|---|
| Lane 0 — shared wire types, lead-owned | Journal 2539 | One reviewed route/type/validation contract; v1 bounds, set-once answer shape and source-only state words agree across clients. No runtime route. |
| [#256 — backend and inbox delivery](https://work.haynesnetwork.com/work_packages/256) | Lane 0 | CRD/store/API behind `--enable-asks`. Prove create/idempotency, list/get/withdraw ownership, portal-only answer CAS, expiry and one attributed inbox message. Explicitly resolve section 5's operator-origin gap; do not borrow an agent sender. Schema, generated code, layout and focused tests pass. |
| [#257 — MCP client](https://work.haynesnetwork.com/work_packages/257) | Lane 0; #256 for integration | Four nonblocking tools with strict bounded inputs. TLS fixtures cover create/status/list/withdraw and Unknown/refusal; no session answer tool. Agent-run remains free of Kubernetes imports. Optional wait is deferred, not a missing v1 endpoint. |
| [#258 — portal board](https://work.haynesnetwork.com/work_packages/258) | Lane 0; #256 for integration | Mobile board, answer POST, private session/CSRF/current-group checks, replay/stale-session/escaping tests and exact scope shown. Lead browser QA at 320/390/1280 in dark/light. Journal 2553 limits the first badge to the board; cross-page attention is the follow-up below. |
| [#259 — notifications and expiry](https://work.haynesnetwork.com/work_packages/259) | #256 merged; #258 link; Q-255-02 before live pushes | Leader-only expiry and bounded linked pushes. Fake clock/sender proves quiet hours, rate limits, restart dedupe and ambiguous outcomes. Default off; no changed keeper login notices. |
| F1 — durable answer delivery qualification | #256 | If not covered by #256, finish operator-origin inbox and recipient-only scope, deterministic outbox recovery and ACK projection. Crash before/after each store write yields one answer message; cap/suspend/absence/replacement/wrong-ACK tests pass. No provider injection. |
| F2 — revocable approval audit | #256, #258, F1 | Add reviewed group-policy/answer identity, operation IDs, revision preconditions and revoke route/form. Two admitted subjects, forged actor, swapped sessions, removed group, stale scope, concurrent answer/revoke and lost response fail or reconcile correctly. Revoked remains visible after ACK. No action adapter. |
| F3 — Needs attention and consumption | #257, #258, F1 | Add cross-page Waiting for you and explicit inbox MCP read/ACK wrappers. Qualified Claude/Codex fixtures post, continue independent work, read/save/ACK once and resume a handoff without reissuing. No automatic wake; unsupported hosts stay explicit. |
| T4 — resource history eligibility | Q-255-03; existing conversation contract | BFF/operator agree on resource-based mode. Multiple compatible Sessions need no listed UID; legacy/unbound provider, out-of-resource target and replaced Pod are unavailable. Send scope does not expand. Current pagination limits remain visible. |
| F4 — coordination retention | F1, F2 | Extend DESIGN-024 bundle archives and read fallback. Prove requester-present/Unknown refusal, full message/ACK linkage, 30-day grace and hash mismatch refusal. Archived approvals are unusable; no deletion until separately enabled. |
| F5 — GitOps installation | Combined lane CI/review, F1–F4, selected T4, signed images and runtime authorization | Separate haynes-ops changes install schema/readers before writers, narrow API/storage rights and isolate the BFF identity. Enable each flag in reviewed order. Record digests, effective identities, rollback floor and observed status. Preserve running Sessions and v1 holds. |
| F6 — owner acceptance | F5; separately authorized disposable Sessions | Tom reads history and answers on phone; Claude and Codex receive and ACK. Demonstrate deny, expiry, revoke and visible delivery failure. Record built/tested/installed/usable acceptance independently. Estimate one hour, one worker. Only accepted delivery permits instruction migration. |

Each future grant, budget or native-decision action adapter gets a separate
story after F6. Acceptance must prove typed scope, live target preconditions,
one-use reservation, Unknown without replay, revocation races, and approval
epoch invalidation after a restored ledger. Its own action-specific
approval and activation gates remain. A generic approve-and-run tool is
outside this design.

## 13. Questions for Tom

These question IDs are local to Story #255. They are proposals, not owner
rulings or consumed native prompts. The Codex worker has no verified phone
question path. The lead must deliver one question at a time through its
verified path, then record the actual answer, source and date here and in
#255. An unanswered question blocks its dependent build scope; it does not
block opening this documentation PR. Do not send this list as one prompt.

| ID | Question and recommendation | Alternative and gate |
|---|---|---|
| Q-255-01 | May any current portal-group member revoke an unused approval given by another member? **Recommend: any admitted member may revoke**, with its own principal, time and reason recorded. The answer group itself already follows the lead contract. | Only the original approver may revoke. Holds the F2 revocation policy; it does not reopen the group access ruling or pause existing source lanes. |
| Q-255-02 | Should routine Ask pushes wait outside 09:00–21:00 America/New_York? **Recommend: that daytime window**, priority 0, no agent bypass, DST-aware deferral while the board remains available. | Push at all hours within the same caps. Holds live Ask notifications only; current login notices are unchanged. |
| Q-255-03 | May the portal show registered history for every compatible Session in `dev-env/internal`, including future Sessions, without listing each UID? **Recommend: resource-based eligibility** under section 10, with the same group and all current registration/Pod fences. | Keep exact history targets for now. Holds T4 enablement, not the Ask board or its context links. |

The stated limits, storage choice and one-answer thread are design
recommendations for review, not additional owner-question prompts. If review
changes them, update the contract and acceptance before building.
