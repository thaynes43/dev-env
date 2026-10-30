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

Native review of the refusal correction found one CAS edge: a concurrent release
could conflict with the NotCreated status receipt and lose the known refusal.
The source now retains that definitive result across up to five fresh UID-checked
status retries without another create. A synthetic conflict at the second status
write preserves release, ends Failed/NotCreated and completes idempotent network
cleanup. Unavailable storage or repeated conflicts still return an explicit error;
this does not claim durable receipt or cleanup when the API cannot record it.

## Existing PR732 CI recovery — 2026-10-30

Recovery owner dev-env-1030-180930 verified PR732 OPEN at
1bcc48a460d003b17f7a5d4f2b9c1f2bd07997b6, prior author exited0 at17:42:40,
and custody release in OP233 activity3518. Parent relay owns follow-through;
lead dev-env-1029-132039 retains sequencing. No TUI input was sent.
Canonical work packages are OP233 and OP219; activity IDs are not work packages.

Required hosted run41100377197 failed Test (job124216878060), so the earlier
queued receipt is superseded by FAILURE, never a qualification pass. The two
reported failures are stale fixtures: TestGrantPolicyRejects/break-glass expected
the previous denial text, while source and generated CEL now explicitly deny
both break-glass and maintenance standing approval; TestParseBrokerFlags omitted
the configured existing human identity from its complete default-options value.
Only those two expectations change. The rejection assertion and whole-struct
flag comparison remain, including maintenance execution default false. No
production code, generated schema, enforcement or policy input is relaxed.

Full api/v1alpha1 and cmd/dev-env-operator packages, affected broker/API/CLI/
controller regressions, generated/layout checks and pinned lint are required for
this correction. Exact final-head local results, independent native source
review and ordinary-push hosted CI receipts will be recorded in the existing PR
and OP233/219, avoiding evidence-only CI cycling. Earlier receipts remain
historical and do not qualify the corrected head.

Policy packet remains byte-identical and UNAPPLIED against haynes-ops
f12bbf60057e1a09d654143ad5c3b54058571dcb. Patch SHA-256:
06dd079a997188a870ff10b2af5bfaead062a6556b171b4f67e5c2cf2d98ef66.
The platform/web maintenance-label exclusions remain intact. Required runtime
qualification remains admission CEL/defaulting/update/resource-lookup behavior,
baseline and other-grant exec isolation, Cilium enforcement, device cgroup/runtime
and image tooling, actual expiry/termination/cleanup and separately authorized
restore/readback. SYS_ADMIN remains broadly administrative with residual selected
node/driver risk; task intent is not kernel containment. Tom's review of the exact
concrete policy/access changes is still required before activation. No merge,
release, deploy, live grant/security/hardware action or runtime acceptance occurs
in this recovery. PR597/565/563 remain preserved.

## Bounded policy correction — 2026-10-30, custodian184308

Fresh PR732 head709f4007d2588569e43f15dee7068b5f587afff2 and fetched branch
agent/dev-env-1030-170111 verified before source edits. Author180930 exited0
at18:37:26; OP233/3548 released custody. This private worktree is the sole
correction writer. Parent relay retains grant-outcome/release followthrough;
lead132039 retains user control, UI182804 is untouched. No competing backlog,
PR, formal GitHub approval, merge, release, deployment or live action.

OP233/3527 / GitHub6530523839 P1 corrected in the UNAPPLIED exact UPDATE rule:
protected metadata explicit/optional comparisons, all other metadata unchanged
except generation/managedFields/resourceVersion, exact olddeadline>1 to1 and
no other spec delta. JSON Patch UID/resourceVersion tests and actor/owner checks
remain. The independent source/schema audit found the pinned global baseline
would deny these existing capability/hostPath choices, and admission lacked
AccessGrant/node lookup reads. The packet therefore includes the smallest
broker-subject, Pod-only two-control PolicyException and GET-only admission
lookup RBAC proposal. No global baseline policy, ordinary-session permissions,
network exclusions, limits, capability set or production Go behavior changes.

Validation, Go1.27.3, GOMAXPROCS2/-p2, short TMPDIR=/tmp, isolated envtest1.35.0:

- Initial direct packet CEL / real API lifecycle regressions PASS4.426s.
- Full affected packages after adding actual stale UID/RV refusal and changed
  managedFields assertions: broker25.507s; schema7.564s; operator0.113s;
  API66.932s; CLI3.299s; controller81.086s — all PASS.
- Adversarial packet-CEL cases refuse protected UID/name/namespace/labels/
  annotations/owners/finalizers/creationTimestamp, spec node/image/capability/
  added or removed key/other deadline, non-shortening updates and non-broker
  callers. Optional absent metadata works. Both synthetic bookkeeping changes
  and actual server-incremented generation/managedFields/RV admit the exact
  deadline patch. Nonterminal DELETE refuses; terminal observation permits.
- Real API lifecycle creates the defaulted Pod, refuses stale UID and RV patch
  tests, applies the exact broker JSON patch, observes normalized state, writes
  synthetic terminal status, then deletes. No scheduler/kubelet/device or
  Kyverno runs. Existing broker tests separately retain node/lease/container
  proof requirements; synthetic terminal status alone is not stop proof.
- Exact forward patch applicability PASS against private files extracted only
  from haynes-ops f12bbf60057e1a09d654143ad5c3b54058571dcb. Initial check copied
  empty intent-to-add entries and correctly failed; a fresh pinned-only fixture
  corrected that harness issue. No haynes-ops checkout or cluster was changed.
- Pinned chart3.9.2 PolicyException v2 structural schema validation PASS.
  Initial two extraction attempts treated Helm directives as YAML and failed;
  omitting only template directive lines allowed structural schema validation.
  This does not execute exception conditions or the Kyverno admission engine.

Current exact UNAPPLIED patch SHA256:
`f15ac0df65e086954348aade5f6143e393a543a258b7c92367f103882a81e37c`.
The earlier06dd079a hash qualifies only the prior packet. Original source base
remains030133dc; prior failed hosted stale expectations and local overlong-TMPDIR
socket failures remain recorded above and in OP233/219, without counter reset.

The packet README names precise remaining runtime gaps: Kyverno compilation,
condition/match/lookup/webhook/defaulting behavior, exec isolation, Cilium,
device cgroups/runtime/image usability and actual expiry/termination/cleanup
with separately authorized restore/readback. SYS_ADMIN remains broadly
administrative with node/driver risk; shared NVIDIA control nodes are not UUID
containment, and hardware persistence is separate from TTL. Source and schema
checks do not establish usable/installed/accepted runtime. Exact final commit,
native immutable-head review and ordinary-push CI receipts are recorded on
PR732 and OP233/219 without an evidence-only follow-up push. Tom's concrete
policy review and activation holds remain intact.

Final local `make check-generated check-layout lint` PASS; generated files are
unchanged and pinned golangci-lint v2.15.0 reports0issues. No local suite failed
in this correction. Schema-extraction and patch-fixture setup failures above
are retained separately from the prior author's actual failed test history.
