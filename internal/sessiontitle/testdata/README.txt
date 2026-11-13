Screen fixtures for OP787 (agents rename a running session), recorded read-only by the coordinator
dev-env-fable-coordinator-1109 on 2026-11-13 from two unused test sessions, Claude Code 2.1.321, with
tmux capture-pane -p -e on the session's own pane. Session names and links are replaced by fakes.
- idle-empty-input.2.1.321.ansi: idle, input row holds the prompt mark only (after a rename ran).
- idle-placeholder.2.1.321.ansi: idle on a fresh session, input row shows the CLI's placeholder text.
- typed-rename-zz.2.1.321.ansi: the text "/rename zz" typed and not sent.
- tmux-facts.txt: claude --version, one list-panes line (window, pane, id, dead, mode, synchronized, pid, command) and the attached client count.
Not recorded: a question prompt, a permission prompt, a choice list. Anything the parser does not recognise must hold.
