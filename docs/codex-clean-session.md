# Native clean-session client

For an already authorized repository and existing paired daemon, run the source
wrapper with Python 3 and installed Codex/tmux. The tmux `default-shell` must
be POSIX compatible (for example `/bin/sh` or bash); non-POSIX shells are
unsupported. `shlex.join` quotes for a POSIX shell, and the wrapper does not
inspect or change the existing tmux shell configuration. This is a local client wrapper,
separate from operator-backed `agent-run rc fresh`. It creates a uniquely named
detached tmux client using the supported interactive `codex --remote unix://…`
route. No project ID is required. It never forks, resumes, selects an old thread,
starts a daemon, pairs a host or changes credentials, configuration or trust.

```sh
python3 scripts/codex-clean-session.py \
  --client /path/to/installed/codex \
  --repo /absolute/authorized/repository \
  --socket /absolute/existing/app-server-control.sock \
  --model gpt-6.1-sol --effort medium \
  --name clean-task-unique-20261030 \
  --task-file /path/to/task-only-handoff.txt
```

Supply only the new task handoff (1..4096 UTF-8 bytes), without an earlier
conversation transcript. `--task-file -` reads stdin; quote the here-document
delimiter if supplying literal shell text. All command arguments are shell quoted
and the task follows `--`, so task text cannot become CLI flags or shell commands.
`--tmux /path/to/tmux` selects an explicit executable when PATH is unsuitable.
The repository path resolves locally; use the authorized directory on this host.
The full quoted tmux command, explicit paths and framing allowance must also fit
the conservative 8192-byte budget; quote-heavy tasks can hit this before the raw
4096-byte task limit. Oversized commands are rejected before any tool call or
name reservation. Shorten the task or paths; no launch has been attempted.
The supplied socket must be an existing local Unix socket; existence alone does
not prove the daemon accepts connections. Inherited provider settings and rules
still apply. Fresh conversation context does not mean a blank HOME or no rules.

`--dry-run` validates local paths, executable availability and task input without
executing tools, connecting to the socket or starting a conversation. It prints
no task text. Without dry-run, bounded help/version probes check the executable
and required flags, then one atomic tmux `new-session` refuses an existing name.
There is no attach/reuse fallback. Use a new name for each distinct authorized
task. Before attempting launch, an empty exclusive name marker is retained in
`$XDG_STATE_HOME/codex-clean-session/` (default `~/.local/state/`). A reserved name
is refused even after its client exits. Markers contain no task or transcript;
they are conservative duplicate guards, not proof of a thread or its readiness.
Do not remove a marker or pick another name to retry an uncertain request.
Dry-run checks an existing marker without creating one.

JSON states and exits:

| State | Exit | Meaning and next step |
| --- | --- | --- |
| `dry-run` | 0 | Local inputs checked; daemon and provider readiness unknown. |
| `client-running-unverified` | 0 | The exact newly returned pane ID is alive after the observation delay; inspect this task's native client for connection, new thread and task acceptance. This is not provider-ready or accepted evidence. |
| `invalid-input`, `prerequisite-unavailable`, `unsupported-client`, `name-consumed` | 2 | Correct the explicit input or executable; launch was not attempted. Argparse usage errors also exit 2. |
| `launch-unconfirmed` | 3 | tmux failed, collided, timed out, returned undecodable output or an invalid pane ID, or observation failed. It may have launched; inspect this exact task name before deciding anything. No automatic retry. |
| `client-not-running` | 3 | Pane exited or its status is unknown. Inspect this task's client locally; do not switch to an old context or blindly launch again. |

Every tool call has a five-second timeout; observation waits one second by
default (`--observe-seconds 0..5`). A living pane may be blocked on a real provider
folder-trust prompt, authentication, connection or another error. The wrapper
cannot establish any of those from process status. Disclose genuine trust prompts
to the authorized person; never silently approve an unknown folder. Never paste
TUI input while a native question modal is pending. Subprocess output and pane
content are not captured into the JSON receipt; no other conversation is read.
If a client exits quickly, tmux may remove its pane and its error text; run only
when this limited observation is sufficient, and preserve available native errors
before considering a further explicitly authorized launch.

The [source handoff and communication ruling](../.agents/sagas/distributed-dev-env/verification/native-clean-session-source-2026-10-30.md) records scope and gates.

The supported route was demonstrated with installed Codex 0.160.1 on an existing
paired host (OP267/3393,3395; OP272/3416). That evidence predates this wrapper and
does not establish its installation or live acceptance. Official
[CLI reference](https://developers.openai.com/codex/cli/reference) and the installed
`codex --help` describe client options; the installed help is the compatibility
probe. This source change performs no live launch or deployment. Test only with
fixtures:

```sh
nice -n 19 python3 -O -B -m unittest discover -s scripts -p 'test_codex_clean_session.py' -v
```
