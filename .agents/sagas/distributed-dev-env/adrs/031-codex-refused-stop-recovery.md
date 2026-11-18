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
   through that pod's life and flag removal. No image-release declaration
   replaces this probe.
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
   thaw or receipt reuse (DESIGN-068 section 3.2/J43). After a capable binding
   exists, a failed probe/request means uncertain dispatch: confirm/reuse the
   operation, never select legacy.
   A capable answer without a recordable pullable digest also takes the clean
   unbound refusal, not a successful legacy stop (J48/section 5.5). Show the
   missing-image-record reason and retain the untouched instance on resume;
   Q-068-04 holds the exact incident repair identity, registry record or reviewed
   image-resolver code change and any separately audited image migration. Only
   the operator service account writes status.codexRemoteStopCapability after
   proof of the original-image mapping, then full qualification and bound stop. Never guess a
   digest or delete an unrescued pod. No upgrade restarts an agent pod;
   c1-only old binaries retain their known M4.
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
   qualified image: copy capability.imageDigest into ordinarily immutable
   operation.recoveryImageDigest and use that exact digest, never the current
   template or flag. Before admitting rescue require the hold's own successful
   capability/digest probe. An unpullable digest keeps recovery pending. A
   present hold with admission explicitly false may be ordinarily removed,
   including while running, because only the no-write hold command and version
   probe were admitted, never rescue ([main.go:331–347](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/cmd/agentd/main.go#L331-L347)).
   Reconfirm false admission, operation/hold/HOME identity and serialise against
   dispatch; use UID/RV preconditions and normal grace. Removal completes nothing.
   True/unknown admission still needs rescue or positive exit evidence. For a
   permanently gone/unusable digest, Q-068-03 asks the owner whether to approve
   one incident replacement that passes the installer check, its own hold probe
   and rescue, preserving original bindings and exit evidence. The immutable
   field requires a separately approved, audited schema repair/migration; no
   automatic template swap or decided exception. J35/J36 give these exits.
   No archive or retry caller may build an ordinary hold during an active operation.
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
   only one configured authenticated ACK_ACTOR may add a correctly bound ACK,
   changing nothing else; nobody may change a present ACK. The operator may
   preserve it unchanged or remove a consumed ACK during validated hold
   replacement or replacement of Complete by a new operation, never add/set one.
   Retain the source ACK through Complete.
   Today's API/controller code has no ACK field/write path; section 7.1 lists
   the status writers. The operator account also serves the session API, so
   its ACK addition must be refused even during otherwise legal transitions
   (J46). Infrastructure/admin wildcard RBAC grants are no exception. Use an
   in-process CEL ValidatingAdmissionPolicy, matching protected-record changes;
   a total guarded change predicate skips unrelated heartbeat/status writes.
   Validation failure denies protected changes, operator writes included. A
   matching-expression error is wider: with no false match condition and
   failurePolicy=Fail, all matching dev-agents AgentSession main/status UPDATEs
   can be rejected, including unchanged heartbeats, suspend/resume and operator
   recovery writes. J47 proves the stall and restoration after policy repair;
   no fail-open or operator exemption. This follows the [Kubernetes 1.35 match
   semantics](https://v1-35.docs.kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/#matching-requests-matchconditions),
   checked 2026-11-18. There is no external webhook dependency, but the namespace
   lifecycle stall risk belongs in Q-068-01. Policy/enforcement remain proposed.
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
    startup, preserving the existing absolute tini/agentd entrypoint chain.
    Today's bare RescueCommand and HOME-first PATH permit a replaceable
    `$HOME/.local/bin/agentd` to forge the entire answer
    ([rescue.go:30–37,103–108](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/rescue.go#L30-L108),
    [Dockerfile:502–518](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/images/agent/Dockerfile#L502-L518)).
    Protected entry points prevent that shim from answering. A separate current
    mutable producer, `$HOME/.local/bin/git`, can falsify bare-Git rescue data
    ([clone.go:189–202](https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/agentd/clone.go#L189-L202)).
    `/usr/bin/git` alone leaves session-controlled hooks, filters, aliases,
    fsmonitor, credentials, SSH, editor/pager programs, config/includes and
    attributes able to run after the final writer inventory. Section 2.2 names
    the actual inherited environment, partial config overrides and every
    unresolved class. G-068-01 blocks activation until the build supplies and
    tests exact per-call environment/config isolation and preservation evidence
    for direct rescue, submodule and bundle calls as well (J42). No complete
    neutralisation measure is claimed present today. The receipt is built in
    agentd memory and
    encoded to stdout, not supplied by a HOME file. No kernel attestation or
    global repair of unrelated legacy rescue paths is claimed.
12. Before any name is added, or template image changes while names remain,
    the installer/GitOps preflight requires release-qualification evidence from
    that exact candidate digest: explicit capability plus released semver at
    or above minimumCodexStopRecoveryVersion. Qualification is an isolated CI
    container run, not cluster exec. The haynes-ops check is required, including
    Renovate template bumps, and pulls/probes the candidate itself; its trusted
    run produces the digest-bound evidence, never a file supplied in the PR.
    Refuse absent evidence, missing
    capability, unknown/below-minimum version or digest mismatch. This installer
    gate also applies after the first install and to rollbacks; it does not
    veto stopping a capable development binary already present. A merge/check
    bypass or direct cluster edit bypasses the installer fence; runtime retains
    the actual old binary's stop limits (c1-only M4, or no c1 on pre-c1 images),
    not permission to clear a record. No runtime digest allowlist is assumed.
13. A contradictory capability binding stays blocked under J41. Its exit is
    an audited, owner-authorized incident correction under exclusive operator
    ownership with exact UID/RV preconditions (a separately approved schema
    migration if validation forbids repair), plus verified original-writer exit
    and recovery rescue whenever that evidence is missing. Ordinary reconcile
    cannot erase/rewrite the immutable binding or choose legacy. A correction
    alone never completes a stop.

DESIGN-068 sections 2–7 define the exact schema, version/receipt, commit points,
compatibility, live RBAC observation and manual evidence. Section 8 gives one
named future envtest for each of J1–J48, with J44 tooling owned by slice A.
Section 10 is a written, unrun proof; section 12 leaves acknowledgement/access,
disposable-host, incident-image replacement and image-repair identity/target
choices open. G-068-01 is a technical activation block, not an owner
choice that can stand in for implemented Git protections.

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
