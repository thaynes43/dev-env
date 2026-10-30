# Source preparation evidence

Initial source head:63b387c69ff07aecd92f79da496c974678a8651a. Base:030133dc9dd9ac24a2c61f30f12ee8e402bd4bf2.
Ready source PR:[dev-env#732](https://github.com/thaynes43/dev-env/pull/732).
No merge/release/deploy/activation. Lead owns the held policy PR and sequencing.

Full local affected suites passed on initial head, Go1.27.3, GOMAXPROCS=2,
GOFLAGS=-p=2, envtest kube-apiserver1.35.0, one bounded invocation:

- api/v1alpha1:7.799s
- internal/broker:25.953s
- internal/apiserver:65.750s
- internal/agentrun:3.320s
- internal/controller:80.846s
- cmd/dev-env-operator:0.112s

Initial pinned lint and hosted lint failed only two goimports import-group checks
in new tests. Corrected with the pinned formatter; failures remain recorded.
Generated schema and stop-write guards passed targeted checks. The exact UNAPPLIED
patch passed git apply --check against a private temporary copy of the pinned
haynes-ops files. No production apply/dry-run or grant/test conversation was used.

Independent native read-only Sol review of initial immutable head found the final
exec reread must preserve original grant UID/digest and executor PodUID/Running.
Corrected with five adversarial final-read response fixtures (replacement grant,
changed scope, replacement executor, StopPending and missing status), each proving
no exec is opened. No additional blocking source finding on the initial head.

Claude advisory review41097583780 was available and completed. Findings addressed:

- CRITICAL4426453984: refuse control-plane/master labels or taints in authoritative
  node checks and proposed executor CREATE policy; four focused source fixtures.
- HIGH4426454366: ADR-012 records the supplied dated owner direction, decision and
  design, with explicit source-only status and separate activation hold.
- MEDIUM4426454675: API uses the existing configured human identity; broker adds
  matching --maintenance-human-service-account (default unchanged), checked by the
  bound decision adapter and Decide. End-to-end fake custom-human fixture passes.

Correction focused tests pass: API schema5.995s, broker0.087s, API0.238s, operator
compiles. Earlier full-suite receipts qualify initial head, not later code changes.
Final immutable-head independent review, final pinned lint and normal required CI
are recorded on the PR and canonical OP233/219 after the final source commit.
Policy runtime/CEL/defaulting/resource-lookup qualification remains unproven and
held for the separate policy PR; fake/API tests do not prove direct Kubernetes exec
admission. Device/runtime usability and actual hardware restoration are untested.

Built source and tested source do not mean installed, usable or accepted. No new
adapter was activated, no credential semantics changed, no node/GPU/keeper/user
session operation ran, and no haynes-ops file was written. Both held TUIs were left
untouched. This story's durable output is the source branch/PR plus review packet.

Final network-reference audit found the pinned platform/web CCNP selectors would
also select maintenance executors. The UNAPPLIED patch now excludes the maintenance
label from both; an additive Kubernetes NetworkPolicy alone cannot override their
allow rules. Offline selector fixtures verify normal sessions stay selected and
maintenance executors are excluded. These tests do not establish live Cilium policy
enforcement. The final immutable-head receipts on OP233/219 and PR732 supersede
earlier source-head qualification when this packet changes.

Final advisory41098971312 found deterministic Pod-create rejection left Unknown
cleanup indefinitely. Source now records NotCreated for definitive admission/auth/
invalid-body refusal, ends Failed with the refusal visible and cleans the owned
network policy. Timeout/5xx/AlreadyExists remain ambiguous and cannot recreate or
claim termination. Offline refusal-versus-timeout fixtures verify one create only,
cleanup idempotence for refusal and retained network/Unknown for ambiguous failure.
No production denial was encountered or bypassed. Final source receipts follow on
the PR and OP records after immutable review and requiredCI.
