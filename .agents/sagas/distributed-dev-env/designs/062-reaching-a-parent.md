# DESIGN-062: Reaching a parent

- **Status:** Accepted 2026-11-10 by the coordinator (story OP545), with the
  rulings in section 12. Nothing is built, switched on or changed by this
  document.
- **Tracking:** [Story OP545](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/545),
  under [Epic OP516](https://work.haynesnetwork.com/work_packages/516).
- **Origin:** Tom, 2026-11-10, typed in coordinator session
  dev-env-fable-coordinator-1109: "agents must be able to reach Meatball and
  other parent coordinators via dev-env capabilities so we need to sort that
  out".
- **Builds on:** [DESIGN-014](014-durable-offline-inbox.md) (the inbox record),
  [DESIGN-060](060-meatball-front-door.md) (the front door and CallerPolicy),
  [ADR-004](../adrs/004-coordination-record-retention.md) (how long a record
  stays), [ADR-020](../adrs/020-task-session-cleanup.md) and
  [DESIGN-061](061-task-session-cleanup.md) (a task session goes away after 7
  days).
- **Decision:** this changes who may address whom, and the inbox API. It needs
  ADR-023, written with the first build slice (section 11). This document does
  not write that ADR. ADR-004 is not edited.
- **Evidence:** dev-env `d6440863` (origin/main; every line below). The live
  operator image `ghcr.io/thaynes43/dev-env-operator:sha-6a8541b` has no diff
  against that commit in the inbox, caller, deliver or podcmd files. The live
  front door image `sha-8b23ee1` has the same ten tools as
  `internal/frontdoor/tools.go`. Live reads at 2026-11-10T15:13Z, `kubectl get`
  only, no Secrets.

## 0. In short

- One command, `agent-run parent tell`, puts a question, a result or a blocker
  on the recorded parent. The caller names no recipient. The parent may be
  another session or the Meatball ServiceAccount.
- The record is an InboxMessage. The store stays. The schema gains a way to
  name the Meatball account, and a way for that account to write back.
- A session parent reads and answers with `agent-run parent`. Meatball does
  the same four things through the front door, and only for sessions it
  started.
- The child learns the answer from one held read, not from a sleep loop. A
  headless task can run that read. `agent-run msg` stays unable to deliver
  into a headless task.
- A note's text is never typed into a parent's screen. A live Claude TUI
  parent is woken by one fixed line with no note text, and only while it is
  not waiting on a person (section 5 and ruling 1). Meatball polls. Nothing new pushes
  into its chat.

## 1. Outcome

A session can leave a question, a result or a blocker where its parent will
find it, without knowing whether that parent is a session or Meatball.
Meatball can list, read and answer what its sessions left, and can send one
message to a session it started. A session sees the answer at the next step
that asks for it. The Asks board stays Tom's.

## 2. How one party reaches another today

Live operator flags that this section depends on:
`--enable-durable-inbox=true`, `--inbox-projects=dev-env`,
`--enable-inbox-questions=true`, `--inbox-coordinator-read-projects=dev-env`,
`--enable-asks=true`. The keeper has `--enable-ask-notifications=true`.

### 2.1 agent-run msg

`agent-run msg` posts once to `POST /v1/sessions/{name}/messages`
(`internal/agentrun/podcmds.go:72-108`, `internal/agentrun/client.go:68-72`).
The API runs `agentd ctl deliver` in the target pod
(`internal/apiserver/podcmd.go:198-246`, route at
`internal/apiserver/server.go:327-329`).

| | |
|---|---|
| Who may send | Any authenticated caller the route accepts. A session may name any session. A coordinator host may name only a session whose parent is that host (`internal/apiserver/podcmd.go:92-93`, `internal/apiserver/coordinator.go:84-92`). The comment on the handler says any caller may message any session (`internal/apiserver/podcmd.go:198-201`). |
| Who is refused before delivery | Meatball's CallerPolicy caller. Its allow list is create, list, get and delete of sessions (`internal/apiserver/callerpolicy.go:132-145`). This route is not on it. |
| Who may be addressed | A session name. The API looks up an AgentSession (`internal/apiserver/podcmd.go:216`). A ServiceAccount is not a name it can look up. |
| What is stored | Nothing. The paste is not an InboxMessage. The API log line is the session, the caller and the byte count (`internal/apiserver/podcmd.go:245`). |
| How the recipient learns | The text is pasted into the TUI and Enter is pressed (`internal/agentd/deliver.go:145-164`). The envelope says the text is information from another party (`internal/agentd/deliver.go:52-60`). That envelope is inside the paste. |
| Headless task | Refused. The launch has `tui` false (`internal/agentd/launch.go:87`). Deliver returns not-addressable (`internal/agentd/deliver.go:118-119`). The API turns that exit into 409 (`internal/apiserver/podcmd.go:239-240`). agent-run prints the API refusal (`internal/agentrun/client.go:314-316`). |
| Busy TUI | Same paste and Enter, whenever the agent process is alive (`internal/agentd/deliver.go:114-120`). The code does not read busy or idle. A deliver lock keeps two pastes from becoming one (`internal/agentd/deliver.go:45-50`, `internal/agentd/deliver.go:124`). |
| Idle TUI | The same paste and Enter. Idle is not a separate branch. |

A pod that is missing, not Running, or not Ready is also 409, from
`runningPod` (`internal/apiserver/podcmd.go:48-65`), before deliver runs.

### 2.2 The durable inbox

Commands: `agent-run inbox send`, `ask`, `reply`, `list`, `read`, `ack`
(`internal/agentrun/inbox.go:34-42`). They call `POST /v1/inbox` and
`GET /v1/inbox` (`internal/apiserver/server.go:297-299`). The stored type is
`InboxMessage` (`api/coordination/v1alpha1/inbox_types.go:25-58`). It keeps
project, key, sender session, recipient session, actor, catalog provenance,
provider, requested model and text. Kind is `question` or `reply`, or absent
for a plain message. A reply names the question in `inReplyTo`. Text is 1 to
16384 UTF-8 bytes. A session may have 512 messages and a project 4096
(`internal/inbox/store.go:32-58`). The record has no delivery side effect
(`api/coordination/v1alpha1/inbox_types.go:5-6`). CEL requires the actor kind
`session` (`api/coordination/v1alpha1/inbox_types.go:23`).

| | send | ask | reply | list, read, ack |
|---|---|---|---|---|
| Who may send or call | A live session whose project snapshot matches `--project` (`internal/apiserver/inbox.go:172-181`). A non-session is hidden as 404. | Same, and `--enable-inbox-questions` must be on. | Same. The caller must be the question's recipient session. | A session reads and acks only messages whose recipient is itself (`internal/apiserver/inbox.go:100-120`). A configured coordinator host may list a project in `--inbox-coordinator-read-projects` and cannot send. |
| Who may be addressed | The named session, which must be in the session namespace and carry the same project snapshot (`internal/apiserver/inbox.go:140-152`). | With no `--to`, the recorded parent session (`internal/agentrun/inbox.go:105-108`, `internal/apiserver/inbox_thread.go:104-123`). With `--to`, that named session, under the same snapshot rule. | The question's sender. The caller does not name a recipient. | The caller's own inbox, or the caller's sent box. |
| What is stored | One immutable InboxMessage. A repeated key with the same intent returns the same record. A different intent is 409. | Same, kind `question`. | Same, kind `reply`, one reply per question. A second distinct reply is 409. | An ack is a separate InboxReadAck. GET of the ack does not create it. |
| How the recipient learns | It does not, until it runs `list` or `read`. | Same. | Same. The asker sees the reply on its next list or read. | `ack` changes the thread state the asker sees on its next read. |
| Headless task | Can send, ask, reply, list, read and ack. Nothing wakes it. | Same. | Same. | Same. The installed guide says to poll at most once a minute (`docs/ask-your-coordinator.md:147-153`). |
| Busy TUI | The record waits. The TUI is not interrupted. | Same. | Same. | Same. |
| Idle TUI | Same. An idle TUI is not told. | Same. | Same. | Same. |

A parent string that contains `/` is refused on ask with 422, "this Session
has no recorded parent Session" (`internal/apiserver/inbox_thread.go:111-112`).
The guide already says an interactive coordinator started without `--project`
cannot be asked (`docs/ask-your-coordinator.md:37-43`).

### 2.3 The Asks board

An Ask is a question, an approval or a grant for a person
(`api/coordination/v1alpha1/ask_types.go:18-22`). A session creates, lists,
reads and withdraws its own (`internal/apiserver/asks.go:73-77`,
`internal/apiserver/asks.go:87-114`). It cannot create a grant ask
(`internal/apiserver/asks.go:99-102`) and it cannot answer. The portal reader
answers. v1 writes no inbox message when an ask is answered
(`internal/apiserver/server.go:21`). The keeper push
says "An agent needs your answer" and carries no question text
(`docs/asks.md:134-142`). That push goes to Tom.

A headless task, a busy TUI and an idle TUI all learn an answer the same way:
the next GET of their own ask. None of them is interrupted. This board is not
a path to a parent coordinator or to Meatball. It stays Tom's.

### 2.4 Task claims and published context

A TaskClaim stores project, repository, task key, owner session, actor and
catalog provenance. The spec says it contains no prompt or rules text
(`api/coordination/v1alpha1/taskclaim_types.go:22-29`). It does not carry a
message.

A PublishedContext stores text, up to 65536 bytes, of kind
`transcript-excerpt`, `result`, `memory` or `handoff`
(`api/coordination/v1alpha1/publishedcontext_types.go:22-33`,
`api/coordination/v1alpha1/publishedcontext_types.go:86-88`).
The publisher is a project-bound session
(`internal/apiserver/publishedcontexts.go:75-100`). A reader in that project
must have a matching snapshot, unless it is a coordinator host
(`internal/apiserver/publishedcontexts.go:51-64`). The record names a source
parent string and does not address one recipient. Nothing wakes a reader. A
parent with no project snapshot cannot read it. It is not a message to
Meatball.

Two narrower routes are not this path either. Coordinator instructions go
from a session parent to an enrolled child, and the publisher must be the
session named by `spec.parent`
(`internal/apiserver/coordinator_instruction.go:53`). CallerPolicy forbids
that enrollment on a Meatball create
(`internal/apiserver/callerpolicy.go:110-111`). Managed child decisions are
for a coordinator host and a Codex task with a workspace
(`internal/apiserver/decisions.go:26-34`).

## 3. The three refusals on 2026-11-10

Ask maps every 404 to one sentence (`internal/agentrun/inbox.go:319-320`).
Send maps every 404 to another (`internal/agentrun/inbox.go:363-364`). Neither
sentence names the check. The live objects show which check fired. Both
sessions are in `dev-agents`.

Worker `dev-env-1110-145244`, uid `1a4af4ba-5551-4253-9d80-7016c2ee1e03`,
created 2026-11-10T14:52:44Z. `spec.parent` is
`dev-env-fable-coordinator-1109`. `spec.usageCaptureParentUID` is
`10ee0155-65ee-41ea-87d1-2e4fb8085359`. Profile `dev`, mode `task`, repo
`dev-env`, no workspace. The project-snapshot annotation is present, 766
bytes, project `dev-env`, selected `dev-env`. The pod runs as
`dev-env-agent` and is Running.

Coordinator `dev-env-fable-coordinator-1109`, uid
`10ee0155-65ee-41ea-87d1-2e4fb8085359`, created 2026-11-09T22:08:27Z.
`spec.parent` is `flux-system/kustomize-controller`. Profile `workbench`,
mode `remote`, repo `dev-env`, no workspace. Annotation keys are the two
Flux keys. There is no project-snapshot annotation. The pod runs as
`dev-env-workbench`, is Running, and is owned by that AgentSession.

The worker's parent UID matches the coordinator, and the coordinator was
created first. `dev-env` is the live inbox project. A workbench pod token is
classified as that session before it is classified as the client
(`internal/apiserver/caller.go:138-141`). The inbox fence accepts the
workbench ServiceAccount (`internal/apiserver/caller.go:196-200`). Neither
refusal is a profile exclusion. The inbox publish path does not read
`spec.profile`.

### 3.1 Ask with no --to, 404

The worker's `agent-run inbox ask --project dev-env --key K` sets `toParent`
(`internal/agentrun/inbox.go:105-108`). Publish then does three checks, in
order:

1. The sender's snapshot project must equal `dev-env`
   (`internal/apiserver/inbox.go:179-181`). The worker's snapshot does. This
   check did not refuse.
2. The parent name must be a session name, the session must exist, the UID
   must match when one is stored, and the parent must have been created
   before the child (`internal/apiserver/inbox_thread.go:108-123`). The live
   parent passes. A missing parent session would be 422 at
   `internal/apiserver/inbox_thread.go:111-112`, and the CLI would say "no
   recorded parent Session". The observed text is the 404 sentence
   (`internal/agentrun/inbox.go:319-320`), so this check did not refuse.
3. The resolved parent must have a project snapshot for `dev-env`
   (`internal/apiserver/inbox.go:148-151`, called from
   `internal/apiserver/inbox.go:222-224`).
   `claimSnapshot` parses the annotation
   (`internal/apiserver/taskclaims.go:102-107`). An absent annotation is
   empty bytes, and `ParseSnapshot` rejects that
   (`internal/projectcatalog/catalog.go:234-240`). The function returns the
   same 404 as a missing record (`internal/apiserver/inbox.go:28`).

The refusal is the parent's missing project snapshot. The worker's project
scope was accepted. The parent's workbench profile was not consulted.

### 3.2 Send from the coordinator, 404

`agent-run inbox send` names the worker and posts a plain message. The 404
text is the generic one (`internal/agentrun/inbox.go:363-364`). Publish hides
the caller at `internal/apiserver/inbox.go:179-181` when the sender's
snapshot is missing or is for another project. The coordinator has no
snapshot, so `claimSnapshot` fails here, before the recipient is read
(`internal/apiserver/inbox.go:222` is later). There is no inbox record to
miss: a missing record on create continues into the write
(`internal/apiserver/inbox.go:192-205`).

The refusal is the sender's project scope. The coordinator is a session, and
its workbench account is allowed through the fence. The worker's snapshot
was not the check that ran.

### 3.3 agent-run msg to a headless task, 409

Deliver refuses a launch that is not a TUI
(`internal/agentd/deliver.go:118-119`). The API maps that exit to 409
(`internal/apiserver/podcmd.go:239-240`). The worker is `spec.mode: task`.
A task launch does not set the TUI flag. Busy and idle do not apply: the
refusal is before the paste.

## 4. A session whose parent is the Meatball account

CallerPolicy `dev-env-system/meatball` is live. Its ServiceAccount is
`dev-env-system/dev-env-meatball`, mode `task`, profile `dev`, at most 2
unfinished sessions, at most 4 workers, timeout at most 4h, models
`claude-opus-5-5`, `gpt-6-astra` and `claude-fable-5-2`. Of the 395
AgentSessions listed at 15:13Z, none has that parent.

The API sets `spec.parent` from the caller token
(`internal/apiserver/validate.go:267`,
`api/v1alpha1/agentsession_types.go:271-276`). For this caller the token's
parent is `dev-env-system/dev-env-meatball`
(`internal/apiserver/callerpolicy.go:21`,
`internal/apiserver/caller.go:159-166`). The field is immutable and at most
253 bytes. The value contains `/`, so it is not a session name.

What that session can do toward its parent:

- `agent-run inbox ask` with no `--to` is 422, because the parent string
  contains `/` (`internal/apiserver/inbox_thread.go:111-112`).
- `agent-run msg` cannot name the ServiceAccount. The message route looks up
  a session.
- `agent-run inbox send` can reach some other project-bound session. It
  cannot name the ServiceAccount as recipient
  (`internal/apiserver/inbox.go:140-147`).
- The retained outcome is still how a finished task reports a PR. It is one
  snapshot at task exit, not a question during the task.

What Meatball can do about messages: the policy caller may create, list, get
and delete sessions it started (`internal/apiserver/callerpolicy.go:132-149`).
Inbox, msg, asks and log are refused. The front door tools are
`backlog_find`, `backlog_show`, `backlog_create`, `backlog_groom`,
`backlog_order`, `backlog_close`, `coordinator_start`, `session_status`,
`session_stop` and `waiting_on_tom` (`internal/frontdoor/tools.go:19-66`).
The call switch has those ten and no other
(`internal/frontdoor/tools.go:76-104`). None lists, reads, answers or sends a
message. `session_status` returns the session and its retained outcome.
`waiting_on_tom` lists Asks for Tom. The same ten names are in the live
front door image.

## 5. The phone-prompt hazard

`agentd ctl deliver` pastes the message and then sends Enter
(`internal/agentd/deliver.go:145-164`). Claude and a managed Codex TUI both
take that path. Legacy Codex uses `codex queue` instead
(`internal/agentd/deliver.go:139-142`).

An open AskUserQuestion is an input waiting for Enter. The paste becomes the
text of that input, and Enter submits it. The envelope in
`messageText` (`internal/agentd/deliver.go:52-60`) is part of the pasted
text, so it is part of what gets submitted. It is not a check that skips
the send.

No function in `internal/agentd` or `internal/apiserver` reads whether a
phone prompt is open. The words AskUserQuestion occur in docs and skills,
including the tmux handoff skill, which tells an agent not to press keys at
a provider prompt. `agent-run msg` does not call that skill. There is nowhere
in this code to prevent the submit, and nowhere that detects the open prompt.

A notice that presses Enter has the same hazard if it is sent while a prompt
is open. Leaving the note in the record, and not touching the pane, is the
only path that can never answer a prompt. But a note that nobody is told
about stalls the work, which is what this design exists to end.

agentd does know one thing the delivery code does not use. It reports the
agent state `waiting` for a TUI whose Claude waits on a person, such as a
question it asked (`internal/agentd/protocol/status.go:32-34`, set in
`internal/agentd/activity.go:253-259` from Claude's own status). So the
operator can hold a notice while the parent's reported state is `waiting`.
The state arrives with the heartbeat, so a prompt opened in the last minute
can be missed. Ruling 1 in section 12 uses both: hold while `waiting`, and
make the notice recognisable so that a session which finds it in a prompt's
answer discards it and asks again.

That state is reported for a Claude TUI only. A managed Codex TUI goes
through the same paste and Enter and never reports `waiting`, so the notice
is for Claude TUI parents only (slice 6).

## 6. Reuse the inbox

The InboxMessage and `internal/inbox` can carry this. They already have the
idempotent key, the text bound, the sender session, the project, the single
reply and the ack. They cannot carry it unchanged, for three reasons:

1. The recipient has to be a live AgentSession with the sender's project
   snapshot (`internal/apiserver/inbox.go:140-152`). The Meatball account is
   not a session. A parent session with no snapshot fails the same check
   (section 3.1).
2. The actor kind is `session` or `coordinator`
   (`api/coordination/v1alpha1/taskclaim_types.go:61-63`), and the inbox CEL
   allows only `session`. Meatball is `kindPolicy`. It cannot publish.
3. The record does not wake anyone, and `msg` cannot deliver into a headless
   task (section 2.1).

Peer `inbox send` and `inbox ask --to` stay as they are. Both ends of a peer
message still need the project snapshot. The parent command is a separate
route. It does not widen who a session may address besides its recorded
parent, and it does not widen who may address a session besides that
session's parent.

## 7. Record and API

ADR-023 decides this shape. The first slice lands the CRD with it.

### 7.1 Fields

Existing InboxMessage objects stay valid. Sender and recipient stay required
on those objects. New objects use exactly one sender form and exactly one
recipient form.

| Field | Bound | Who sets it |
|---|---|---|
| `project` | existing project name, max 63 | the server, from the child session's snapshot |
| `key` | existing key, max 128 | the caller |
| `text` | 1 to 16384 UTF-8 bytes | the caller |
| `kind` | `question`, `result`, `blocker` on a child note; `reply` on an answer; absent on a parent-originated plain message | the caller for the note; the server for the reply |
| `sender` | existing SessionIdentity | the server, when a session writes |
| `callerSender` | ServiceAccount `namespace/name`, max 317, plus the CallerPolicy UID, max 128 | the server, when Meatball writes. The account must be `dev-env-system/dev-env-meatball` |
| `recipient` | existing SessionIdentity | the server, when the other party is a session |
| `callerRecipient` | the same caller shape as `callerSender` | the server, when the parent is the Meatball account |
| `inReplyTo` | existing message ref | the server, on an answer |
| `actor.kind` | add `policy` to the ClaimActor enum | the server |

The immutability rule `self == oldSelf` stays
(`api/coordination/v1alpha1/inbox_types.go:22`).

Replace `self.actor.kind == 'session'`
(`api/coordination/v1alpha1/inbox_types.go:23`). `sender` and `recipient`
become optional, so that rule would refuse every `policy` write, and it
would also refuse an object whose session sender was absent. The replacement
is one rule: `actor.kind` is `session` when `sender` is set, and `policy`
when `callerSender` is set. With it:

- Exactly one of `sender` and `callerSender`.
- Exactly one of `recipient` and `callerRecipient`.
- The reply rule already on the object stays
  (`api/coordination/v1alpha1/inbox_types.go:24`): `inReplyTo` is present
  exactly when `kind` is `reply`.
- The kind enum grows from `question;reply`
  (`api/coordination/v1alpha1/inbox_types.go:52`) to
  `question;result;blocker;reply`. Absent kind stays a plain message.
- A plain message that sets `callerSender` has a session `recipient`. A
  plain message between two sessions is unchanged. That is today's peer
  `inbox send`, and it is `parent send` from a session parent.

Peer inbox answer stays limited to a question
(`internal/apiserver/inbox_thread.go:140-142`). A parent-note answer accepts
`question`, `result` and `blocker`.

`InboxReadAck` keeps `actor.kind == 'session'`
(`api/coordination/v1alpha1/inbox_types.go:89`). Meatball writes no ack
object. The reply is the retention ack.

`repository`, `provider`, `requestedModel`, `actor` and `provenance` stay
required. A session writer still fills them from that session
(`internal/apiserver/inbox.go:183`). A policy writer copies `repository`,
`provider` and `requestedModel` from the child session, and copies `actor`
from the caller token. `actor.podName` and `actor.podUID` stay required. The
front door presents a projected token, so those two are the front-door pod
(`internal/apiserver/auth.go:39-42`, `internal/apiserver/auth.go:97-98`).
Provenance stays the child's project snapshot.

A session sender keeps `inbox.MessageName(sender, key)`
(`internal/inbox/store.go:81-86`). A policy sender uses
`PolicyMessageName(serviceAccount, policyUID, key)`. Both names are
`message.` plus 64 hex digits, so the ref pattern stays
(`api/coordination/v1alpha1/inbox_types.go:64`). Create, the archive readers
(`internal/apiserver/coordination_inbox_archive_get.go:52`,
`internal/apiserver/coordination_inbox_archive_list.go:112`) and the
retention summary (`internal/coordretention/summary.go:187`) call the
function for the sender form that is set. The archive manifest's recipient
UID stays the session UID when `recipient` is set
(`internal/coordretention/archive.go:197`). When `callerRecipient` is set,
that field holds the CallerPolicy UID, and the readers compare against that
form.

`validMessageSpec` follows the same exactly-one rules
(`internal/inbox/store.go:116-125`). It accepts `actor.kind` `policy` only
for `dev-env-system/dev-env-meatball`. `validActor` grows the same kind
(`internal/inbox/store.go:161-163`). `validCorrelation` accepts `result` and
`blocker` with no `inReplyTo`, as it accepts a question
(`internal/inbox/store.go:129-136`).

`ClaimActor` is shared. TaskClaim and PublishedContext gain a CEL rule that
refuses `actor.kind == 'policy'`, so the new value cannot land on them.

`spec.parent` on the session is the only address the child command uses. The
server copies it. A session parent is resolved as ask resolves a parent
today: the session exists, the stored parent UID matches when one is set,
and the parent object was created before the child. The parent's project
snapshot is not required. A parent of `dev-env-system/dev-env-meatball` is
stored as `callerRecipient`. Any other non-session parent is refused. The
list of accepted accounts is that one constant, not a request field.

The child's project comes from its snapshot. A session with no snapshot
cannot use the command. The command takes no `--project` and no recipient.

An answer is a reply InboxMessage, not a status field. One reply per note,
the same race rule as today (`internal/apiserver/inbox_thread.go:150-160`).
The reply's recipient is the note's sender. The reply's sender is the parent
session, or `callerSender` for Meatball.

A parent-originated plain message has `callerSender` or the parent session
as `sender`, and the child session as `recipient`. The API checks, on the
live child, that `spec.parent` is that caller. `spec.parent` is immutable
(`api/v1alpha1/agentsession_types.go:271-276`).

### 7.2 Routes

All of these require a new flag `--enable-parent-notes`, default false,
which requires `--enable-durable-inbox`. The design's own pull request added
no flag; build slice 2 adds it.

| Method and path | Who | Effect |
|---|---|---|
| `POST /v1/parent-notes` | the child session | stores one note to its recorded parent |
| `POST /v1/parent-notes` with a child session named in the body | that child's parent session, or the Meatball caller when `policyOwns` (`internal/apiserver/callerpolicy.go:147-149`) | stores one plain message to that child |
| `GET /v1/parent-notes?box=inbox\|sent` | a session, or the Meatball caller | inbox: notes addressed to this caller. sent: notes this caller wrote. `limit` 1 to 100 |
| `GET /v1/parent-notes/{name}` | the sender or the recipient | one note, including text |
| `POST /v1/parent-notes/{name}/answer` | the recipient of that note | one reply |
| `GET /v1/parent-notes/{name}/wait?seconds=N` | the note's sender | holds until the reply exists, or until N seconds. N is 1 to 900. The handler calls `holdResponse` for N plus 30 seconds before it blocks |

A hidden note is 404, the same shape as today's inbox hide
(`internal/apiserver/inbox.go:28`). A session does not learn that some other
parent has notes. Meatball does not learn about sessions it did not start.
`policyCallerAllowed` gains these paths and no others
(`internal/apiserver/callerpolicy.go:132-145`).

The wait uses a watch on the reply, one watch per held request, and returns
when the reply is stored or the deadline passes. The handler does not spin.
A second wait on the same note returns the stored reply at once. Over 8
held waits for one session on one API replica, the route returns 429. Two
API replicas can hold sixteen for one session. The wait needs the verb
watch on inboxmessages for the operator's Role, granted in haynes-ops
before the flag is on. A refused watch, or a watch that cannot start,
answers 503 with a fixed sentence and holds nothing, and it does not poll.

The API server's read timeout is 30 seconds and its write timeout is 60
seconds (`internal/apiserver/server.go:604-605`). Before the wait handler
blocks, it calls `holdResponse` for N plus 30 seconds
(`internal/apiserver/threads.go:169-181`). That helper moves this request's
read deadline and write deadline, which is how a thread launch already
outlives those two limits (`internal/apiserver/threads.go:103`). Every
other route keeps the server limits. A wait of 61 to 900 seconds can then
finish with 200.

`agent-run`'s shared API client gives up after 30 seconds
(`internal/agentrun/creds.go:64`, `internal/agentrun/creds.go:182`).
`parent wait` copies that client and sets its timeout to N plus 2 minutes,
the same shape thread uses for one long request
(`internal/agentrun/thread.go:30-33`, `internal/agentrun/thread.go:234-238`).
The 2 minutes sit past the server's extra 30 seconds, so the response can
be written before the client gives up. The other parent commands keep the
30 second client. The client calls wait again only when it wants longer
than 900 seconds, or when the connection closes before the handler answers.

### 7.3 Failure cases

| Case | Result |
|---|---|
| Flag off | 503, nothing stored |
| No recorded parent | 422 |
| Parent session missing, UID mismatch, or created after the child | 404 |
| Parent is a ServiceAccount other than `dev-env-system/dev-env-meatball` | 422 |
| Child has no project snapshot, or it has a workspace | 422 |
| Empty text, text over 16384 bytes, bad UTF-8, bad key, bad kind | 400 |
| Same key and the same intent | 200, the stored note |
| Same key and a different intent | 409, nothing replaced |
| Answer to a note that is not yours, or that is not a note | 404 |
| A second, different answer | 409 |
| Meatball reads or answers a note for another parent's session | 404 |
| Meatball sends to a session whose `spec.parent` is not its account | 404 |
| A session sends to a child it did not start | 404 |
| Wait deadline with no reply | 200, state `waiting`, no text |
| Wait when the reply is already stored | 200, the reply text |
| Project or sender over the inbox limits | 429, nothing stored |
| Too many held waits for one session on one API replica | 429. Two replicas can hold sixteen |
| Watch refused or cannot start | 503, nothing held |
| Schema not upgraded, so a new field would be dropped | 503, nothing stored |

`agent-run msg` to a headless task stays 409. This design does not change
that route.

## 8. Front door and agent-run

Meatball uses only the front door. Descriptions are at most 30 words. The
tools refuse a session the caller did not start, and they return the same
status the API returned, without the note text on a refusal.

| Tool | Arguments | Description |
|---|---|---|
| `parent_waiting` | `limit` optional, 1 to 100 | List notes waiting from sessions this caller started. Names, kinds and ages only. |
| `parent_read` | `name` | Read one note to this caller, including its text, from a session this caller started. |
| `parent_answer` | `name`, `key`, `text` | Store one answer to a note from a session this caller started. A second different answer is refused. |
| `parent_send` | `session`, `key`, `text` | Send one message to a session this caller started. The session reads it with its next parent read. |

`session_status` gains one line, `parent notes waiting: N`, for the session
it already returns. The line is a count. It has no text.
`waiting_on_tom` is unchanged. It stays the list of Asks for Tom.

What tells Meatball that something waits, given that nothing can push into
its chat:

1. Meatball calls `parent_waiting` on its own schedule. Recommended. It
   already calls the front door. No new credential.
2. The count on `session_status`. Useful when Meatball is already reading
   that session. It misses a note until then. Ship it together with the
   tool, not instead of the tool.
3. An outbound notification Tom configures later. There is no credential
   here for a push into Meatball's conversation. The Asks push goes to Tom
   and carries no question text. This design does not use it, and it does
   not add a webhook.

`agent-run` for sessions:

| Command | What it does |
|---|---|
| `agent-run parent tell --key K --kind question\|result\|blocker` | text on stdin. No recipient and no project. Default kind is `question`. |
| `agent-run parent list` and `parent list --sent` | metadata only, as inbox list is |
| `agent-run parent read NAME` | text on stdout |
| `agent-run parent answer NAME --key K` | text on stdin. The caller must be the parent. |
| `agent-run parent wait NAME --for 15m` | one held read. Prints the answer, or `waiting`. |
| `agent-run parent send CHILD --key K` | a parent session writing to its own child. This one names the child. |

The command that needs no recipient is `parent tell`.

## 9. Who learns, and when

A session parent finds the note on `agent-run parent list` and reads it with
`parent read`. That is the same pull the inbox uses today. The note's text is
never pasted. A live Claude TUI parent also gets the one-line notice of
ruling 1, held while its reported state is `waiting`. A busy Claude TUI and an
idle one both receive it; the record waits either way. A managed Codex TUI
parent gets no notice and reads its list. A headless parent can list and read,
because those are API reads. It still cannot take `msg`.

The child learns the answer by `agent-run parent wait` or `parent read`.
Wait holds on the watch until the reply is stored or the deadline passes.
The route moves its own deadlines, and `parent wait` uses a client whose
timeout is that wait plus 2 minutes, so one call can last the full 900
seconds (section 7.2). The client sleeps nothing and polls nothing during
that call. A headless task
runs the same command. If the work order's wait is longer than 900 seconds,
the task runs wait again when it is ready to block again. A repeat returns
at once when the answer is already there. The answer stays in the record.
Pasting it into the child's TUI would have the same hazard as section 5.

Meatball learns by calling `parent_waiting`, and by the count on
`session_status` when it reads that session.

## 10. Limits, retention, logs

Text, key, list page and the 512 / 4096 caps stay the inbox caps (section
2.2). Questions, results, blockers, replies and parent-originated messages
all count. One answer per note. The wait cap is 900 seconds and 8 held
waits per session on one API replica. Two replicas can hold sixteen.

ADR-004 keeps an InboxMessage while the sender session object exists
(`internal/coordretention/rules.go:155-162`). After that sender is gone, it
releases the record when an ack exists or the recipient session is gone, and
then waits 30 days (`internal/coordretention/runnable.go:19-20`, 720 hours)
before archive and delete. A task session is removed about 7 days after it
finishes, or soon after a reap (ADR-020). Removal of the session does not
delete the InboxMessage. DESIGN-061 records that an inbox message is not a
write to the session home.

For these notes, ADR-023 adds two release facts and leaves the 30 days:

- A `callerRecipient` or `callerSender` is not a session. It counts as
  absent for the recipient or sender test, so a Meatball note does not wait
  forever on a session object that will never exist.
- A stored reply counts as the ack. The note and the reply then follow the
  existing grace once the child session is gone.

A note to a session parent who never answers, while that parent session
still exists, stays, as any unacked inbox message to a live recipient stays.
After that parent session is gone, and the child session is gone, the 30
days start. A finished child's note to Meatball stays through the child's
remaining life and then 30 days after the child session object is gone. It
is not deleted with the home at day 7.

Logs and metrics carry the record name, the kind, the byte count, and
whether the parent side was a session or the Meatball account. They do not
carry the text, the key, the prompt or the answer text. Today's message
route already logs bytes and not text (`internal/apiserver/podcmd.go:245`).
Inbox publish logs no text. These routes follow that.

## 11. Build slices

Each slice is one worker session. Tests are named. No slice turns the flag
on in the cluster. The haynes-ops copy of `config/crd/` lands in the same
slice as the CRD change, before any operator image that writes the new
fields.

1. **ADR-023 and the schema.** Fields and CEL in section 7.1, generated CRD,
   and the haynes-ops CRD copy. Tests:
   `api/coordination/v1alpha1/inbox_parent_validation_test.go` (accept a
   child note to a session, a child note to the Meatball account, a reply
   from that account, a plain message from that account, and a peer plain
   message with a session sender and a session recipient; refuse both
   sender forms at once, both recipient forms at once, a plain message
   with `callerSender` and `callerRecipient`, `policy` on a TaskClaim, and
   a reply with no `inReplyTo`). The same slice tests
   `PolicyMessageName` against `MessageName`: both match the ref pattern,
   and the same key from the two sender forms produces two names.
   `validMessageSpec` accepts those objects and still accepts a peer plain
   message. The archive readers, the retention summary and the archive
   manifest's recipient UID call the matching sender and recipient form.
   No HTTP route.
2. **A session parent with no project snapshot.** `POST` and `GET` for a
   child and for that parent session. Tests:
   `internal/apiserver/parent_note_test.go` with the two live shapes from
   section 3 (worker snapshot `dev-env`, parent snapshot absent, parent UID
   matched, workbench account). Tell succeeds. The parent lists and reads.
   A peer `inbox ask` in the same test still returns 404. Also
   `internal/agentrun/parent_test.go` for `parent tell` with no recipient
   flag.
3. **Answer and wait.** The reply and the held read. Tests:
   `internal/apiserver/parent_note_wait_test.go` (reply already stored
   returns at once; deadline already passed returns `waiting` without
   blocking; a second distinct answer is 409; a server whose write timeout
   is 200ms, and whose read timeout is 200ms, still returns 200 when the
   reply arrives after those timeouts, because the handler called
   `holdResponse`). `internal/agentrun/parent_wait_test.go` asserts the wait
   request uses a client whose timeout is N plus 2 minutes, and that the
   other parent commands keep the 30 second client.
4. **Meatball.** The policy allow list, ownership, and the four front-door
   tools plus the count on `session_status`. Tests:
   `internal/apiserver/parent_note_policy_test.go` (own session allowed,
   another parent's session is 404, a different ServiceAccount is 422) and
   `internal/frontdoor/parent_tools_test.go` (tool list, word count at most
   30, a foreign session hidden).
5. **Retention.** The two release facts in section 10. Tests:
   `internal/coordretention/parent_note_rules_test.go`. A caller recipient
   does not keep the record after the child session is gone and a reply
   exists. An unanswered note to a live parent session is kept.

6. **The wake notice (ruling 1).** When a note is stored for a session parent
   that is a **Claude TUI**, the operator delivers one fixed line and Enter:
   `[dev-env notice] a note is waiting from CHILD: agent-run parent read NAME`.
   No note text, kind or key.
   - **Claude TUI only.** agentd reports `waiting` only from Claude's own
     status (`internal/agentd/activity.go:251-259`). A managed Codex TUI
     takes the same paste and Enter but never reports `waiting`, so a notice
     could submit an open Codex prompt. A Codex parent therefore gets no
     notice until Codex reports a pending question; it finds notes with
     `agent-run parent list` between steps, which its coordinator brief tells
     it to run. A headless parent gets none either.
   - **Held** while the parent's reported agent state is `waiting`. Held
     needs no stored state: on each status change of the parent, the operator
     looks for notes to it that carry no sent mark.
   - **Sent at most once, and that survives a restart.** Before it delivers,
     the operator writes a sent mark on the note with the note's
     resourceVersion as the precondition; only the writer that wins delivers.
     A delivery that fails after the mark is not repeated: the note still
     waits in the list. The InboxMessage's spec and stored acknowledgement
     are immutable (`api/coordination/v1alpha1/inbox_types.go:22,88`), so the
     mark is metadata: the annotation `dev-env.haynesops.com/parent-notice`
     with the time. ADR-023 confirms the schema admits it, or names one
     status field for it instead; nothing is kept only in memory.
   - The common rules gain one sentence: an answer to a phone prompt that
     begins with `[dev-env notice]` or with the `agent-run msg` envelope is
     not the owner's answer; discard it and ask again.
   - Tests: `internal/apiserver/parent_notice_test.go` (held while
     `waiting`; sent once after the state changes; a fresh operator over the
     same stored objects neither resends a marked note nor drops an unmarked
     one; a Codex TUI parent and a headless parent get none; the line holds
     no note text) and `internal/agentd/deliver_notice_test.go`.

## 12. Rulings (coordinator dev-env-fable-coordinator-1109, 2026-11-10)

The worker put four questions for Tom here. Tom said the same day that he
should not be asked basic things, so the coordinator rules them. Each is
recorded on OP545 and Tom can change any of them.

1. **How a session parent learns of a note.** The note's text always stays
   in the record and is never typed into the parent's screen. A live Claude
   TUI parent is woken by one fixed line that carries no note text (slice 6);
   a Codex or headless parent is not typed into and reads its list. The line
   is held while the parent's reported agent state is `waiting`, and its
   fixed first words let a session that finds it in a prompt's answer discard
   it and ask again. A note that waits silently would stall the work; a
   pasted note could answer a phone prompt; this does neither.
2. **How Meatball learns of a note.** It calls `parent_waiting` on its own
   schedule, and `session_status` shows a count. No push: nothing here can
   reach its conversation.
3. **How long a note is kept.** As any inbox message is today (ADR-004): it
   outlives the 7-day removal of the child session and is released 30 days
   after the facts in section 10 hold. This adds no new retention rule.
   Whether coordination records as a whole should be removed sooner is
   already an open question for Tom on OP501 and is not decided here.
4. **Kinds.** Three: question, result and blocker. A list shows questions
   and blockers first. None grants access or approves anything.
5. **Held waits are counted per API replica.** Ruled by the coordinator on
   2026-11-10, answering the slice 3 question on OpenProject #686. Eight
   held waits for one session protect one API process. The operator runs
   two replicas and both serve the API, so one session can hold at most
   sixteen across them. The counter stays on that process's Server. The
   429 sentence stays "at most 8 parent waits may be held for one session".
