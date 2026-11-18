# Prove a coordinator's own wait wakes its idle thread

This is a procedure for a later, separately authorized story on a disposable
**ordinary-profile** host. It has not been run. The source adapter and fake
clock tests do not prove a Codex wake. No production coordinator or running
session is a test target.

The source audit for image-pinned Codex 0.160.1 establishes an execution-end
event, not a fresh idle-thread turn. The [official Hooks documentation](https://learn.chatgpt.com/docs/hooks#how-background-hooks-run)
says an asynchronous hook finishing while idle waits for the next user turn.
A supported alternative completion extension in that pinned version is
**Unknown**. If there is no supported way for the thread's own background tool
completion to start the required turn, record `wake_unconfirmed` and stop this
proof. Do not install a hook hoping it will change that documented behavior.
See [DESIGN-067 section 9](../.agents/sagas/distributed-dev-env/designs/067-named-codex-workbench-coordinator.md#9-wake-and-watch)
and [ADR-024](../.agents/sagas/distributed-dev-env/adrs/024-parent-held-wait.md).

## Preconditions, recorded by the later story

The story authorizes the disposable host, exact image/source pins, observation,
and the three stimuli. It names an existing, recorded child of that host for
the addressed-note stimulus. Preparing either session belongs to that later
story, not this procedure. Owner authorization for unattended operation and its
budget remains Q-067-02. Upstream work requires Q-067-04 if needed.

Use one already prepared native thread on that host for **all three** proofs.
Record the host Session UID and exact native thread ID privately. Obtain the
thread ID from supported native metadata, never the latest thread in a cwd or
a title. Record the starting native turn ID and original background tool call
ID for each proof, and prove there is no active turn before its stimulus.
The observer must have a supported read-only way to see turn-start/completion
metadata. A phone screenshot, a shell process exit or an execution-end event
alone does not show a new model turn.

Keep private artifacts mode 0600 under a mode-0700 directory in the disposable
host's HOME. Do not export transcripts, session links, enrollment or pairing
material. Public evidence contains implementation heads, version, kinds,
relative time differences and verdicts only. Private evidence may contain the
binding and tool/turn IDs needed to compare equality.

Before the trials, the test thread prepares the following in its own normal
foreground tool call. Replace the two placeholders with the **explicit**
qualified native ID and a future UTC deadline within the approved test budget:

```sh
umask 077
mkdir -p "$HOME/.dev-env-c2-proof/handoffs"
printf '%s\n' 'EXPLICIT_NATIVE_THREAD_ID' > "$HOME/.dev-env-c2-proof/thread-id"
printf '%s\n' '2026-11-19T12:10:00Z' > "$HOME/.dev-env-c2-proof/budget-end"
agent-run version > "$HOME/.dev-env-c2-proof/agent-run-version"
codex --version > "$HOME/.dev-env-c2-proof/codex-version"
```

The date above is an example parameter, not permission to wait until that date.
Use the same saved deadline for every invocation of a given wait identity. A
new identity does not authorize more work or extend the story budget.
Record the setup time and baseline directory listing; no stimulus file exists
yet. Existing directory entries are the initial baseline. `--after`, if used,
must be the existing parent wait API's cursor captured **before** the child
publishes; otherwise the command captures one cursor with `seconds=0` itself.

The initial instruction, delivered while this thread is active, tells it to:

1. Start exactly one trial wait through its supported background command tool.
2. Finish its current turn and remain idle. Do not poll the tool, file, note or
   thread, run a second timer, or keep an active model request waiting.
3. If native completion independently starts a new turn in this same thread,
   read only the returned JSON metadata and acknowledge its event once. Write
   the acknowledgement file described below. Do not start unrelated work.
4. Treat questions and approvals as holds on their own lanes. A completion is
   neither an owner answer nor permission to release a hold.

The CLI tool request for the timer trial is `exec_command` with
`yield_time_ms: 1000` and the following `cmd`. This deliberately lasts longer
than the tool's yield. Record the tool's yielded process/call identity. If the
pinned harness has a differently named supported background tool, the later
story must record its exact equivalent request before proceeding; do not
invent an extension or a new prompt transport.

```sh
c2_thread=$(cat "$HOME/.dev-env-c2-proof/thread-id")
c2_end=$(cat "$HOME/.dev-env-c2-proof/budget-end")
agent-run coordinator wait --thread "$c2_thread" --wait-id c2-timer \
  --directory "$HOME/.dev-env-c2-proof/handoffs" --budget-end "$c2_end" \
  --timer 30s --scan-every 2s --max-entries 32 --max-pending 16
```

Do not use `write_stdin`/a wait-tool poll to fetch completion during the idle
interval: that would be an active tool workflow, not this proof. Do not use a
Stop hook continuation: it creates a continuation prompt, not completion of
the registered wait while idle. No `turn/start`, `thread/resume`, fork, queued
prompt, `agent-run msg`, terminal paste, MCP notification or replacement Session
may supply the wake.

## Three trials

**Timer:** run the command above from the one test thread. Leave the handoff
directory unchanged and publish no note. The observer records the thread going
idle before the 30-second deadline. The command should return `kind: timer`.
Observe up to 30 seconds after completion for one new turn and its ACK, without
sending any input. No new turn or ACK is `wake_unconfirmed`, even if the command
exited successfully. Stop on failure; do not work around the boundary.

**Addressed child note:** once the timer proof has passed, the same thread starts
the following background command and goes idle. Its longer timer lets the note
win. Use a new wait identity so the prior trial's ACK is not consumed again.

```sh
c2_thread=$(cat "$HOME/.dev-env-c2-proof/thread-id")
c2_end=$(cat "$HOME/.dev-env-c2-proof/budget-end")
agent-run coordinator wait --thread "$c2_thread" --wait-id c2-note \
  --directory "$HOME/.dev-env-c2-proof/handoffs" --budget-end "$c2_end" \
  --timer 5m --scan-every 2s --max-entries 32 --max-pending 16
```

The observer confirms the held read was armed and the thread is idle. In the
**already prepared child**, whose saved parent is this exact host Session UID,
run once (key unique to this trial):

```sh
umask 077
printf '%s\n' 'Disposable c2 note stimulus; metadata only is the wake.' > "$HOME/c2-note-stimulus.txt"
agent-run parent tell --key c2-proof-note-1 --kind result < "$HOME/c2-note-stimulus.txt"
```

Record its confirmed note reference and compare it with the returned `note` in
`kind: note`, cursor and new turn's ACK. The adapter uses the existing API's
addressed-note fence; it does not address a native thread itself. Publish no
second note and do not resend on an uncertain receipt. A note from an unrelated
session must not produce a result: the fake/API regression tests cover that
filter; a later authorized negative trial may also check it before acceptance.

**New file:** once the note proof has passed, the same thread starts the same
wait command with `--wait-id c2-file` and `--timer 5m`. The observer confirms its
baseline is saved and the thread is idle. An authorized ordinary local writer,
not the test thread, creates one file in the explicitly watched directory:

```sh
umask 077
printf '%s\n' 'Disposable c2 file stimulus.' > "$HOME/.dev-env-c2-proof/handoffs/new-handoff.txt"
```

The writer must be on the disposable host and use that host's exact prepared
HOME path. This is not an instruction to copy another session's worktree or
exec into a production pod. Record creation time. Expect `kind: file`, the
fixed directory path (one coalesced burst), and its new watermark. Neither the
file text nor the note text may appear in any wake result.

For **each** successful wake the same thread, in the automatically started
turn, writes the following through its next normal tool call. Replace EVENT_ID,
KIND and the native TURN_ID from the observed completion; this does not give an
observer permission to send a prompt or manufacture the ACK:

```sh
umask 077
python3 - 'EVENT_ID' 'KIND' 'TURN_ID' <<'PY'
import datetime, json, os, pathlib, sys
p = pathlib.Path.home() / '.dev-env-c2-proof'
ack = dict(id=sys.argv[1], kind=sys.argv[2], turn=sys.argv[3],
           thread=(p / 'thread-id').read_text().strip(),
           time=datetime.datetime.now(datetime.timezone.utc).isoformat())
with (p / (sys.argv[2] + '.ack.json')).open('x') as f:
    json.dump(ack, f)
PY
```

The thread also says `ACK <kind> <event-id>` once in that turn. The exclusive
file creation detects a second acknowledgement. Observe a further 30-second
quiet interval for duplicate turns/ACKs without further stimuli. Only after the
trial's evidence is saved may the owner drive the next trial. Owner input
between trials does not count as a wake.

## Verdict and record

A trial passes only if the idle evidence predates its stimulus, one JSON event
matches the source, one fresh native turn starts from completion of the original
owned tool call, the thread ID equals the original ID, and that turn produces
exactly one matching acknowledgement. A different thread, an active-turn
continuation, a manually polled result, no ACK, two ACKs, budget expiry, terminal
input or an injected prompt fails. A host or timer alone is never acceptance.
Record event ID/kind, confirmed cursor and directory watermark, wait identity,
start/idle/stimulus/completion/turn/ACK times, exit status, duplicate count, and
source/version pins. Record observer provenance and any missing measurement as
Unknown. Keep the native binding private.

All three must pass on the same idle thread. Then a separate installed-host
proof and explicit lane release are still needed before watch custody moves.
If any cannot pass without violating ADR-024, record the conflict and leave
watch lanes with the outgoing coordinator. No typed wake is a fallback.

The later resume trial is separate: stop the wait with SIGTERM or its private
regular `.stop` marker, save the stopped result/cursors, and after authorized
host recovery explicitly re-arm the same thread with `--resume`, `--ack ID`,
`--after CURSOR` and `--watermark HASH`, unchanged limits and budget. Another
thread ID must refuse. Reopening the same native thread after host resume is
Unknown until that later proof; this command never rebinds one automatically.
