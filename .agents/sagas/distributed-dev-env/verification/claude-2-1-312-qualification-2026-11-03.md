# Claude Code 2.1.312 zero-turn /usage qualification, 2026-11-03

After the approved collector handoff, the first natural cycles (57, 58) refused
Claude at the version stage: `version_unqualified`, client `2.1.312`. Root's
replacement Pod runs the 2.89.0 agent image, which pins Claude Code 2.1.312. The
collector qualifies a client by the exact `--version` string, and only
`2.1.296 (Claude Code)` was qualified. The refusal is the intended fail-closed
behavior, not a defect.

## Evidence (offline, public artifact, nothing executed)

Method as in `usage-cycle30-refusal-localization-2026-11-03.md`: the public npm
platform package `@anthropic-ai/claude-code-linux-x64`, integrity verified against
the registry (SRI sha512 and sha1) before extraction; archives were read as bytes
(no client, node or bun was run, no member made executable) and deleted afterwards.

| | 2.1.296 | 2.1.312 |
|---|---|---|
| archive SHA256 | `eef5a2e2b09a5e7d2ce784d1b3ba335ae7fc6c7f6256c802a0ebf2c21e4fd0e1` | `f1c3d923ff3ac7e7080b8b9c638833dde72feab15e46940a65a0122d728c6bfd` |
| native member SHA256 | `24972e3bc859fab2b46ed4c1e51f7d6130f06d3bd550811a114640de3370d0de` | `4f4178ce41d83e23341ebfbaecdbcf8ea6bf7baea0690025abe49eeec3a5d01d` |
| zero-usage constructor offset | 213965710 | 246466162 |
| `type:"result"` builder offset | 208139610 | 241157236 |

Identical in both: the zero-usage constructor (byte-identical, four zero
aggregate counters plus `output_tokens_details:{thinking_tokens:0}`); the result
builder after normalization; the usage and `usage_report` schemas; the SDK
zero-turn result factory (`is_error:false`, `num_turns:0`, cost 0); the use of the
zero constructor as `usage` on the zero-turn path; and the `--version` output
format `<x.y.z> (Claude Code)`. Sums over an empty per-model record are zero in
both aggregators.

Differences found and how each is handled:

- The exact version pin refused 2.1.312 (the only hard difference). The pin is
  now an exact two-member set; there is no range, prefix or ordering.
- 2.1.312 adds an optional top-level result field `model_usage_since_last_result`
  (per-model token counts). The parser did not inspect it. A zero-turn `/usage`
  makes no model call, so it must be absent, `null` or an empty object; anything
  else now refuses with the closed reason `builtin_model_usage_unqualified`, and
  its name joins the canonical-field-name check (case variants refuse).
- Four new system subtypes exist in 2.1.312 for remote or resume conditions. The
  parser already refuses any system event whose subtype is not `init`.
- Additive optional fields (init `original_cwd`, `additional_directories`; result
  `terminal_reason`, `api_error_origin`) are ignored by the struct decode, as
  other unlisted result metadata already is. Required zero turns, zero cost and
  four zero token counters still gate every result.

## What this does not establish

The public package is not an attestation of the binary running in Root, whose
hash was not examined. The exact JSON a real run emits, whether `rate_limits` is
populated for Root's account, the init event size against the 64 KiB limit, and
auth or cache side effects of running 2.1.312 are unverified here. Any deviation
still refuses with a closed reason. Installed behavior, a usable Claude row and
two complete all-provider cycles remain unproven. No provider command, model call,
login or private history was used.
