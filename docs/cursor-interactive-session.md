# Interactive Cursor session helper

Authorized coordinators can start, inspect, reply to, and answer one approved
prompt in one dedicated interactive Cursor session with Python 3, installed
`tmux`, and the official `cursor-agent` executable. This is a local wrapper. It is not an operator API,
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

python3 scripts/cursor-interactive-session.py answer \
  --tmux /usr/bin/tmux \
  --name cursor-task-unique-20261030 \
  --receipt 'v1:...' \
  --approval-ref 'OP399-approval-2026-11-04T21:40:00Z' \
  --expect-kind modal-trust \
  --trust-path /absolute/authorized/worktree \
  --expect-text-file /path/to/expected-prompt.txt \
  --choice a
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
reloads state, and re-reads the pane before any keys. Inspect, reply and answer
share a local POSIX file lock so concurrent helpers cannot spend the same
receipt twice or overwrite a newer inspect (`answer` does not wait for it). State replacements use unique mode-0600 temporary
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

## Answering one approved prompt

`answer` sends one approved, specific prompt choice, and nothing else. `reply`
still refuses every prompt.

**The caller is responsible for a verified approval of this exact action,
choice and target.** Nothing in the helper verifies an approval.
`--approval-ref` is audit metadata, not proof: the helper records it and uses
it to refuse a second answer, but a reference, a quoted "approved" or a
recommendation grants nothing. Get the approval first through a verified
channel (for dev-env work, Tom's own answer, or a relay from his designated
coordinator verified by channel metadata).

Before the lock and before any tmux call, `answer` checks its input:

- `--approval-ref`: 8..200 characters of letters, digits and `._:/@#-`.
- `--expect-kind`: `modal-trust`, `modal-permission` or `modal-approval`.
  `modal-unknown` is never answerable.
- `--expect-text-file`: 1..20 nonblank lines, at most 4096 UTF-8 bytes, no
  control characters except newlines. This is the whole prompt, row by row,
  exactly as the screen shows it (rule 4 below). Each line is trimmed the same
  way as a screen row: leading and trailing whitespace and box-drawing
  characters only.
- `--choice`: exactly one printable ASCII key that appears as `[key]` in the
  expected text.
- `--trust-path`: required with `--expect-kind modal-trust` and refused with
  any other kind. It is the one directory the approval covers: an absolute,
  normalized path of letters, digits and `_.@+-` between single slashes, with
  no `.` or `..` component and no trailing slash, at most 1024 bytes. One
  expected line must contain it as a whole word (bounded by whitespace or the
  line's ends).
- `--receipt`: the exact token from the latest `inspect`.

Then it takes the task's lock without waiting: if another helper call holds
it, the state is `lock-busy` and nothing is sent.

### One answer per approval

Each approval reference is spent once, across receipts, task names and
restarts. The ledger is
`$XDG_STATE_HOME/cursor-interactive-session/approvals/<sha256 of the reference>`
(directory 0700, file 0600). A reference that already has an entry is
`approval-spent`, checked first and again by an exclusive create just before
the key, so two callers cannot both spend it. A new `inspect` and a new
receipt with the same prompt still showing do not allow a second answer.

The entry records `answered_at` (UTC), `approval_ref`, `name`, `kind`,
`choice`, `expected_text_sha256`, `pane_id`, `pane_pid`, `owner`,
`trust_path` (for a trust answer) and `outcome`. The task's state file keeps the same record in `answers` (the last
32). `outcome` is `sent`; `not-sent` when the pane changed after the spend
(below); or `uncertain` when tmux failed or timed out. An
entry still at `sending` means the process ended before it recorded the
outcome; treat it as uncertain. An uncertain answer stays uncertain until a
supported observation settles it; it is never answered again under the same
approval. No prompt text or
instruction is stored or printed.

### The prompt must be the current screen

The capture asks tmux for 80 history rows and the visible pane
(`capture-pane -p -S -80`). tmux's manual numbers the rows: negative numbers
are history, `0` is the first visible row, and the default end is the end of
the visible pane. Its source (`cmd-capture-pane.c`, tmux 3.4, 3.5a and
master) prints every row from the start to the end, blank rows included, one
per line, when `-J` and `-H` are not given. So the capture has exactly
`min(history_size, 80) + pane_height` rows, and the last `pane_height` of them
are the visible screen. `answer` reads `#{history_size}` with the pane's
identity and checks that count rather than assuming it. It goes ahead only
when:

