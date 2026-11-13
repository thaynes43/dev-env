# ADR-026: A session's title may change after create, and the platform types the rename into a running session only under a guard

- **Status:** Proposed 2026-11-13, for the coordinator to review. It
  installs nothing, and nothing it decides is built.
- **Date:** 2026-11-13
- **Decider:** Proposed under OpenProject #787 for the coordinator session
  `dev-env-fable-coordinator-1109`, drafted by a worker. The one decision of
  Tom's it records is his choice of a guarded typed rename on 2026-11-13
  (item 774 comment 7547), quoted below. Everything else is the
  coordinator's, not Tom's.
- **Amends:**
  1. D-39 in [DESIGN-001](../designs/001-dev-env-v2.md) section 3.3
     (decision 23): "spec is immutable after create, except `operatingMode`
     and `lifecycle`". After this ADR, `title` may also change, through one
     route. DESIGN-001 is accepted with [ADR-001](001-distributed-dev-env.md)
     and is not edited.
  2. [DESIGN-065](../designs/065-session-display-titles.md) ruling 4's second
     paragraph (the platform never types into a running session) and safety
     rule 1 (no terminal input). That design is amended in place, with each
     change marked and the first text kept in words.
  3. Decision 105 in the saga README, in the same two points.
- **Supersedes:** nothing whole. [ADR-024](024-parent-held-wait.md) stands:
  nothing is typed to wake a parent. `agent-run msg` is unchanged; its pane
  findings are OpenProject #707.
- **Design:** [DESIGN-065](../designs/065-session-display-titles.md) ruling 8
  and section 9 (rules 8.1 to 8.12).
