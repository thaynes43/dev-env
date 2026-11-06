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
`Enter`, but only after a submit fence (below). Without `--submit`, Enter is not sent. All C0 and C1 control
characters and DEL are refused, even with `--observed-ready`. This includes
newlines, tabs, carriage returns, Escape, Ctrl-C, and Backspace. Neither flag
is passed to Cursor.

**Submit fence.** Cursor 2026.10.14 holds fast literal input in a 75 ms paste
buffer. An Enter inside that window still submits the whole text, but the text
box keeps a copy as a stale draft. This happened on the G1 pane on 2026-11-05 at
23:43Z: the turn succeeded with the full 565-byte prompt, and the composer still
held it. The mechanism was read from Cursor's UI code, not observed by pressing
keys.

So with `--submit`, the helper waits 0.3 s after the text, then reads the pane up
to five times. On each read it checks the same pane, process and owner, and looks
only at the composer: the visible screen from its last `→`, `>`, `›` or `❯` row
down, ignoring wrapping whitespace. The same text in the transcript or history
does not count. Outcomes:

- The composer shows the instruction's last 24 non-space characters more often
  than before typing: Enter is sent.
- A prompt row appears above the composer that the verified screen did not have,
  such as Cursor's command prompt: Enter is not sent, and the reply ends with that
  prompt's kind. Every visible row is checked, the composer rows included, so a
  prompt drawn below the composer is seen too.
- The instruction contains a known prompt phrase (such as `yes/no` or `Run this
  command?`): refused before anything is typed (`invalid-input`), because its
  echo could not be told apart from a real prompt with the same words.
- The text never shows, or the screen cannot be read: Enter is not sent, and the
  reply ends `send-unconfirmed`.
- The instruction is over 800 characters: refused before anything is typed
  (`invalid-input`), because Cursor shows a longer paste as `[Pasted text #N +M
  lines]` and its end would never show.

When Enter is not sent, the text may be in the composer: do not resend it or send
Enter; inspect again. The fence narrows the race; it does not prove Cursor
cleared the box, so inspect after every reply.

Fail closed, with no keys and no Enter:

