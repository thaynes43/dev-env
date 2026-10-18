# Private finite budget adapter — Story105 source evidence

Saga85 / Epic103 / Story105, prerequisite campaign CLI PR421 (e6035b2).
One source unit joins private dispatch, launch reservation, agentd stop observation
and independently verified stop accounting. Worktree: agent/private-budget-1018.

The TLS/envtest journey uses the production API/controller/ledger over a real
isolated Kubernetes API. It creates one private Haiku-selected task, reserves
before the single Pod write, loses the Create acknowledgement, observes the exact
Pod, latches at the local checkpoint, refuses stale-Lease stop evidence, accepts
fresh whole-container/Node/Lease evidence, and preserves the original HOME and
Pod with resume/relaunch refused. A separate test root inspector and existing
synthetic token identity fixture stand in for native readiness and TokenReview;
no kubelet or provider is run. Zero model actors are launched.

Regressions also cover complete-document size refusal before charging (including
same-idempotency retry), missing/replaced HOME/common inputs, root not admitted,
fixed zero-child native fixture, private timeout bounds, unknown reservation ACK,
no launch replay, preboot TLS latch/error/binding refusal before state writes,
API response ambiguity, PodGC synthetic termination, and missing/replaced Pod
during the final stop observation. The native independent reviewer found the last
two issues, both were fixed and the focused review confirmed resolution.

Run relevant suites serially with TZ=UTC, GOMAXPROCS=2, nice19 and go test -p1
under the pod's shared test lock. API/schema/controller/agentd/apiv1/agentrun and
ledger suites are the acceptance checks; hosted CI is required on the final head.
Do not run stress, CPU burners, wide or repeated test loops in the shared pod.

This source does not supply production native-root readiness, pair a computer,
load a new binary into an existing session, enable private admission, publish a
held task release, or prove a usable native workflow. Operator/agent image pins,
credentials/access, H7, two-agent rendezvous and v1 changes stay outside this unit.
The root remains charged in the fixture: only private child stop is proven, not
whole-campaign stop. Retained-session instruction loading is separate acceptance.

The haynes-ops follow-up copies only this generated common-rule CEL block. Its
older omission of agent.attached and restart status (idle-drain PR375) is preserved
and recorded, rather than silently changing running-session restart semantics.