1. the capture has exactly `min(history_size, 80) + pane_height` rows, the
   bounded text still holds the whole visible screen, and the byte cap did not
   cut into it (otherwise `prompt-unclear`);
2. the visible screen shows exactly one prompt kind, the expected one (none
   is `no-modal`, or `stale-prompt` when the expected prompt is only in older
   rows; two kinds is `prompt-unclear`; another kind is `modal-mismatch`);
3. for a trust prompt, the visible screen names exactly the trust path
   (below);
4. the expected lines are the whole prompt, exactly. Each visible row is
   trimmed of its leading and trailing whitespace and box-drawing characters
   (U+2500 to U+257F), the border and padding, and of nothing else. Then:
   - every expected line must equal one trimmed row, case-sensitively, with
     no substring, prefix or case folding;
   - the matched rows must be consecutive, except for rows that trim to
     nothing (borders, padding, empty rows);
   - the block must appear exactly once on the visible screen;
   - every border on the visible screen must pair into boxes. A border row
     has only box-drawing characters and spaces, with one left and one right
     corner. A top border runs from `╭`, `┌`, `╔` or `┏` to `╮`, `┐`, `╗` or
     `┓`; a bottom border runs from `╰`, `└`, `╚` or `┗` to `╯`, `┘`, `╝` or
     `┛`. Reading down the screen, each bottom border closes the latest open
     top border, and the two must have their corners in the same columns.
     A border that does not pair, or a row with corners of both kinds,
     leaves the screen unclear;
   - the prompt is bound to the **outermost** box around the block. That box
     must hold no other box. Its rows must have nothing to the left of its
     left corner column: a side border there means it may sit inside a box
     whose borders are off the visible screen. `answer` does not bind a
     prompt drawn with a box inside a box;
   - every row inside the bound box that does not trim to nothing must be
     one of the expected lines. The expected text is the whole box, row for
     row. A Cursor trust box puts a padding row between its rows, so checking
     only the rows next to the block would not show a row left out.

   The states:
   - If the lines are only in older rows, the state is `stale-prompt`.
   - It is `prompt-unclear` when the block is shown twice, or cannot be
     bound to one box. That covers:
     - an unboxed prompt;
     - a box that runs off the visible screen;
     - borders that do not pair, such as different widths, a box left open,
       or a box closed whose top is off the screen;
     - a box inside the bound box, or the bound box inside another box.
   - Any other difference is `modal-mismatch`. That covers:
     - a longer target (`git status && echo EXTRA`);
     - a path with a suffix or in another case;
     - a changed space inside a row;
     - a wrapped continuation row;
     - any row in the box that the expected text leaves out: the header, the
       footer, or an unexpected or changed row past a padding row.
5. no composer row (`>`, `›` or `❯` at the start of a row) follows the last
   expected line, and the cursor row is not a composer row (otherwise
   `stale-prompt`).

`inspect` and `reply` do not use rule 4. Their classification still looks for
known phrases as case-folded fragments of a row. That is detection, and it
only ever leads to a refusal: `reply` refuses every prompt. `answer` alone acts
on an approval, so it alone binds the prompt row for row.

Like `reply`, it also requires the latest receipt, the same pane id, pid,
worktree, session, command and owner marker, and a capture unchanged since
that inspect (a changed screen, including someone typing, is `stale-receipt`).

Limits: this rule reads tmux text only. A prompt whose phrasing is not in the
helper's phrase list is not answerable. The row count is checked against
tmux's own `history_size` and `pane_height`, not against a real tmux run: a
tmux that dropped blank rows would fail the count and be refused, and output
that scrolls between the identity read and the capture changes the count or
the capture and is refused too. The count cannot detect a tmux that printed
the right number of rows out of order. A prompt drawn with a composer below it
is refused even if it is live. A prompt drawn with a box inside a box is
refused, and so is any screen whose box borders do not pair, even where the
odd border is far from the prompt. When the helper cannot tell which prompt is
current, it refuses.

### A trust answer names one exact path

