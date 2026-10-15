# Coordination retention observations — 2026-10-15

Source-only record for issue 335 (`R3-OBSERVATIONS-1015`). This document describes the worktree contract and fake-client tests. It does not claim an installed operator, a retention pass, archive activation, or R3 completion.

## Accepted design used

DESIGN-024 section 5 evaluates each summary from uncached reads, then looks up live records in bulk. A later mark or reset consumer is still separate. Backlog 11 keeps R3 as the dev-env source retention unit and R6/R7 as the installed pass. Workflow guide section 8 keeps source evidence distinct from pilot acceptance and v1 cutover.

Dependencies already on this branch base `baa95b2025d6a93cfbf304c8e0a9801bd094422d`: merged Evaluate (#300) and accepted bulk typed lookup (#301).

## Contract

`coordretention.Result` carries `Observations`, values of the existing `protocol.CoordArchiveObservation` type. Reasons come from the pass memo and the reads `Evaluate` already performs. `Decision` alone does not invent a reason, and building observations does not repeat a GET.

The memo key stays namespace plus name. Not-found stays distinct from a different live UID. Each requested UID is compared on its own. The same UID is `present` while the Session is deleting. Owner and source use those roles. A message whose sender is absent and whose bound ack object is valid records the sender reason and `recipient`/`acknowledged` with that ack object's UID. Without an ack, the recipient comes from the same memo. A present sender records only that present observation. Unknown results and cancellation record none.

A read ack whose live message UID is still present records `message`/`present`. Proved absence with archive evidence still pending keeps `RequiredMessage` and does not record `message-released`. Known holds, or bulk lookup holds for that accepted pending message, record `message-released` and `Released`. Known or bulk absence of holds stays `Kept` with no observation. Bulk lookup does not convert an ordinary `Unknown` result.

A `Released` observation list is a complete protocol `absent` event. A kept present observation list is a protocol `present` event. Pending archive and no-holds results have no event. No protocol type, rule string, or schema was added.

## Tests

`TestObservation` in `internal/coordretention/observations_test.go` calls `Evaluate` and, for holds, `Evaluate` then `Lookup`. It checks roles, identities, reasons, ack UID, and GET counts. Protocol `Validate` uses the fixed instant `2026-10-15T23:09:48Z`, by `coordretention-observation-fixture`, and the existing rule for that resource.

## Not accepted here

No archive write, mark, reset, runnable, schedule, delete, flag, API route, or controller wiring. Future mark/reset still needs its own story: uncached UID and resourceVersion recheck, existing `put --mark`, caps, and message/ack pairing.

## Bounded source evidence

The final focused capture on 2026-10-15 at 23:22:33–23:22:35 UTC passed one top-level method, 27 named test nodes (26 nested nodes, 22 leaf cases), and zero skips. The command selected only `^TestObservation` in `./internal/coordretention`, using the cached Go 1.27.2 toolchain, shared test lock, `nice -n 19`, `GOMAXPROCS=2`, `GOFLAGS=-p=1`, `GOTOOLCHAIN=local`, `GOPROXY=off` and `GOSUMDB=off`. Existing rules and lookup tests were compiled but were not selected for replay.

Two earlier local failures remain part of this story: changing the internal `session` return shape broke two existing test callers; restoring its original signature fixed that compile error. A new fixture expected two bulk lookup groups instead of the three for the ack, message and ordinary claim; correcting that count left production grouping unchanged. The failed assertion run has 24 passing and three failing nodes, including parent groups. The subsequent passing worker capture is separate evidence, not one composed test run.

Independent source review then identified an unused helper and incomplete message identity/read-count assertions. The final correction removed the unused helper and added exact namespace/name/UID/role/reason/ack-UID checks for present and released read-ack observations, plus the missing no-extra-GET checks. The focused final capture above binds the corrected Go sources. Required exact-head CI and independent closure are recorded with source delivery; this document does not substitute for those gates.
