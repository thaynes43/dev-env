# Cycle 30 refusal localization, 2026-11-03

Root supplied actual installed operator `sha-1322f53` / digest `812a475c…`,
Flux revision `629349d5…`, two ready operator replicas and preservation evidence
at 09:37Z. Naturally scheduled cycle 30 started at 09:33:47Z. Codex succeeded;
Claude refused at `builtin_tokens_unqualified`, and Cursor refused at
`builtin_terminal_unqualified`. Root's normal cached API/native read exited zero
and truthfully retained the two unavailable sources. This is not recovery.

The Claude code covers three separate checks: required zero counters, other
unqualified top-level token fields, and contradictory nested counters. The Cursor
code covers unsupported escape sequences, private modes, parameters, operations,
characters, control bytes and bounds. These branches need distinct fixed codes
before a change to accepted protocol can be justified.

The diagnostic changes only label those existing refusal branches. The separate
qualified compatibility changes below accept one additional printable character,
one exact final dashboard footer, and one typed explicit zero-counter object.
All other acceptance predicates,
command arguments, process ownership and cleanup, account/version fences,
terminal screen semantics, stderr/frame size bounds, and collection cadence stay
unchanged. The new codes contain no vendor field names, values, characters,
escape parameters, output, tokens, identities or clocks. They remain valid only
at the builtin stage with an empty clientVersion. Existing code names stay in the
closed decoder for compatibility.

Synthetic regression cases first failed with the two ambiguous original codes
(Go test package elapsed 0.006s); the same ten cases then passed (0.003s).
They also assert the public ErrCapture contract and absence of a private sentinel
from diagnostics. Terminal failures authorize neither input nor a capture. No
provider client, login, model, manual acquisition or private history was used.

Publication and installation of this delta would only improve diagnosis on a
naturally scheduled cycle. A causal fix, two actual all-provider success cycles,
and fresh API/native/UI acceptance remain required within the original window.


## Retained Cursor ellipsis counterexample

Parent Root supplied a retained 07:07 version-qualified manual autocomplete
observation containing `Create custom subagents for specialized AI…`. Current
source already skips OSC8 hyperlink escapes; OSC8 is not the proposed cause.
The terminal whitelist excludes U+2026 ELLIPSIS. Feeding that exact retained
visible text to the existing terminal refused it (focused regression RED,
0.003s). Add only U+2026 as one visible character. Preserve it in the screen;
do not strip it or allow arbitrary Unicode.

Regressions cover the exact usage selection alongside this unselected
printable description, and refusal of wide, bidi, combining, unknown, control
and invalid UTF8 input, confirmation prompts and bounds overflow. This is a
concrete compatibility defect. The manual PTY width differed from the helper's
180 columns, and raw ANSI/cursor coordinates were not retained, so it does not
prove the actual cycle 30 terminal refusal had this cause. New scheduled
runtime evidence remains necessary. Claude's actual path has already accepted
an assistant usage report, a successful result, zero turns and zero cost before
refusing a usage-token counter; the three fixed subreasons preserve those guards
without treating unknown, missing, null or object values as numeric zero.


## Retained Cursor dashboard footer counterexample

Parent Root supplied the exact retained visible footer
` View in dashboard: cursor.com/dashboard?tab=usage`. The global question-mark
fence rejects its literal query marker even after the separately observed
ready, selected command and final quota screen states. A regression through
those real state-machine transitions reproduced this refusal (RED0.003s).

Exempt exactly one question mark on exactly one inactive row matching this
literal dashboard label, only in final usage-display states3/4 with a usage
title and inactive `Esc to close` row. Retain the entire visible text for all
other guards and quota validation. Any additional question mark still refuses;
no arbitrary URL is skipped. The final display still passes its existing
unique-title, reset, category and quota-row checks. This is a second concrete
visible-contract compatibility defect, not proof of the earlier cycle30 cause.

The meaningful regression passed (0.004s) together with negatives for ready,
trust and selection states; active or duplicate footers; adjacent, active,
prefixed and suffixed questions; foreign hosts/query/prefixes; incomplete
displays; confirmation and authentication text. The full71136 review remains
immutable; the amended head needs an independent current delta review and CI.


## Pinned Claude explicit zero-counter contract

The official public NPM platform package `@anthropic-ai/claude-code-linux-x64`
version2.1.296 contains readable embedded source. Its tarball SRI was verified
without installing or executing the client. Archive SHA256:
`eef5a2e2b09a5e7d2ce784d1b3ba335ae7fc6c7f6256c802a0ebf2c21e4fd0e1`;
native member SHA256:
`24972e3bc859fab2b46ed4c1e51f7d6130f06d3bd550811a114640de3370d0de`.
Public source:
<https://registry.npmjs.org/@anthropic-ai/claude-code-linux-x64/-/claude-code-linux-x64-2.1.296.tgz>.

At binary byte offset208145935 the usage schema declares
`output_tokens_details` as an optional/nullable object with an optional integer
`thinking_tokens` counter. This schema alone does not make absent/null counters
zero. At offset213965714 the pinned zero-usage constructor explicitly sets
`output_tokens_details` to an object containing numeric-zero `thinking_tokens`,
alongside all four numeric-zero aggregate token counters.

The constructor-to-result connection is explicit in the same artifact. The SDK
result factory `eFn` at offset213991063 imports `uf` from
`chunk-srmqtve7.js` (import at213968657), copies it into the result's `usage`
field, and sets cost zero. Its no-outcome branch returns success with
`is_error:false` and `num_turns:0`. The SDK/bridge `sendResult` call site at
231696637 calls that factory and passes the result to `Tr`; this is an explicit
zero-turn result publication path, not just an unused schema or constant.
This identifies an SDK/bridge path; it does not assert that Root's print command
used this particular path.

There is also a complete engine aggregation path. At224362006 the engine imports
that same `uf`. Its `ns` function at224437667 spreads `uf` and explicitly builds
`output_tokens_details.thinking_tokens` from a per-model sum with initial value
zero. At224426459 the final-result common fields set `usage:ns()`; the success
branch passes those fields and its turn count to `M7`, then yields the result
via `j(gr,Rs)`. The `M7` builder at208139610 copies the common fields into a
`type:"result"` record. Thus the qualified object is carried into result records,
including the explicit zero-turn factory path above. The earlier233421572
excerpt only showed `usage:Z.usage`; that excerpt alone did not establish the
constructor connection and is not used as the sole qualification.

That exact known structure fails the former blanket object rejection (real
reader RED0.003s).

Allow only this exact top-level field, with exactly one `thinking_tokens` key
whose value independently passes numeric-zero validation. Missing, null, empty,
scalar, array, extra-field, string and positive variants refuse. Unknown
object-valued token fields still refuse; the nested counter walk still rejects
contradictory counters. Case-insensitive token-key detection also prevents an
unknown capitalized token field from hiding activity. Required aggregate
counters, zero turns/cost and cleanup/binding/version guards remain mandatory.
The focused known-object and strict-negative regressions passed (0.004s).

This qualifies a concrete pinned-version compatibility defect without a
provider command, model call, login, private history or raw provider frame.
The public artifact is not an attestation of Root's running binary hash, and it
does not prove which counter branch caused cycle30. Current source/review/CI
must be refreshed; installed actual captures, usable quota rows and TWO complete
all-provider cycles/API/native/UI remain unproven.
