# ADR-031: A committed named-host stop finishes before resume

- **Status:** Proposed. No implementation, installation or owner acceptance.
- **Date:** 2026-11-18.
- **Tracking:** [OP877](https://work.haynesnetwork.com/work_packages/877), epic #91.
- **Design:** [DESIGN-068](../designs/068-codex-refused-stop-recovery.md).
- **Decider:** Proposed to coordinator `dev-env-fable-coordinator-1109`; requires
  another-model-family review before merge. This worker grants no live authority.
- **Supersedes:** No accepted ADR. Refines the refused-stop recovery left open by
  DESIGN-067 and proposed ADR-030; other session and retention rules remain.
- **Evidence pin:** dev-env main
  `ffb6af5ce118b243c5c6820c235f104f4159c08b`.

## Context

The named c1 stop writes a fence before quiesce, and leaves the supervisor frozen
on failure. Stop error returns no rescue report. The controller's Running branch
can then retain the same pod and report Running on resume. The envtest exposes
that controller behavior using fake rescue, not real kernel processes.
([codex_remote_stop.go:172–239](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/agentd/codex_remote_stop.go#L172-L239),
[rescue.go:49–66](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/agentd/rescue.go#L49-L66),
[reconciler.go:712–728](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L712-L728),
[characterization:18–80](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/codex_remote_stop_resume_test.go#L18-L80))

The existing name decision applies at construction, with no names by default.
The generic rescue record cannot represent an outstanding stop: it is absent on
stop error and superseded when service is requested. A separate durable contract
is needed for this pod-bound lifecycle intent.
([remote_stop.go:13–48](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/codexworkbench/remote_stop.go#L13-L48),
[controller/codex_remote_stop.go:14–39](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/codex_remote_stop.go#L14-L39),
[reconciler.go:712–721,852–855](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L712-L855))

## Proposed decision

1. Only an operator-named Codex remote host with the matching pod construction
   decision and a successful read-only recovery-capability probe starts this
   lifecycle. Old unsupported agents keep today's suspend and require no receipt.
   Unknown probe results send no stop. No upgrade restarts an agent pod.
2. Confirm optional `status.codexRemoteStop` in API storage before sending the
   qualified stop. Bind Session, operation, original pod and original HOME UIDs. Carry the
   expected Session/pod UIDs in the request; the agent refuses a replacement
   before any fence or signal, not merely when the controller reads it afterward.
   A confirmed record commits that pod to replacement even if the request never
   arrives. Cache uncertainty before that write commits nothing.
3. Resume requests eventual service. It cannot cancel an active stop or restore
   its pod. Finish stop, completed rescue and normal guarded removal first.
   Before the removal decision, discard stale-generation reports. After the
   acknowledged removal decision, a new desired generation queues behind it.
   Keep `wantsPodGone` unchanged; add a separate pod-bound guard branch.
4. Require an operation-bound agent-reported receipt plus the existing completed
   rescue verdict, persisted before delete. The receipt does not independently
   prove kernel state. Preserve the distinction between a stop error and a
   completed failed Git rescue, which retains HOME and blocks archive.
5. Clear active recovery by confirming Complete after the exact target is gone.
   Retain the bounded completed record as the original-HOME binding across later
   resumes. Only a fresh pod on that HOME may serve; a later stop gets a new
   operationID. Holds, including empty and recovery holds, cannot bypass their
   active stop through the usual resume shortcut.
6. Missing pod/node evidence stays RecoveryRequired. Tom or his explicitly
   authorized custodian must establish writer exit, retain HOME and submit the
   bound audited acknowledgement. The controller clears the active operation
   only after a distinct same-HOME hold's successful rescue and guarded removal.
   Status editing alone, time, NotFound and node NotReady are not rescue proof.
7. Pending recovery shows plain waiting/recovery words and PodReady=False on
   the phone, even while the kubelet reports the old pod Ready. No automatic
   thread adoption, fence clearing, thaw, force delete or timeout-to-success.
8. Deliver the generated schema copy to haynes-ops before the operator pin.
   Every status-writing controller/API replica must understand the contract
   before activation. Old replicas can drop unknown status fields and ignore
   the operation; a receipt's unknown JSON key is ignored by the old decoder.
   Rollback to such a binary waits until qualified pods are safely stopped and
   retained records are separately handled. This ADR authorizes no install.

DESIGN-068 sections 2–7 define the exact schema, capability/receipt, commit points,
compatibility and manual evidence. Its section 8 binds named future envtests to
20 restart rows. Section 10 is a written, unrun disposable-host proof. Section 12
leaves the manual custodian and proof host decisions with the owner.

## Alternatives

| Alternative | Reason to reject |
| --- | --- |
| Resume the same stopped pod | Does not restore its frozen supervisor; risks skipping the outstanding preservation work. |
| Apply to every Codex host or infer support from missing receipt | Broadens the name opt-in and can make old agents unsuspendable. |
| Wait to record intent until exec returns | Loses the operation on a crash or lost reply after signals ran. |
| Unfreeze or remove the HOME fence | Re-admits writers while the stop/rescue outcome is uncertain. |
| Put intent in wantsPodGone | Changes an owner-intent predicate for unrelated lifecycle callers and lacks a pod argument. |
| Clear on NotFound or an elapsed deadline | Neither proves writer exit nor completed rescue. |
| Delete the status key when recovery completes | Loses the original HOME identity during a delayed resume or restart. |
| Rely on the active leader during mixed-version rollout | An old standby may become leader and ignore/drop the record. |

## Consequences

A resume can recover automatically after the failed operation later succeeds,
while a permanent failure stays visibly pending with its HOME retained. A
confirmed but undelivered stop deliberately costs a full pod replacement. Old
and unnamed hosts retain their existing path. Manual lost-writer recovery is
slower because it needs explicit exit evidence and a successful hold rescue.

The status contract, agent protocol, guard branch and rollout gate need their own
build, tests and cross-family review. Source merge, installation, disposable
proof and owner acceptance remain separate. No credential, RBAC, retention or
native-thread authority is added by this proposed ADR.
