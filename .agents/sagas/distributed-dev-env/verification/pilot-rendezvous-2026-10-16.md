# Private pilot rendezvous source/offline acceptance, 2026-10-16

Story C7-PILOT-RENDEZVOUS-OFFLINE-1016, sole Chapter7 owner; accepted base
`c72d2e48f70f0131845dc53f55a7b177e0ac32e1`. The new worktree and exact path
claim/clocks are preserved in
`/home/dev/work/orders/dev-env-chapter7-rendezvous-offline-1016-1414/story.json`.
Pilot closure remains a completed PARTIAL result in the earlier runtime orders;
this work cannot reopen its actors, window or consumed approval.

[DESIGN-027](../designs/027-pilot-rendezvous.md) defines the gate and exact bounded
phase amendment. This artifact is a future execution procedure, not permission
to execute it now. No live API/model calls are part of source testing.

## Focused offline proof

Use cached Go, downloads off, shared test lock, GOMAXPROCS=2, -p1 and nice 19:

```
go test -count=1 -v ./scripts/trials/pilot_rendezvous ./internal/agentrun \
  -run 'Test(Gate|CapturedTiming|NeverReady|AllAdapter|DurableAttempt|Infeasible|DefaultAndDrift|ExistingCLIAdapter|ProjectCreateOnce|ProjectCreateDefault|LogOnce|LogDefault|ActualStartup|CapturedReal|ExistingRun)'
```

The harness clock/observation/dispatch are injected. The actual command adapter
is exercised through a fake invocation, not a plan-only return. The CLI wire
checks use net.Pipe and synthetic credentials: no listener, DNS, operator,
external transport, subprocess or model. Record raw commands/results and final
exact head/patch in the orders. A first new CLI test used an incomplete Session
fixture; strict-wire validation correctly refused it. The fixture was corrected
to the existing complete Session DTO; that failed capture is retained. Independent
Mac review found a real startup issue: the first adapter requested a log before
metadata was ready. The correction defers that request and replays nil metadata,
bootstrap-in-progress and ready/claim states to exactly one dispatch. No actual
CLI/authority error is swallowed.

## Ready-to-execute procedure (held; separate runtime authorization required)

1. Accept this exact source through native review/required CI and ordinary
   delivery. Build/source-bind `agent-run` and this harness from accepted source;
   record SHA256s. The old v2.29.2 CLI lacks create/log `--once` and is not a
   compatible execution binary. No operator or task image change is required.
2. Prepare a separately reviewed immutable two-slot package using the phase
   amendment in DESIGN-027. Keep the existing15-minute task/20-minute Pod/30-minute window,
   provider/model/resource/identity/access constraints. Retain all old seals.
   Required activation, actual sealUID binding, preflight and checked closure
   PR/inverse must be established before any actor. No currently approved new
   actor/window exists; do not run step3 onward under this source story.
3. In that future approved scope, create A ONCE through corrected CLI with
   `--once --wait=0 -o json`, using its exact policy request. Record returned AUID
   and original executor PodUID through existing authorized metadata. Prebind
   those identities; no replacement after missing/unknown results. Prepare the
   harness before A claims; task-start timestamp may be omitted and will be
   derived from A's supported log. Keep peer/v1 preservation baselines.
4. Save `config.json` with canonical Config field names:
   `policy` (entire reviewed actual-UID-bound policy), `aUID`, `aPodUID`, `head`
   (accepted fresh task commit), `commonMarker`, `repositoryMarker`, `binary`
   (absolute corrected agent-run path), `binarySHA256`, `caFile`, `caSHA256`,
   `promptFile` (exact sealed B prompt bytes), `receiptDir` (existing private
   absolute directory, stable for this cohort). Optional `aTaskStarted` must
   equal the real log start. Markers are comparison inputs, never added to prompts.
   Config contains NO tokens. Existing CLI credential discovery/TLS remain intact.
   The reviewed A prompt must emit the exact successful claim
   receipt line defined in DESIGN-027 in one SHORT COMPLETED tool invocation,
   before a separate long wait invocation. Do not put create/receipt and wait
   in a single captured subprocess: Python flush alone does not establish CLI
   log visibility. Verify the wrapper is visible within30s in a later separately
   authorized supported trace; no live proof occurs in this source story. The captured-log fixture preserves actual start,
   marker and operation lines projected from actor-A/log-6.stdout.
