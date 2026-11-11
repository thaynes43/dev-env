# ADR-024: A parent is woken by its own held wait, never by typed input

- **Status:** Accepted 2026-11-11 by the coordinator session
  `dev-env-fable-coordinator-1109`.
- **Date:** 2026-11-11
- **Decider:** that coordinator, reversing its own ruling 1 of
  [DESIGN-062](../designs/062-reaching-a-parent.md) section 12. This ADR does
  not record a decision by Tom.
- **Supersedes:** in [ADR-023](023-reaching-a-parent.md), decision 1's
  sentence that a live Claude TUI parent is woken by one fixed line, and the
  paragraph that names the annotation `dev-env.haynesops.com/parent-notice`
  as the sent mark. Everything else in ADR-023 stands, including that a
  note's text is never typed into a parent's screen.
- **Design:** [DESIGN-062](../designs/062-reaching-a-parent.md) section 13.
- **Tracking:** OpenProject [#689](https://work.haynesnetwork.com/work_packages/689)
  (the withdrawn slice) and [#708](https://work.haynesnetwork.com/work_packages/708)
  (the held wait). Parent epic #516.

## Context

DESIGN-062 slice 6 had the operator type one fixed line and Enter into a
Claude TUI parent's terminal when a child stored a note. The line carried no
note text. It was held while the parent reported `waiting`.

The slice was built as dev-env PR 1072 and reviewed twice by another model
family, read-only. Each review found two HIGH faults on a head whose CI was
green:

| Review | What it found |
|---|---|
| First, head `0261ce7f` | An unknown client state was reported as `busy` and passed. The paste and the Enter went to whichever tmux pane was active. |
| Second, head `eb25657b`, after both were fixed | With tmux pane synchronisation on, Enter is copied to a second pane. A status record left by an earlier process under a reused pid passes after a clock step. The 30 second deadline does not reach the process at the keyboard. |

The second review's trace also says what no fix changes: no ordering of
checks makes the read of the client's state atomic with the keystroke.

The harm on the bad path is specific. A session that waits on a person has an
input open. A typed line and Enter become that person's answer (DESIGN-062
section 5).

## Decision

1. **Nothing is typed into a parent's terminal to wake it.** Slice 6 is
   withdrawn. dev-env PR 1072 is closed unmerged and its branch is kept.
2. **A parent holds its own wait.** `agent-run parent wait-any` makes one
   held read on the operator. It returns when a note addressed to the caller
   exists that is newer than the caller's cursor, or when its time is up. It
   prints the note's name, its child and its kind, and a cursor. It prints no
   note text.
3. **The parent's own harness does the waking.** A coordinator starts the
   command in the background. When the command exits, the harness tells the
   session, the way it does for any background command. No other process
   presses a key.
4. **A parent that is not waiting reads its list.** That is what a Codex TUI
   parent, a headless parent and Meatball already do.
5. **No sent mark.** The operator writes nothing on a note. The annotation
   `dev-env.haynesops.com/parent-notice` is not used. The operator needs no
   new verb: the held read uses the watch it already has.

## What this rejects

| Rejected | Why |
|---|---|
| Another round of fixes on the typed line | Two reviews in a row found HIGH faults in the same place for different reasons. The line depends on tmux, process identity, a status file and the wall clock agreeing at one instant. |
| Typing only when the client says idle | Built in the second round. The reviewer showed the read cannot be made atomic with the keystroke, and that the record can be stale. |
| A mark on the note so a line is sent once | Needed only because the operator pushed. A caller that pulls keeps its own cursor. |
| A sentence in the common rules telling a session to discard the line | Needed only because a line could arrive as an answer. Nothing arrives. |
| The verb `patch` on inboxmessages for the operator | Needed only for the mark. Not granted. |

## Consequences

- A coordinator is woken only while it runs the command. The coordinator
  brief tells it to. One that does not is no worse off than before this ADR:
  it finds notes on its next `agent-run parent list`.
- The held read counts against the same limit as `parent wait`: eight held
  reads for one session on one API replica.
- `agent-run msg` still pastes and presses Enter, as it did before
  DESIGN-062. The second review's findings about the pane apply to it. That
  is OpenProject #707, not this ADR.
- No haynes-ops change is needed for this decision.
