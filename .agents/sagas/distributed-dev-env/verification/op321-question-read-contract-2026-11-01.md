# OP321 portal question-state read contract — 2026-11-01

Source contract only. It does not deploy, release, accept the production UI,
or authorize a new read. Question-state production UI stays open. No runtime,
quota, history, or account read was made. `.git/HEAD` was not readable from
this writer; the dispatch pin is `7e581c7555a969ea78359d85ba5ceb62cfb3e9f8` on
`agent/op321-read-contract-grok-1101-102716`. The target path was absent. No
other file was edited, and no tracker hold was written.

## Answer

The smallest connected contract is a new, default-off portal GET that returns
one proven session’s live inbox box or sent box as metadata. It calls the
installed `inboxThread` / `pageReplies` / `KubeStore.List` / `ListSent` path
and copies that result. It does not recompute state, and it does not add a
second projector.

There is no existing portal callsite that may do this. `kindPortalReader` may
not call `/v1/inbox`. Putting thread fields on `GET /v1/sessions` is the wrong
shape: the current portal decoder rejects unknown page and row keys, and a
session page is not an inbox list.

Human Asks (`/v1/portal/asks`) are a different store. `metadataReader` is not
on the production path.

## Why the portal reader has no inbox read

Production wiring is `LoadProduction` → `productSources` →
`optionalSessionSource` / `optionalTaskSource`. The assembly sets
`Reader: portal.UnavailableReader{}`. `newMetadataReader` is not called.
`SessionSource.read` GETs only

`https://dev-env-operator.dev-env-system.svc.cluster.local:8443/v1/sessions?limit=50&view=activity`

with `X-Dev-Env-Task-Outcome: v1`. `decodeSessionPage` / `decodeSessionRow`
accept the six summary keys plus optional `activity`. `TaskSource` reads
OpenProject project 3 work packages. Neither client calls `/v1/inbox`.

`legacy metadataReader.sessionSummaries` GETs `/v1/sessions?repo=dev-env`.
`portalPageQuery` rejects `repo`. That reader is unwired and already
incompatible with the portal session query.

Operator classification:

- `resolveCaller` maps `dev-agents/dev-env-v2-portal` to `resolvePortalReader`
  only when `Policy.PortalReaderEnabled` is set. The pod token must match the
  live pod UID and ServiceAccount. `ValidatePortalCaller` refuses that account
  as human, client, session, workbench, or coordinator.
- `serve` then allows `kindPortalReader` only for: `GET /v1/sessions`;
  `POST` portal messages when targets are configured; `POST` portal launch
  when repos are configured; `GET` a configured conversation; and
  `portalAskRoute` when the Asks store exists. Every other path returns
  403 `the portal reader may only list session summaries`.
- `listSessions` for that kind calls `listPortalSessions`, which lists
  `AgentSession` objects and projects `portalSummary` / `portalActivity`.
  It does not touch the inbox store.
- `--enable-portal-reader` is documented as session summaries only, and it
  requires `--session-namespace=dev-agents`.

Inbox classification is a different class:

- Routes are `GET/POST /v1/inbox`, `GET /v1/inbox/{message}`, and
  `GET/POST /v1/inbox/{message}/ack`, marked `inbox: true`.
- `inboxReadScope` admits a coordinator only when the project is in
  `InboxCoordinatorReadProjects` and `claimCaller` succeeds. That scope has
  a nil recipient, so `KubeStore.List` returns the project’s messages, and
  `box=sent` is refused (`a coordinator host sends none`).
- Any other kind goes through `inboxRecipientCaller`, which requires
  `kindSession`, the original session UID, and a current non-deleting pod
  controlled by that session. `kindPortalReader` fails closed as
  `inboxHidden` (404). Publish and ack also require `kindSession`.
- With the portal reader enabled, the allowlist answers 403 before the
  handler. With it disabled, `resolveCaller` 403 is rewritten to inbox 404
  because the route is `inbox: true`.

DESIGN-014 states that claim metadata and broad session read/reap rights do
not grant inbox text, and that content read belongs to the original recipient
UID or a configured coordinator project. The 2026-11-01 audit slice says the
CLI work does not provide the held portal inbox read permission and does not
close OP321’s installed UI.

