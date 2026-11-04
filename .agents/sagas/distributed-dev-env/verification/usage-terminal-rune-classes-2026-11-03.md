# Cursor refused-rune Unicode classes, 2026-11-03

After the approved collector handoff (operator `sha-2d42e72`), the first natural
cycle 57 produced a fresh Codex row. Claude refused at the version stage
(`version_unqualified`, client 2.1.312; handled separately). Cursor refused at
the builtin stage with the generic `builtin_terminal_rune_unqualified`. The
refused character is unknown. The existing finer reasons (encoding, combining,
spacing, format, dot-set) did not match, so the character is a visible,
non-spinner character outside the qualified set.

The earlier dot-set note (`usage-dotset-refusal-diagnostic-2026-11-03.md`) says every
other unqualified character keeps its existing coarse reason; this change refines
that coarse bucket and supersedes that sentence, leaving the dot-set label itself
unchanged.

This change adds closed labels for the remaining Unicode classes, so the next
natural cycle can localize the refusal without disclosing the character:

Labels are assigned in this order, first match wins (after the older encoding,
combining, spacing, format and dot-set labels, which come first). Each row covers
only what the earlier rows did not take, so a character belongs to the first class
that matches it, not to a block it happens to sit in.

| order | reason | what it covers (after the earlier rows) |
|---|---|---|
| 1 | `builtin_terminal_letter_unqualified` | any letter |
| 2 | `builtin_terminal_number_unqualified` | any number, wherever it sits (for example U+2776 to U+2793 dingbat digits are numbers) |
| 3 | `builtin_terminal_dash_unqualified` | dash punctuation (Pd) |
| 4 | `builtin_terminal_quote_unqualified` | initial and final quote punctuation (Pi, Pf) |
| 5 | `builtin_terminal_punct_unqualified` | all other punctuation |
| 6 | `builtin_terminal_c1_unqualified` | C1 control characters (U+0080 to U+009F; C0 and DEL are refused earlier as control bytes) |
| 7 | `builtin_terminal_shape_unqualified` | any symbol in U+25A0 to U+25FF, including its math symbols |
| 8 | `builtin_terminal_math_unqualified` | math symbols (Sm) outside the shape block (for example U+266F is math, not pictograph) |
| 9 | `builtin_terminal_currency_unqualified` | currency symbols (Sc) |
| 10 | `builtin_terminal_checkmark_unqualified` | the remaining check and cross marks: U+2714 to U+2718, U+2705, U+274C, U+274E (U+2713 is accepted earlier and is never refused) |
| 11 | `builtin_terminal_star_unqualified` | stars, asterisks and flower dingbats: U+2605, U+2606, U+2721 to U+274B (spinner-style asterisks included) |
| 12 | `builtin_terminal_miscwin1_unqualified` ... `builtin_terminal_miscwin8_unqualified` | what is left of the Miscellaneous Symbols block (U+2600 to U+26FF) in eight aligned windows of 32 code points, after rows 2, 8 and 9 and the star and check rows: win1 U+2600 to U+261F, win2 U+2620 to U+263F, win3 U+2640 to U+265F, win4 U+2660 to U+267F, win5 U+2680 to U+269F, win6 U+26A0 to U+26BF (since Round 4 in four parts of 8: `miscwin6a` U+26A0 to U+26A7, `miscwin6b` U+26A8 to U+26AF, `miscwin6c` U+26B0 to U+26B7, `miscwin6d` U+26B8 to U+26BF; plain `miscwin6` is no longer emitted but still decodes), win7 U+26C0 to U+26DF, win8 U+26E0 to U+26FF |
| 13 | `builtin_terminal_dingbat_unqualified` | what is left of the Dingbats block, U+2700 to U+27BF (ornaments, crosses, dingbat arrows) |
| 14 | `builtin_terminal_pictograph_unqualified` | the remaining symbols from U+1F000 up (emoji and other pictographs; the number and math code points in that range, such as U+1F100 and U+1F8D0, are taken by rows 2 and 8) |
| 15 | `builtin_terminal_symbol_unqualified` | every remaining symbol (including braille patterns outside the dot set) |
| 16 | `builtin_terminal_rune_unqualified` | private-use, unassigned and noncharacter code points only

Class sizes are not uniform. The checkmark class is the most guessable and, with the four window-6 parts added in Round 4, is tied for the smallest at 8 code points (U+2705, U+2714 to U+2718, U+274C, U+274E), then quote (about 22), dash (about 26) and star (about 45). The eight Miscellaneous Symbols windows hold 30 to 32 characters each (window 1 loses the two stars, window 4 loses U+266F); the remaining classes are larger. A label still names a class of at least that many characters, never one character. This round makes the split below the earlier dash and quote classes deliberately, because the refusal sits in a family where no coarser label can find the glyph; a finer split than the checkmark class is not made.

