# Cursor usage semantics: plan after the public-code comparison (2026-11-04)

Status: plan only. Source, no live effect. Nothing here is installed, accepted or authorized to
collect. It adds no credential, private route or provider call.

## Problem

Cursor is refused by the built-in terminal reader at one unqualified glyph in U+26A0 to U+26BF
(`builtin_terminal_miscwin6_unqualified`; operator log diagnostic lines for cycles 163 to 168). PR #910 split that window into four
8-code-point labels (`miscwin6a` to `miscwin6d`); no cycle has read them yet. The collector window
ended 2026-11-04T15:33:23Z; that and the cycle-168 final state are recorded in the
COLLECTOR-EXPIRY-VERIFICATION artifacts in the OP323 session's private handoff directory (not in the
repo). Nothing below changes that refusal.

## What the public comparison gives

Source: the coordinator's research file `OPENUSAGE-CODEXBAR-RESEARCH.json` (openusage `629f1740`,
CodexBar `897bff31`; MIT), which lives in the coordinator's private handoff directory, not in the repo. Both call Cursor HTTP routes with a token and never read a terminal, so
neither explains the glyph. They give field semantics only, and every fixture is synthetic.

## Choice: a plan, not an implementation

A narrow implementation has no defensible input today:

1. The only qualified source is the built-in Usage display, which `CursorCLIUsageCapture`
   (`internal/providerallowance/cursor_cli_capture.go`) already types: plan, rounded integer
   percent, vendor reset month/day, local acquisition interval, explicit unknowns
   (`display_unknown`, `providerObservedAt` null, `period` `not_exposed`), account binding by
   hashed email. Window and reset arithmetic cannot be derived from it: the period is not exposed.
2. The Sand ("Grok Bot") fields (`usagePercent`, `nextResetTimestampUtc`, `currentPeriodStart`,
   `includedLimitZero`, `usesPooledEnterpriseAllowance`, `isUnified`) come from a private
   DashboardService RPC. Calling it needs a delivered user token and creates a second refresher.
   That is forbidden here and is not proposed.
3. Whether the built-in display shows any Sand or Grok line at all is not known. A mapper written
   against the JSON shape would be dead code or a guess about the display.

## Missing evidence, named exactly

- E1: which 8-code-point part holds the refused glyph, and what the glyph is. Needs one natural
  cycle on an image that carries #910, in a collection window that has not been granted.
- E2: a sanitized capture of the full built-in Usage display for an account that has the Sand line,
  to show whether and how that line renders. Owner-approved session only.
- E3: an owner decision on whether any first-party but undocumented route is acceptable. The
  research file lists the options (terminal readers only, a new solely owned login, accept
  Unknown for Grok). Not decided here.

## Existing Sand machinery (already in the repo)

`internal/providerallowance/cursor_display.go` already separates a direct xAI/Grok reading from the
Cursor pool: route `xai_direct` with `CursorDisplayXAICapability` (line 23), the `finishXAI` rules
(lines 565 to 576: product `grok`, availability `unsupported`, no plan, account, window, used, reset
or spend), and the registry extension that must be caller-trusted before such measures validate
(line 72). So a Sand reading is not a new measure in the plan capture; it would be its own item, and since
`xai_direct` carries no value, a value needs a new route (step 2).

Account binding, reconciled: the plan capture binds by hashed email (`accountRef`), while `xai_direct`
requires binding `Unknown` and no `accountRef` (`finishXAI`). A Sand item therefore cannot claim the
Cursor account by identity. Their link is co-collection in one cycle only, never a shared binding, and
the pool relationship stays Unknown until E2 or E3 provide evidence. Nothing here weakens that rule.

## Steps (source only; 1 and 2 are gated, 3 to 5 are feasible now)

1. Qualification harness: a table test that, given the part label from E1, asserts the exact glyph
   is accepted only in the exact context where the display places it, refuses every other member
   of the part, and keeps all other classes unchanged. Written after E1, never from a guess.
2. Gated on E2. Sand as its own item on a NEW route modelled on `xai_direct` (the existing route
   carries no value: `finishXAI` refuses used, plan, account, window, reset and spend, and emits no
   measures), never folded into the plan percent; `Unknown`
   for absent, pooled-enterprise and zero-limit states; a displayed 0% is display evidence but not a
   known quota observation, the same rule as `cursor_cli_capture.go:59-65`; the trial expiry is not a
   recurring reset; no per-model Grok semantics are assumed.
3. Reset arithmetic: use the display's month/day only as a label (as today). Epoch arithmetic
   (`reset = end`, `period = end - start`) applies only to a source that exposes both ends.
4. Closed errors: keep the existing closed diagnostic vocabulary. Today the parser ignores any row
   other than Included, Auto and API (`cursor_protocol.go:136-156`), so an extra Sand row neither
   fails nor enters the capture. A future Sand parser must decide separately whether a malformed
   Sand row refuses; that is not assumed here.
   Pinned now by `TestCursorProtocolIgnoresRowsOtherThanIncludedAutoAndAPI` (RED against a mutation
   that refuses any extra `% used` row).
5. Privacy floors: no rune, count, text or hash in logs; labels no finer than 8 code points; no
   raw response excerpt in an error (the public code embeds 200 characters; do not copy that).

## Future LIVE acceptance gate

Not met, not claimed. Accepted only when, with a granted collection window, two natural cycles
each show Codex, Claude and Cursor usable with known values and provider-time provenance, the API
read and the UI agree, and source, merged, installed and accepted are recorded separately with an
external clock. Grok stays Unknown until E3 and its own evidence exist.
