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
qualified Cursor compatibility change below accepts one additional printable
character. All other acceptance predicates,
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