The existing precedence is unchanged: encoding, combining, spacing, format and
the fixed dot set come first. Every label covers at least several characters (the checkmark class, 8, is the smallest), so it cannot
identify the one seen. No rune, code point, text, count, hash or extra field is
emitted: the same five JSON fields, Cursor-only provider, builtin-only stage and
empty `clientVersion` remain mandatory (the decoder rejects any change). The
acceptance predicate is unchanged: every unqualified character still refuses
before it is rendered, and a refusal advances no protocol state.

Tests: each class is exercised with several members, byte-identical diagnostic
output across members, absence of the private sentinel and the sample, no
protocol advance in any state, provider/stage/payload boundaries, and the
generic label for private-use and unassigned code points. Older tests that
asserted a coarser label for a snowman and a few other characters now assert
their classes (the snowman moved from the generic label to pictograph in the first
round, to the broad Miscellaneous Symbols label in the second round and to a window in the third round).

This is diagnostics only. It qualifies no character, widens no input and runs no
provider. Qualifying a character, if the evidence supports it, is a separate
reviewed change with its own evidence. Installed behavior, usable Cursor rows and
two complete all-provider cycles remain unproven.

## Round 2 (after the first natural cycle with the class labels)

Cycle 64, on operator `sha-84fa61c`, read Claude and Codex (known last-reported
values, provider clock unknown) and refused Cursor at
`builtin_terminal_pictograph_unqualified`: a symbol in U+2600 to U+27BF or from
U+1F000 that the earlier rows (letters, numbers, punctuation, C1 controls, shapes,
math and currency) did not take. The client binary is not part of this
repository's image and Root's private client is out of reach, so the next step is
to narrow the same class further with labels only. The pictograph label is split
into the four sub-classes above plus the emoji remainder. Each still covers at least several
characters (the smallest, checkmark, covers 8), still emits no character, code point or count, and qualifies nothing:
a refusal remains a refusal until evidence supports accepting a specific glyph.

Not covered by any pictograph-family label, by design: symbols outside U+2600 to
U+27BF and below U+1F000 that are not shapes, math or currency (for example
U+2B50, U+231A, U+2B1B). They reach `builtin_terminal_symbol_unqualified`, so a
future refusal at that label points outside this family.

## Round 3 (after cycle 71)

Cycle 71 on operator `sha-1365b60` refused Cursor at `builtin_terminal_miscsymbol_unqualified`: the
glyph is a symbol in the Miscellaneous Symbols block (U+2600 to U+26FF) that the earlier rows (numbers,
math, currency, stars, check marks) did not take. That label is replaced by eight window labels that
split the block into aligned runs of 32 code points. A window label names a run, never a character, a
single code point or a count; every window still holds many assigned symbols (the smallest after the
star and math exclusions still holds 30, in window 1). Window ranges are public in the table above and are not
secret; they are fixed code-point boundaries, not a lookup of the character seen. Nothing is qualified or
widened: an unqualified character still refuses before it is rendered, and the acceptance predicate is
unchanged. A glyph, once localized, is qualified only by a separate reviewed change with its own evidence.
Cycle 70 stopped earlier, at `identity_before` with reason `deadline`, which is a different stage and
looks transient; it is not addressed here.

## Round 4 (2026-11-04, after cycle 87)

Cycle 87 refused Cursor at `builtin_terminal_miscwin6_unqualified`, the window U+26A0 to U+26BF. Window 6 is
split into four aligned parts of 8 code points: `miscwin6a` U+26A0 to U+26A7, `miscwin6b` U+26A8 to
U+26AF, `miscwin6c` U+26B0 to U+26B7, `miscwin6d` U+26B8 to U+26BF. Eight code points is the smallest label,
so the privacy floor set above (the checkmark class) is kept; all 8 code points in each part are assigned symbols; no finer split is made. Plain `miscwin6` is no
longer emitted and stays valid only to decode older diagnostics. Nothing is qualified: an unqualified
character still refuses before it is rendered, and the glyph is qualified only by a separate reviewed
change with its own evidence. Separately, the slot ConfigMap `dev-env-system/dev-env-provider-allowances`, `report.json` `sourceStatuses[cursor]`, read 2026-11-04T14:33Z (cycle 162), showed `lastAttempt` result `error`, code `acquisition_unavailable` and `freshness.reason` "source not configured". That is a collector configuration blocker, not addressed here.