`TestPortalReaderSessionSummaries` shows the enabled portal token listing
bounded session summaries, and 403 on session create, session detail, and
task claims. It does not open `/v1/inbox`.

## Installed derivation to reuse

Do not reimplement these.

| Behavior | Function |
| --- | --- |
| Thread and sent queries; questions flag required | `inboxQuery`, `inboxQueryGated` |
| Recipient vs sender lists, metadata only | `KubeStore.List`, `KubeStore.ListSent` |
| One reply index per page, then matched reads | `pageReplies`, `KubeStore.ReplyIndex`, `KubeStore.RepliesIn` |
| Derived state, waiting owner, ack | `inboxThread`, `KubeStore.ReadAck` |
| Ended vs resumable | `inboxRecipientState` |
| Wire object the CLI already checks | `apiv1.InboxThread`, `printInboxAudit`, `auditThread` |

`inboxRecipientState` (`internal/apiserver/inbox_thread.go`): missing,
UID mismatch, deleting, phase `Archived` or `Failed` is ended. A `task`
whose agent status is `exited`, `failed`, or `interrupted` is ended with no
phase check, so phase `Running` does not keep it live. Any other readable
session in the session namespace, including phase `Suspended`, is live.
`remote` and `local` are not ended by those agent statuses. An unreadable
Get stays `inboxEndedUnknown`. `inboxThread` maps that to `Unknown` and does
not guess `Unanswered`. `WaitingOn` is set only for `inboxRecipientLive`.

`TestInboxThreadStates` sets phase `Suspended` and still expects
`Unanswered`, then `Stale` after `inbox.StaleAfter` (30 minutes, 1800
seconds). The same test sets `AgentExited` on a fixture session created as
`Mode: "task"` (`fixture.sessionPod`) and expects `RecipientEnded` with no
`WaitingOn`, without moving the phase to `Archived` or `Failed`.
`TestInboxArchivedOrFailedRecipientEnded` covers those two phases.
`TestInboxThreadUnknownWhenRecipientUnreadable` expects Unknown.

List responses are `InboxMetadata` (no `text`). `readInbox` adds `Text`.
The portal read must use the list functions only. `view=thread` is opt-in;
without it the older list shape is unchanged. The CLI asks for
`view=thread`, retries the plain list on 400, and refuses `--sent` unless
`box` echoes `sent`. Audit output prints server state only when kind and
refs agree, and its footer says an API ack is not provider consumption or
approval.

`pageReplies(project, &session, sent)` limits the index to that session
(`from` on the inbox box, `to` on the sent box). A nil caller indexes every
reply in the project. The portal must pass the proven session identity, not
the coordinator nil.

## Identity, route, and project constraints that stay

- Portal account: `dev-agents/dev-env-v2-portal`, pod-bound token, class
  `portal-reader`. It stays off the human, client, session, and coordinator
  lists.
- Session inventory stays `GET /v1/sessions` with `limit`, `cursor`, and
  `view=activity` only. Unknown query keys are refused. Summaries expose
  name, UID, repo, phase, observation, and the activity allowlist. Repo is
  not the catalog project. `sessionsPage` groups by `s.Repo`.
  `ProductFilter.Project` is that repository. `ProductFilter.Session` is a
  UID for conversation load and does not narrow the list.
- Inbox project scope is `claimSnapshot` → `AnnotationProjectSnapshot`
  (`dev-env.haynesops.com/project-snapshot`), and `snapshot.Project()` must
  match the message project. `claimSnapshot` also requires
  `snapshot.Selected().Name == spec.repo` and no workspace. Fixture claims
  use project `sample` and repo `alias`.
- Durable inbox and questions stay behind `--enable-durable-inbox`,
  `--inbox-projects`, and `--enable-inbox-questions`. Coordinator content
  read stays `--inbox-coordinator-read-projects`. None of these name the
  portal ServiceAccount.
- A replacement session with the same name and a different UID does not
  inherit the inbox. `inboxReadFence` repeats the scope after thread reads.
- Archive list (`archived=only`) rejects the thread query. This contract
  does not add an archive scan. A single archived thread read already
  returns correlation with state `Unknown`.

## Additive producer and consumer

### Producer

