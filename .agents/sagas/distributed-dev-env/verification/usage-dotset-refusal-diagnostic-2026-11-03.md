# Refused Cursor dot-set membership diagnostic, 2026-11-03

Scheduled new-helper cycle41 on signed operator312/f538 produced real,
account-bound usable quota captures for Codex and Claude. Cursor still refused at `builtin_terminal_rune_unqualified`. The
actual rejected character is unknown. An already-retained manual final screen
contained no unsupported characters; a transient frame is only a hypothesis.

The exact pinned public Cursor archive fetch returned HTTP403Forbidden and is
stopped. This change does not fetch that archive through another route, read
Root's denied private client/controller/history/artifact, run a provider or
install a client/model. It uses a different, explicitly authorized engineering
path: a public standard vocabulary for a diagnostic membership test.

The fixed set is the ten `dots` frames from cli-spinners, pinned to commit
`c5f18f654e0f6bdbf830713927f815a42190d774`:
<https://raw.githubusercontent.com/sindresorhus/cli-spinners/c5f18f654e0f6bdbf830713927f815a42190d774/spinners.json>.
Source file SHA256:
`91b0d44a709e836adc24de83f8b999dfd670a0e25037931d8c5186bb9e923a2b`.
This is not evidence that the pinned Cursor renderer uses this set.

All unsupported characters still return ErrCapture before being rendered.
The acceptance predicate is unchanged. A refused member of this fixed set
gets the single reason `builtin_terminal_dotset_unqualified`; every other
unqualified character retains its existing coarse reason. The same five JSON
fields, Cursor-only provider, builtin-only stage and empty clientVersion remain mandatory. No rune,
codepoint, text, value, count, hash, extra field or per-character label is emitted.
Every member produces byte-identical diagnostic output, so the bucket cannot
identify which member was seen. Other vendor input never enters the payload.

Regressions first failed against the old generic label (0.004s), then passed
with the membership label (0.004s). They exercise all members, exact five-key
payloads, byte-identical output, private-sentinel absence, other/bidi/combining/
wide/control/invalid UTF8 boundaries, fragmented UTF8, no protocol advance and
an actual owned synthetic subprocess. A separate RED0.003s exposed an
initially unconstrained provider label; the final decoder restricts this new
reason to Cursor and rejects added rune/codepoint/value/count fields. No real
provider client was invoked.

Independent privacy/security review and current CI are required before source
publication and operator-only adoption. The scheduled result must match this
fully tested set before any separate acceptance candidate may be merged or
activated. This release accepts none of these characters. It is diagnosis,
not Cursor recovery or an all-three-success cycle. Original expiry, cadence,
backoff, accounts/host/config, reader identity, binding and cleanup guards stand.