5. Run the harness once WITHOUT `--execute`:
   `pilot-rendezvous --config /absolute/config.json`.
   Expect NONEXECUTING and the exact B argv including `--once`; no observation,
   dispatch or model call occurs. Bind the reviewed binary/CA/prompt hashes and
   the full policy digest to the pickup receipt. Do not change them in flight.
6. Only with that later explicit runtime authority, one noninteractive command
   `pilot-rendezvous --config /absolute/config.json --execute` observes A and
   invokes B directly in the same step. NO model turn, phone report or manual
   copy/paste of a returned B command intervenes. Its one observation is show+log;
   its durable attempt capture includes raw gate, fixed argv and phase ends.
   All errors mean abort/retain evidence, not retry. A durable run reservation
   prevents restarting an earlier failed/unknown run in place. Keep the attempt marker even
   if the create result is unknown. Do not switch credentials, model or path.
7. A/B follow the absolute phase/read budgets. Require correct B ownership,
   duplicate-Aclaim refusal retaining AUID, B-to-A message sender/recipient/Pod,
   A's exact message read/ACK, publication sourceUID/content hash and reply.
   B must preserve full raw original-A task-terminal evidence before reading
   the exact authorized publication. Unknown private executor stop stays Unknown.
   Neither model exit 0 nor B-create acceptance is semantic pilot acceptance.
8. At completion OR abort, deliver the pre-reviewed remove-only closure through
   its exact-head gates; verify actual no-policy/global-private-false replicas
   and all peer/v1/seal/Session/HOME identities preserved. No actor deletion,
   revival, replacement or window extension. Save raw per-operation timestamps,
   actual request counts, task results and explicit partial acceptance.

Remaining usable-MVP proof is two-way ACK and published stopped-handoff read with
attribution and peer preservation. The last runtime demonstrated neither; this
source/offline replay does not claim them. A fresh future reviewed runtime
package/authorization is needed to prove them, not a new core redesign.

## Local-only heartbeat/visibility correction pickup

Story C7-RENDEZVOUS-FRESHNESS-LOCAL-1016, same sole Chapter7, accepted base
`71b713e849de52aecb540dc722733514cf5c6d95`, isolated worktree
`/home/dev/work/dev-env-c7-rendezvous-freshness-1016-1538`. Exact paths and fresh
15-minute checkpoint/25-minute stop are recorded BEFORE code edits in
`/home/dev/work/orders/dev-env-chapter7-rendezvous-freshness-local-1016-1538/story.json`.
All prior failures/clocks and the closed runtime remain retained unchanged.

The stopped claim was local patch, offline checks, and independent review only.
That claim did not push, open a PR, merge, trigger hosted CI, release, build or
publish an image, or make a live API or model call. On 2026-10-16 Tom directed
the Cursor pickup to deliver this exact patch as source. Required CI remains a
delivery gate. Offline tests are not a substitute for it, and they are not
runtime acceptance.

Focused checks replay the installed60s heartbeat cadence including the actual
31.419783s refusal offset, retain genuine stale/future/missing and wrongUID refusal,
and inspect the exact predicate/evidence in the saved gate rejection. A buffered
315.233877s claim-output counterexample must send no create; an old visible claim
must refuse; a short completed claim call with timely visibility dispatches
exactly once without a reporting/model turn. All adapters and clocks are fake;
this does not prove native streaming visibility. Final results belong in the
local orders; no new runtime package or retry is implied.
