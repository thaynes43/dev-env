# Owned-root operator replacement correction — 2026-10-19

Existing Saga85/Epic107/Story110, same sole owner and original thread/HOME.
The bounded eighth live attempt preserved its Pod but stopped its native process
with `budget authority unavailable` during operator replacement. It is retained,
not retried. That reason combines remote authority and local access sync; the
actual failed phase is unknown. Safe phase-only warnings separate future failures.
No tokens, URLs, projections or native auth contents enter diagnostics.

Real-TLS regressions reproduce two independent source defects: a reused
connection lost before response headers fails observation despite an available
replacement, and the old ten-second API shutdown returns before an existing
sixteen-second native request can finish. Observation alone gains the standard
HTTP transport's replayable-request marker (not transmitted); it still requires
a new complete exact-binding response within its original two-second context.
Admission and mutations do not gain retries. Server drain becomes nineteen
seconds to include the existing eighteen-second caller envelope (authentication
and budget preflight precede the sixteen-second native timer) plus its response,
without extending request/task deadlines. Local access sync is unchanged.

Regression coverage retains denial, revocation, identity, malformed/truncated
status, cancellation/deadline, lost-admission and fresh-connection failure refusal.
The real owned gate uses healthy synthetic access to isolate the remote failure.
Serving-until-withdrawal tests require fresh authority and fresh revocation on
both retained and new connections; they do not prove Kubernetes route timing.
Deployment must keep the old listener serving while endpoints withdraw, within
the existing termination grace. A fixed preStop interval alone is not acceptance.

Scope excludes credentials/RBAC/trust/pairing, model/provider calls, new framework,
and held v1/rendezvous/H7/grant actions. Tests run serially, two Go CPUs under nice19;
no load/stress/wide loops. Source/CI/publication/installed/live acceptance remain
separate. Counters, stop receipts, finite budgets and prior attempts are retained.

## Delivered checkpoint — 2026-10-19 09:34 UTC

Canonical work remains OpenProject Saga85 / Epic107 / Story110; one original
implementation owner. No replacement story, owner, session or campaign reset.

- Source [#440](https://github.com/thaynes43/dev-env/pull/440) merged as
  `4d81c4b1d32e4930181dfa1666f4e4ca5b1cc0d6` after exact-head required CI,
  independent review and advisory review. Release
  [#441](https://github.com/thaynes43/dev-env/pull/441) passed its required CI
  and independent review, then merged as
  `597306ade2185692b1bc0f38f0b422f2e1d7c4af` and published v2.30.7.
- Operator/helper [publication39228988690](https://github.com/thaynes43/dev-env/actions/runs/39228988690)
  and task [publication39229014332](https://github.com/thaynes43/dev-env/actions/runs/39229014332)
  completed actual signing and verification; task build/smoke/push also passed.
  Operator digest is `0526414f5aa56ea14c26874d9635ee24ff21d5f4e6fcd21b855db30fb09d20f6`;
  task digest is `cd9845500f154a4a9690d739bb089d696e0fd6e60f44393d5a55d7f861e98d9d`.
- [haynes-ops#4095](https://github.com/thaynes43/haynes-ops/pull/4095) merged
  `100ee8d6f4d427d8b8db27563badb7b4e9916b8e`. Flux installed the operator
  digest, native preStop5s and explicit existing30s grace. Generation99,
  two Ready replicas, actual digest and zero restarts were verified. Flags,
  fixture008 mapping/policy, Roles and task pins remain unchanged. Generic task
  templates/worker remain2.30.6; task2.30.7 is published, not installed there.

All54 protected Pods,26 Sessions and28 retained budget records were identical
before/after delivery, including both Pending old canaries, real native/HA/v1
Pod identities/images/restarts and private-history records. A bounded180s
service observation returned718 expected unauthenticated401 responses,
359 fresh and359 reused, no transport failures, maximum8ms. This is connectivity
only. Retiring old Pods lacked the new hook, so its five-second delay was not
observed. No active owned root existed: response, active-session update survival,
retained native history, stop/continue and phone acceptance remain **unproven**.
The earlier ordinary single-Haiku instruction/response checkpoint is distinct.

Fresh009 static HOME/public inputs/narrow network resources were delivered by
[haynes-ops#4094](https://github.com/thaynes43/haynes-ops/pull/4094). No009 policy
activation, root Pod, budget, token or model was created. An inert/public packet
is frozen with all eight hashes/modes verified on the existing Mac: final17
Python3.9 inert tests and seven actual Darwin helper dry runs passed. The
baseline response precedes its sole restart; unexpected stop and model-tool
items refuse acceptance. Original600s/120s bounds and all001–008 evidence remain.

### Exact remaining access hold

Automatic approval review rejected the parent's proposed authorization because
trusted approval for these exact persistent named reads was missing:

- Role `dev-agents/dev-env-operator`: append ConfigMap
  `dev-env-owned-luna-1019-9-inputs` to its existing GET-only rule.
- Role `dev-env-system/dev-env-operator`: append ConfigMap
  `dev-env-owned-luna-1019-9` to its existing GET-only rule.

Neither addition was executed; actual Role reads confirm absence. Same operator
SA/RoleBindings, no new subject, verb, Secret, credential, destination or pairing.
Tom has been asked once. Hold these additions and dependent009 wiring/root/token/
model until materially new trusted approval; do not duplicate the question or
work around the rejection. No other current backend access gap is identified.
Phone remains separately parked. Existing runtime authorization ends12:00UTC;
the handoff does not renew it. Ordinary v2 image delivery is not held by v1 rules.
V1 restart/cutover/retirement, rendezvous/cohort,H7/livegrant and held drafts remain
held. Publication is irreversible; consumer Git pins/config provide rollback,
which cannot resurrect a stopped process. Native continuation supports only the
original living guard/Pod/HOME and valid original budget, not Pod-loss recovery.
