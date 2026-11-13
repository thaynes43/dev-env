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
