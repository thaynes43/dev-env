# Owned Codex root authority — source phase2, 2026-10-18

Saga85 / Epic107 / existing Story110. Same sole source owner; no new runtime
campaign, epoch, attempt or deadline was reset. This follows the finite private
child adapter (#422), rather than broadening its synthetic root callback into
production permission.

## Source boundary

The explicit-off ordinary root profile starts the immutable guard directly as
nonroot PID1. Protected memory, kernel original-process identity, a fresh Unix
challenge and the original native peer/RPC supply admission/readiness/stop proof.
Operator-owned policy binds the configured host/task/Pod, finite spec, reviewed
image, Bound RWOP HOME UID and approved recovery baseline/network-policy digest.
The source contract is in DESIGN-003 and hostexecutor/README.md.

Prepared is initial admission only; Starting cannot launch children. Fresh Ready
is checked at dispatch CAS, launch reservation and private continuation. Unknown
authority latches. A validated original Wait/ECHILD/helper-exit stop atomically
latches the campaign while child accounting remains active until separate proof.
Bootstrap intent and directory are fsynced before one create; conflict/unknown
ACK never recover or replay. Stop ACK must name the exact canonical evidence.

## Evidence and limits

The focused vertical test replaces the permissive root fixture with the actual
Run guard/helper/inert native Unix RPC, actual ProbeEvidence, production
OwnedRootProber/OwnedRootInspector, TLS budget API, real isolated Kubernetes
storage/admission and private child controller. It covers initial refusal without
a guard, actual Ready dispatch, reservation before one create, lost create ACK,
early original root stop with a still-charged child, stale child-stop proof,
independent child stop, retained HOME/Pod and denied replay/resume. Initial full
focused run passed in4.38s (suite5.495s). No provider/model actor was used.

This fixture has no kubelet/CRI/Cilium datapath. Its original guard is the test
process, not PID1; the exec adapter performs the real kernel peer/RPC observation
and does not inject readiness. Cilium's stored policy object and token identities
are synthetic; Pod/PVC/ConfigMap/ledger use the real API. A separate hosted CI step
runs the actual lifecycle as UID1000 PID1 in a fresh bounded namespace, including
the protected-parent executable FD4 path. Required current-head CI is the merge
gate; final run/commit/publication receipts belong in the PR and durable handoff.

Targeted tests also cover baseline changes/aliases/auth-directory/hardlinks,
relatime changes, unsafe probe output, missing Yama isolation, policy/HOME/Pod/
image drift, policy changes during observation, startup versus authority-loss
latches, loss between dispatch and launch, wrong stop proof/ACK, bootstrap
conflict/unknown/replay, and checkpoint/deadline crossing during a readiness check.
Independent native read-only design and final implementation review found no
remaining source blockers after the post-probe clock fix. The reviewer did not
run tests or claim runtime acceptance. Eight relevant local suites passed serially: hostexecutor7.931s,
taskbudget0.181s, apiv1 0.062s, apiserver40.693s, controller81.271s,
agentd244.996s, cmd/agentd0.390s and operator0.077s. Final source/clock/marker/
flags rechecks passed (taskbudget0.120s, cmd/agentd0.024s, operator0.024s,
API5.326s). Native review confirmed commitf72f0a6, including the directory-FD
bootstrap marker and retained-admission requirement. Hosted required checks,
PID1 result, merge SHA and publication receipts are recorded separately in the PR
and durable handoff rather than inferred from local tests.

All local tests run serially under the shared lock, nice19/GOMAXPROCS2/-p1. No CPU
burner, stress/load loop, pod image build, provider call or live activation occurs.

## Remaining adoption work and holds

Production must independently verify Yama>=1 and a root-owned0600 FIFO supplied
from CRI exec process creation. Unsupported transport refuses; no kernel setting
is changed. Effective RWOP/Cilium enforcement, trusted sole exec-client ownership,
approved HOME/recovery contents, real pinned native behavior and complete lifecycle
remain installation acceptance. This source does not pair/enroll a real host or
prove phone/recovery/MVP usability. Runtime resource suitability is unmeasured.

Normal operator/helper SHA publication is allowed after merge. No task agent
release/tag, held #369 equivalent, image pin/flag/deploy, new credentials/RBAC/trust,
H7/grant/rendezvous/two-agent cohort, held draft, v1 restart or cutover is included.

## Advisory fixes before final CI

The three MEDIUM findings on PR423 are addressed: a fixed five-second client
clock sanity allowance leaves the server deadline unchanged; actual HOME UID/
baseline preflight occurs before intent/API mutation and is repeated before
admission; at most four concurrent500ms evidence handlers prevent one idle peer
from blocking all observation, and shutdown drains them. New actual idle-peer,
bounded/excess-skew and HOME-drift checks pass. A second hosted nonroot PID1
fixture proves bootstrap ordering: wrong baseline spends neither intent nor HTTP,
valid intent precedes exactly one synthetic HTTPS create, and replay refuses.
Both PID1 checks run before the full hosted suite. Final independent delta review
finds no new blockers. Local updated focused suites pass (host1.035s, apiv1 0.044s,
cmd/agentd0.016s, API8.331s); pinned lint again reports zero issues. Final-head
hosted CI/merge/publication remain separate receipts.
