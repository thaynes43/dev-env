# Cursor refused-rune Unicode classes, 2026-11-03

After the approved collector handoff (operator `sha-2d42e72`), the first natural
cycle 57 produced a fresh Codex row. Claude refused at the version stage
(`version_unqualified`, client 2.1.312; handled separately). Cursor refused at
the builtin stage with the generic `builtin_terminal_rune_unqualified`. The
refused character is unknown. The existing finer reasons (encoding, combining,
spacing, format, dot-set) did not match, so the character is a visible,
non-spinner character outside the qualified set.

This change adds closed labels for the remaining Unicode classes, so the next
natural cycle can localize the refusal without disclosing the character:

| reason | class |
|---|---|
| `builtin_terminal_letter_unqualified` | any letter |
| `builtin_terminal_number_unqualified` | any number |
| `builtin_terminal_dash_unqualified` | dash punctuation (Pd) |
| `builtin_terminal_quote_unqualified` | initial/final quote punctuation (Pi, Pf) |
| `builtin_terminal_punct_unqualified` | other punctuation |
| `builtin_terminal_math_unqualified` | math symbol (Sm) |
| `builtin_terminal_currency_unqualified` | currency symbol (Sc) |
| `builtin_terminal_shape_unqualified` | geometric shapes U+25A0 to U+25FF |
| `builtin_terminal_pictograph_unqualified` | U+2600 to U+27BF and U+1F000 up (misc symbols, dingbats, emoji) |
| `builtin_terminal_symbol_unqualified` | any other symbol (including braille patterns outside the dot set) |
| `builtin_terminal_rune_unqualified` | private-use and unassigned code points only |

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