- **Tracking:** OpenProject [#787](https://work.haynesnetwork.com/work_packages/787)
  (slice 3), after [#774](https://work.haynesnetwork.com/work_packages/774)
  and [#785](https://work.haynesnetwork.com/work_packages/785), under epic
  #91.

## Context

DESIGN-065 gave a session an optional title beside its stable name, set at
create and passed to the Claude app at launch (slice 1, installed; item 785
comment 7560). Two things were left for later: changing a title, and getting
a new title onto a session that is already running.

Tom, by phone prompt in the coordinator's session on 2026-11-13 (item 774
comment 7543), word for word: "I could always do this my ask is agents can
rename or set useful names that follow a pattern".

The coordinator then tried the routes on the unused test session
`dev-env-1113-004614` (item 774 comment 7546). `agent-run msg` cannot carry
the provider's rename command: it wraps every message in a notice
(`messageText` in `internal/agentd/deliver.go`). Typed into the terminal,
`/rename Test | Title probe é` renamed the session at once, and name, UID,
conversation id, Remote Control session id and link, pod UID and restart
count did not change. The name survived a suspend and resume, although agentd
passed the session name to `--remote-control` again, so passing a title at
resume does not replace a typed one. The CLI's own record,
`~/.claude/sessions/<pid>.json`, holds the new `name` with `nameSource` "user"
after a rename, and a derived name with `nameSource` "derived" for a session
only named at launch.

Then, by phone prompt in the same session (item 774 comment 7547), Tom was
asked: "Agents renaming a session that is already running: I tested it on a
spare session. The only way that works is the platform typing the rename
command into that session. It worked, kept the same link and conversation,
and the name survived a restart. The risk: if a session has a question open
or half-typed text, typed input can land there. Should I build agent renames
that way, typed only when the session is idle with an empty input line and
held back otherwise?" He picked "Yes, with that guard (Recommended)", whose
text was: "Agents can rename any running session, including the five
coordinators you have now. The platform types the rename only when the
session is idle and its input line is empty; otherwise it waits and tries
later."

The harm the guard is for is recorded. On 2026-11-12 a routine `agent-run
msg` landed in an open phone question on another coordinator and came back
as that question's answer (item 501 comment 7450). And ADR-024 withdrew a
typed wake line after two reviews found HIGH faults in typing into a live
terminal: an unknown state passed as busy, keys went to the active pane,
pane synchronisation copied Enter, a stale record passed after a clock step,
and no ordering of checks makes a read atomic with a keystroke.

## Decision

1. **`spec.title` may change after create, through one route.**
   `PUT /v1/sessions/{name}/title` with `{"title", "expect"}`, under the
   `resourceVersion` precondition, answering 409 `title_changed` when the
   stored title is not `expect`. Two CEL rules change and no other: the spec
   rule that no field may be added or removed loses its `title` term, and the
   `self == oldSelf` rule on `Title` goes. The bounds stay and run on every
   update. An empty title clears it. A summoned session takes no title. The
   CRD copy goes to haynes-ops before the operator pin that serves the
   route.
2. **Who may change it.** Tom's caller and a trusted client, for any session;
   a workbench session, for any session but itself; a session or a
   coordinator host, for its own children. Never the session itself, and no
   other caller (DESIGN-065 rule 8.2). At most one change per session per 60
   seconds.
3. **The platform may type the provider's rename command into a running
   session, and nothing else for a title.** agentd in the session's own pod
   types `/rename <composed title>` (or `/rename <session name>` for a clear)
   and Enter into its own pane. It does this only for a Claude session in
   remote mode, never for Codex, a task, a local session, a summoned session
   or an explicit continuation.
4. **Only under the guard of DESIGN-065 rule 8.5.** Every condition is a
   state agentd reads in its pod, and every failed or unknown read holds the
   rename. The CLI's own record says "idle", for at least 30 seconds, and
   belongs to this boot's live process; "waiting" is never accepted.
   Remote Control is registered. No tmux client is attached. The pane is
   the only pane, unsynchronised, not in a mode, and runs the CLI in the
   foreground; keys go to its pane id. The CLI version is one whose screens
   the build recorded. The screen shows an empty input line and no prompt.
   The last typed rename is at least 10 minutes old.
5. **A second check before Enter.** After typing the text, agentd reads the
   pane, the record and the clients again. Enter is pressed only if nothing
   changed and the input row holds exactly the command. Otherwise the text
   stays unsent, the rename is Failed, and agentd types it no more. agentd
   never sends Escape, Backspace or a control key and never clears input.
6. **Held, not forced.** A refused rename is held, with its reason in
   status, and tried at each heartbeat for as long as the session runs. A
   newer title replaces it. Nothing is typed because the provider's name
   differs from the record: only a recorded change that this session has not
   applied is typed, so the owner's own rename in the app is never undone.
7. **agentd learns the title from its heartbeat answer.** The route answers
   200 with the recorded title instead of 204. An answer without it, from an
   older operator, means "unknown", never "cleared". An older agentd
   discards the body and does nothing.
8. **Confirmed from the CLI's own record.** agentd reports a rename applied
   only when the record's `name` equals the composed title. The record is an
   undocumented provider file that agentd already reads; if its shape
   changes, agentd reports Unknown and types nothing.
9. **Reported, without the text in logs.** Status gains
   `remoteControl.providerTitle` (the record's name after a rename) and
   `remoteControl.rename` (state, reason, since, attempts). Logs carry a
   hash prefix of the title, never its text.
10. **Reviewed by another model family before the build merges,** and first
    used on an unused test session.

## What this rejects

| Rejected | Why |
|---|---|
| Record the title and pass it at the next resume, typing nothing | A resume does not replace a typed title (item 774 comment 7546), and it would need someone to restart a working session. |
| `agent-run msg` carrying the command | It wraps the text in a notice, so the model receives a message, not a command (comment 7546). |
| Tom renames each session in the Claude app | His ask is that agents name sessions (comment 7543). |
| Typing at once, whatever the session is doing | Tom declined that option (comment 7547), and it is the harm of item 501 comment 7450. |
| Typing from outside the pod by exec | The session's own agentd holds the state reads, the delivery lock and the record; an outside route adds an identity that can type into every session. |
| Escape or clearing the input line first | It destroys a person's unsent text. |
| Pressing Enter without the second check | If a prompt opened after the first read, the Enter would answer it. |
| Typing whenever the provider's name differs from the record | It would undo the owner's own renames. |
| Reading a missing title in the heartbeat answer as a clear | An operator rollback would type `/rename <name>` into every titled session. |

## Consequences

- An agent can title any session it started, and a workbench coordinator
  can title its peers. The main coordinator's own title is recorded by
  another workbench session.
- A rename can wait a long time on a busy session, and that is reported,
  not forced.
- The residual risk is the window from the second check to the Enter, under
  one second. That no prompt can open inside it is an inference, not a
  measurement. Tom chose this route with the risk stated.
- A CLI release that changes the screen holds every rename until the build
  records new fixtures; nothing is typed meanwhile.
- If agentd's second check fails, the rename text can be left unsent in the
  session's input line, and the owner sends or deletes it.
- haynes-ops gets one CRD copy before the operator pin. Rolling the operator
  back below slice 1 while any session has a title would now lose that
  title without an error.
- Sessions whose agentd predates the build show the recorded title in
  listings and get the provider rename after their next resume onto a newer
  image. Nobody restarts a session for it.