| Condition | State |
| --- | --- |
| No owner marker, or the marker does not match | `not-owned` |
| Pane gone | `pane-missing` |
| Pane dead | `pane-dead` |
| Pane id, pid, cwd, session, or command changed | `pane-replaced` |
| tmux copy/mode flag set | `pane-in-mode` |
| Receipt missing, old, or capture changed | `stale-receipt` |
| Trust, permission, approval, or unrecognized confirmation phrasing on the visible screen (anywhere in the capture when that screen cannot be located), except in a trust box the ledger shows answered ([the answered trust box](#the-answered-trust-box)) | `modal-trust`, `modal-permission`, `modal-approval`, `modal-unknown` |
| Cursor row, or the row above a blank cursor row, is a nonempty `>`, `›`, or `❯` composer | `user-draft` |
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
  expected text. The one exception is Cursor's command prompt (below): there
  the choice is `y`, shown once as `(y)` at the end of a row, with
  `--expect-kind modal-approval`. No other key is accepted for that prompt.
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
`trust_path` (for a trust answer) and `outcome`. Since ledger `format` 2 it
also records `worktree`, `box_rows_sha256` (a SHA-256 of the expected lines,
with the selection marker `▶ ` taken off) and `box_marker_rows` (which of
those lines had the marker). `inspect` and `reply` use these to know the
answered box again ([below](#the-answered-trust-box)). The task's state file
keeps the same record in `answers` (the last 32). `outcome` is `sent`;
`not-sent` when the pane changed after the spend (below); or `uncertain` when
tmux failed or timed out. An
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
identity and checks that count rather than assuming it. The identity also
carries `#{pane_width}`, and the receipt binds it: a resize after `inspect`
is `stale-receipt`. It goes ahead only when:

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
   - every border on the visible screen must pair into boxes. Take spaces
     and side bars (`│`, `║`, `┃`) off both ends of a row. A border row then
     starts with a left corner and ends with the matching right corner, with
     no other corner between:
     - a top border runs from `╭`, `┌`, `╔` or `┏` to `╮`, `┐`, `╗` or `┓`;
     - a bottom border runs from `╰`, `└`, `╚` or `┗` to `╯`, `┘`, `╝` or
       `┛`;
     - text between the corners is a title, as in `╭─ Note ─╮`. A titled
       border pairs like any other;
     - a row with text that does not end in the matching corner, such as a
       tree row `└── foo`, is ordinary text.

     Reading down the screen, each bottom border closes the latest open top
     border, and the two must have their corners in the same columns. The
     screen is unclear when a border does not pair, or when a row made only
     of box-drawing characters and spaces has a corner but is not a border
     (two corner pairs, or mixed kinds);
   - the prompt is bound to the **outermost** box around the block. That box
     must hold no other box, and its own borders must have no title: the
     title would be text the expected lines do not bind. Its rows must have
     nothing to the left of its left corner column: a side border there means
     it may sit inside a box whose borders are off the visible screen.
     `answer` does not bind a prompt drawn with a box inside a box;
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
     - a box inside the bound box, or the bound box inside another box,
       titled borders included;
     - a title on the bound box's own top or bottom border.
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

`inspect` and `reply` locate the visible screen the same way (rule 1), but do
not use rule 4. Their classification looks for known phrases as case-folded
fragments of a visible row. That is detection, and it only ever leads to a
refusal: `reply` refuses every known prompt phrase on the visible screen.
`answer` alone acts on an approval, so it alone binds the prompt row for row.

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

### Cursor's command prompt

Cursor asks before it runs a command that is not in its allowlist. That
prompt is not a box (the G1 pane, 2026-11-05; the anonymized capture is
`scripts/cursor-interactive-session-fixtures/g1-command-approval.json`):

```text
────────────────────────────────────────────────────────────────────── (pane width)
 $  sha256sum /home/dev/.local/state/.../XX-XXXXXXXXXX-1.md && wc -l    (the command,
    ...                                                                  wrapped)
    .../XX-XXXXXXXXXX-1.md in .

 Run this command?
 Not in allowlist: sha256sum, wc
  → Run (once) (y)
    Add Shell(sha256sum), Shell(wc) to allowlist? (tab)
    Run Everything (shift+tab)
    Skip & tell the agent what to do instead (esc or n)
```

`answer --expect-kind modal-approval --choice y` binds it instead of a box
when the expected text shows `(y)` at the end of exactly one row and no
`[y]`. Rules 1 to 3 and 5 hold as for a box. Rule 4 becomes:

- the expected text is every nonblank row from the first command row to the
  `Skip` row (for queued commands, from the `Approval N of M` row to the
  switch hint; see below), with only spaces taken off each row's ends (not the box-drawing
  and other whitespace characters rule 4 trims, because the command has no
  border: `wc -l ─` is not `wc -l`), exactly, in order and once; expected
  rows that start or end with such a character are `invalid-input`; a different
  or longer command, another path, an added, missing or reordered row, or a
  cropped prompt is `modal-mismatch` (or `prompt-unclear` when the rows left
  out are its first or last);
- the rows are not inside a box;
- the row directly above the first expected row is the divider: `─` repeated
  exactly the pane width, nothing else;
- every row after the last expected row, to the pane bottom, is blank;
- no other row on the visible screen holds a known prompt phrase, except
  Cursor's own `Waiting for approval...` status line for the pending command,
  which it shows above the divider.

The binding is to the rows Cursor draws, not to the command's bytes. Cursor
wraps a long command and indents the continuation rows, and the trim removes
that indent, so a newline in the command and a wrap at the same point look the
same, though bash runs them differently. Characters that Cursor or tmux do not
draw, such as zero-width characters, are invisible here too. Approve the
command from its own record, not from what the screen shows.

Anything else is `prompt-unclear`. Only `y`, Run (once), is ever sent, as one
literal key without Enter. The helper refuses every other choice for this
prompt (`invalid-input`) and never sends Tab (allowlist), Shift-Tab (Run
Everything), Escape or `n` (Skip), or Enter. A persistent allowlist or a
settings change is out of its scope. Each command needs its own recorded
approval reference, spent once like any other answer.

#### Queued commands: Approval N of M

When more than one command waits, Cursor shows them one at a time (G1 pane,
2026-11-05T20:36:55Z, Cursor 2026.10.14-0f9edde; anonymized capture
[`g1-command-approval-queue.json`](../scripts/cursor-interactive-session-fixtures/g1-command-approval-queue.json)):

```text
────────────────────────────────────────────────────────────────────── (pane width)
 Approval 1 of 3

 $  sha256sum ... && wc -l
    ... in .

 Run this command?
 ...
    Skip & tell the agent what to do instead (esc or n)

 ←/→ to switch approval
```

The expected text then starts with the `Approval N of M` row and ends with the
`←/→ to switch approval` row, so it binds the index, the count and the shown
command together. `answer` refuses (`invalid-input`) expected text that has
one of those rows without the other, has either one twice or out of place, or
shows N above M, a count below 2, or a number above 99 (a longer queue is
refused). A different index, count or command on the
screen is `modal-mismatch`. The helper never sends an arrow key, so it answers
only the approval already shown.

What `y` does there is a property of the Cursor build, read from its installed
UI code for 2026.10.14-0f9edde, not observed by pressing it: `y` maps to
`approve` for the shown decision only (`decisions[min(activeIndex, n-1)]`),
whatever option is highlighted. The pending-decision store resolves and
removes that one id. Run Everything (Shift-Tab, never sent) is the path that
approves every pending decision. After the answer, the next queued command is
shown as a new prompt (approval N of M-1, or the last one if N was M) and
needs its own approval and a new `inspect`. On another Cursor build, check
this again before relying on it.

The gap between `answer`'s last capture and its key (see
[Sending and after](#sending-and-after)) matters more with a queue. If the
shown approval is answered by someone else inside that gap, Cursor shows the
next queued command at once, and the `y` approves that one, which may be a
different command or a second run of the same one. With a single prompt, a
stray `y` most likely lands in the composer instead. So answer a queue only
while nothing else can act on the pane, and check the agent's transcript
after the answer for exactly one run.

#### Rejecting the shown command: ctrl-c

`answer --expect-kind modal-approval --choice ctrl-c` rejects the command
Cursor shows, with the same binding as `y`: the exact expected rows under the
divider, including any `Approval N of M` row and switch hint. It sends one
`C-c` through tmux and nothing else: no `n`, Enter, Escape or arrow key. It
is refused on any other prompt kind and on a boxed prompt.

What Ctrl+C does is read from the installed UI code of Cursor
2026.10.14-0f9edde, not observed by pressing it:

- Cursor renders with raw mode on and `exitOnCtrlC` off, so a tmux Ctrl+C
  reaches it as a key, not as SIGINT.
- At the approval screen the composer is unfocused; the decision handler
  maps Ctrl+C to `reject` for the active decision only. The store resolves
  that one id as not approved, with no reason, and removes it. No agent turn
  starts and no other decision changes.
- In message mode (no decision left), one Ctrl+C stops a running turn, clears
  a draft or arms exit, and a second within two seconds quits Cursor. So reject one decision, inspect again, and
  never send a second Ctrl+C without a fresh inspect that still shows a
  decision.
- `n` (Skip) is not used: it opens a reason composer, and Enter there can
  send a queued message as a new turn.

When the agent turn has ended with an error, Cursor draws it under the prompt
as `Error: <title>` (for example `Error: Agent stopped retrying` or
`Error: Connection failed`) and still draws the stale decisions above it (G1 pane,
2026-11-05T21:41:47Z; anonymized capture
[`g1-command-approval-aborted.json`](../scripts/cursor-interactive-session-fixtures/g1-command-approval-aborted.json)).
The expected text for a rejection may then end with the footer rows, after the
Skip row or the switch hint, once. Any row after the Skip row or the hint
refuses `y`, and refuses `ctrl-c` unless those rows start with one `Error: `
line: a command whose turn may have ended is never approved. After one rejection,
the last decision shows alone, without the counter row or hint, and needs a
fresh `inspect` and its own approval reference.

#### Lesson: qualify a prompt class on a real capture first

- **Observed** (2026-11-05 19:30Z, Cursor 2026.10.14-0f9edde): a
  command-approval prompt is not a box. It sits below a full-width divider
  and writes its keys as `(y)`. `answer` refused it: `Choice must appear as
  [y]`.
- **Root cause**: the earlier `answer` fixtures and rules assumed every prompt
  is a bracketed `[key]` inside one paired box. That assumption came from the
  trust prompt and was never checked against a real command prompt.
- **Rule**: before promising that the helper can answer a prompt class,
  capture one real, sanitized example of that class and qualify it: a
  fixture plus a test that is RED before the change and GREEN after. Until
  then, report the gap. Do not broaden the matcher to a generic shape, and
  do not send raw keys.

The command prompt's fixture is
[`g1-command-approval.json`](../scripts/cursor-interactive-session-fixtures/g1-command-approval.json),
the queued form's is
[`g1-command-approval-queue.json`](../scripts/cursor-interactive-session-fixtures/g1-command-approval-queue.json),
and their regression tests are the `test_command_prompt_*`, `test_queue*`, `test_queued_*`,
`test_answer_sends_only_y_to_the_shown_queued_approval`,
`test_command_fixture_is_the_captured_shape` and
`test_answer_sends_only_y_to_cursors_command_prompt` tests in
[`test_cursor_interactive_session.py`](../scripts/test_cursor_interactive_session.py).

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

After an answer, inspect again. Old prompt text can stay in the history rows
of the capture. `inspect` and `reply` do not count it there; `reply` refuses
while a prompt is on the visible screen, except an answered trust box (next).

### The answered trust box

After `[a]` answers a trust prompt, Cursor drops the `▶` marker, prints
`⏳ Trusting workspace...` in place of the box's last row (the navigation
hint), and draws its banner and composer below. The box can stay on the
visible screen, with its top in the history rows. The live G1 screen of
2026-11-05 is frozen in
`scripts/cursor-interactive-session-fixtures/g1-live-capture.json` (tmux
3.3a, a 24x80 pane under 11 history rows, the box across both), with its
worktree, task name, owner marker and pid replaced by synthetic values of
the same length. `inspect`
and `reply` stop counting that box as a prompt only when all of these hold:

1. **Ledger.** The last answer in the task's state is a trust answer whose
   ledger entry has `format` 2, `outcome` `sent`, and this task's name, pane
   id, pane pid, owner marker and worktree (also as `trust_path`). The state's
   record is the entry without its `name`. An unreadable entry or any other
   outcome is not evidence, and the screen alone never is. Only a regular
   file is read: a symbolic link is not followed, and the open does not block.
   An entry written before `format` 2 is evidence only with
   `--answered-text-file` (below).
2. **Box.** The box is read in the whole bounded capture, which the receipt
   binds: history rows and the visible screen. Every border there pairs
   (rule 4). Exactly one box holds a prompt phrase. It has no title, holds no
   other box, is not inside one, and has nothing to its left. Its top border
   may be in history, but not on the first row of a truncated capture (that
   row may be cut); a box whose top is past the capture bound has no top and
   does not pair. Its bottom border and its status row are on the visible
   screen. Its rows, trimmed as in rule 4 and without blank rows, are the
   answered prompt's rows with the last one, which must be exactly
   `Use arrow keys to navigate, Enter to select, or press the key shown`,
   replaced by one row that is exactly `⏳ Trusting workspace...` (U+23F3, a
   space, the words, three full stops). The `▶` marker must be gone from
   every row: a box that still shows it stays a prompt. Nothing else may
   differ: `box_rows_sha256` decides (`box_marker_rows` records which rows
   had the marker). A box that keeps the hint and adds the status row is
   refused; the live screen does not show that.
3. **Around.** The cursor is on the last pane row, below the box. From the
   row after the box's bottom border to the cursor row, exactly one row is
   Cursor's composer (its first character after spaces is `→`), and that row
   is exactly `→ Plan, search, build anything`: the empty composer. Cursor
   parks its cursor on a blank last row, so the cursor row alone does not
   show that the composer was drawn. The moment while Cursor shows only
   `⏳ Trusting workspace...`, a composer with typed text, a second composer
   row, or a `>` composer keep the box a prompt. No history row outside the
   box has a prompt phrase, and no visible row outside the box has one
   either.

`inspect` stores with the receipt which ledger answer, if any, its
classification relied on. `reply` refuses with `stale-receipt` when its own
reliance differs, for example after an `inspect` without
`--answered-text-file` and a `reply` with it.

Then the screen is classified as if the box were not there. With Cursor's
`→` composer that is `unclassified`, which still needs `--observed-ready`.
`classification_detail` names the approval reference. Anything else is refused as before: no ledger entry, an entry for
another pane, pid, owner or worktree, a box that differs, a second prompt box
(in history too), a nested box, another prompt in the capture, a box cut by
the capture bound or by a narrower pane, a screen that cannot be located, a
stale receipt (a changed history row included) or a changed pane. The ledger shows that the key was sent, not
that Cursor accepted it.

**Ledger entries written before `format` 2** (by every helper before it:
5bf24fec and earlier, 8326e2d2 among them) bind the prompt only by
`expected_text_sha256`. To use one, pass
`--answered-text-file` to both `inspect` and `reply`: the exact
`--expect-text-file` of that answer. It is read and checked like
`--expect-text-file` (at most 4096 bytes, 1..20 nonblank lines, no control
characters but newlines; otherwise `invalid-input`). The helper accepts it
only when:

- its SHA-256 equals the entry's `expected_text_sha256`;
- the entry is the task's last answer, a trust answer with `outcome` `sent`;
- the entry names this task, pane id, pane pid and owner marker;
- its `trust_path` is the recorded worktree;
- the state's record is the entry without its `name`, and the entry has none
  of the `format` 2 fields.

It then derives `box_rows_sha256` and `box_marker_rows` from the file as
`answer` would, and applies rules 2 and 3 unchanged. With a `format` 2 entry,
the file must give the same evidence as the entry. A file that is not
accepted is `answered-text-unaccepted`, and nothing is sent. The flag reads
the ledger and the state and never writes them. It changes nothing else: a
stale receipt, a changed pane, another prompt or a draft still refuse.
Without it, an entry written before `format` 2 is not evidence.

This command is source only. It is not deployed to any host. Using it on a
live pane, such as the PR #917 continuation's Cursor pane, still needs Tom's
specific approval for that exact prompt and path through a verified channel.

## UI classification limit

The installed Cursor TUI has no stable, documented layout that this helper can
prove from tmux text. Classification is intentionally narrow:

- Classification reads the visible screen: the last `pane_height` rows of the
  capture, located by the row count in rule 1 of the `answer` section. The
  history rows above it are returned in `text` and bound by the receipt, but
  modal phrases there do not count.
- A known modal phrase on the visible screen blocks sending. Phrases include
  workspace trust, permission required, command approval, and generic
  confirmations such as `yes/no` or `are you sure`. A phrase that is only in
  the history rows, such as an answered prompt that scrolled up, does not
  block. Neither does a trust box that the ledger and the box rows show
  answered ([the answered trust box](#the-answered-trust-box)).
- When the visible screen cannot be located (the row count does not match, or
  the bounded capture does not hold all of it), the same rules read the whole
  bounded capture, with its last row as the last pane row: a phrase anywhere
  blocks, a draft (below) is `user-draft`, and an empty composer is
  `unclassified`, never `active`. That is the classification the helper used
  before it read the visible screen, with `active` made `unclassified`.
  `classification_detail` then says the phrase is in the captured window and
  that the screen was not located.
- A user draft is a `>`, `›`, or `❯` prompt plus text, and only when tmux
  reports the cursor on the last pane row. The draft must be on that row, or,
  when that row is blank, on the row just above it: tmux prints a blank row
  under the composer. Other rows are echoed transcript. Displayed text such as
  an answered `S8` line is not an unsent draft.
- `active` means that same cursor row is only the empty prompt prefix. An
  empty prompt above a blank cursor row is `unclassified`. `active` is not
  proof the UI is idle, that a modal is absent, or that the served model
  matches `--model`.
- Any other screen, including a prompt glyph when the cursor is elsewhere, is
  `unclassified`. Cursor's own composer row (`→ Plan, search, build anything`)
  is not a `>`, `›`, or `❯` prompt, so that screen is `unclassified` too.
  Reply then requires manual `--observed-ready` for that receipt. If a real
  Cursor screen cannot be classified, do not invent a safer reading;
  re-inspect or confirm the pane manually.
- A prompt taller than the pane, whose phrase rows scrolled into history, is
  judged only by its visible rows. A person giving `--observed-ready` reads the
  receipt's whole `text`, history rows included.

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
| `invalid-input`, `prerequisite-unavailable`, `unsupported-client`, `name-consumed`, `not-owned`, `pane-missing`, `pane-dead`, `pane-replaced`, `pane-in-mode`, `stale-receipt`, `modal-*`, `user-draft`, `unclassified-ui`, `answered-text-unaccepted` | 2 | Nothing was sent. Correct the input or re-inspect. |
| `launch-unconfirmed`, `client-not-running`, `inspect-unconfirmed`, `send-unconfirmed` | 3 | The outcome is unknown or the pane did not stay up. Do not retry automatically. |

Tool calls time out after five seconds. `--observe-seconds` is `0..5` and defaults
to 1. Subprocess stderr is not copied into the JSON.

## Tests

```sh
nice -n 19 python3 -O -B -m unittest discover -s scripts -p 'test_cursor_interactive_session.py' -v
```

The tests use fake executables only. They do not launch Cursor or contact a model account.
