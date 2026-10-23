# Workflow result custody — 2026-10-23

Source-only record for [#177](https://work.haynesnetwork.com/work_packages/177). Baseline `380a759d947faf1131653867cdecf4b7ade878f5`. Branch `agent/workflow-result-custody-20261023`.

The change adds `docs/coordinator-result-handoff.md` and the offline checker in `scripts/trials/workflow_handoff`. Focused fixtures cover one terminal review verdict, blocked or single successor custody, retained scope and holds, a worker deadline, and a merge recommendation that the checker does not perform.

This record does not accept [#117](https://work.haynesnetwork.com/work_packages/117) or [#101](https://work.haynesnetwork.com/work_packages/101). It does not change runtime orchestration, TaskClaim, inbox delivery, admission, or deployment. Serving model and cost for the implementation session are Unknown. Configured UI is not serving evidence.

Sol keeps result pickup, native review, required CI, findings disposition, and the ordinary dev-env merge and release path. The checker grants none of that.

## SOL177 bounded correction, 2026-10-23

The review of head `f4a260300a4d4c2cccce7e20c5d26b7c5881daaf` returned FIX-NEEDED for F3. This repair matches schema tags with Unicode `EqualFold`, checks tag collisions with the same rule, and exercises actual nested gate/disposition keys, escaped keys, long s, Kelvin sign, canonical input, exact unknown keys and literal string values. ASCII case fixtures now use known event fields.

Minor dispositions: m1 requires EOF in both token and typed parser paths, preserving trailing whitespace. m2 blocks merge advice while executor writes remain recommended. m3 keeps the offline deadline display-only, documents mandatory coordinator clock ownership, and includes a finite post-deadline observation without an enforcement event. m4 permits only strictly later hold dispositions, with both equal-time input orders and a later positive case. m5 reports a conflict when retained mutually superseding scopes resolve to no current scope, preserving holds and blocking merge in both orders. m6 preserves exact replay timestamp representation conservatively and tests same-instant offset spelling conflict. m7 updates the PR body for the final repair; older reviews cover only their recorded heads. m8 displays a verified-stopped executor as exited on either side of a deadline event, preserving turn-ended and stop evidence.

These are finite offline checks, not live orchestration or broader #117/#101 acceptance. Parent coordinator retains fresh final-head CI/native review, findings disposition, unchanged-head preflight and authorized merge/release; the implementation successor stops at ready handoff. Exact head and validation outcomes are in the PR and designated SOL177-REPAIR-RESULT.md receipt.

## Subsequent corrections and review boundaries

- `9f13d18`: same-owner followup fixed scope fallback through equal-time cycles, latest matching pass write revocation, mismatched pass executors, input-tag reflection coverage and narrow docs. The native review found an equal-time pass disagreement afterward; its verdict is historical, not current approval.
- `0724556`: prescanned equal-time pass conflicts and preservation times, with replay/UTC-offset controls. The next native review found cross-kind first-executor and identity-reuse gaps; earlier passing tests were bounded evidence, not acceptance of those gaps.
- `30b310d`: added cross-kind executor-binding prescan, dropped conflicting identities involving a pass, and separated temporal from known-reference validation. The native review returned FIX-NEEDED for non-pass binding identity conflicts and preservation conflicts detected after a tied stop. Its advisory write-reopen claim was concretely rejected because sticky conflictedPass makes writesOpen false; no code change is attributed to that false finding.

The current correction generalizes ambiguous-identity suppression to every binding kind (including unauthorized worker passes), prescans all preservation times before event application, removes the redundant applyPass conflict guard, and names every dropped record kind/identity. A collided hold or scope remains unresolved raw evidence, not permission or a disposition. Table-driven finite regressions cover both orders, UTC offsets, different instants, replay, tied stops and successor suppression, while preserving the earlier guards.

The implementation handoff/PR identify the current exact commit without embedding its own SHA here. New exact-head required CI and independent native review remain pending coordinator gates. All older verdicts cover their recorded heads only. These records prove source and finite offline simulation only; no runtime, deployment or broader #117/#101 acceptance follows.