New route, registered only with a new default-off switch, for example
`--enable-portal-question-read`, which also requires
`--enable-portal-reader`, `--enable-durable-inbox`, and
`--enable-inbox-questions`. Pair it with an explicit project list,
`--portal-question-read-projects`, empty by default. Membership in
`--inbox-coordinator-read-projects` does not admit this route. The portal
account is not added to coordinator hosts.

`GET /v1/portal/question-state`

Query, each key once: `session` (DNS name), `uid`, `box` (`inbox` or
`sent`), `view=thread`, optional `limit` 1..100, optional `continue`.
No body. `serve` gains one allowlist arm for this path and method. No
publish, ack, message text, or `archived` key.

Handler order:

1. Caller is already `kindPortalReader` (`resolvePortalReader`).
2. `inboxAvailable()` and the questions flag. If questions are off, refuse.
   Do not fall through to a plain inbox list.
3. Live Get of `AgentSession` in `dev-agents`. Name and UID must both match.
   A miss is the same hidden response as a foreign inbox read.
4. `claimSnapshot`. `snapshot.Project()` must be in the new project list and
   in `InboxProjects`. Selected repo must match `spec.repo`.
5. Build `coord.SessionIdentity` from that object. `box=inbox` calls
   `KubeStore.List(project, &id, ...)`. `box=sent` calls `ListSent`. Then
   `pageReplies(project, &id, sent)` and `inboxThread` per item, with one
   shared `inboxRecipientState` memo. Drop items whose kind is not
   `question` or `reply` (`inboxThread` returns nil for plain).
6. Fence the same name, UID, and snapshot project. A change hides the page.
7. Copy name, UID, kind, sender and recipient namespace/name/UID,
   `inReplyTo`, and the thread fields `inboxThread` already set: `state`,
   question ref, reply ref, `replies` when greater than one, `waitingOn`,
   `staleAfterSeconds`, `observedAt`. Echo `contract`, `view=thread`,
   `box`, `project`, and session name/UID.

Omit `text`, `key`, actor, provenance, provider, requested model,
repository text, and acknowledgement actor. `Acknowledged` remains the
existing API-ack state. The handler does not read history, provider output,
or message bodies, and it does not map `agentState`, phase, or
`AgentWaiting` onto an inbox state.

Suggested response object, beside the existing inbox types, not inside
`PortalSessionSummary`:

- `contract`: `question-state.v1`
- `view`: `thread`
- `box`: `inbox` or `sent`
- `project`, `session.name`, `session.uid`
- `items[]`: message name/UID, kind, sender, recipient, `inReplyTo`, `thread`
- `continue` when a page remains

`thread.state` is only the six existing words: `Unanswered`, `Stale`,
`Answered`, `Acknowledged`, `RecipientEnded`, `Unknown`.

### Consumer

A separate client on `SessionSource`, using the same operator token, CA, and
pinned host as `sessionGet`. Call it only after a successful inventory, for
one row the browser already holds (name and UID from that
`PortalSessionSummary`). Failure, 403/404, or a missing echo becomes
“question state not connected” on that row. It must not fail
`decodeSessionPage` or replace the Sessions page.

The client checks: HTTP 200, JSON, `contract=question-state.v1`,
`view=thread`, `box` equal to the requested box, echoed session name and UID
equal to the row, project non-empty, item count ≤ limit, and thread state in
the six-word set. Apply the same ref agreement as `auditThreadTrusted`: a
question’s thread names that message; a reply’s thread names that message
and its `inReplyTo` names the question. A mismatch is Unknown for that item.
Render `waitingOn` only when the server sent it. Do not infer waiting from
`sessionAttention` (“Waiting for your input” is `AgentWaiting` and carries
no question text).

Two GETs (inbox and sent) match the installed split. One combined body would
be a new projector.

### Compatibility

- Absent switch: route not served. Current `GET /v1/sessions` bytes stay as
  they are. `decodeSessionPage` exact keys stay. An older portal never sends
  this request.
- A newer portal against an older operator gets 404 or no echo and shows
  not connected. An empty `items` array is valid only when `contract` and
  `view` echo. Silence is not an empty inbox.
- Echo `box` the way the CLI echoes `box=sent`, so an ignored query cannot
  present the inbox box as the sent box.
- No CRD field is added. `kind` and `inReplyTo` already exist. This route
  does not publish, so the schema dry-run on publish stays the questions
  rollout rule.
