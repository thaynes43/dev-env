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
invalid or oversized later update, tombstones the id. Completion and failure
discard the metadata and retain a closed tombstone. Prompt end and process exit
clear the cache.

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
nested values, including `path` and `paths`, which still reduce to a basename. A
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

## Successor integration, 2026-11-01

The bounded successor imported the nested-path correction patch
`0049eaa158531583d24237305d014ed730351509570cfc9381fa24e3ad9cbc6a`
on exact prior head `52c99bf98fa481b61aeb32b255f06744bb531de3`.
All five file hashes in its producer receipt matched before integration edits.
The patch preserves inherited command and argument screening beneath `path`
and `paths`, adds direct and helper-to-checker negatives and benign basename
controls, and tests 33 completed tool ids followed by a correlated rejection.
The integrator corrected the helper guide and this record: completion drops
metadata, retains a bounded closed tombstone, and does not prevent a
`reject_once` reply when later context is unavailable.

Both assertion-enabled Python suites passed together: 120 tests in 20.233
seconds, under `nice -n 19` and the serial test lock. No provider was launched.
The source worker ran no tests. Its recorded start was 18:04:17Z and terminal
exit 0 was 18:21:27Z, beyond the requested 600-second bound and the 18:14:34Z
stop request. The terminal artifact is retained; bound compliance is not claimed.

The earlier automated cache-availability finding is answered by the intentional
bounded no-resurrection rule and the new rejection fixture. Closed ids remain
remembered until prompt or process cleanup; eviction could make an old id appear
new. This limits displayed context during a long prompt. It does not approve
permissions or establish question delivery. Native exact-head review, hosted CI,
guarded merge, signed publication and any separately qualified installation
remain later evidence levels. Live own-platform Cursor question/reply/ACK is
still unproven.

Final native review identified a helper/checker mismatch for a non-path field
beneath an `args.path` container. The checker now carries the same inherited
argument flag as the helper; an actual path string still cannot retain slashes.
A direct and cached helper-to-checker fixture for `args.path.description`
failed first with `invalid-permission-context`, then passed after the correction.
The same review found that a present null status was treated as omitted despite
the documented invalid-status rule. Null now tombstones the id, with the existing
malformed-status fixture extended to cover it.

After these integration fixes, both assertion-enabled suites passed: 121 tests
in 20.894 seconds under the same serial lock and priority limit. The automated
review independently reported the same inherited-argument mismatch; that finding
is covered by this correction and fixture. These are source and offline protocol
results, with no provider launch or runtime replacement.

## Opaque tool-ID source correction and structural diagnostic

A separately authorized reject-only development capture showed a same-session
tool update with the expected synthetic command, followed by an absent cache
entry and absent diagnostic ordinal. The later permission copy omitted that
input. The fixed-schema capture retained no actual tool ID characters or
length. Source inspection confirms that both paths used the same display-label
validator before storing or assigning the ID. This identifies the source drop
predicate without inventing the original ID spelling.

The source correction validates tool IDs as exact bounded UTF-8 strings,
independently of display labels. An invented punctuation-ID fixture reproduces
the observed update/status/partial-permission sequence: it fails before the
change and recovers the cached command afterward. Negative and compatibility
fixtures retain session correlation, cache bounds, closed/expired/poisoned
entries, privacy and reject-once policy. Null-status handling is unchanged;
that was not the observed cause. The optional private structural diagnostic
is default off and is described in the helper guide.

The source owner reports 105 helper tests and 39 checker tests passing, with
an independent native privacy/code review of the exact frozen files. The
integration coordinator also ran the hosted optimized-mode discovery command
locally: all 144 tests passed. Hosted CI reported an early test failure; its
actual cause and any correction must be recorded before merge. No retry or
flaky-pass inference follows from the local pass.

The development run sent a matching reject-once and ended with its sentinel
absent; the known child group was subsequently absent. Exact process exit UTC
was not retained, so the capture file timestamp does not prove a 120-second
process bound. This is not installed post-fix acceptance. A later permitted
installed-path check, and the separate platform question/answer/provider-ACK
chain, still need their own evidence.


## JSON depth and offline capture correction

The automated review attributed the hosted failure to the 2000-level nested
JSON fixture: that runner decoded the object instead of raising a refusal.
The helper had relied on CPython's recursion limit. It now scans UTF-8 input
before decoding and refuses more than 64 object/array containers, counting the
root. String delimiters and escapes do not affect that count. The decoder
continues to reject duplicate keys, invalid syntax and non-finite numbers.
Boundary fixtures cover object, array and mixed nesting below, at and above
the cap, including the original 2000-level case.

Independent integration review also identified a scheduling race in the three
offline subprocess diagnostic fixtures. They required a final capture marker,
but the production daemon writer intentionally gets at most 50 ms to close.
The offline subprocess harness now calls the original close and explicitly
joins that writer for at most five seconds before exiting. Capture completeness,
privacy, recovered command and rejection assertions remain required. Production
close and its separate stalled-storage regression remain unchanged. This is
source correction and offline qualification; installed metadata acceptance
and the separate platform question/answer/provider-ACK chain remain unverified.