A workspace-trust approval covers one directory, so a trust answer is bound
to it in three places. Under the lock, before any tmux call, `--trust-path`
must equal, exactly as a string, the worktree that `start` recorded for this
task. `start` records the resolved real path (symlinks resolved, no trailing
slash, no `.` or `..`), so a symlinked, trailing-slash or dotted spelling of
the same directory is refused. The pane's cwd must match it too. Otherwise
the state is `trust-path-mismatch`. The expected
text must contain it (above). And the visible screen, at the first check and
again at the re-check right before the key, must name exactly that path:

- The helper splits each visible row into words at whitespace and box-drawing
  characters (U+2500 to U+257F). A word is path-like when it contains `/` or
  starts with `~`.
- Every path-like word on the visible screen must be exactly the trust path.
  Another path, a parent, a child, a longer name that starts with the path
  (`-old`), a path that ends with it, a trailing `/` or attached punctuation,
  a `~` form, a shortened form with `…`, or a URL is `trust-path-mismatch`.
- A path-like word that starts or ends its row might continue on the row
  above or below (tmux prints a wrapped line as separate rows), so it is
  `trust-path-unclear`. The trust path must have a separator before and after
  it in its own row, as the box border gives it.
- If no visible word is exactly the trust path (no path is shown, or it is
  split across rows), the state is `trust-path-missing`.

Each of these sends nothing and leaves the approval unspent at the first
check; at the re-check it is `prompt-changed` with the approval spent. The
trust path is recorded with the approval reference in the ledger entry and
the state's `answers`. A worktree path with characters outside the allowed
set cannot be answered by this helper.

### Sending and after

The helper spends the approval, then the receipt. Those writes take time, so it
then reads the pane once more, right before the key: the same pane id, pid,
cwd, session, command and owner marker, a capture identical to the one it
verified, and the same current-prompt rules. If anything differs, it sends
nothing, records `outcome: not-sent` in the ledger and the state, and refuses
with `prompt-changed`. The approval stays spent: a new approval is needed.

Only then does it send exactly `tmux send-keys -t <pane> -l -- <key>`, without
Enter or any other key. tmux has no compare-and-send, so a short window
remains between that last capture and the key: the time to start one tmux
process. A change inside that window can still receive the key. The helper
narrows the window; it cannot close it.

After an answer, inspect again: old prompt text can stay in the captured rows,
and `reply` keeps refusing while it is there.

This command is source only. It is not deployed to any host. Using it on a
live pane, such as the PR #917 continuation's Cursor pane, still needs Tom's
specific approval for that exact prompt and path through a verified channel.

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
| `answered` | 0 | One approved prompt key went to the matching pane, without Enter, and was recorded. |
| `modal-mismatch`, `no-modal`, `stale-prompt`, `prompt-unclear`, `approval-spent`, `lock-busy` | 2 | `answer` sent nothing: the prompt kind or text did not match, no prompt is showing, the expected prompt is not the current screen, the screen is unclear, the approval was already used, or another helper call holds the lock. |
| `trust-path-mismatch`, `trust-path-unclear`, `trust-path-missing` | 2 | `answer` sent nothing and did not spend the approval: `--trust-path` is not the recorded worktree, or the visible screen names another path, a path that may continue across rows, or no exact path. |
| `prompt-changed` | 2 | `answer` sent nothing: the pane changed after the approval was spent. The approval stays spent and its outcome is `not-sent`; a new approval is needed. |
| `answer-unconfirmed` | 3 | The answer key's outcome is unknown and is recorded as `uncertain`. The approval and the receipt are spent; do not retry or send another key. |
| `invalid-input`, `prerequisite-unavailable`, `unsupported-client`, `name-consumed`, `not-owned`, `pane-missing`, `pane-dead`, `pane-replaced`, `pane-in-mode`, `stale-receipt`, `modal-*`, `user-draft`, `unclassified-ui` | 2 | Nothing was sent. Correct the input or re-inspect. |
| `launch-unconfirmed`, `client-not-running`, `inspect-unconfirmed`, `send-unconfirmed` | 3 | The outcome is unknown or the pane did not stay up. Do not retry automatically. |

Tool calls time out after five seconds. `--observe-seconds` is `0..5` and defaults
to 1. Subprocess stderr is not copied into the JSON.

## Tests

```sh
nice -n 19 python3 -O -B -m unittest discover -s scripts -p 'test_cursor_interactive_session.py' -v
```

The tests use fake executables only. They do not launch Cursor or contact a model account.
