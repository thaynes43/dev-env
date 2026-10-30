# Interactive Cursor session helper

Authorized coordinators can start, inspect, and reply to one dedicated
interactive Cursor session with Python 3, installed `tmux`, and the official
`cursor-agent` executable. This is a local wrapper. It is not an operator API,
a scheduler, or a persist session.

The test lives at `scripts/test_cursor_interactive_session.py`, next to
`scripts/test_codex_clean_session.py`. That is the established script-test
layout in this repo.

## Route

`start` reserves a task name, then runs one detached tmux `new-session`. The
pane command is exactly:

```text
exec cursor-agent --workspace <worktree> --model <requested-model>
```

Every argument is shell-quoted with `shlex.join` because tmux parses one
shell command. The helper does not interpolate instruction text into that
command. `inspect` and `reply` pass argv arrays to tmux and never use a shell.

The helper does not call `cursor-agent persist`, `models`, `login`, or
`--print`. It never passes `--force`, `-f`, `--yolo`, `--auto-review`,
`--approve-mcps`, `--trust`, `--sandbox`, `--resume`, or `--continue`. It does
not change Cursor permissions, config, or account files.

The dispatch records that the installed CLI model list exposes `grok-4.7-high`.
This helper does not call that list. `--model` is only the requested id.
A running pane does not prove which model answered.

`persist` can start a session that survives disconnects. This wrapper does not
use it. A persist attach or stop is not bound to one pane receipt.

No live Cursor conversation is started by the source tests.

## Commands

```sh
python3 scripts/cursor-interactive-session.py start \
  --client /path/to/cursor-agent \
  --tmux /usr/bin/tmux \
  --worktree /absolute/authorized/worktree \
  --model grok-4.7-high \
  --name cursor-task-unique-20261030

python3 scripts/cursor-interactive-session.py inspect \
  --tmux /usr/bin/tmux \
  --name cursor-task-unique-20261030

python3 scripts/cursor-interactive-session.py reply \
  --tmux /usr/bin/tmux \
  --name cursor-task-unique-20261030 \
  --receipt 'v1:...' \
  --text-file /path/to/instruction.txt \
  --submit
```

`--dry-run` on `start` checks the local path, name, model, and executables.
It does not reserve a name or call tmux. `--text-file -` reads the instruction
from stdin. Outbound instructions must be a single line of UTF-8 text without
control characters, including newline, tab, carriage return, DEL, or C1 controls.
For longer instructions, send a single line pointing to an authorized work-order
file. Shell metacharacters remain literal text.

## Identity and receipts

The owner marker is `ci1-` plus a SHA-256 prefix of the task name and resolved
worktree. It is stored in `$XDG_STATE_HOME/cursor-interactive-session/<name>`
(default `~/.local/state/...`, mode `0600`) and on the tmux session as
`@cursor_interactive_owner`. The marker holds no instruction and no transcript.

`start` only accepts the pane id returned by that new session. `inspect` and
`reply` target the recorded pane. They refuse a missing owner file, a different
tmux binary, a dead pane, a tmux copy mode, or any change of pane id, pid,
session name, cwd, command, or owner marker. They do not attach, split, or
reuse another pane. An uncertain launch or a name collision leaves the
reservation in place. A second start does not launch again.

`inspect` returns at most the last 80 capture lines and 8192 bytes, plus a new
receipt:

```text
v1:<inspect-id>:<snapshot-sha256>:<pane-id>:<pid>
```

The snapshot covers pane identity and the bounded text that inspect returned.
Older scrollback is outside the receipt. `reply` requires that same receipt.
It reads and validates the entire instruction first, then locks the task name,
reloads state, and re-reads the pane before any keys. Inspect and reply share a
local POSIX file lock so concurrent helpers cannot spend the same receipt twice
or overwrite a newer inspect. State replacements use unique mode-0600 temporary
files, fsync, and atomic rename.
A content change, a new inspect, or a spent receipt is stale: re-inspect, do
not send. The receipt is durably spent before the first send attempt, including
an unknown tmux result, and is not retried. Lock files remain in the state
directory and must not be removed while helpers may be running.

