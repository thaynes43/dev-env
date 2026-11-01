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

Provider-to-client JSON frames are bounded by `MAX_PROVIDER_FRAME`: 4 MiB
(4,194,304 bytes), including the terminating newline. This allows file contents
and tool output in `session/update` frames above the former 64 KiB bound.
Coordinator input, client-to-provider frames and receipts retain the 64 KiB
`MAX_FRAME` bound. Reads remain in chunks of at most 4096 bytes, and an
incomplete frame buffer holds at most its limit plus one byte. Oversized input is
refused without draining it.

Provider frames also share a byte budget of `MAX_PROVIDER_FRAME`. The reader
takes a frame's wire bytes, newline included, before parsing it, and the client
returns them after handling it, first in, first out. While the budget is full,
the reader waits and reads nothing more, so a fast provider blocks on its own
writes. With nothing outstanding, any frame within the bound is admitted. The
128-entry event queue can no longer fill with parsed 4 MiB frames. In the worst
case the client holds:

- parsed or parsing provider frames whose wire bytes total at most
  `MAX_PROVIDER_FRAME`;
- one more raw frame that waits for budget; and
- the read buffer that frame came from (at most the limit plus one byte, with a
  short-lived second copy while it grows or splits).

Parsed JSON is larger than its wire form. Measured with `tracemalloc` on Python
3.11, the peak reached about 45 times the wire size for deeply nested empty
lists. It was about 25 times for empty objects, 13 times for short strings and
2 times for one long string. Provider framing therefore holds roughly 200 MiB at
worst. Coordinator input has no budget. It is the caller's own input: at most
128 queued frames of at most 64 KiB each (8 MiB of wire bytes).

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
and adds a `context` object. Explicit `kind`, `title`, `locations` and `rawInput`
on this request's `params.toolCall` are used as given. A null or omitted field
can be filled from a sanitized `tool_call` or `tool_call_update` in this same
session with the exact same `toolCallId`. Assistant text, reasoning, tool
`content`, `rawOutput`, diffs and transcripts are not stored and are not read
back. No terminal, pane or credential is consulted.

