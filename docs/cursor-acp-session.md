# Cursor ACP source client

This is a source-only integration increment for OP87/193/262. It is not installed,
not an AgentSession provider, and not live acceptance. DESIGN-026's held runtime
operations and the existing provider/admission decisions remain unchanged.

The existing tmux helper remains unchanged. This client never reads a tmux pane,
attaches an existing conversation, loads provider history, or reads credentials.
Do not use it to route around a transcript, authentication, or access denial.

## Supported route and limits

The client targets Cursor's ACP mode, described in
[Cursor's ACP documentation](https://cursor.com/docs/cli/acp). It runs
`<client> --model <requested-model> acp` as a child with newline-delimited
JSON-RPC over stdin/stdout. The client initializes protocol 1, creates exactly one
fresh session in an explicit worktree with no supplied MCP servers, and sends one
prompt. Existing CLI authentication is used; this client never logs in, supplies
credentials, or changes Cursor permissions. The model id is requested metadata,
not served-model evidence. Existing workspace settings may still load.

**UNVERIFIED against Cursor's current public ACP documentation.** These points
follow the generic ACP schema. They have not been checked against Cursor's
current doc or a live run:

- The `--model` flag placed before the `acp` subcommand, and whether `acp` honours it.
- Permission option spelling. The client requires ACP schema kinds
  (`allow_once`, `allow_always`, `reject_once`, `reject_always`). If Cursor sends
  another spelling, such as `allow-once`, the request fails closed with
  `invalid-permission-options`. The client accepts only a `reject_once` decision.
- The skipped ACP `authenticate` step. The client relies on an existing CLI login.
  If Cursor requires `authenticate`, `session/new` fails with
  `provider-error-session-new`; the client does not authenticate.
- Whether Cursor sends `session/update` before answering `session/new`. The
  client fails closed on that with `early-session-update`, because no session id
  exists yet to correlate it. A live run that shows this order needs a separate
  reviewed change, not a silent ignore.

None of this is live acceptance.

A later, separately authorized owner can invoke:

```sh
python3 scripts/cursor-acp-session.py run \
  --client /absolute/path/to/cursor-agent \
  --worktree /absolute/authorized/worktree --model grok-4.7-high \
  --prompt-file /absolute/path/to/work-order.txt --timeout 600
```

`--client`, `--worktree` and `--prompt-file` must be absolute. `--model` must
match `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`, so it cannot start with `-`; bad
arguments fail with `invalid-arguments` or `invalid-input` before any child starts.

No live invocation is part of this source increment. This client cannot adopt the
existing tmux task or bypass its inspection restriction. The subprocess uses the
caller's identity and workspace; it does not become an authenticated dev-env
session or gain inbox permissions. There is no daemon, listener, registry, or
new API endpoint.

## Child environment

The child does not inherit the caller's environment. It receives exactly these
names, each only when set in the caller's environment, and nothing else:

`PATH`, `HOME`, `LANG`, `LC_ALL`, `TERM`, `TMPDIR`, `XDG_RUNTIME_DIR`

Pod tokens and keys (for example `CLAUDE_CODE_OAUTH_TOKEN`) never reach the
third-party binary. Any Cursor authentication variable, such as an API key,
needs a separate approved change; this increment passes none.

## Coordinator input and receipts

Stdout contains bounded JSON events. Assistant text, reasoning, tool inputs and
outputs, plans, and stderr are not copied into receipts. `question-required`
contains the provider's explicit structured question and choices, not a terminal
capture. It is task content, not authenticated approval or generic metadata.
`permission-required` contains only request correlation and option
ids/kinds; tool prose and arguments are withheld.

After seeing an exact pending question, the coordinator writes one JSON line to
this client's stdin (one option per question unless allowMultiple):

```json
{"requestID":42,"sessionID":"observed-session-id","kind":"question","answers":[{"questionId":"q1","selectedOptionIds":["option1"]}]}
```

For a permission request, the client accepts only an explicit rejection, naming
the `optionID` of a `reject_once` option from `permission-required`:

```json
{"requestID":43,"sessionID":"observed-session-id","kind":"permission","optionID":"reject-once"}
```

**This increment never approves tool permissions.** Approval, plan acceptance, new
credentials, and access changes stay with their existing supported approval path.
The client reports and rejects unknown blocking methods, including plan approval;
it never infers an answer from prose. A pending request id is spent before sending,
so duplicate/stale/cross-session/unknown replies fail rather than replay. Bad or
oversized frames, unexpected response ids, provider errors, closed input while a
question waits, and the bounded total deadline terminate the owned subprocess.
No automatic retry, resume, cancellation of another process, or fresh launch occurs.

The client reports `prompt-complete` only for a matching successful prompt result.
A sent response and a completed process do not establish that a human approved it,
that tools did useful work, or that durable dev-env inbox/claim state changed.
`failed` output uses fixed reason codes only; provider errors, exception text
and raw output are withheld. An unexpected client error is reported as
`internal-error`. A malformed request id is `invalid-request-id`; a reused one is
`duplicate-request`.
Coordinator receipt writes and provider input writes are nonblocking and share
the same deadline. If the coordinator stops reading stdout, the failure receipt
may be absent or partial, but the client still terminates its child.
Shutdown sends SIGTERM to the child's process group, waits up to one second for
the leader, then always sends SIGKILL to the group, so a child or grandchild that
ignores SIGTERM does not survive. It is not proof against a tool that escaped the
process group (for example with `setsid`) and is not a replacement for managed
task lifecycle.

## Qualification

```sh
nice -n 19 python3 -I -B -m unittest discover -s scripts -p 'test_cursor_acp_session.py' -v
```

Tests use an injected fake transport and a fake CLI script only:
initialize/new/prompt correlation, question/reply and denied permission (also
over the subprocess transport), stale and duplicate decisions, malformed ids,
early and cross-session updates, `fs/*` and `terminal/*` callbacks refused with
-32601, provider failures and early exit, bad inputs, bounded frames and
deadlines, an undrained coordinator output pipe, the child environment (synthetic
token names absent; names compared, values never read out), and a grandchild that
ignores SIGTERM. They neither start a provider nor touch credentials, existing
conversations, or live panes. Actual
provider compatibility, startup/version/trust, allowed identity and workspace,
resource policy, and live communication acceptance remain later prerequisites.