The local lock only coordinates this helper. External UI or tty input can change
the pane between verification and sending; capture and input are not atomic.
A successful send proves neither model consumption nor the served model.

## What reply will type

Reply sends `tmux send-keys -t <pane> -l -- <exact instruction>`. It does not
split the instruction into key names. `--submit` then sends one separate
`Enter`. Without `--submit`, Enter is not sent. All C0 and C1 control
characters and DEL are refused, even with `--observed-ready`. This includes
newlines, tabs, carriage returns, Escape, Ctrl-C, and Backspace. Neither flag
is passed to Cursor.

Fail closed, with no keys and no Enter:

| Condition | State |
| --- | --- |
| No owner marker, or the marker does not match | `not-owned` |
| Pane gone | `pane-missing` |
| Pane dead | `pane-dead` |
| Pane id, pid, cwd, session, or command changed | `pane-replaced` |
| tmux copy/mode flag set | `pane-in-mode` |
| Receipt missing, old, or capture changed | `stale-receipt` |
| Trust, permission, approval, or unrecognized confirmation phrasing | `modal-trust`, `modal-permission`, `modal-approval`, `modal-unknown` |
| Cursor row is a nonempty `>`, `›`, or `❯` composer | `user-draft` |
| Screen is not that composer and has no known modal phrase | `unclassified-ui` unless `--observed-ready` |

`--observed-ready` records that a coordinator looked at this exact receipt and
confirmed a normal composer. It does not override a modal or a nonempty
composer or control characters, and it is not a trust, approval, or permission grant.

## UI classification limit

The installed Cursor TUI has no stable, documented layout that this helper can
prove from tmux text. Classification is intentionally narrow:

- A known modal phrase anywhere in the bounded capture blocks sending.
  Phrases include workspace trust, permission required, command approval, and
  generic confirmations such as `yes/no` or `are you sure`. A phrase still
  visible after the prompt is gone still blocks until a later capture drops it.
- A user draft is only the last captured line, and only when tmux reports the
  cursor on the last pane row and that line is a `>`, `›`, or `❯` prompt plus
  text. Lines above that row are echoed transcript. Displayed text such as an
  answered `S8` line is not an unsent draft.
- `active` means that same cursor row is only the empty prompt prefix. That is
  not proof the UI is idle, that a modal is absent, or that the served model
  matches `--model`.
- Any other screen, including a prompt glyph when the cursor is elsewhere, is
  `unclassified`. Reply then requires manual `--observed-ready` for that
  receipt. If a real Cursor screen cannot be classified, do not invent a safer
  reading; re-inspect or confirm the pane manually.

`pane_current_command` must stay the basename observed at launch and must be
the client basename, `cursor-agent`, `agent`, or `node`. The official launcher
execs Node, so `node` is a weak name. The owner marker, pane id, pid, and
worktree still have to match. If the client changes cwd, later inspects fail
closed as `pane-replaced`.

## States

| State | Exit | Meaning |
| --- | --- | --- |
| `dry-run` | 0 | Local checks only. |
| `client-running-unverified` | 0 | The new pane is alive. UI and provider are not verified. Nothing was typed. |
| `inspected` | 0 | Bounded text and a fresh receipt. |
| `sent` | 0 | Literal text went to the matching pane. `submitted` says whether one Enter followed. |
| `invalid-input`, `prerequisite-unavailable`, `unsupported-client`, `name-consumed`, `not-owned`, `pane-missing`, `pane-dead`, `pane-replaced`, `pane-in-mode`, `stale-receipt`, `modal-*`, `user-draft`, `unclassified-ui` | 2 | Nothing was sent. Correct the input or re-inspect. |
| `launch-unconfirmed`, `client-not-running`, `inspect-unconfirmed`, `send-unconfirmed` | 3 | The outcome is unknown or the pane did not stay up. Do not retry automatically. |

Tool calls time out after five seconds. `--observe-seconds` is `0..5` and defaults
to 1. Subprocess stderr is not copied into the JSON.

## Tests

```sh
nice -n 19 python3 -O -B -m unittest discover -s scripts -p 'test_cursor_interactive_session.py' -v
```

The tests use fake executables only. They do not launch Cursor or contact a model account.
