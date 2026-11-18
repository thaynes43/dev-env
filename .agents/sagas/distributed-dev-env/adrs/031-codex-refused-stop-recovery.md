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

1. Naming belongs to the pod's construction record: its explicit Session-UID
   environment entry, never the stop-name flag as read later. A mismatch is not
   named and receives no bound request. Probe
   `/usr/local/bin/agentd version --format=json`; require explicit integer
   `codexStopRecovery: 1`, independently of its version stamp, including dev and
   prerelease builds. Persist the first capable result and that pod's verified
   pullable image digest in bounded operator-owned status before any stop. Keep it
   through that pod's life and flag removal. No image-release declaration replaces this probe.
   These are proposed additions to the existing construction-only decision,
   which is shown by [controller/codex_remote_stop.go:14–39](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/codex_remote_stop.go#L14-L39);
   the existing version route is [cmd/agentd/main.go:217–223](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/cmd/agentd/main.go#L217-L223).
   Plain version/heartbeat output stays unchanged: today's strict host selector
   rejects added fields/suffixes ([select.go:517–546](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/codexhostpool/select.go#L517-L546)).
   The three older-release sources in DESIGN-068 section 2.1 have no capability
   field and reject the new format option. Never infer capability from old text.
2. Before any capable binding/active recovery operation, an old answer, failed
   probe, missing field or unrecognised output attempts the absolute-path legacy stop
   with no new receipt gate.
   An old binary runs its existing route. A recovery binary refuses an unbound
   stop before lock, fence, freeze or Git, so that supported fallback cannot
   freeze an unrecorded pod. An in-pod/raw-exec caller can still invent a bound
   operation and freeze its own pod; this known self-inflicted limit recovers
   through a real recorded same-pod stop/rescue and normal replacement, never
   thaw or receipt reuse (DESIGN-068 section 3.2/J43). After a capable binding exists, a failed probe/request
   means uncertain dispatch: confirm/reuse the operation, never select legacy.
   No upgrade restarts an agent pod; c1-only old binaries retain their known M4.
3. Confirm `status.codexRemoteStop` before the qualified request, bound to the
   Session, operation, original pod, original HOME and node. The agent validates
   expected Session/pod UIDs before any mutation. A confirmed operation commits
   that pod to replacement even if the request never arrives. Cache uncertainty
   before the write commits nothing. Both capability and operation status have
   bounded fields and status/root CEL rules refusing key or whole-status erasure.
4. Resume requests eventual service; it cannot cancel an active stop or restore
   its pod. Keep `wantsPodGone` unchanged and use a separate pod-bound branch.
   Persist the receipt, full rescue verdict/reference and RemovalCommitted in
   **one** confirmed status transaction, not two writes with a report-only middle
   state. Discard stale-generation reports before commit; later generations queue
   behind a confirmed decision. The receipt is an agent-reported completion,
   not independent kernel proof. Preserve completed failed Git rescue for normal
   executor removal, while retaining HOME and RescueFailed.
5. An ordinary hold with no recorded stop keeps today's resume shortcut, shown
   by [guard.go:137–150,289–300](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/guard.go#L137-L300).
   Do not create a stop solely to remove an untouched hold on resume. If an
   ordinary hold's stop is recorded, finish its stop/completed report, then keep
   D-55's mode distinction: resume permits removal even after failed Git rescue;
   continued suspension needs successful rescue. Only a recovery hold built to
   finish an existing stop must successfully rescue regardless of desired mode.
   That is a new recovery block. Build its capability from the record and a
   qualified image: copy capability.imageDigest into immutable
   operation.recoveryImageDigest and use that exact digest, never the current
   template or flag. Before admitting rescue require the hold's own successful
   capability/digest probe. An unpullable digest keeps recovery pending; only
   a strictly proved never-ran hold may then be ordinarily removed without
   rescue, and that removal completes nothing. A failed probe keeps admission
   false: repair/retry it, or prove ended/never-ran before replacement. A running
   hold cannot be deleted just because its probe failed (J35/J36). No archive
   or retry caller may build an ordinary hold during an active operation.
6. An original pod that ended but remains present has a positive-evidence exit.
   Confirm the exact pod UID, original HOME and bound node UID; require that
   live node's kubelet reports every admitted container terminated with a
   finish time, with no missing/unknown/currently running status. Persist that
   evidence and RecoveryRequired before ordinary UID/resourceVersion deletion.
   After absence, a capable hold on the original HOME must successfully rescue
   and be removed before Complete. This narrowly extends today's ended-pod/D-55
   route ([guard.go:128–150,278–300](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/guard.go#L128-L300),
   [reconciler.go:988–1014](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L988-L1014)).
   A non-Ready node or unreadable/incomplete status waits with a reason. An ended
   recovery hold uses the same positive proof and still needs a replacement's
   successful rescue. Phase, time and object absence alone never prove exit.
   Both the evidence write and delete recheck reuse executorNodeLive: exactly
   one Ready=True condition, non-deleting bound Node, matching non-deleting
   node Lease holder/owner UID renewed within 40 seconds and not in the future
   ([workspace_stop.go:64–110](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/workspace_stop.go#L64-L110)).
   Missing ephemeral termination or Waiting regular status after re-run init
   stays held; J39/J40 require later strict evidence or an authorized verified-exit
   incident, source absence and bound human ACK before recovery-hold rescue.
7. Unexpected source/hold absence without such evidence stays RecoveryRequired.
   A bound human exit acknowledgement can permit the same recovery-hold route;
   a status edit alone never completes rescue. The live RBAC inventory in
   DESIGN-068 section 7.1 includes operator and infrastructure wildcard grants;
   it establishes no custodian role and no live admission result. Q-068-01 leaves
   Tom the choice between his own administrator login and a new narrow right for
   a named account, a separate access decision. **Either choice also requires
   owner approval of the exact admission rule in DESIGN-068 section 7.1**:
   only the operator and one configured authenticated ACK_ACTOR may change the
   ACK slots; the actor may only add its correctly bound ACK, changing nothing
   else. Infrastructure/admin wildcard RBAC grants are no exception. Fail-closed
   policy and effective enforcement are proposed, not assumed or approved.
8. Confirm Complete only after exact committed removal, retaining the bounded
   operation as the original-HOME latch for later resumes. Only a fresh pod on
   that HOME may serve. RecoveryRequired must remain visible in ADR-018's
   168-hour owner decision batch, or its explicit missing-clock exception; no
   hidden indefinite retention, invented completion clock or expiry-to-delete.
   This is visibility under the existing owner policy, not a new cleanup right.
9. Show plain waiting/recovery words and PodReady=False, even if the old pod is
   kubelet-Ready. Running intent waits in Pending; Suspended retains its requested
   phase with recovery detail. No automatic thread adoption, thaw, fence clearing,
   force delete, out-of-service-taint recipe or timeout-to-success is authorized.
10. Copy the generated schema to haynes-ops before the operator pin. Every
    status-writing controller/API replica must understand it before activation,
    including sessiondirectory.KubeStore.writeReceipt
    ([store.go:267–275](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/sessiondirectory/store.go#L267-L275)).
    With both removal latches installed, an old typed whole-status update omitting
    the record is rejected; it cannot erase it. A preserving merge patch may
    succeed. Old control flow can still ignore the operation, so the mixed-version
    activation/rollback hold remains mandatory. The older rescue decoder still
    ignores unknown receipt keys. No install follows from this ADR.
11. Every operator command for this feature uses `/usr/local/bin/agentd` in
    the read-only root, including probes, fallback, bound rescue and hold
    startup. Today's bare RescueCommand and HOME-first PATH permit a replaceable
    `$HOME/.local/bin/agentd` to forge the entire answer
    ([rescue.go:30–37,103–108](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/rescue.go#L30-L108),
    [Dockerfile:502–518](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/images/agent/Dockerfile#L502-L518)).
    Protected entry points prevent that shim from answering. A separate current
    mutable producer, `$HOME/.local/bin/git`, can falsify bare-Git rescue data
    ([clone.go:189–202](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/agentd/clone.go#L189-L202)).
    Protected `/usr/bin/git` calls and audit of helpers/hooks are an activation
    gate for this recovery report. The receipt is built in agentd memory and
    encoded to stdout, not supplied by a HOME file. No kernel attestation or
    global repair of unrelated legacy rescue paths is claimed.
12. Before any name is added, or template image changes while names remain,
    the installer/GitOps preflight requires release-qualification evidence from
    that exact candidate digest: explicit capability plus released semver at
    or above minimumCodexStopRecoveryVersion. Refuse absent evidence, missing
    capability, unknown/below-minimum version or digest mismatch. This installer
    gate also applies after the first install and to rollbacks; it does not
    veto stopping a capable development binary already present.
13. A contradictory capability binding stays blocked under J41. Its exit is
    an audited, owner-authorized incident correction under exclusive operator
    ownership with exact UID/RV preconditions (a separately approved schema
    migration if validation forbids repair), plus verified original-writer exit
    and recovery rescue whenever that evidence is missing. Ordinary reconcile cannot erase/rewrite the immutable
    binding or choose legacy. A correction alone never completes a stop.

DESIGN-068 sections 2–7 define the exact schema, version/receipt, commit points,
compatibility, live RBAC observation and manual evidence. Section 8 gives one
named future envtest for each of J1–J45. Section 10 is a written, unrun proof;
section 12 leaves the acknowledgement/access and disposable-host choices open.

The Kubernetes 1.35 [pod lifecycle documentation](https://v1-35.docs.kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#garbage-collection-of-pods)
confirms failed objects can remain until removal. Its [node shutdown documentation](https://v1-35.docs.kubernetes.io/docs/concepts/cluster-administration/node-shutdown/#graceful-node-shutdown)
confirms termination without restoration after cancelled shutdown; the exact
Failed API phase on graceful shutdown remains **Unknown from those docs**.
The design uses observed positive evidence, not that inferred cause.

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
| Rely on the active leader during mixed-version rollout | An old standby may become leader and ignore the record even when the schema refuses its erasure. |

## Consequences

A resume can recover automatically after the failed operation later succeeds,
while a permanent failure stays visibly pending with its HOME retained and the
unresolved evidence surfaced for the owner under accepted
[ADR-018](018-two-tier-home-retention.md). A
confirmed but undelivered stop deliberately costs a full pod replacement. Old
and unnamed hosts retain their existing path. Manual lost-writer recovery is
slower because it needs explicit exit evidence and a successful hold rescue.

The status contract, agent protocol, guard branch and rollout gate need their own
build, tests and cross-family review. Source merge, installation, disposable
proof and owner acceptance remain separate. No credential, RBAC, retention-duration or
native-thread authority is added by this proposed ADR.
