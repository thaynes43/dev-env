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
| 10 | `builtin_terminal_checkmark_unqualified` | the remaining check and cross marks: U+2714 to U+2718, U+2705, U+274C, U+274E (the already-qualified U+2713 never reaches a label) |
| 11 | `builtin_terminal_star_unqualified` | stars, asterisks and flower dingbats: U+2605, U+2606, U+2721 to U+274B (spinner-style asterisks included) |
| 12 | `builtin_terminal_miscsymbol_unqualified` | what is left of the Miscellaneous Symbols block, U+2600 to U+26FF (weather, warning, hearts, and so on) |
| 13 | `builtin_terminal_dingbat_unqualified` | what is left of the Dingbats block, U+2700 to U+27BF (ornaments, crosses, dingbat arrows) |
| 14 | `builtin_terminal_pictograph_unqualified` | symbols from U+1F000 up (emoji and other pictographs) |
| 15 | `builtin_terminal_symbol_unqualified` | every remaining symbol (including braille patterns outside the dot set) |
| 16 | `builtin_terminal_rune_unqualified` | private-use, unassigned and noncharacter code points only

Dash and quote are the smallest and most guessable classes (a few dozen characters each); the label still names a class, never a character, and no finer split is made.

The existing precedence is unchanged: encoding, combining, spacing, format and
the fixed dot set come first. Every label covers many characters, so it cannot
identify the one seen. No rune, code point, text, count, hash or extra field is
emitted: the same five JSON fields, Cursor-only provider, builtin-only stage and
empty `clientVersion` remain mandatory (the decoder rejects any change). The
acceptance predicate is unchanged: every unqualified character still refuses
before it is rendered, and a refusal advances no protocol state.

Tests: each class is exercised with several members, byte-identical diagnostic
output across members, absence of the private sentinel and the sample, no
protocol advance in any state, provider/stage/payload boundaries, and the
generic label for private-use and unassigned code points. Two older tests that
asserted the previous generic label for a snowman and a few other characters
now assert their classes.

This is diagnostics only. It qualifies no character, widens no input and runs no
provider. Qualifying a character, if the evidence supports it, is a separate
reviewed change with its own evidence. Installed behavior, usable Cursor rows and
two complete all-provider cycles remain unproven.

## Round 2 (after the first natural cycle with the class labels)

Cycle 64, on operator `sha-84fa61c`, read Claude and Codex (known last-reported
values, provider clock unknown) and refused Cursor at
`builtin_terminal_pictograph_unqualified`: a symbol in U+2600 to U+27BF or from
U+1F000, after numbers, math and currency. The client binary is not part of this
repository's image and Root's private client is out of reach, so the next step is
to narrow the same class further with labels only. The pictograph label is split
into the four sub-classes above plus the emoji remainder. Each still covers many
characters, still emits no character, code point or count, and qualifies nothing:
a refusal remains a refusal until evidence supports accepting a specific glyph.
