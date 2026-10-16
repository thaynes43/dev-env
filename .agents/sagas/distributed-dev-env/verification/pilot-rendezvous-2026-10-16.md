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

Use cached Go, downloads off, shared test lock, GOMAXPROCS2, -p1 and nice19:

```
go test -count=1 -v ./scripts/trials/pilot_rendezvous ./internal/agentrun \
  -run 'Test(Gate|CapturedTiming|NeverReady|AllAdapter|DurableAttempt|Infeasible|DefaultAndDrift|ExistingCLIAdapter|ProjectCreateOnce|ProjectCreateDefault|LogOnce|LogDefault)'
```

The harness clock/observation/dispatch are injected. The actual command adapter
is exercised through a fake invocation, not a plan-only return. The CLI wire
checks use net.Pipe and synthetic credentials: no listener, DNS, operator,
external transport, subprocess or model. Record raw commands/results and final
exact head/patch in the orders. A first new CLI test used an incomplete Session
fixture; strict-wire validation correctly refused it. The fixture was corrected
to the existing complete Session DTO; that failed capture is retained.

## Ready-to-execute procedure (held; separate runtime authorization required)

1. Accept this exact source through native review/required CI and ordinary
   delivery. Build/source-bind `agent-run` and this harness from accepted source;
   record SHA256s. The old v2.29.2 CLI lacks create/log `--once` and is not a
   compatible execution binary. No operator or task image change is required.
2. Prepare a separately reviewed immutable two-slot package using the phase
   amendment in DESIGN-027. Keep the existing15m task/20m Pod/30m window,
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
   All errors mean abort/retain evidence, not retry. Keep the attempt marker even
   if the create result is unknown. Do not switch credentials, model or path.
7. A/B follow the absolute phase/read budgets. Require correct B ownership,
   duplicate-Aclaim refusal retaining AUID, B-to-A message sender/recipient/Pod,
   A's exact message read/ACK, publication sourceUID/content hash and reply.
   B must preserve full raw original-A task-terminal evidence before reading
   the exact authorized publication. Unknown private executor stop stays Unknown.
   Neither model exit0 nor B-create acceptance is semantic pilot acceptance.
8. At completion OR abort, deliver the pre-reviewed remove-only closure through
   its exact-head gates; verify actual no-policy/global-private-false replicas
   and all peer/v1/seal/Session/HOME identities preserved. No actor deletion,
   revival, replacement or window extension. Save raw per-operation timestamps,
   actual request counts, task results and explicit partial acceptance.

Remaining usable-MVP proof is two-way ACK and published stopped-handoff read with
attribution and peer preservation. The last runtime demonstrated neither; this
source/offline replay does not claim them. A fresh future reviewed runtime
package/authorization is needed to prove them, not a new core redesign.
