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

This delta only labels those existing refusal branches. All acceptance predicates,
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