The cache is process memory only: at most 16 entries, 8192 bytes of sanitized
JSON, and 300 seconds from the last valid update for that id. Ids that do not
fit are remembered in a dropped set of at most 16. One further uncached id sets
a saturation latch and is not stored. While that latch is set, every id that is
not already cached fails closed with `tool-cache-bound`, including a later
explicit benign permission for an id the cache could not record. The cache is
not evicted to make room, and the dropped set does not grow. Completion or
failure discards that call's metadata and retains a closed tombstone, which
still counts toward the 16 entries until the prompt completes or this process
exits. Closed ids cannot be resurrected by a later benign update. More than 32
distinct ids in one prompt can exhaust context availability; permission events
and correlated `reject_once` replies still work. Prompt completion and process
exit clear the cache. A later kind or title that disagrees,
or any redacted, invalid or oversized update, tombstones the id. A `status`
that is present and is not a string equal to `pending`, `in_progress`,
`completed` or `failed` tombstones the id as well, including a list or object;
that value does not leave the helper. An older benign snapshot cannot come
back. Unknown ids, another session, expiry, completion and a full cache fail
closed. Usable context still needs a recognized `kind` and a nonempty `title`,
from the request or from that cached update. `toolCallId` is only the
correlation key. The [ACP v1 schema](https://agentclientprotocol.com/protocol/v1/schema#toolcallid)
defines it as a string. The helper keeps its exact UTF-8 spelling in memory,
with a local 1024-byte limit, rather than applying the display-label grammar.
It does not trim or normalize tool IDs, put them in paths, or emit them in
context or diagnostics. Session, request and option identifier rules remain
unchanged. A source correction for this distinction does not prove that a
particular installed provider request now has usable context.

The [ACP v1 request schema](https://agentclientprotocol.com/protocol/v1/schema#requestpermissionrequest)
uses `ToolCallUpdate`; every field except `toolCallId` may be omitted. This
client projects only `kind`, `title`, `locations` (basename `path` and optional
`line`), and `rawInput`. Tool call ids, content, output, `_meta` and unknown
fields are not projected. This projection does not establish that an action is
safe. It does not approve or run the action.

A usable example (synthetic data):

```json
{"event":"permission-required","requestID":43,"sessionID":"observed-session-id","options":[{"optionID":"deny","kind":"reject_once"}],"context":{"state":"available","kind":"execute","title":"Run checks","rawInput":{"command":"python3","args":["check.py"]}}}
```

Every projected string is limited to 256 characters and 256 UTF-8 bytes. The
serialized ASCII JSON context is limited to 2048 bytes; the source tool call is
limited to 4096 serialized bytes. Oversized strings are refused rather than
truncated. Lists/maps have at most eight entries and raw input has at most three
nesting edges. Control bytes, DEL, Unicode format controls, surrogates, and the
line and paragraph separators U+2028 and U+2029 are rejected. Each string is
displayed in its NFKC form, so fullwidth or compatibility slashes become real
separators before path reduction. Paths become basenames, including path
tokens within displayed prose.

**Non-ASCII context fails closed by design.** The NFKC display form must be
printable ASCII (U+0020 to U+007E). Fullwidth and other compatibility forms that
normalize to ASCII stay displayable; everything else yields insufficient context:
accented letters (`Café`, composed or decomposed), combining marks, homoglyphs
such as Cyrillic `р`/`а` in `раssword`, and fillers such as U+3164 in
`pass` + U+3164 + `word`. The secret patterns below are ASCII, so text they
cannot see through is refused rather than shown. The same rule applies to every
percent-decoded form, to object keys and to dropped fields, so
`%D1%80assword` and a non-ASCII key fail the whole context closed.
Path reduction is lexical and does not read the filesystem. Free-form titles,
commands and argument strings containing a path separator plus whitespace,
quotes or angle brackets are ambiguous and yield insufficient context.
Explicit `path`/`paths` values safely reduce the whole path, including spaces.
Absolute `args` also reduce to a basename. A structured `args` element that is
a relative pattern is kept whole: a `.` or a segment of letters, digits, `.`,
`_` and `-`, or the segment `...`, separated by `/`, with no `..` and no `~`.
`./...` and `./internal/...` are the representative cases. The same checks
descend into nested values under `args` or `command`, including `path` and
`paths`. These path fields still reduce to a basename while retaining the
inherited command and argument checks. A nested
`description` such as `echo;rm` therefore fails closed. Before the `..` check,
`\` is folded to `/`, so `..\\parent\\file.py` in `args` or `command` cannot
reduce to `file.py` and look available. The kept pattern still uses `/` only.
A free-form `command` or `title` such as `go test ./...` stays insufficient
context, including when that prose is nested under `args`. The client does not
split that prose into argv and does not execute it. This does not adopt a
human-only command rule or a fixed command allowlist. `command` and `args` also
fail closed when they contain a shell metacharacter (`;`, `&`, `|`, `<`, `>`,
`` ` ``, `$`, `(`, `)`, `{`, `}`), `~`, or a `..` segment. Credential
assignment, dynamic substitution and control operators stay fail-closed under
those rules and the secret checks below.

A correlation failure adds one fixed `context.reason` and no request text:
`no-correlated-tool-call`, `tool-call-unavailable`, `tool-call-conflict`,
`tool-call-closed`, `tool-call-expired` or `tool-cache-bound`.
`scripts/cursor-acp-smoke-check.py` accepts a preserved relative argument only
in a structured `args` string that matches the grammar above, and accepts
`reason` only when it is the sole extra field and one of those six words. A
slash in a title, location, `command`, `path`, `query` or top-level
`description` is still refused. Any other reason or extra field is refused.
The verdict still records only the state.

ACP defines `rawInput` as arbitrary JSON; this deliberately narrower display
subset accepts strings, lists and objects. Only object keys `command`, `args`,
`path`, `paths`, `query` and `description` are copied, recursively. An object with
no recognized keys, unsupported scalar, invalid location or unknown tool kind is
insufficient. Omitted/null raw input and locations are allowed when kind/title
supply context.

A source string or object key is secret-like when it, its percent-decoded form
or its NFKC form contains any of the patterns below. Decoding and normalizing
repeat, closed under both, for at most four rounds; text still changing after
the fourth round (four or more percent-encoding layers, such as
`%25252570assword`) fails closed as insufficient context.

- a name, case insensitive, matched anywhere, so `_`-joined forms such as
  `DB_PW`, `API_KEY` and `GH_TOKEN` match: `token`, `key`, `secret`, `password`,
  `passwd`, `passphrase`, `pwd`, `pw`, `auth`, `cookie`, `credential`, `bearer`,
  `session`, `signature`, `private` or `jwt`. `pass` matches when no letter
  touches it (`DB_PASS`, `SMTP_PASS`, `--pass`, `PASS1`, but not `compass`,
  `bypass` or `passport`) or as a camelCase part after a lowercase letter
  (`dbPass`). JSON inside a string, such as `{"pw":"..."}`, matches by name;
- a bearer prefix, or a base64/hex-like run of 32 or more characters (`/`
  counts, so long absolute paths match conservatively);
- URL userinfo, meaning any `@` after `://` in the same whitespace-free token
  (so a password holding `/`, `?` or `#` matches, and so does a harmless
  `?mail=a@b` query), or bare `user:password@`;
- a short credential flag: `-p` or `-P` followed by any value, attached or
  spaced (mysql, sshpass, zip); `-k`, `-K` or `-a` followed by a spaced value
  that does not start with `-` (openssl enc, redis-cli); and `-u`, `-U`,
  `--user` or `--proxy-user` followed by a `user:password` or `user%password`
  value.

Each string is checked before basename reduction. The title, every raw input
value in source order, and the location paths are also checked joined by spaces,
so a flag and its value in separate list items or fields (`["-u",
"user:password"]`, `["--password", "value"]`) are caught. They are checked once
more concatenated with no separator, for names, userinfo and flags (not the
32-character run, which gluing words and paths would trip), so a name or
userinfo split across fields (`["--pa", "ssword=value"]`,
`["https://user:", "password@host"]`) is caught too.

The heuristic prefers false positives: harmless forms such as `mkdir -p build`,
`patch -p1`, `grep -P x`, `curl -k https://...`, `ls -a build`, a
`/private/...` path, or `cp` followed by an argument starting with `w` (glued
into `pw`) also fail closed. Attached flag clusters such as `ls -al` and
`curl -kL` stay displayable.

It cannot recognize every possible secret. Known keyword-less gaps, accepted:
short values in query parameters with no listed name (`?code=...`,
`?sig=...`, `?otp=...`); a bare AWS access key id (`AKIA` plus 16 characters,
shorter than the 32-character run); a `-k` or `-a` value attached to its flag;
names glued without a letter boundary in all capitals or all lowercase
(`DBPASS`, `dbpass`); positional secrets with no flag or name (for example
`htpasswd -b file user value`); tool-specific short flags not listed above; and
encodings other than percent-encoding and NFKC (base64 shorter than 32
characters, hex escapes or other obfuscation). Do not supply
credentials.

Any secret-like match fails the whole context closed. The receipt then carries
`state: "insufficient-context"`, the validated `kind`, `title: "[REDACTED]"` and,
when the source had raw input, `rawInput: "[REDACTED]"`. No title, location or
raw input text survives beside a redaction, even from a field that looked clean.

Missing, malformed, oversized or unrecognized context also yields
`context.state: "insufficient-context"`, usually with no other context fields.
The coordinator may explicitly reject or leave the request unanswered until the
existing deadline; this state never enables approval. Even `available` context
enables no approval. No rejection is sent automatically.

**OP314 compatibility:** `scripts/cursor-acp-smoke-check.py` accepts this optional
`context` on a permission receipt, checks it against the bounds above and records
only its state (`available`, `insufficient` or `absent`), never the text. A
permission rejection stays apart from a structured question decision. See
[the checker](cursor-acp-smoke-check.md). Live acceptance remains a later gate.

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
Framing failures still exit 2 with `event: "failed"` and `reason: "invalid-frame"`.
Their metadata-only `source` is the fixed value `provider` or `coordinator`, for
the stream that failed. Their `detail` distinguishes:

- `frame-too-large`, with `byte_count` and `limit`. `byte_count` is always
  `limit` plus one: the client refuses at the first byte past the limit and
  reads no further, so the count never gives an oversized frame's full size.
- `frame-malformed-json`, for invalid JSON/encoding or a non-object frame.
- `frame-duplicate-key`, without copying the duplicate key.
- `frame-truncated-eof`, with `byte_count` for buffered bytes lacking a newline
  when EOF arrives.

For example, an oversized provider frame produces only:

```json
{"event":"failed","reason":"invalid-frame","detail":"frame-too-large","source":"provider","byte_count":4194305,"limit":4194304}
```

No frame content, transcript, file contents or parser exception text is added.
Stream I/O failures retain the generic `invalid-frame` reason. The old receipt
for Grok run `ded6cc77` (GROK-S3) cannot prove which framing cause occurred.
That frame is unavailable. A plausible cause is a valid update larger than
64 KiB; this remains a hypothesis, not a recovered or reproduced provider frame.

The smoke checker (`scripts/cursor-acp-smoke-check.py`) accepts these
enriched receipts only on `reason: "invalid-frame"`. It checks the `detail` kind
against the four fixed kinds; a non-string detail is refused, not a crash.
`source` is optional, so receipts written before it still pass; when present
it must be `provider` or `coordinator`, and it is allowed only beside `detail`.
`byte_count` is required for `frame-too-large`
and `frame-truncated-eof`, and forbidden otherwise. `limit` is required only
for `frame-too-large`, where `byte_count` must exceed it. Every count is a
positive integer up to 2^31. Any other field, or a detail on another reason,
is still `unexpected-field`; a malformed detail or source is
`invalid-failure-detail`. The verdict reports `helperFailureDetail` as kind,
source and counts only. One test builds each receipt from the client's own
`FrameRefusal`, the way `main()` does. Another pipes the real receipt stream of
each provider framing failure through the checker. Successful receipt streams
and generic failure receipts keep their existing checker behavior.

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
nice -n 19 python3 -I -B scripts/test_cursor_acp_smoke_check.py -v
```

CI runs both suites in one step, with assertions stripped (`-O`), so the tests
use `unittest` assertions only:

```sh
nice -n 19 python3 -O -B -m unittest discover -s scripts -p 'test_cursor_acp_*.py' -v
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

Frame qualification includes synthetic frames just below and at 4 MiB,
refusal at 4 MiB plus one byte without draining, each framing detail,
UTF-8 byte counts at truncated EOF, a 200 KiB tool update, coordinator input's
unchanged 64 KiB bound (65,536 bytes with the newline accepted, 65,537 refused),
the `source` of each failure, and provider cleanup on every framing failure.
The byte budget has two tests. With no consumer, a reader over three frames of
just over half the budget queues exactly one, uses exactly that frame's bytes
and stops short of the stream's end; each release admits the next one. Three
such updates over the fake CLI complete, so the transport releases the budget
after handling each frame.

OP318 qualification also covers full/missing/malformed/oversized permission
context, controls, secret values in title and raw input, unknown field dropping,
request/session mismatch, every non-reject decision, and OP314 schema compatibility.
Fail-first on main `55ff477e9f2f12afea9f4a18aaa025c9bb22fffc`, re-measured with the
test file at `6b9ae3d7`: 34 tests ran; all 30 fake-CLI context fixtures failed
with `KeyError: context`. The unchanged client was used; only the tests changed.

The review fix after `6b9ae3d7` adds URL userinfo, short-secret, split-flag,
fail-closed, percent/NFKC, combining-mark and separator cases: 22 fake-CLI
fixtures and 9 direct projection tests. Against the unchanged `6b9ae3d7` client,
43 tests ran: the 22 new fixtures, the 5 earlier secret fixtures (whose expected
state is now `insufficient-context`) and 8 of the 9 new tests failed; the ninth
is a no-false-positive guard that passes on both. With the fix, 43 tests pass.

The re-review of `5792587b` adds underscore/camelCase `pass`, unsettled
percent-encoding, non-ASCII (homoglyph) context, `-P`/`-k`/`-K`/`-a` flags,
names and userinfo split across fields, userinfo whose password holds `/`, `?`
or `#`, and a timing guard: 11 fake-CLI fixtures and 8 direct projection tests.
The existing `Café` check now expects insufficient context. Against the
unchanged `5792587b` client, 51 tests ran: the 11 new fixtures, 6 of the 8 new
tests and the changed `Café` check failed (111 failures, counting subtests); the other two
new tests (`pass` inside words and the timing guard) pass on both. With the fix,
51 tests pass, and `scripts/test_cursor_acp_smoke_check.py` (14 tests) passes.
`permission_context` on 33 adversarial 4096-byte sources, under `nice -n 19`,
took at most 32 ms (best of three; `:a` repeated), against a 100 ms budget;
the unchanged client took at most 22 ms on the same set.
This is source and fake-CLI evidence, not installed or live provider acceptance.

The 2026-11-01 permission-context change is source plus the targeted tests in
`scripts/test_cursor_acp_session.py` and `scripts/test_cursor_acp_smoke_check.py`.
The checker now follows the helper for recovered relative argv and the six
correlation reasons. Nested shell syntax, backslash parent segments, a
non-string status, and a saturated dropped-id set fail closed. This paragraph
does not record a run of those tests and it is not live provider acceptance.
Reject-once is unchanged. Recovered context does not approve an action. See
[the source note](../.agents/sagas/distributed-dev-env/verification/op318-permission-context-2026-11-01.md).

## Optional private structural diagnostic

`--diagnostic-file BASENAME` is off by default. When explicitly selected, the
helper exclusively creates a 0600 regular file directly in the private
worktree; it refuses an existing target, symlink or path escape. Default
stdout, stderr and permission policy stay the same. The file contains only
known schema field presence and types, fixed enum tags, counts, in-memory
correlation ordinals and fixed cache/projection flags. Content, raw output
and extension containers remain opaque. Raw IDs, commands, titles, unknown
keys or values, history, credentials and hashes of those values are omitted.
One fixed synthetic-command comparison produces only a boolean.

Capture is bounded to 64 records and 96 KiB, at most 8 KiB per record, with
bounded shape traversal and an asynchronous writer. Stalled storage cannot
hold up permission handling. A final `capture-end` marker is required for
complete evidence; `capture-bound` means truncated capture. A missing end
marker or write failure leaves incomplete evidence and does not authorize
a retry or a permission decision. This diagnostic does not fetch history,
change protocol version or establish question delivery, model understanding
or action execution. Any real run needs its own bounded authorization.
