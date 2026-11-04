# Cursor usage semantics: plan after the public-code comparison (2026-11-04)

Status: plan only. Source, no live effect. Nothing here is installed, accepted or authorized to
collect. It adds no credential, private route or provider call.

## Problem

Cursor is refused by the built-in terminal reader at one unqualified glyph in U+26A0 to U+26BF
(cycles 163 to 168, `builtin_terminal_miscwin6_unqualified`). PR #910 split that window into four
8-code-point labels (`miscwin6a` to `miscwin6d`); no cycle has read them yet. The collector window
ended 2026-11-04T15:33:23Z. Nothing below changes that refusal.

## What the public comparison gives

Source: the coordinator's research file `OPENUSAGE-CODEXBAR-RESEARCH.json` (openusage `629f1740`,
CodexBar `897bff31`; MIT). Both call Cursor HTTP routes with a token and never read a terminal, so
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

## Steps that are feasible now (source only)

1. Qualification harness: a table test that, given the part label from E1, asserts the exact glyph
   is accepted only in the exact context where the display places it, refuses every other member
   of the part, and keeps all other classes unchanged. Written after E1, never from a guess.
2. Sand as a separate measure, only if E2 shows the line: a distinct measure id, never folded into
   the plan percent; `Unknown` for absent, pooled-enterprise and zero-limit states; the trial
   expiry is not a recurring reset; no per-model Grok semantics are assumed.
3. Reset arithmetic: use the display's month/day only as a label (as today). Epoch arithmetic
   (`reset = end`, `period = end - start`) applies only to a source that exposes both ends.
4. Closed errors: keep the existing closed diagnostic vocabulary. An optional line missing never
   fails the primary read; a present but malformed line refuses with a closed code.
5. Privacy floors: no rune, count, text or hash in logs; labels no finer than 8 code points; no
   raw response excerpt in an error (the public code embeds 200 characters; do not copy that).

## Future LIVE acceptance gate

Not met, not claimed. Accepted only when, with a granted collection window, two natural cycles
each show Codex, Claude and Cursor usable with known values and provider-time provenance, the API
read and the UI agree, and source, merged, installed and accepted are recorded separately with an
external clock. Grok stays Unknown until E3 and its own evidence exist.
