# OP318 permission context, 2026-11-01

Source-only correction for the Cursor ACP helper and its offline smoke checker.
This file records the source change. It is not a test run, not live provider
acceptance, and not approval to execute a tool or to change the reject-once
decision.

The frozen input patch
`276c6fdbb7952b30be1b7beb4c388ab5727e4e148599de6e8aa34809fa44b251`
was already applied on base
`9aa09b6bdbf85f65bd46f544d3033373c3829e71`. This correction does not replace
that implementation. It closes four review defects in the helper and the
checker gap those receipts exposed. No image was published and no helper was
installed. This worker did not run the tests.

## What the source does

`session/request_permission` may carry a `toolCall` that is only a
`toolCallId`. The helper keeps sanitized `kind`, `title`, `locations` and
`rawInput` from `tool_call` and `tool_call_update` notifications in that same
session, keyed by the exact id. A later permission with omitted fields can show
that sanitized context. Explicit fields on the permission are not replaced when
they disagree with the cache. A conflicting kind or title, or a redacted,
invalid or oversized later update, tombstones the id. Completion, failure,
prompt end and process exit clear the cache.

Bounds: 16 entries, 8192 bytes of sanitized JSON, 300 seconds from the last
valid update, and 16 remembered ids that did not fit. One further uncached id
sets a saturation latch and is not stored. While the latch is set, every id
that is not already cached fails closed with `tool-cache-bound`, including a
later explicit benign permission. The cache is not evicted and the dropped set
does not grow. Unknown ids before saturation, another session, expiry and
completion still fail closed with a fixed `context.reason` and no request text.
Assistant text, reasoning, `content`, `rawOutput` and transcripts are not stored.

A `status` that is present and is not a string equal to `pending`,
`in_progress`, `completed` or `failed` tombstones that id
(`tool-call-unavailable`). A list or object does not leave the helper.

Structured `args` that match a relative pattern, including `./...` and
`./internal/...`, stay visible. Those `args` and `command` checks descend into
nested values, except `path` and `paths`, which still reduce to a basename. A
nested description such as `echo;rm` fails closed. Before the `..` check, `\`
is folded to `/`, so a backslash parent segment in `args` or `command` cannot
reduce to a basename and look available. The kept pattern uses `/` only.
Absolute args still reduce to a basename. A free-form string such as
`go test ./...` stays insufficient context, including under `args`. The helper
does not infer a command from a title and does not run one.

## Checker

`scripts/cursor-acp-smoke-check.py` accepts a preserved relative argument only
in a structured `args` string that matches the helper grammar. It accepts
`reason` only as the sole extra field on insufficient context, and only for the
helper's six words: `no-correlated-tool-call`, `tool-call-unavailable`,
`tool-call-conflict`, `tool-call-closed`, `tool-call-expired`,
`tool-cache-bound`. Bare and redacted shapes, size bounds, and dropping the
text from the verdict are unchanged. A slash in a title, location, command,
path, query, or top-level description is refused. An unknown reason or any
extra field is refused. A permission receipt stays distinct from a question
decision.

`docs/cursor-acp-smoke-check.md` is updated so it does not keep the previous
"slash refused anywhere" sentence. `docs/cursor-acp-session.md` is the helper
description.

## Free-form commands

A free-form command or title remains insufficient context. This correction does
not choose a human-only command rule or a fixed command allowlist. It does not
add an actor, config, credential, or replay path.

## Decision path

The permission decision path still accepts only `reject_once`. `allow_once`,
`allow_always` and `reject_always` still raise
`permission-approval-not-supported` and send nothing. Recovered context does
not approve an action.

## Integrator qualification, 2026-11-01

The integrator imported the finite combined correction patch `dcdffb0f26edbe300335c55ec180005e8d992056fba97ccaa21f2a70ada8daef` on source `9aa09b6bdbf85f65bd46f544d3033373c3829e71`; all six owned file hashes matched the producer receipt. The separate checker documentation patch was reviewed and included because its existing path/reason contract needed the same update; it changes no code or policy.

One assertion-enabled local run of both Python suites passed: 116 tests in 20.318 seconds, under nice 19 and the shared serial test lock. This includes actual helper-to-checker fixtures for recovered structured relative arguments, the six fixed correlation reasons, malformed status, nested dynamic arguments, backslash traversal, and poison after bounded cache saturation. The worker ran no tests. No real provider process, permission approval, runtime helper replacement or platform question delivery was exercised by this qualification.

The relay records controller start at 17:32:30Z and exit 0 at 17:51:44Z, later than its requested 900-second bound. The integrator preserves that timing discrepancy and its freeze/stop request; it does not certify the requested wall-clock bound as met. Source hashes in the final receipt match the 17:47:50Z observation.

Independent final native review, hosted CI, guarded source merge and signed publication are subsequent gates. Installation and real Cursor question/reply/recipient acknowledgement remain unverified. The existing relay's project-scope gap and a default-off adapter proposal are recorded privately; this source change adds no actor, access or configuration to resolve them.
