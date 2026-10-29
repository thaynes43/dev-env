# DESIGN-034: An Asks and approvals board for agents

- **Status:** Proposed; documentation only, 2026-10-29.
- **Decision:** [ADR-010](../adrs/010-agent-asks-and-approvals-board.md).
- **Tracking:** [Story #255](https://work.haynesnetwork.com/work_packages/255),
  [Epic #254](https://work.haynesnetwork.com/work_packages/254), Saga #85.
- **Source:** PR #658 at `4de52a3427254cc66c33f0ed2139fb9811508c0b`,
  based on `18b55268ab6ec143abe1802a9020c3e4c284582a`.
- **Work order:** ASKS255-DESIGN-FIX-20261029-02. One ready superseding
  PR; the lead reviews and merges. No code or runtime action here.

## v1 build contract

The authority is Epic #254 **journal 2539**, amended by **2556** and
**2579 (v1.1)** and **2595 (v1.2)**. The later journal controls wherever they differ. The table is
an index for lanes 0/A (wire/backend), B (keeper), C (MCP), D (portal).
It describes required behavior, not installed or accepted functionality.

| Field, route or rule | Contract journal in #254 | Lane / section |
|---|---|---|
| `id`, `requestId`, `type`, `question`, `options`, `recommended`, `allowFreeText` | 2539; 2579 A6; 2595 A6'/A17 (lane 0 bounds) | 0/A/C/D; §2 |
| `context.sessionName/sessionUID`, `workPackage`, `links` | 2539; 2579 A6/A7 (wire URLs vs rendered links) | 0/A/D; §2 |
| `scope.action/target/expiresAt`, `createdAt`, `expiresAt`, create `expiresIn` | 2539; 2579 A8 (24h approval cap, Ask expiry ≤ scope expiry); 2595 A16 (whole-second clock) | 0/A/C/D; §2/§7 |
| `state`; immutable spec and set-once `answer` | 2539; 2579 A12 (CEL) | 0/A; §2/§4 |
| `answer.optionId/text/approved/answeredAt`; notes and explicit `*bool` | 2539; 2556; 2579 A13 | 0/A/C/D; §2 |
| `answer.answeredBy.issuer/subject/displayName`; session redaction | 2539; 2579 A2/A6; 2595 A2' | A/C/D; §3/ADR-010 |
| `delivery.inboxMessage/deliveredAt` reserved, empty in v1; answer read from Ask | 2579 A1; inbox CRD change is F1 | A/C; §5 |
| `POST /v1/asks` | 2539; 2579 A9 (201 new, 200 identical, 409 changed) | A/C; §3 |
| `GET /v1/asks?mine=true`, `GET /v1/asks/{id}` | 2539; 2579 A1/A2/A11 | A/C; §3 |
| `POST /v1/asks/{id}/withdraw`; no portal withdraw | 2539; 2579 A3/A9 | A/C; §3 |
| `GET /v1/portal/asks?state=open\|all`, `GET /v1/portal/asks/{id}` | 2539; 2579 A3 (detail, any state) | A/D; §3 |
| `POST /v1/portal/asks/{id}/answer`; answer-time expiry and retry codes | 2539; 2556; 2579 A8/A9/A13 | 0/A/D; §3 |
| Limits 16 open/requester, 512 total/requester, 4096/namespace; count then create, 80% warning | 2579 A10 (uncached list count; `CheckRoom` is metadata-only) | A; §2 |
| Newest first `(createdAt desc, id)`; operator sort/cursor, page limit ≤100 | 2539; 2579 A11 | A/C/D; §3 |
| `ask` returns Ask JSON, no revision, optional configured portal link; computed validity | 2579 A4/A5 | C/D; §6/§7 |
| `ask_status`, `list_my_asks`, `withdraw`; no session answer tool | 2539; 2579 A1 | C; §6 |
| Approve/Deny primary, secondary options cannot contradict decision | 2579 A15 | D; §8 |
| Default-off flags; keeper pushes; operator expiry sweeper; lane custody and merge order | 2539; unchanged by 2579/2595 | All; §9/§12 |
| T4 outside v1; profile allowlist, workbench opt-in, grant exclusions | 2579 A14; Q-255-03 remains pending | Follow-up; §10/§13 |

## 1. Result and existing parts

An agent posts a question and immediately receives the Ask JSON. A client
with a configured portal origin can also offer a board link. Tom opens the
board, reads the context and answers. In v1 the answer stays on the Ask;
the original Session reads it through get or list at a safe checkpoint.
The agent keeps doing independent work. Work that depends on the answer
remains held.

An Ask has one final answer. A follow-up is another Ask linked to the
first. Ordinary conversation stays in the session message/history views.
The board is for questions and approvals, rather than ongoing conversation.

| Existing source | Contract to preserve or extend |
|---|---|
| [DESIGN-029 group ruling](029-portal-foundations.md#group-based-action-scopes-2026-10-29) | Fresh UserInfo admits the portal group for the request's principal. Send/history currently have exact name/UID targets. Ask answers store the portal principal, which changes DESIGN-029's actor boundary for Asks only (ADR-010). Message `actorRef` handling and history scope are unchanged. |
| [`server.go`](../../../../internal/apiserver/server.go) | TokenReview caller classes and narrow portal-reader route allowlist. New Ask routes need explicit admission. |
| [`inbox_types.go`](../../../../api/coordination/v1alpha1/inbox_types.go), [DESIGN-014](014-durable-offline-inbox.md) | Immutable messages require original Session senders and saved-project recipient provenance. F1 must change the CRD before operator-origin delivery. V1 creates no InboxMessage. |
| [DESIGN-010](010-atomic-task-claims.md), [DESIGN-024](024-coordination-record-retention.md) | Protected coordination group, bounded records, uncached checks and archive before deletion. A claim or ACK grants no authority. |
| [`decisions.go`](../../../../internal/apiserver/decisions.go), [`grant_attestation.go`](../../../../internal/apiserver/grant_attestation.go) | Managed-child decisions and agent grant claims do not establish portal-principal approval. Existing enforcement remains authoritative. |
| [`mcp.go`](../../../../internal/agentrun/mcp.go) | Extend the stdio `agent-run mcp` server and preserve its grant tools. No inference call or provider login is needed. |
| [Portal runtime](../../../../docs/portal-session-runtime.md), [`commands.go`](../../../../web/portal/session/commands.go), [`authorization.go`](../../../../web/portal/session/authorization.go) | Same-origin forms, CSRF, encrypted browser sessions, fresh UserInfo and request-private binding. A message `actorRef` does not name an approver. |
| [DESIGN-031](031-pages-lookup.md), [`notifier.go`](../../../../internal/keeper/notifier.go) | The keeper sends pushes. Sent means Pushover accepted the request, not phone delivery or reading. Broadly readable page records must not contain Ask prose or identity. |

## 2. Ask record and bounds

Add namespaced `Ask` in `coordination.dev-env.haynesops.com/v1alpha1`,
namespace `dev-agents`. Its immutable spec is the request; the operator
alone writes status with compare-and-swap (CAS). Sessions use the API,
not direct Kubernetes access. No Session owner reference may erase the
Ask on reap. Requester provenance needed by the store is server metadata,
not a new client-supplied wire field. Create drops `status`. Until the
operator's first CAS status write, readers treat an absent
`status.state` as `open`.

| Field | v1 meaning and bound |
|---|---|
| `id`, `requestId` | Server-generated `ask-` plus 16 lowercase hex digits. Create `requestId` matches `^[A-Za-z0-9-]{8,64}$`, keyed per original requester. Identical retry returns the same record; changed body under that key conflicts. |
| `type` | Exactly `info` or `approval`. |
| `question` | UTF-8 plain text, 1–1000 runes, not blank. |
| `options` | 0–6 choices; unique IDs matching `^[a-z0-9-]{1,24}$`, labels 1–80 runes, descriptions 0–300 runes. An info Ask needs at least one option or `allowFreeText: true`. An approval needs no options. |
| `recommended`, `allowFreeText` | Recommendation is an option ID or empty. Free text controls info answers; it does not restrict approval notes. Never preselect approval. |
| `context` | Server-filled `sessionName` (≤253 bytes) and `sessionUID` (≤128 bytes); optional positive OpenProject `workPackage` integer and 0–5 HTTPS links of 1–300 bytes each. No caller-supplied requester identity. |
| `scope` | Required for approval, absent for info. `action` and `target` each 1–200 nonblank runes, plus RFC 3339 `expiresAt`. Describe one exact action and target with material limits. Approval scope expires 1m to 24h after creation in v1 (`AskScopeMaxTTL`). |
| `createdAt`, `expiresAt` | Server creation time. Create uses `expiresIn`, a Go duration string in whole seconds, `1m` to `168h` (7 days), default `24h`. An approval Ask's `expiresAt` is ≤ `scope.expiresAt` (A8): when `expiresIn` is omitted, `expiresAt` is the earlier of `createdAt + 24h` and `scope.expiresAt`, so the default never fails; an explicit `expiresIn` that would outlive the scope is refused with `400` (lane 0 `ExpiresAtFrom`, `ValidateAt`). The 24h scope cap therefore also bounds the answer window. An answer never extends either expiry. Servers truncate the clock to whole seconds before `ExpiresAtFrom` and `ValidateAt`, and store that value in the spec as `createdAt` (`metav1.Time`), not `metadata.creationTimestamp` (2595 A16). |
| `state` | `open`, `answered`, `withdrawn` or `expired` only. |
| `answer` | Set once: `optionId` (an existing choice or empty), `text` (0–2000 runes), `approved` (required `*bool` for approval, absent for info), `answeredBy` and server `answeredAt`. An explicit false means Deny; omission is not false. |
| `answer.answeredBy` | Operator audit/portal: issuer ≤300 runes and subject ≤200 runes, each nonblank when present and both required on the portal answer request; displayName ≤120 runes, nonblank when present. The operator stores issuer and subject, plus displayName when known. A returned actor is valid with a nonblank displayName, or with both issuer and subject (2595 A2'). Session responses return only displayName, `Portal user` when none is known; issuer and subject are omitted, not empty-string placeholders. No access tokens or cookies. |
| `delivery` | Reserved shape `{inboxMessage, deliveredAt}`. Empty/omitted in v1. No delivery or ACK fact is inferred from an answer read. |

Lane 0's validation bounds apply. Text must be valid UTF-8. Reject NUL,
other controls, format and invisible characters (Unicode Cf, U+2060 to
U+206F, tag characters), bidi overrides and line/paragraph separators.
Multi-line fields (question, option description, answer text) also allow
LF, tab, the zero-width joiner and the variation selectors U+FE00 to
U+FE0F (2595 A17). Hangul fillers, U+034F and U+E0100 to U+E01EF are
refused everywhere. U+2800 counts as blank. Clients turn CRLF and lone
CR into LF with `NormalizeAskText` before validating.
Do not silently truncate a question, scope or answer to pass validation.

The wire accepts **any HTTPS host**: an absolute URL with a host, no
credentials/userinfo, printable ASCII with no spaces or controls, at most
300 bytes. Percent-encode or use punycode where needed. Do not fetch it.
The portal renders anchors only for `claude.ai`, `github.com`, the
configured OpenProject origin and the configured portal origin. Other
hosts render as escaped plain text with the host visible. This is a
rendering allowlist, not a wire rejection rule (A7).

Info answers require an option or text; text requires `allowFreeText`.
Approval answers require the explicit boolean; option and text are
optional. **Approval notes are allowed regardless of `allowFreeText`.**
A selected option must exist; it cannot contradict Approve/Deny in the
portal (§8). Text or an option label never implies approval.

Admission limits are **16 open per requester, 512 total per requester and
4096 per namespace**. Closed records still count toward totals. Count
open Asks from an uncached list of that requester's Asks (at most 512),
using `EffectiveState`, or from a state label kept in step with status.
[`taskclaim.CheckRoom`](../../../../internal/taskclaim/store.go) counts
metadata only and is keyed by project, so it cannot count open Asks.
Check ownership against `spec.context.sessionUID`, never a label alone.
Resolve an identical create retry before the room check; it needs no
slot. Compare the request as sent, including `expiresIn`, not the
derived times. A full bound returns `429 limit_exceeded`, as task claims
and inbox do; an unconfirmed count returns `503`. Log and emit a metric
at 80%. This is not a new cross-replica CAS capacity
reservation and does not claim atomic quota enforcement. Read, answer and
withdraw do not require a new slot. Keep old records instead of dropping
them to admit another Ask. Counts/logs must not contain question or
principal labels.

### States

| Stored state | Evidence |
|---|---|
| `open` | Request stored, no final answer. |
| `answered` | The set-once human answer is durably stored. |
| `withdrawn` | The requester withdrew the open question. |
| `expired` | The answer window ended before an answer won. |

Only `open` can transition, to one of the other three states. Answer,
withdraw and expiry race through CAS. At answer time the Ask must be open
and `now < expiresAt`; for approval, also `now < scope.expiresAt`. Enforce
these checks at the write even if the expiry sweeper has not run. Lane 0's
`ValidateForAt(ask, now)` helper expresses the answer-time checks; handlers
map expired/conflicting outcomes to the codes in §3. Check for an identical
retry by the same issuer and subject before calling `ValidateForAt`. That
retry returns `200`; `ValidateForAt` reports every answered Ask as not open.

No reopening, scope edit or answer replacement. Use a new linked Ask for a
correction. Answer history remains `answered` after its approval expires;
computed validity is separate. V1 has no delivered/acknowledged stored
state and no revocation or consumption state.

## 3. API and caller classes

All operator Ask routes are behind `--enable-asks`, default off. Preserve
TLS, audience-bound TokenReview, rotating token-file reads and current
Session/Pod checks. No operator bearer token reaches the browser.

| Caller | v1 rights |
|---|---|
| Session | Create as itself; list/read only its own Asks; withdraw its open Ask. No answer route or supplied human principal. |
| Portal reader (BFF) | List and read Asks, including detail in any state; answer after authenticating the current human and checking the portal group. No withdrawal, consumption or general inbox access. |

Add only the listed portal routes to the portal-reader allowlist. Its
identity must remain unavailable to agent pods. The BFF derives the actor
from its current request-private session and fresh UserInfo, never from
browser fields or agent headers. The operator trusts that assertion from
the admitted BFF, not a generic relay or its Kubernetes Human caller.
A later dedicated answerer service identity or group-policy audit envelope
needs a separate reviewed change; neither is a v1 wire field.

**Identity boundary (A2):** the operator stores all three `answeredBy`
fields for audit, and portal routes return them. Every session-facing
response under `/v1/asks`, including create replay, lists and withdraw,
omits issuer and subject and exposes only displayName. Clients must accept
that projection without reconstructing or requiring the raw identity.
[ADR-010](../adrs/010-agent-asks-and-approvals-board.md) records the change
from DESIGN-029's HMAC message actor boundary for approvals. Current
message identity handling remains separate.

| Route | Caller and behavior |
|---|---|
| `POST /v1/asks` | Session. `201` new; `200` identical retry; `409` same requester/requestId with changed body. Returns the Ask, with session identity redaction. |
| `GET /v1/asks?mine=true&limit=...&cursor=...` | Session, own records only, including stored answers. |
| `GET /v1/asks/{id}` | Owner Session only; answer read has no ACK or consumption side effect. |
| `POST /v1/asks/{id}/withdraw` | Owner Session, open only; `409` when not open. No portal withdraw route. |
| `GET /v1/portal/asks?state=open\|all&limit=...&cursor=...` | Portal reader, bounded list. |
| `GET /v1/portal/asks/{id}` | Portal reader, any state; supports push links and `?ask=` selection. |
| `POST /v1/portal/asks/{id}/answer` | Portal reader with BFF-derived actor, set-once CAS. Open/unexpired Ask; approval scope still future. `200` for a confirmed answer, also returning the stored Ask on an identical answer retry by the same issuer and subject; otherwise `409` for answered/withdrawn and `410` for expired. |

Another Session's Ask returns `404`, the same as an unknown ID. A caller
of the wrong class returns `403`. Withdraw returns `409` when
`EffectiveState(now)` is not open. Map Ask store errors explicitly; the
generic Kubernetes error helper turns Invalid into `422`. Session lists
require `mine=true`. Lists default to 20 records (`AskListDefaultLimit`),
at most 100, newest first; `state=open` means the effective state at read
time.

Validation errors return **400**, not 422. An identical answer retry
compares the actor's issuer/subject and answer content; another actor
cannot obtain a successful retry by choosing the same answer. The stored
answer and original `answeredAt` remain unchanged. V1 adds no revision or
stable operation-ID field to answer/withdraw. Expiry checks, CAS and
confirmed retry semantics must agree; a late expiry sweep cannot permit
an overdue answer.

Both lists order **newest first by `(createdAt desc, id)`**. The operator
selects the bounded set by labels, sorts it, and uses its own opaque
cursor. Do not rely on Kubernetes list order or use its continuation token
as the public Ask cursor. Page `limit` is at most 100. Invalid cursors
must not broaden caller scope. List responses use `asks`, `nextCursor`
and `complete`. A filtered list may change between pages; do not imply a
snapshot or turn Unavailable into an empty result.

A lost response is unconfirmed. Reconcile by Ask ID, or retry the identical
creation key/body if the generated ID is unknown. An answer retry may use
the same actor and identical answer under A9; it must not replace it or
infer success from the browser. No new key or altered scope after timeout.
Log fixed reasons and IDs, not Ask prose, raw principals or auth material.

The portal-group answer rule is the contract; **whether that answer counts
as Tom's common-rule action-specific approval once remains Q-255-04**.
Revocation authority (Q-255-01) is deferred until F2. Quiet hours
(Q-255-02) and wider transcript eligibility (Q-255-03) are also unsettled;
no route in this section resolves them.

## 4. Storage and retention

Use the protected coordination CRDs so an Ask remains readable when the
portal database is unavailable. Portal PostgreSQL retains its browser
session and private receipt role. Sessions and the portal get no direct
CRD rights; add narrow operator create/get/list and status-write rights
through GitOps.

Put `self == oldSelf` on the `AskSpec` type and make `spec` required, as
`InboxMessageSpec` and `TaskClaimSpec` do. Put the set-once rule on the
`AskStatus` type:
`!has(oldSelf.answer) || (has(self.answer) && self.answer == oldSelf.answer)`.
A transition rule runs only when the field exists in both objects, so add
a root rule that stops a status write from dropping `status`, as
`SessionDirectory` does:
`!has(oldSelf.status) || !has(oldSelf.status.answer) || (has(self.status) && has(self.status.answer))`.
In Kubernetes CEL an absent field is not `null`; journal 2579 A12's
`== null` form fails, so these rules use `has()` (correction posted on
#254). Together they keep A12: an absent answer may be set, and a present
answer cannot change or disappear.
Prove both rules with real API-server validation, including status writes.
Use bounded fields and lists so CEL stays within the API server cost
budget. Status confirmation must use authoritative reads, not an informer
cache as proof that an answer committed. V1 stores no inbox/outbox intent.

Asks are not automatically covered by DESIGN-024's existing kind registry.
Keep records until F4 extends archive validation, read fallback, metrics,
release rules and delete allowlist. Enforce admission bounds meanwhile;
do not introduce an unreviewed age-based deleter.

The proposed F4 release rule requires the original requester UID proved
absent, a closed Ask, and no active approval or pending action reservation.
Once F1 exists, each answer message must be ACKed or its recipient proved
absent. Archive the complete Ask and linked message/ACK/audit facts before
deleting any member. Preserve DESIGN-024's 30-day grace, fresh release
checks, digest verification, UID/resourceVersion delete preconditions and
indefinite private archive. Unknown proof releases nothing; a shelf outage
must not erase an Ask. Archived approvals are unusable.

Before future machine consumption, define approval-policy epoch recovery:
an etcd restore can restore an old approval or consumption fact. A
GitOps-managed epoch outside that restored ledger must invalidate old
approvals before action adapters resume. Browser-session recovery policy
does not waive this requirement. This is follow-up work, not a v1 field.

## 5. Answer retrieval; F1 inbox delivery

**V1 reads the answer from the Ask itself** through get/list and MCP
`ask_status`/`list_my_asks`. `delivery` remains empty. Lane A must neither
create nor edit InboxMessage. There is no inbox ACK to wait for, no stored
read acknowledgement and no automatic wake of a busy model. A suspended
Session can read its Ask when it returns with the original UID; never
restart it or deliver an answer to a replacement UID.

F1 is a separate **CRD change**, not unfinished v1 wiring. It needs an
operator-origin InboxMessage variant and a recipient-only scope for
Sessions without saved project snapshots. Preserve original Session-origin
schema and project rules. Bind the new origin to Ask ID/UID, immutable
answer identity/digest and exact recipient namespace/name/UID. Do not
invent a Session sender, project snapshot or provider invocation.

F1 must qualify compatible readers before emission, explicit recipient
read/ACK wrappers and archive linkage. The original proposed body
`{askId, state, answer}` is an F1 candidate, not emitted by v1. Review its
identity projection and complete encoded size against the inbox bound.
Copies into ordinary chat are data, not approval authority.

For crash recovery, atomically retain delivery intent with the Ask,
create one deterministic message, then confirm its UID and immutable
content. A crash after creation must find the same message on recovery.
At a cap, keep “Answer saved; delivery pending” and reconcile later; do
not erase an answer. Read and matching explicit ACK are separate facts;
an ACK proves neither model comprehension nor action execution. Any
future ACK projection needs reviewed schema; do not add a v1 stored state.
F2 revocation notices would need their own message and ACK facts.

```mermaid
sequenceDiagram
    participant A as Agent
    participant O as Operator and Ask store
    participant K as Keeper
    participant P as Portal BFF
    participant T as Tom
    A->>O: ask(requestId, question, scope if approval)
    O-->>A: Ask JSON, state open
    Note over A: Continue independent work
    opt Ask pushes enabled after Q-255-02
        K->>O: Read new Asks with narrow get/list Role
        K-->>T: Bounded push with portal link
    end
    T->>P: Open Ask and submit answer
    P->>P: Current session, Origin, CSRF, fresh group check
    P->>O: Answer with server-derived principal
    O->>O: Check answer-time expiry; CAS set-once answer
    O-->>P: Stored Ask
    P-->>T: Answer saved
    A->>O: ask_status or list_my_asks at safe checkpoint
    O-->>A: Ask with answer; displayName only
    A->>A: Save answer and next step in handoff
```

Push is optional. Failure never changes answer storage or retrieval.
The keeper reads the Ask CRD directly under its narrow Kubernetes Role;
the diagram's read is not a new operator-to-keeper API adapter.

## 6. MCP and harness use

Extend `agent-run mcp` and preserve existing grant tools. Use the Session's
projected token; no provider API key or inference call is needed.

| Tool | v1 contract |
|---|---|
| `ask` | Typed create intent and caller-stable `requestId`; returns the Ask JSON immediately. No revision. Include a portal link only when lane C has a configured portal origin; otherwise none. |
| `ask_status` | Read own Ask, including its stored answer. Immediate return, no ACK or consumption. Any approval validity shown is labelled computed, not a server field; use lane 0 `EffectiveState` and `ApprovalValid` rather than reimplementing them. |
| `list_my_asks` | One page of own Asks, including answered records, with the operator cursor. No all-session scan or automatic continuation. |
| `withdraw` | Withdraw own open Ask; cannot erase an answer or revoke approval. No revision/operation-ID extension is required in v1. |

There is no session answer/approve tool. The earlier story term
`list_answers` maps to the answered records returned by `list_my_asks`,
not another store. Optional bounded wait and inbox read/ACK wrappers are
follow-ups; v1 does not depend on them.

Claude Code, Codex and another compatible MCP harness use the same local
stdio command in managed configuration. **New MCP tools arrive with the
next agent image at the Session's idle drain.** They do not arrive through
a configuration reload. Preserve retained state; do not rewrite running
clients or force a restart to install them.

Instructions should have agents check answers at turn start, before a
held action and before handoff. Record the Ask ID and next safe step in
that handoff. These are retrieval checkpoints, not an unsolicited native
turn guarantee. If all remaining work depends on the answer, save
“Waiting for answer” and stop that bounded work. No unbounded poll loop.

External agents and Session-less native hosts need a separately reviewed
requester identity adapter. They cannot borrow a Session UID or pod token.
“Any agent” means any harness with a supported authenticated requester.

## 7. Approval authority, expiry and follow-up revocation

V1 records one exact action/target/expiry and has no generic executor.
Material limits must fit those fields or be clearly bound to the reviewed
artifact. A wildcard or “approve my plan” cannot define an exact action.
A changed scope needs a new Ask. Each future machine-consumable action kind
needs its own typed validator and enforcement adapter: for example PR
number/head SHA, grant UID/role/TTL, or budget ledger UID and numeric caps.
Missing limits never mean unlimited.

Only a human currently admitted by the portal group may answer. The BFF
checks `portal_is_active` and `portal_is_admin` for that exact request-private
session before form rendering and immediately before dispatch. Preserve
session revocation, expiry, subject and token-digest checks around
UserInfo. An agent/coordinator cannot approve its own or another Ask;
a relay may link to the Ask but cannot synthesize the portal principal.

At answer time the Ask must be open/unexpired and approval scope still in
the future. **No revocation exists in v1**, so `scope.expiresAt` is capped
at `createdAt + 24h` and Ask `expiresAt` cannot exceed scope expiry.
Clients display validity as **computed** from
`approved && now < scope.expiresAt` (lane 0 `ApprovalValid`); it is not a
server field. Deny is an
answer, not an active approval. An expired approval is unusable even
though the historical answer remains visible.

Logout or group removal blocks later portal operations on fresh checks;
it does not rewrite a historical answer. The record supplies no credential
or machine action permission. Q-255-04 asks whether Tom recognizes an
unexpired approval as his action-specific approval once under common
rules. That decision is pending; do not activate that interpretation from
this design or spend an approval twice.

F2 adds revocation, audit and any required revision/digest/operation fields.
**Who may revoke is deferred Q-255-01**, not “any member” as an adopted
rule. A proposed audit binds group-policy/check time, issuer/subject,
Ask/requester UID, canonical scope/digest, policy epoch and operation.
Bound it to a small fixed number of audit entries and retry attempts;
final limits need F2 wire review. No such fields are claimed in v1.

Later consuming adapters must reread validity and target preconditions,
CAS-reserve one use, and record executor/applied/failed/unknown result.
Unknown cannot trigger action replay. Revocation before reservation must
prevent use; after reservation it can only request cancellation and report
confirmed, too late or Unknown. It cannot undo an action already started,
remove a copied credential or refund a spent budget. Restore/epoch behavior
must be qualified before these adapters ship. Existing grant broker,
budget ledger and native-decision enforcement remain authoritative.

## 8. Portal board and Needs attention

Add **Asks** to the shared mobile/desktop shell. Default to open Asks with
an all-state view. Use the portal resource route
`/portal/projects/dev-env/environments/internal/asks?ask=<id>` for selection
and same-origin `POST .../asks/{id}/answer` for the form. The BFF uses the
portal detail API to read a selected Ask in any state, including push
links to an already answered Ask. No portal withdrawal control or route.

A card shows question, requester, work item/links, recommendation, deadline
and age. Render URLs under A7, including the plain host for untrusted link
hosts. Selected detail shows full exact scope before the answer controls.
Fetch compatible conversation on demand under the current separate
history authority; missing history cannot prevent reading or denying an
Ask. T4 is not a prerequisite for the board.

For approval Asks, **Approve and Deny are the primary controls**. Neither
is preselected. Options are secondary and cannot contradict the decision:
for example, an option named `reject` with Approve is refused or warned.
Approve/Deny alone suffices without any option. Allow an optional note
even when `allowFreeText` is false. Info answers use the option/free-text
rules. No choice label or text is treated as an implicit boolean.

Use the encrypted browser session, `__Host-` cookie, exact Origin, bounded
form keys/body, constant-time CSRF check and request-private principal.
Fresh UserInfo immediately before dispatch must refer to the same handle,
principal and token digest. No actor from the browser. GET, push links and
prefetch cannot answer. Expired login requires a new form; no queued offline
approval. Bind private form receipts to that browser session under the
existing receipt pattern. A lost response offers a status read or the
identical answer retry allowed by A9, not a changed answer.

Show “Answer saved” in v1. Computed approval expiry is separate from the
historical answer. Delivery pending/delivered/ACK/revoked/consumed displays
wait for the relevant follow-ups. A follow-up question is a new linked
Ask, not an answer edit.

Needs attention should eventually include each visible open Ask and its
requester's “Waiting for you” link while preserving other liveness and
attention reasons. This describes a human dependency, not a claim the
whole Session is stopped. Cross-page projection is F3. Lane D's first
increment keeps its count on the Asks board under #258 journal 2553;
it must not claim the full projection is already usable. An absent
requester's Ask still appears on the board.

Use a single column at phone width, touch-sized controls, visible keyboard
focus and labelled fields. Scope must not need horizontal scrolling.
Escape prose; no active Markdown, remote images, thinking, raw tool output
or executable attachments. Refresh shows current facts; no background
polling is required. Unavailable counts never render as zero.

## 9. Notifications

The keeper sends Pushover notices using the existing sender and
[DESIGN-031](031-pages-lookup.md) outcome vocabulary. Add a **narrow keeper
Role to get and list Asks** in `dev-agents`, rather than an
operator-to-keeper adapter. It grants no answer/status-write right and no
general inbox access. Notification credentials stay in the keeper.
Its read authority and network policy need GitOps review; the notice and
page record still contain no Ask text or approver identity.

Use a fixed title/body such as “An agent needs your answer” and a link to
the configured HTTPS portal origin and selected Ask. No question text, PR
title, principal, token or signed approval in the push. The link opens an
authenticated GET; it never answers or approves. Record Ask ID and event
kind as correlation. Pushover acceptance is not phone delivery.

Proposed caps: one new-Ask push per Ask, at most one per requester per five
minutes and six per recipient per hour. Persist deduplication/rate state
across keeper restarts, in a keeper-owned ConfigMap that holds no Ask
text; lane B's PR proposes its shape. Coalesce within those caps; never hide the durable
question. An ambiguous send response is Unknown, not success; do not
repeat it automatically. A definite pre-send failure may retry once after
five minutes while open and within caps. Push success does not affect
Ask storage or answer retrieval. Keeper login notices remain unchanged.

**Q-255-02 is unanswered.** The source premise is **no quiet-hours code
exists**; this is a new Ask notification policy, not reuse of an installed
schedule. Propose priority 0, 09:00–21:00 America/New_York, DST-aware
deferral and no agent bypass. Ask Tom before lane B goes live. Tests and
default-off source work may proceed; live pushes wait for the answer.
This section settles neither quiet hours nor Q-255-01/03/04.

## 10. T4: transcripts for compatible Sessions (not v1)

**T4 is outside v1 and awaits Q-255-03.** Propose default-off
`eligible-registered` history for the configured `dev-env/internal`
resource. It removes per-UID history edits only within that resource;
it adds no cross-project, all-namespace or send scope.

Eligibility must satisfy all of these:

- An **operator-owned profile allowlist: `full`, `dev`**. `workbench` is
  opt-in, not implicitly included. Exclude Sessions holding credential or
  break-glass grants, even if their profile would otherwise qualify.
- Operator-managed namespace and declared repository/project scope,
  including only explicitly covered projectless dev-env Sessions.
- Fresh original Session UID and current controlled Pod checks, Running
  and non-deleting, as the conversation route requires.
- Compatible agentd registered-conversation reader; original/current
  registration, conversation and worktree agree, current Pod is bound,
  and the provider adapter validates the transcript format.
- Current human passes fresh portal-group checks before fetch and release,
  and the operator/BFF resource policy agrees.

Operator policy selects eligibility, never agent labels, supplied paths
or a reusable name. Preserve exact targets until this mode is reviewed and
activated. Missing/legacy/unregistered/replaced/out-of-profile or
credential-bearing Sessions show Unavailable. Do not scan homes, adopt an
unregistered file, read raw logs or use archive/inbox as a transcript
fallback. No restart or rebind to make a Session eligible.

Keep current plaintext roles/redaction, no thinking/tool bodies, 60 turns,
4 KiB per turn and 96 KiB total rendered text. Preserve bounded fetches,
coverage and source times. Current cursors bind a complete transcript
snapshot; appends invalidate them and the BFF fetches the newest page.
T4 does not establish stable growing-history pagination. Suspended/archive
history and new provider adapters need separate contracts.

This proposal expands transcript exposure to future qualifying Sessions
within the allowlist. Redaction cannot guarantee removal of private text
or credentials. Keep no-store responses and content-free access audit.
Fresh group removal cannot retract already shown text. Tom must decide
Q-255-03; this design does not settle that expansion.

## 11. Migration and outage behavior

After usable acceptance and Tom's decision on Q-255-04, propose board use
for routine questions and bounded approvals. Change shared instructions
through GitOps; do not remove the phone-question rule based on source
alone. V1 acceptance requires an actual agent reading its stored Ask
answer, not an inbox ACK. Harness security prompts and action permissions
remain separate.

During outage, reconcile uncertain creation/answer by its stable key/ID.
Preserve it in OpenProject and handoff; continue independent authorized
work. Use a verified supported native phone prompt for urgent questions,
one at a time. Record actual source/scope; never fabricate a portal
principal from a fallback answer. Reconcile or withdraw the original
Ask when reachable, and never spend one approval twice.

Codex has no verified native phone fallback here. The lead owns delivery
of pending questions through its verified route. Keep dependent work held;
do not invent a notification channel, repair credentials or treat a tracker
comment as action-specific approval. An outage does not reset authority
already granted in the session.

## 12. Build stories and acceptance

Reuse canonical lanes #256–#259. Journal 2539 gives custody and merge
order: lane 0, then A, then C/D/B (B after A). Lane 0 (#656,
`7057c17`) carries 2579 and 2595; old lane heads are not evidence that
v1.2 is implemented. All features are default off. The lead owns
independent review, UI QA and integration.

| Story | Dependency | Outcome and evidence |
|---|---|---|
| Lane 0 — shared wire types (merged #656) | 2539, 2556, 2579, 2595 | Routes, bounds and validators match v1.2, including removal of portal withdraw, 24h approval cap and `ValidateForAt`. Source merged. |
| [#256 — lane A backend](https://work.haynesnetwork.com/work_packages/256) | Amended lane 0 | CRD/CEL/store/API behind flag. Prove idempotency/status codes, ownership, session redaction, portal detail, answer-time checks/CAS, uncached-list room limits (16 open, 512 total, 4096 namespace; `429`/`503`; identical retry resolved first) and sorted paging. No InboxMessage create/edit; delivery empty. Generated schema/layout/focused tests pass. |
| [#257 — lane C MCP](https://work.haynesnetwork.com/work_packages/257) | Lane 0; A for integration | Four immediate tools and TLS/refusal/retry fixtures; Ask JSON, no revision, link only if configured. Session actor projection accepted; validity labelled computed. No Kubernetes imports. |
| [#258 — lane D portal](https://work.haynesnetwork.com/work_packages/258) | Lane 0; A for integration | Detail route, same-origin answer, CSRF/session/current-group checks, A7 links, primary Approve/Deny plus notes/secondary-option safeguards, escaping/replay/stale-session tests. Lead QA at 320/390/1280, light/dark. Initial count is board-local. |
| [#259 — lane B keeper/expiry](https://work.haynesnetwork.com/work_packages/259) | A merged; D link; Q-255-02 before live | Narrow keeper get/list Role and bounded pushes. The leader-only expiry sweeper runs in the operator, which alone writes Ask status. Fake clock/sender checks rate limits, restart dedupe, ambiguity and chosen quiet-hours policy. Default off; login notices unchanged. |
| F1 — operator-origin inbox delivery | A | Separate CRD origin and projectless recipient scope, compatible readers, deterministic recovery and explicit message/ACK linkage. Crash/cap/suspend/absence/replacement/wrong-ACK tests. No fabricated Session sender or provider wake. |
| F2 — revocation and audit | A/D; Q-255-01 when F2 is picked up; F1 for notices | Reviewed revoke authority, route/form, audit and retry fields. Prove forged actor, swapped session, group removal, stale scope, races and lost response handling. Revocation remains visible even when notice delivery fails. No action adapter. |
| F3 — Needs attention and agent retrieval | C/D; F1 for inbox wrappers only | Cross-page Waiting for you; qualified Claude/Codex post, continue, read/save answer and resume without reissuing. Explicit inbox wrappers only after F1. No automatic wake. |
| T4 — resource history eligibility | Q-255-03; current history contract | Operator-owned full/dev allowlist; workbench opt-in; credential/break-glass grants excluded. Multiple qualifying Sessions without UID list; incompatible/out-of-scope/replaced Pod unavailable. Send unchanged. Not v1. |
| F4 — Ask coordination retention | A; F1/F2 linkage when present | Extend DESIGN-024 registry/archive/read fallback. Prove live/Unknown requester refusal, full linkage, grace and digest checks. No deletion until separately qualified/enabled. |
| F5 — v1 GitOps installation | Combined lane CI/review, signed images, runtime authorization; Q-255-02 for live B | Schema/readers before writers, narrow operator/keeper rights and isolated BFF identity. Record exact images/effective identities/flags/rollback floor. MCP image reaches sessions at idle drain. No dependency on F1/F2/T4 to claim only v1. Preserve running Sessions and holds. |
| F6 — v1 owner acceptance | F5; separately authorized disposable Sessions | Tom answers on phone; Claude/Codex read the Ask, save answer and continue. Demonstrate Deny, expiry, identity redaction, lost-response reconciliation and push failure. No inbox/ACK/revocation/T4 claim. Record source/tested/installed/usable separately. Q-255-04 precedes common-rule approval migration. |

Follow-up labels are design references, not another task queue. Search
before filing each bounded story under Epic #254; record scope, acceptance,
prerequisites, owner/custody, budget, stop criteria and handoff before
pickup. Estimate a source follow-up at two hours/one worker with a
90-minute checkpoint; actual usage cost/quota is unknown. Split if needed.
Tests use at most `DEV_ENV_CPU_LIMIT` workers, one suite at a time; no
CPU burners, stress or wide test loops. Do not start another story at stop.

Each grant, budget or native-decision consuming adapter needs a separate
story and authorization after its prerequisites. Prove exact typed scope,
one-use reservation, Unknown without replay, revocation races where
supported and restore/epoch invalidation. No generic approve-and-run tool.

## 13. Questions for Tom

These IDs belong to #255. Q-255-04 has the dated ruling below; the other
questions remain unresolved. The lead must deliver active questions one at
a time through its verified phone route and record actual answer/source/date
here and in #255. Open questions do not block this docs PR; they hold only
their dependent behavior. Do not send this table as one prompt or reopen a
deferred question early.

| ID / status | Question and recommendation | Alternative / gate |
|---|---|---|
| Q-255-01 — **DEFERRED until F2** | May any current portal-group member revoke an unused approval given by another member? Recommend any admitted member, with revoker/time/reason retained. This is a proposal, not current authority. | Original approver only. Ask when F2 begins; no v1 revocation. |
| Q-255-02 — pending | No quiet-hours code exists. Should new Ask pushes wait outside 09:00–21:00 America/New_York? Recommend daytime only, priority 0, DST-aware deferral and no agent bypass. | All hours within caps. Ask before lane B goes live; source/tests may proceed default off. Login notices unchanged. |
| Q-255-03 — pending | May the portal expose transcripts for all compatible Sessions in the resource without per-UID entries, with an operator-owned full/dev profile allowlist, workbench opt-in and exclusion of credential/break-glass grant holders? Recommend that bounded eligibility. | Keep exact targets. Holds T4, which is not v1; Ask board/history links use current scope meanwhile. |
| Q-255-04 — **RULING, 2026-10-29** | Tom, OpenProject #255 journal 2680: “It's situational, I may be asked to approve binding changes that are beyond one request.” Also: “I don't want to have to approve the same thing over and over” and “here I do not want to approve agents to always send emails just that I approve the one that was drafted.” A v1 portal Approve covers one exact action, target and content, once, within expiry. It never becomes standing permission. | A standing rule is a separate approval kind: approve a scoped policy once, then list, manage and revoke it. See [DESIGN-036](036-standing-rules.md). Journal 2680 supersedes journal 2675, retracted in 2676. This records the distinction; it grants no credential, removes no other hold and creates no executor. |

The dated Q-255-04 ruling supersedes this document's earlier references to
that question as pending. The quotations are excerpts; operational examples
are omitted. One-off use and standing-rule management are separate. Source,
installation, common-rule instruction migration and usable acceptance still
need their own reviewed delivery; this docs update changes no live rule.

The v1 bounds/routes above come from the lead contract. Future audit,
retention and action-enforcement details remain proposals for their own
reviewed stories, not settled owner decisions.
