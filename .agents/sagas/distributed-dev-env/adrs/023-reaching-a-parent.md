# ADR-023: A session reaches its parent through the inbox

- **Status:** Accepted 2026-11-10 by the coordinator session
  `dev-env-fable-coordinator-1109`.
- **Date:** 2026-11-10
- **Decider:** that coordinator, from the rulings in
  [DESIGN-062](../designs/062-reaching-a-parent.md) section 12. Those rulings
  are recorded on OpenProject #545. This ADR does not record a decision by Tom.
- **Supersedes:** nothing.
- **Retains:** the InboxMessage as the record; peer inbox between two
  sessions; ADR-004's 30 days; InboxReadAck as a session acknowledgement only.
- **Design:** [DESIGN-062](../designs/062-reaching-a-parent.md)
- **Tracking:** OpenProject [#545](https://work.haynesnetwork.com/work_packages/545)
  (parent #516). The schema slice is
  [#684](https://work.haynesnetwork.com/work_packages/684).

## Context

A session whose parent is another session, or the Meatball account, has no
stored note that parent can read. `agent-run msg` to a headless task stays
409. An inbox ask whose parent has no project snapshot stays 404. DESIGN-062
reuses InboxMessage. The coordinator ruled the four open points on
2026-11-10. Each ruling can be changed later. This ADR records them and the
stored shape they require.

## Decision

1. **A session parent learns from the record.** The note's text stays in the
   InboxMessage and is never typed into the parent's screen. A live Claude
   TUI parent is woken by one fixed line that carries no note text, held
   while its reported agent state is `waiting`. A Codex parent and a
   headless parent are not typed into. They read the list. The line's fixed
   first words let a session that finds it in a prompt's answer discard it
   and ask again.
2. **Meatball learns by pulling.** It calls `parent_waiting` on its own
   schedule, and `session_status` shows a count. Nothing pushes into its
   conversation.
3. **A note is kept as an inbox message.** ADR-004 applies. The note outlives
   the 7-day removal of the child session and is released 30 days after the
   facts in DESIGN-062 section 10 hold. This decision adds no new retention
   period. Whether coordination records as a whole should be removed sooner
   is an open question on OpenProject #501 and is not decided here.
4. **Three kinds.** `question`, `result` and `blocker`. A list shows
   questions and blockers first. None grants access or approves anything. A
   reply stays `reply`. An absent kind stays a plain message.

### Stored shape

Existing InboxMessage objects stay valid. Those objects keep a session
`sender` and a session `recipient`. A new object uses exactly one sender
form and exactly one recipient form.

| Field | Bound |
|---|---|
| `sender` | the existing session identity, optional. A zero value is omitted, so an absent sender stays absent |
| `callerSender` | `serviceAccount` `dev-env-system/dev-env-meatball` (max 317) and `policyUID`, the CallerPolicy UID (max 128) |
| `recipient` | the existing session identity, optional. A zero value is omitted, for the same reason as `sender` |
| `callerRecipient` | the same caller shape as `callerSender` |
| `actor.kind` | `session` when `sender` is set, `policy` when `callerSender` is set |
| `kind` | `question`, `result`, `blocker` or `reply`; absent is a plain message |
| `inReplyTo` | present exactly when `kind` is `reply` |

`policy` is refused on a TaskClaim and on a PublishedContext. A plain
message that sets `callerSender` has a session `recipient`. The spec stays
immutable (`self == oldSelf`). InboxReadAck stays `actor.kind == session`.
Meatball writes no ack. The reply is the retention ack.

A session sender keeps `inbox.MessageName`. A policy sender uses
`PolicyMessageName(serviceAccount, policyUID, key)`. Both names are
`message.` plus 64 hex digits. The archive readers, the retention summary
and the archive manifest's recipient UID use the sender and recipient form
that is set. When `callerRecipient` is set, that manifest field holds the
CallerPolicy UID.

The sent mark for the wake notice is the annotation
`dev-env.haynesops.com/parent-notice`. The spec's immutability rule does not
cover metadata, so the schema admits the annotation. This ADR adds no status
field for the mark. Slice 6 writes the annotation. Nothing is kept only in
memory.

The two release facts in DESIGN-062 section 10 are part of this decision: a
caller party is not a session and counts as absent, and a stored reply
counts as the ack. The schema slice does not apply them.

## What this rejects

| Rejected | Why |
|---|---|
| Typing the note into the parent's screen | The text could answer a phone prompt. It stays in the record. |
| A notice to a Codex TUI or a headless parent | A Codex TUI does not report `waiting`, so a paste could submit an open prompt. A headless parent has no screen. Both read the list. |
| A push into Meatball's conversation | Nothing here can reach it. Meatball pulls. |
| A new retention period, or deleting the note with the child home at day 7 | The note follows ADR-004. A shorter life for every coordination record is the open question on #501. |
| A kind that grants access or approves something | The three kinds are notes. None is an approval. |
| A second store or a new object kind | The inbox already stores an immutable message. |
| Both sender forms, or both recipient forms, on one object | Exactly one of each. |
| `policy` on a TaskClaim or a PublishedContext | The new actor kind is for this note only. |
| A plain message with `callerSender` and `callerRecipient` | A plain message from the Meatball account is to a session. |

## Consequences

| Id | Consequence |
|---|---|
| C-61 | Good: one record reaches a session parent and the Meatball account. Old inbox objects stay valid. |
| C-62 | Good: the note text is not typed into a prompt. |
| C-63 | Bad: a Codex or headless parent sees a note only when it lists. A Claude TUI parent is woken by one fixed line, and that line is a later slice. |
| C-64 | Bad: Meatball sees a note only when it calls, or when it reads that session. |
| C-65 | Neutral: retention stays ADR-004 until the release-fact slice. The flag stays off. This ADR builds no route. |
