Screen fixtures for OP787 (agents rename a running session), recorded read-only by the coordinator
dev-env-fable-coordinator-1109 on 2026-11-13 from two unused test sessions, Claude Code 2.1.321, with
tmux capture-pane -p -e on the session's own pane. Session names and links are replaced by fakes.
- idle-empty-input.2.1.321.ansi: idle, input row holds the prompt mark only (after a rename ran).
- idle-placeholder.2.1.321.ansi: idle on a fresh session, input row shows the CLI's placeholder text.
- typed-rename-zz.2.1.321.ansi: the text "/rename zz" typed and not sent.
- question-open.2.1.321.ansi: an AskUserQuestion prompt is open. There is no input box; the selected
  option row begins with the same mark as the input prompt. Measured at the same moment, the CLI
  record's status was "waiting". This screen must never parse as an empty input or as exact text.
- tmux-facts.txt: claude --version, one list-panes line (window, pane, id, dead, mode, synchronized,
  pid, command), the attached client count, and the list-panes format that produced that line.
  The format uses #{pane_synchronized}. tmux 3.3a has no window_synchronized. The first three lines
  are the coordinator's recording. The fourth line is the format whose fields are that recording.
Measured, not a fixture: text typed into the input line and not sent leaves the record's status and
updatedAt unchanged. A question that is open reads status "waiting".
Added 2026-11-13 08:35Z (all from the same unused test session, Claude Code 2.1.321, pane 200 by 50, no client attached).
The names and links in these screens are the fakes from the recording; they are not rewritten here.
- cursor-and-format-facts.2.1.321.txt: the list-panes format string as it was run, its output line, and the real cursor
  (tmux display-message, cursor_x cursor_y pane_width pane_height) for: idle empty input (2 46), "/rename zz" typed and
  not sent (12 46), after ten Backspace keys (2 46), "/rename é|z" typed (13 46: one column for é), after eleven Backspace
  keys (2 46). The cursor row equals the input row's 0-based line number in the capture.
- busy-thinking-empty-input.2.1.321.ansi: a turn is running (record status "busy"), spinner row "✻ Hatching… (8s · ...)"
  and a Tip row above the box, empty input. Cursor 2 46.
- busy-thinking-typed-rename-zz.2.1.321.ansi: the same turn with "/rename zz" typed and not sent. Cursor 12 46. Ten Backspace
  keys sent during the turn erased it (cursor back to 2 46, input row empty) and the turn carried on.
- busy-spinner-U<code>-empty-input.2.1.321.ansi: one frame for each spinner mark seen in 70 frames of one running turn:
  U+273B, U+2722, U+273D, U+2736, U+00B7 (middle dot) and U+002A (asterisk). The spinner row is "<mark> <Word>… (...)".
  These are empty-input frames of the same session. The empty-input cursor recorded for that session is 2 46.
- busy-streaming-no-spinner-empty-input.2.1.321.ansi: the same running turn while the answer text streams. There is NO
  spinner row and no hint: 54 of the 70 frames looked like this. The record status was "busy" on all 70.
  Empty input, cursor 2 46, the empty-input cursor recorded for that session.
Measured, not fixtures (quoted from the coordinator's recording, not re-measured):
- The cursor: idle empty input is column 2 on the input row; "/rename zz" typed is column 12; "/rename é|z" is column 13;
  ten Backspace keys return it to column 2 with an empty row.
- The screen does not show that a turn is running. In 70 frames of one running turn the record status was "busy" every
  time, the footer never showed "esc to interrupt", 54 frames had no spinner row, and 16 had a row "<mark> <Word>… (...)"
  with the marks U+273B, U+2722, U+273D, U+2736, U+00B7 and U+002A. The record is the only busy signal.
- "/rename <text>" and Enter sent while a turn runs renames at once and does not disturb the turn. Backspace keys during
  a running turn erase typed text and do not disturb the turn.
- A finished turn leaves a row such as "✻ Crunched for 15s · done 8:31 AM" or "✻ Worked for 12s · done 8:29 AM".