- `PortalSessionPageMaxBytes` (512 KiB) stays the session-page cap. Question
  pages use the inbox list cap (100) and stay off that object.

## Source work and the activation decision

Ordinary source work, still unauthorized until the decision below: the new
apiv1 struct, the handler that only calls the functions above, the `serve`
arm, default-off flag parsing, the portal client and strict decoder, and
targeted tests. A DESIGN-014 amendment belongs in that same change. Default
remains off, so merging source does not let the portal read messages.

Activation is a separate owner decision. It is not granted by
`--enable-portal-reader`, `--enable-durable-inbox`, `--enable-inbox-questions`,
`--inbox-projects`, or `--inbox-coordinator-read-projects`. Turning it on
means setting the new switch and the explicit project list in the operator
deployment, then running a portal image that calls the route. This note does
not approve that change.

Source DESIGN-014 says the operator already lists and reads inbox records, so
this route indicates no new Kubernetes Role and no CRD change. This research
did not read the live Role. If that Role were absent, activation would also
need a reviewed Role; the source contract does not show that gap. The new
authority is the API class, not a second store.

No existing allowed callsite can be cited for the portal. The allowed portal
GETs remain `listPortalSessions` and, when separately configured, conversation
and portal-ask reads.

## Regression scenarios

1. Portal token, reader enabled: `GET /v1/inbox` and `GET /v1/sessions/{name}`
   stay 403; `GET /v1/sessions?view=activity` still omits prompt text. Reader
   disabled: inbox classification stays the hidden 404, not a portal page.
   Adding the portal account as a coordinator or client still fails
   `ValidatePortalCaller`.
2. Same `inboxThread` result the CLI lists: task `AgentExited` / `Failed` /
   `Interrupted` is `RecipientEnded` with no `WaitingOn` while phase is still
   `Running`. Phase `Suspended` on a task, and `Suspended` on `remote` or
   `local`, stays `Unanswered` or `Stale` with `WaitingOn`. Portal output
   matches those words and does not recompute them from phase.
3. Questions switch off, or `view` / `box` / `contract` echo missing: the
   question panel is not connected, and the Sessions inventory still renders.
   `AgentWaiting` stays the existing attention sentence, not an inbox state.
4. Same session name with a different UID, a snapshot project outside the
   new list, or a repo that disagrees with the snapshot: one hidden response,
   no item metadata. A project present only on
   `--inbox-coordinator-read-projects` does not admit the portal. The nil
   recipient list is not used.
5. Reply index or session Get failure yields `Unknown`, never `Unanswered`.
   The question-state JSON has no `text` field. A stray key on
   `GET /v1/sessions` still fails `decodeSessionPage`. An API
   `Acknowledged` value is not rendered as approval.

## Follow-up files

Owned by the later source change, not by this note:

- `internal/apiserver/apiv1/inbox.go` — response struct and path constant
  only, or a sibling file if inbox.go must stay the session/coordinator wire.
- `internal/apiserver/server.go` — one `kindPortalReader` allowlist arm.
- New `internal/apiserver/portal_question_state.go` — the handler sequence
  above. It calls `inboxThread` and the store; it does not copy the state
  switch.
- `cmd/dev-env-operator/main.go` — default-off flag and project list,
  rejected unless portal reader, durable inbox, and inbox questions are on.
- `web/portal/session/product.go` — failure-isolated client. Leave
  `decodeSessionPage` key sets unchanged.
- `web/portal/session/product_activity.go` — display copy of server state
  only, after a successful echo.
- Tests: `portal_reader_test.go`, a portal-token case next to
  `inbox_askreply_test.go`, and a product decoder case for echo failure.
- `.agents/sagas/distributed-dev-env/designs/014-durable-offline-inbox.md` —
  amendment in that change. This verification file is the research record.

## Gates

| Gate | State |
| --- | --- |
| This document | Research record. No implementation. |
| Source merge of the handler | Future work. Flag default off. Tests are the five scenarios above, not a wide suite. |
| Installed operator / portal | Unchanged. No flag, CRD, RBAC, ACL, ConfigMap, or network change follows from this file. |
| OP321 production UI and cross-provider acceptance | Open. CLI audit consumption is not portal acceptance. |
| Authorization | The proposed route is not authorized. Activation is the explicit flag and project-list decision above. |
