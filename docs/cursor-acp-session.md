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

Pod tokens and keys held in environment variables (for example
`CLAUDE_CODE_OAUTH_TOKEN`) never reach the third-party binary through its
environment. That is an environment boundary only: the child runs as the same
user, so it can still read files that user can read, such as `/creds/gh_token`,
the ServiceAccount token, and anything under `HOME` or `XDG_RUNTIME_DIR`. A live
run therefore needs a separate identity or filesystem boundary, reviewed in its
own change. Any Cursor authentication variable, such as an API key, also needs a
separate approved change; this increment passes none.

If the client itself receives SIGTERM or SIGHUP, it still stops the child's
process group. SIGKILL to the client cannot be handled; the child then sees end
of input on stdin. A process that calls `setsid` to leave the group escapes.

## Coordinator input and receipts

Stdout contains bounded JSON events. Assistant text, reasoning, tool outputs,
plans, and stderr are not copied into receipts. `question-required`
contains the provider's explicit structured question and choices, not a terminal
capture. It is task content, not authenticated approval or generic metadata.
`permission-required` retains request/session correlation and option ids/kinds,
and adds a `context` object derived solely from this request's `params.toolCall`.
No transcript, terminal, pane, credential or earlier `session/update` is consulted.
The [ACP v1 request schema](https://agentclientprotocol.com/protocol/v1/schema#requestpermissionrequest)
uses `ToolCallUpdate`; its fields may be omitted. This client requires a valid
`toolCallId`, recognized `kind`, and nonempty `title` for usable context. It
projects only `kind`, `title`, `locations` (basename `path` and optional `line`),
and `rawInput`. Tool call ids, content, output, `_meta` and unknown fields are
not projected. This projection does not establish that an action is safe.

A usable example (synthetic data):

```json
{"event":"permission-required","requestID":43,"sessionID":"observed-session-id","options":[{"optionID":"deny","kind":"reject_once"}],"context":{"state":"available","kind":"execute","title":"Run checks","rawInput":{"command":"python3","args":["check.py"]}}}
```

Every projected string is limited to 256 characters and 256 UTF-8 bytes. The
serialized ASCII JSON context is limited to 2048 bytes; the source tool call is
limited to 4096 serialized bytes. Oversized strings are refused rather than
truncated. Lists/maps have at most eight entries and raw input has at most three
nesting edges. Control bytes, DEL, Unicode format controls and surrogates are
rejected. Paths become basenames, including path tokens within displayed prose.
Path reduction is lexical and does not read the filesystem. Free-form titles,
commands and argument strings containing a path separator plus whitespace,
quotes or angle brackets are ambiguous and yield insufficient context.
Explicit `path`/`paths` values safely reduce the whole path, including spaces.

ACP defines `rawInput` as arbitrary JSON; this deliberately narrower display
subset accepts strings, lists and objects. Only object keys `command`, `args`,
`path`, `paths`, `query` and `description` are copied, recursively. An object with
no recognized keys, unsupported scalar, invalid location or unknown tool kind is
insufficient. Omitted/null raw input and locations are allowed when kind/title
supply context. Source strings containing `token`, `key`, `secret`, `password`,
`auth`, `cookie` or `credential` (case insensitive), bearer prefixes, or long
base64/hex-like runs (32 or more characters) are replaced wholesale with
`[REDACTED]`. Secret-bearing object keys cause the containing object to become
that fixed marker, so key names cannot leak values. Whole path values are checked
before basename reduction; long paths may therefore be redacted conservatively.
This heuristic can redact benign data and cannot recognize every possible secret
encoding. Do not supply credentials.

Missing, malformed, oversized or unrecognized context yields
`context.state: "insufficient-context"`, usually with no other context fields.
A redacted title is retained as `[REDACTED]` with that state. The coordinator may
explicitly reject or leave the request unanswered until the existing deadline;
this state never enables approval. Even `available` context enables no approval.
No rejection is sent automatically.

**OP314 compatibility:** `scripts/cursor-acp-smoke-check.py` currently checks an
exact permission receipt schema. It deliberately refuses these enriched receipts
with `unexpected-field`; it must not be bypassed by stripping context to claim
new permission-context acceptance. Unchanged informational question streams still
pass. The fake-CLI tests assert both outcomes. A checker-schema extension and
live acceptance remain a later gate outside this three-file correction.

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

**An agent-selected command or tool approval is a permission decision, not an
ordinary informational question.** It must not be relabeled as `question-required`
or answered through the informational question route. Provider text and metadata
are untrusted task content, never authority to approve an action.

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
nice -n 19 python3 -I -B scripts/test_cursor_acp_session.py -v
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

OP318 qualification also covers full/missing/malformed/oversized permission
context, controls, secret values in title and raw input, unknown field dropping,
request/session mismatch, every non-reject decision, and OP314 schema compatibility.
Fail-first on freshly fetched main `55ff477e9f2f12afea9f4a18aaa025c9bb22fffc`:
32 tests ran in 12.266s; 15 new fake-CLI context fixtures failed with
`KeyError: context`. The unchanged client was used; only the tests were modified.
This is source and fake-CLI evidence, not installed or live provider acceptance.
