# DESIGN-068: Recover a named Codex host after a refused stop

- **Status:** Proposed. Documents and proof preparation only. No recovery code,
  schema, installation or owner acceptance is delivered here.
- **Date:** 2026-11-18.
- **Tracking:** [OP877](https://work.haynesnetwork.com/work_packages/877), under
  epic #91 in the distributed dev-env saga. OP858 slice (d) waits for OP877 to
  close. This design does not close the build or installed-proof acceptance.
- **Decision:** [ADR-031](../adrs/031-codex-refused-stop-recovery.md), proposed.
- **Source pin:** dev-env main
  `ffb6af5ce118b243c5c6820c235f104f4159c08b`, fetched 2026-11-18. Every source
  link below uses this pin. Proposed behavior is identified as such.
- **Owner:** coordinator `dev-env-fable-coordinator-1109` owns the story, review
  and install gates. Design worker `dev-env-1118-164400` owns this docs branch.
  Another model family must review the final head before merge.

## 1. Problem and boundary

At the pin, a named host's stop publishes its HOME fence **before** quiescing.
Quiesce freezes the supervisor and leaves it frozen on failure. Agentd returns
an error without a rescue report when that stop fails.
([codex_remote_stop.go:190–239][agent-stop],
[codex_remote_processes.go:387–425][freeze], [rescue.go:49–66][agent-rescue])

The controller records no rescue on that error. A later resume selects ensure,
which keeps a present pod. Observation can report Running from kubelet readiness
and clear RemovalBlocked. The characterization test demonstrates those API
facts with a fake rescuer; it does not demonstrate frozen kernel processes.
([reconciler.go:712–728,852–867][reconcile],
[reconciler.go:1395–1402][ensure], [reconciler.go:1780–1801][conditions],
[reconciler.go:2005–2006][phase],
[codex_remote_stop_resume_test.go:18–80][m4-test])

Proposed rule: after the operator confirms a durable stop operation for a
qualifying pod, a resume queues service after that pod's stop, completed rescue
and removal. It cannot return that pod to service. A completed operation permits
one replacement on the same original HOME. A refusal or missing evidence keeps
service pending. There is no deadline that changes failure to success.

In: the rule, bounded status/protocol shapes, compatibility, manual recovery,
phone wording, build slices and proofs. Out: code, generated files, manifests,
installation, live experiments, changes to enrollment or native thread selection.
The only change to DESIGN-067 is its section 14 pointer here. Section 9 and slice
(c2) remain with their worker.

Inputs: the private `op858/c1-m4-proposal.md` is a rejected starting proposal;
`slice-c1-recheck1-crossfamily.md` part D supplies nine objections answered in
section 9. Recheck 2 records M4 as still open. OP858 comments 8750 and 8765
establish the default-empty name gate and hold installation until OP877 closes
and the authorized disposable-host proof passes. These are coordinator rulings,
not new quotations or approvals from Tom.

## 2. Eligibility and old images

Today `--codex-remote-stop-hosts` names at most 16 exact host names in the session
namespace. Its zero value names none. Only construction writes the explicit
`AGENTD_CODEX_REMOTE_STOP_SESSION_UID` entry. Changing the list does not mutate
a running pod. Agentd reads the entry, requires its Session UID, and takes the
legacy path when the entry is absent or empty.
([remote_stop.go:13–48][names], [controller/codex_remote_stop.go:14–39][decision],
[agentd/codex_remote_stop.go:27–46][agent-decision])

The proposed operation starts only when all these checks pass:

1. The current operator names this Codex remote host. It is neither a task,
   local session, shared workspace nor a private-history lifecycle.
2. The owned pod's explicit agent-container entry equals this Session's UID.
   A name added after pod construction does not qualify that pod.
3. A **read-only capability probe of the actual pod**, described below, confirms
   recovery protocol version 1. The pod UID, container restart count and imageID
   still match the reads around the probe. A template revision, current template
   image, heartbeat or missing stop receipt cannot establish this capability.
4. Uncached reads confirm the Session UID/generation, original owned HOME claim
   name/UID, owned pod UID and node name/UID before recording the operation.
   Never acquire the original claim UID from a later same-name replacement.

A persisted active operation survives later flag removal. That is completion of
an already named stop, not a new opt-in for an unnamed host. Flag removal cannot
undo a possibly frozen process. Never create an operation for an unnamed host.
The existing private-history path stays separate; remote sessions are not newly
latched by that feature today.
([private_history.go:34–46][private-history])

### 2.1 Capability before stop, never a destructive probe

Add a future `agentd ctl codex-stop-capabilities` command. It only reads compiled
support, the Session document, Pod UID and the existing environment decision.
It acquires no lifecycle lock, creates no file, signals no process and invokes
no provider. Its bounded, strict JSON response is:

```json
{"version":1,"sessionUID":"SESSION_UID","podUID":"POD_UID","recoveryVersion":1,"stopEnabled":true}
```

The operator execs it with the existing pod exec route, with a 15-second bound,
a 4 KiB stdout cap and no logged raw output. Validate exact identity, all keys,
EOF and version. Re-read pod identity, imageID and restart count afterward.
Support is an assertion by this agent binary, not an independent trust proof.
No new operator flag or image-version comparison is needed.

At the pin, a one-word unknown ctl command reaches a fixed diagnostic and usage
exit without running rescue. Use that exact, empty-stdout, normal-process-exit
answer to classify the pinned old binary as unsupported. The build must fixture
other supported old releases' actual answers; an unrecognised answer is unknown,
not unsupported.
([cmd/agentd/main.go:849–867,945–947][old-ctl])

An unsupported old image takes **exactly today's rescue/stop route**, with no new
operation and no required receipt. This includes an older c1 image: its current
named-stop behavior, including M4, is not secretly repaired by this design.
Installation must never opt an unqualified c1 pod into use. Safely suspend it by
its existing route and later resume onto a qualified image when authorized.
An unnamed old host needs no probe and behaves exactly as at the source pin.

A network error, timeout, malformed response or replaced pod is **unknown**, not
proof of an old agent. Send no stop and create no operation; report a retryable
capability check failure. A resume before any operation keeps the untouched pod.
This adds a retryable preflight for named candidates, never a permanent
receipt requirement for an old image. An upgrade restarts no host to gain a
capability. Once an operation exists, losing the probe cannot select legacy
fallback. A missing/mismatched receipt then blocks recovery.

## 3. Cost of an undelivered stop and the durable contract

Check cache currency **before** creating an operation. Today cacheBehind causes
a requeue before rescue; the existing writer uses whole-status Update, and a
separate private-history helper already compares intended status with both the
write response and an uncached read.
([reconciler.go:845–851,1047–1058][cache],
[reconciler.go:805–815][status-update], [private_history.go:63–92][ack])

Proposed order: uncached eligibility reads; optimistic status write; compare the
intended record with the write response; uncached confirmation of the record and
all target bindings; then stop-bearing exec. Failure, conflict, pruning, missing
ACK or cache uncertainty sends no stop. Never acknowledge a pruned record by
comparing two already-pruned objects. A later reconcile reads the durable record
before doing anything else that could start or remove a pod.

There is a deliberate conservative cost. **After a confirmed record, even an
exec request that never reaches agentd commits this pod to replacement.** A lost
reply cannot establish that no signal ran. Retrying that operation finishes stop
and rescue even when the owner has asked to resume. A cache-behind requeue before
the write costs no replacement. A failure after the write costs the full stop,
rescue, removal and restart; any turn still running when the stop finally reaches
it is cut. The phone must say this before the owner treats resume as recovery.
No claim of agent-side entry is inferred from a controller timestamp.

### 3.1 Exact proposed status shape

Add optional `status.codexRemoteStop`. This is an operation record, not a
condition, rescue verdict or deletion permission by itself. The enclosing
AgentSession UID is checked on every read. Example values below are placeholders.
All object members are listed; there are no unbounded lists or maps.

| Member | Type and validation | Meaning |
| --- | --- | --- |
| `version` | required integer, enum `1` | Contract version. |
| `operationID` | required string, UUID syntax, max 36 | New random ID for each new stop, reused for its retries. |
| `sessionUID` | required string, min 1, max 128 | Immutable owning Session UID. |
| `podUID` | required string, min 1, max 128 | Original stop target; never rebound to a replacement. |
| `podRole` | required string enum `Executor`, `Hold` | Original target role. |
| `homeClaimName` | required DNS subdomain string, min 1, max 253 | Exact mounted original HOME claim. |
| `homeClaimUID` | required string, min 1, max 128 | Original owned PVC UID. |
| `nodeName` | required DNS subdomain string, min 1, max 253 | Original target's node name. |
| `nodeUID` | required string, min 1, max 128 | Original node identity, not evidence of writer exit. |
| `startedAt` | required date-time | Operation creation time, not a stop receipt. |
| `lastAttemptAt` | required date-time | Stored before dispatch; limits retries after restart. |
| `phase` | required enum `StopPending`, `RemovalCommitted`, `RecoveryRequired`, `RecoveryHold`, `Complete` | Steps defined below. |
| `receipt` | optional object with required `version` = 1, `operationID`, `sessionUID`, `podUID`; string bounds as above | Agent-reported stop completion for the current removal target. |
| `rescue` | optional object with required `podUID` (1..128), `generation` (int64, minimum 1), `stamp` (1..128), `result` (enum `Verified`, `CleanAndPushed`, `Failed`) | Reference to the completed `status.rescue`, not a copied generic success flag. |
| `removalCommittedAt` | optional date-time | Acknowledged decision to remove the receipt's pod, after its completed rescue. |
| `recoveryAck` | optional object, exact shape below | Authorized human assertion of the old writer's exit. |
| `recoveryHoldUID` | optional string, min 1, max 128 | Distinct current rescue hold; cannot equal original pod UID. |
| `recoveryHoldNodeName` | optional DNS subdomain string, min 1, max 253 | Current recovery hold's node name. |
| `recoveryHoldNodeUID` | optional string, min 1, max 128 | Current recovery hold's node UID. |
| `recoveryHoldAck` | optional object, same members and bounds as recoveryAck | Separate assertion for an unexpectedly lost recovery hold. |

`recoveryAck` has required `operationID`, `sessionUID`, `podUID`,
`homeClaimUID`, `nodeUID` (same bounds and equality to the containing record),
`at` (date-time), `actor` (string, 1..253), `evidenceRef` (string, 1..512),
and `writerExit` (boolean, only `true`). The reference names a private incident
record, never credential material, a transcript or a phone session link. An
actor string grants no authority. Kubernetes authorization and the incident's
custodian authorization govern the write; the audit record identifies its caller.

For `recoveryHoldAck`, the pod/node UIDs equal recoveryHoldUID and
recoveryHoldNodeUID; operation/Session/HOME IDs still equal the record. Require
both recovery-hold node members whenever recoveryHoldUID is present. Retain the
original recoveryAck after a lost hold; it is not overwritten by recoveryHoldAck.

The controller owns all members except `recoveryAck` and `recoveryHoldAck`, which
an already authorized cluster custodian writes only under section 5. Heartbeat input cannot write any
of them. Preserve them through status observation and every status writer.
At the pin heartbeat uses a merge patch; this does not make other whole-status
writers safe with an unknown field.
([heartbeat.go:155–175][heartbeat], [agentsession_types.go:915–947][status-type])

Validation must bound every string used by CEL. Within an active operation,
identity members, role and startedAt are immutable; lastAttemptAt cannot move
backwards. Receipt operation/Session IDs equal the record. Its pod UID is either
the original pod or the bound recovery hold. Rescue and receipt pod UIDs agree.
RemovalCommitted requires receipt, rescue and removalCommittedAt; RecoveryHold
requires recoveryAck. Hold-role RemovalCommitted additionally requires a
successful rescue, as section 5 specifies. Complete requires the completed
removal evidence. A new
operation may replace a Complete record only with the same Session and HOME
binding, a fresh operationID and a different currently owned pod UID. An invalid
or unknown-version record fences construction and deletion; it never means absent.
Tests must show legal transitions, rejected contradictions and acceptable CEL
cost in the generated schema. Do not use CEL alone to infer process exit.

**Clear the active stop by writing `phase: Complete`, not by deleting the JSON
key.** Keep this one bounded completed record as the HOME identity latch across
a delayed resume or operator restart. Complete asks for no pod removal. It lets
ensure build only on the recorded claim name/UID, never create/adopt a new HOME.
A later independent stop replaces it with a fresh operation on that same HOME.
Normal archive, if requested and otherwise allowed after completion, remains a
separate terminal path; an archived Session cannot resume. Do not let an active
operation reach archive, releaseIfEmpty or a finalizer release. This design does
not extend a suspended host's normal retention period after completion.
([reconciler.go:708–711,750–780][reconcile],
[guard.go:312–345][volume-guard])

### 3.2 Receipt and completed rescue

Extend the future `protocol.AgentStop` with optional `codexRemoteStop`, shaped
exactly as the status receipt above. The qualified stop request is
`agentd ctl rescue --stop-agent --codex-stop-operation OPERATION_UUID`.
The new agent accepts it only for an enabled, identity-matched remote stop.
Emit the receipt only after c1's final inventory and fence recheck, then return
it in the rescue report. Retry uses the same operationID. An unrelated invocation
or a copied old receipt cannot satisfy it.

A receipt means **an agent implementing c1 reported a completed stop** over the
same exec channel as rescue. It is neither an independent kernel observation nor
an attestation against malicious raw exec. At the pin AgentStop contains no such
receipt; the rescue decoder tolerates unknown JSON keys. An old operator would
ignore a new receipt.
([protocol/rescue.go:131–143][stop-type],
[controller/rescue.go:207–223][decode],
[codex_remote_processes.go:568–592][final-inventory],
[codex_remote_stop.go:217–239][agent-stop])

Keep the full existing verdict, ref coverage, pod identity and HOME checks.
Persist `status.rescue`, the validated receipt and its reference in one confirmed
status transaction. A report for an older generation after a concurrent resume
is discarded and retried. No transport error, nil report, missing receipt,
contradictory report or failed status write authorizes delete.
([reconciler.go:1024–1044,1084–1105][record],
[controller/rescue.go:261–289,375–386][verdict])

A completed report with a **failed Git rescue** is distinct from stop refusal.
Preserve D-10 for an executor: that pod may go after its completed stop/rescue,
while HOME stays and RescueFailed blocks archive. A hold retains D-55's stronger
successful-rescue requirement; a failed hold rescue keeps the hold and record. The new receipt is additionally required for
this qualifying pod. Do not silently require every Git rescue to be Verified in
the normal removal path. The stricter manual recovery path in section 5 requires
a successful hold rescue because it repairs missing original evidence.
([controller/rescue.go:520–536][rescue-predicates])

## 4. Reconcile and the removal guard

Do **not** change `wantsPodGone(s)`. It remains the owner's suspend/reap intent.
At the pin it takes no pod; podRemovalAllowed, holdRemovalAllowed, recordRescue,
phase, idle and other paths consult it. Its broad call surface is why a Session
predicate must not acquire a pod-bound side effect.
([guard.go:75–151][pod-guard], [reconciler.go:1024–1032][record])

Add a separate proposed `codexStopTargetsPod(session, pod)` check. It requires
an active valid operation, exact Session/pod ownership and UID, HOME binding,
and the appropriate original or recovery-hold role. It does not read desired
mode as proof. `codexStopPending(session)` reports any active/invalid operation,
even if no matching pod exists. A mismatch blocks, never stops the different pod.

Insert reconciliation of an existing operation after uncached Session/pod/HOME
reads and before admission-drift early returns, rescue supersession, ensure,
drain, idle, archive or finalizer release. An active operation always owns this
lane, including when spec says Running. Also guard ensure, pod deletion, volume
deletion and finalizer-release boundaries so another caller cannot bypass it.
Existing private/shared lifecycle gates keep their precedence and may refuse;
this record grants them no exception. The current relevant branch order and
write-before-delete path are the insertion points, not a request to reorder all
lifecycles.
([reconciler.go:584–634,697–790,818–888][reconcile],
[guard.go:96–151,318–365][pod-guard])

For the operation's pod only, adapt removePod/needsRescue and recordRescue to
accept this separate removal cause. Keep current UID, ownership, original HOME,
report and generation checks when recording each attempt. Fresh reads must agree
at the decision; never edit spec to pretend the owner suspended again. No other
caller's `wantsPodGone` answer changes.

After recording the completed rescue, confirm an optimistic transition to
RemovalCommitted with the exact report reference and receipt, while that report's
generation is current. That is the durable removal decision. `podRemovalAllowed`
gets an explicit narrow branch for this target before the generic Running/hold
shortcuts. It validates the operation and its recorded decision. `deletePod`
remains the sole ordinary deletion primitive with pod UID/resourceVersion
preconditions and normal grace. It also rereads Session and HOME at this boundary.
([guard.go:96–151,354–365][pod-guard])

A generation change **before** RemovalCommitted discards the old report. A resume
**after** that confirmed decision queues behind it; it cannot invalidate this
already completed stop or re-admit that pod. This is the new lifecycle rule, not
a general relaxation of rescueRanIn's generation check. Record the generation at
commit; never rewrite an old rescue's generation to manufacture current proof.
The operation-specific branch may finish the committed deletion using that
recorded generation. The legacy guard and all other rescue generations retain
their current meaning.
([rescue.go:520–536][rescue-predicates])

Wait for uncached disappearance, not deletionTimestamp. Require the committed
receipt/rescue, original Session, original HOME still present and no unexpected
same-name pod. Then confirm Complete by an optimistic write and uncached reread.
Only a later reconcile may ensure service if desired mode is still Running,
with the retained HOME UID checked at construction. No second pod, no new claim,
no thaw, fence deletion, force delete or shortened grace. The last completed
record remains for the next resume, as section 3.1 specifies.

Store lastAttemptAt before every stop dispatch, including the first. The writer
that confirms that update may dispatch immediately; a reconstructed reconciler
waits until at least one minute after that value before retry. This carries the
current one-minute execution retry across restarts, without claiming that time
proves completion. Keep the existing slower transient Git retry rules where
applicable. Refusals refresh bounded conditions without a tight status-write loop.
([reconciler.go:95–96,852–860][retry])

## 5. Missing pods, manual recovery and hold pods

A pod NotFound, missing Node object, NotReady node or elapsed time is not writer
exit. If the source vanishes before a confirmed RemovalCommitted, retain the
binding in RecoveryRequired. A different same-name pod is a conflict, never an
inheritor of the old receipt. The controller creates no executor or recovery hold
until original-writer exit is established. Existing finalizers protect retention;
the operation must also fence archive while this uncertainty remains.
([guard.go:41–52,312–345][volume-guard])

Normal manual repair keeps the original pod: Tom or his expressly authorized
custodian resolves the signal/node fault. The controller retries the same stop,
gets its completed rescue, removes it and marks Complete. No human status edit
is needed and a second suspend is not required.

If the original pod has already gone without that evidence, the custodian must:

1. Keep the original Session and HOME. Verify their exact UIDs. Record evidence
   that the original container cannot run again or write HOME: for example the
   node's confirmed power fencing plus storage detach, or verified container
   termination on that node. API absence or an action request is insufficient.
2. Have **Tom or his explicitly authorized disruptive-recovery custodian**, with
   existing permission to update this status, write only recoveryAck using a
   resourceVersion precondition. It binds this operation, source pod, Session,
   HOME and node UIDs to the incident evidence. It does not set Complete or delete
   the field. No ordinary host thread, heartbeat or parent note can acknowledge
   its own stop. A cluster custodian who lacks status permission stops; this
   design grants neither credentials nor a new route.
3. The controller validates the acknowledgement and uncached absence, then
   enters RecoveryHold. It creates a credential-free D-55 hold on the **same
   original HOME**, captures its UID, probes its version and runs the qualified
   rescue. A lost Create ACK requires uncached discovery of the one owned hold;
   never adopt an executor or different HOME. A failed hold rescue retains the
   hold and active record. A mismatching/stale acknowledgement retains the block.
4. Require the hold's stop receipt and a successful Verified or CleanAndPushed
   rescue, recorded before its normal guarded removal. After that hold is gone,
   the controller confirms Complete. This is exactly how a manual recovery
   clears the active record; section 3.1 retains its HOME identity and evidence.
   An authorized status patch alone is not a completed rescue.

The acknowledgement is an explicit human assertion checked against bindings;
software cannot verify a power fence from its evidenceRef. Audit and the incident
record must identify who made it and what they observed. A false assertion is
outside this controller's trust boundary. No automatic fallback treats NotFound
as that assertion. Q-068-01 holds the first use of this manual path.

Holds need their own handling. Today a hold has `agentd hold`, no agent credentials
or heartbeats; resume may remove it before a rescue. A named hold receives the
same environment decision, and c1 can freeze its actual hold supervisor. Empty
HOME uses a runtime fence to preserve the empty-volume proof.
([pod.go:303–315][hold], [guard.go:137–150][pod-guard],
[controller/codex_remote_stop.go:30–39][decision],
[codex_remote_processes.go:173–227][hold-supervisor],
[codex_remote_stop.go:195–217,233–237][agent-stop])

Proposed: a qualifying hold gets the same stop operation before its first
stop-bearing rescue. Once active, a resume cannot take the existing
`!wantsPodGone` hold-removal shortcut. For an eligible hold that resume would
remove before any attempt, first complete its stop/rescue under this rule.
Unnamed/unsupported holds retain today's behavior. A recovery hold is a distinct
target within the original operation; it never overwrites the original pod UID.
Its own disappearance before a committed rescue returns to RecoveryRequired,
with a new recoveryHoldAck required for that hold before another hold. Do not
reuse the source acknowledgement to assert the missing hold stopped. The second
acknowledgement binds the lost recoveryHoldUID and recoveryHoldNodeUID, while
retaining the source recoveryAck. Only a matching fresh hold acknowledgement and
confirmed absence permit replacing that hold's binding. Persist and confirm the
new hold binding, and clear the consumed recoveryHoldAck, before its rescue.
No active source/hold binding is rewritten merely because a pod name is free.
Status retains one source and one current hold, not an unbounded incident ledger.
A lost replacement hold needs its own new acknowledgement under the same rule.

## 6. What Tom sees on the phone

Proposed session detail and `agent-run show` wording, with no native conversation
or enrollment link added:

> Resume is waiting. This computer started stopping and cannot restart in the
> same pod. We are finishing its stop and saving its work. Its HOME is kept.

On refusal append a bounded plain reason, for example:

> A process has not stopped. We will try again. Resuming again will keep waiting.

For lost evidence:

> Resume needs recovery. We cannot confirm the old computer stopped. Its HOME
> is kept. Tom or his authorized custodian must confirm the old writer is gone,
> then rescue that HOME before a new computer starts.

While active, phase is Pending and PodReady=False regardless of kubelet readiness.
Use reason `CodexStopPending`, `CodexStopRecoveryRequired` or
`CodexStopRecoveryHold`; RemovalBlocked=True explains the step and last refusal.
`status.pending` repeats the plain message. Suspended desired mode and deletion
still display their requested intent alongside this pending work. Complete
clears only the stop block; preserve any RescueFailed, admission or archive block.
Only normal readiness of the fresh pod may say Running. Repeated resume requests
change no operationID and create no additional stop targets.

Today observe derives phase/pending/conditions and podPhase can say Running from
kubelet PodReady; those are the code points the build must change. The phone's
actual rendering is future proof, not established by status fields alone.
([reconciler.go:1767–1801][conditions], [reconciler.go:1986–2009][phase])

## 7. Schema delivery, mixed versions and rollback

A CRD field is required. HOME markers and pod annotations cannot be the
controller's only memory across pod loss. status.rescue alone is insufficient:
it is absent on stop error and deliberately superseded when Running wins.
The current status contract contains rescue and conditions, not this operation.
([reconciler.go:712–721,852–855][reconcile],
[agentsession_types.go:915–947][status-type])

The later build changes the Go API type, deepcopy and generated CRD together.
Then copy the generated AgentSession CRD to haynes-ops
`kubernetes/main/apps/dev-env-system/crds/app/agentsessions.yaml` **before** the
operator image pin. Read back schema preservation with a disposable test object
before any real record. No haynes-ops source or live admission/RBAC was inspected
by this design worker. These are delivery requirements, not claims about installed
permissions or pins.

An older operator decodes neither this status member nor the new receipt. Its
typed whole-status Update can drop an unknown member, while its Running branch
can ignore the pending stop and ensure a pod. An older agent ignoring a key does
not make this safe. The known source points are whole-status Update, the Running
branch and the non-strict report decoder; mixed-version effects must be tested,
not merely assumed safe.
([reconciler.go:712–728,805–815][reconcile],
[controller/rescue.go:207–223][decode])

Therefore activation is held until **every controller and API replica that can
write Session status** understands and preserves the field. Existing leader
election is not a compatibility gate: an old standby could become leader. Use
an install window with the stop-name list empty, no pending operations and no
lifecycle use of an already qualified pod while versions overlap. Record image
pins and read back every replica before lifting the hold or naming the disposable
host. On the first install there must be no c1-only named pod. A service upgrade
must not restart an agent pod. The later installer must prove current RBAC and
admission allow only the intended writers; no new permission is decided here.

Rolling between recovery-aware versions may keep records only when both versions
support this contract. Rollback to a pre-recovery operator is blocked while any
active operation or qualified pod remains. First stop/rescue/remove qualified
pods under the aware operator, retain HOME, checkpoint completed records, and
remove the name setting. The owner must separately authorize retiring completed
status bindings and downgrading; no ordinary reconcile deletes them. Reinstalling
an old schema or binary during recovery is not a recovery method.

## 8. Restart boundaries and envtest preparation

This is the required state/restart table and the matching envtest plan. Each
named test is **future work**, not a test run by this docs worker. Each test uses
real API storage, a fresh reconciler/APIReader at the stated boundary, fake exec
and clock, and a recording client. It asserts Session, pod and HOME UIDs, condition
text, exec count and status-write-before-delete order. Only one fresh executor
may be created. No table entry authorizes force removal or volume deletion.

| Row / boundary | Recorded state | Next reconcile and required assertion | Named envtest |
| --- | --- | --- | --- |
| J1 Operator restart before record ACK | Absent, pruned or unconfirmed write | No stop sent; reread storage. If stored, treat as StopPending; if absent, retry eligibility. Resume before any stored record retains untouched pod. | `TestCodexStopRestartBeforeAcknowledgementSendsNoStop` |
| J2 Operator restart after ACK before exec | StopPending, original pod/HOME | Wait remaining retry interval; stop/rescue same target even if original request never arrived. No in-place resume. | `TestCodexStopRestartBeforeDispatchKeepsCommittedIntent` |
| J3 Operator restart during exec or after lost reply | StopPending; no confirmed completed report | Retry same operation/target after interval. A partial/late/missing receipt cannot authorize deletion. | `TestCodexStopRestartAfterLostReplyRetriesSameOperation` |
| J4 Operator restart after report before status ACK | StopPending or atomically stored report/decision | No delete from process memory. Reread and either retry report or use exactly the stored RemovalCommitted evidence. | `TestCodexStopRestartBeforeRescueAcknowledgementDoesNotDelete` |
| J5 Operator restart after removal decision, before Delete | RemovalCommitted, receipt and completed rescue | Revalidate bindings and normally delete exactly the committed UID. A later desired-generation change cannot cancel this decision. | `TestCodexStopRestartAfterRemovalCommitFinishesExactPod` |
| J6 Operator restart during termination | RemovalCommitted, pod deletionTimestamp | Wait for actual absence. No replacement, even on repeated resume; no grace override. | `TestCodexStopRestartWhilePodTerminatesKeepsHomeAndWaits` |
| J7 Operator restart after absence before Complete ACK | RemovalCommitted, matching receipt/rescue, original HOME | Confirm absence uncached and Complete durably; no ensure in the same reconcile. Pruning/conflict sends no creation. | `TestCodexStopRestartBeforeCompleteAcknowledgementStartsNothing` |
| J8 Operator restart after Complete | Complete, retained original HOME binding | If Running, ensure only that HOME; if Suspended, keep normal retention rules. Same-name replacement/missing PVC refuses creation. | `TestCodexStopRestartAfterCompleteUsesOriginalHomeOnly` |
| J9 Agentd restart in the same pod | StopPending or RemovalCommitted plus HOME fence | Do not infer service from heartbeat or restart count. Fence rejects supported launch; retry/finish same target. Envtest proves controller behavior, agent tests prove fence refusal. | `TestCodexStopAgentdRestartDoesNotRestoreReadiness` |
| J10 Pod gone after authorized removal | RemovalCommitted, matching completed stop/rescue | Absence completes the operation; exactly one fresh pod may follow on original HOME. | `TestCodexStopPodGoneWithCompletedRescueCompletes` |
| J11 Pod gone unexpectedly, or same name/different UID | StopPending or inconsistent RemovalCommitted | RecoveryRequired. No hold/executor, no adopting the different UID, no clear from NotFound. | `TestCodexStopPodGoneWithoutProofRequiresRecovery` |
| J12 Node gone or NotReady; pod still present | Any active state, no writer-exit assertion | Retain pod/HOME and pending reason; retry only reachable same target. Time and Node deletion are not stop success. | `TestCodexStopNodeGoneNeverBecomesExitProof` |
| J13 Node gone and pod gone | Active record without completed removal | RecoveryRequired; only correctly bound custodian ACK permits same-HOME recovery hold. | `TestCodexStopNodeAndPodGoneNeedCustodianAcknowledgement` |
| J14 Second suspend of the same target | StopPending or RemovalCommitted | Same operationID, no second record. Before commit generation change discards stale report; after commit finish the decision. | `TestCodexStopSecondSuspendKeepsOperationAndChecksGeneration` |
| J15 Resume after refusal | StopPending | Pending, PodReady=False, RemovalBlocked retained. Retry stop; later completed rescue/removal permits a fresh UID on same HOME. | `TestCodexStopResumeAfterRefusalQueuesFreshPod` |
| J16 Resume races report or committed delete | StopPending then RemovalCommitted | Stale-generation report before commit is discarded; resume after commit waits through delete and Complete. Never relabel old report generation. | `TestCodexStopResumeRaceHonorsCommitBoundary` |
| J17 Restart during manual recovery | RecoveryRequired/RecoveryHold, bound ACK/hold | Recover only original HOME after verified ACK; discover the one owned hold after lost Create ACK; receipt plus successful hold rescue precede hold deletion and Complete. | `TestCodexStopManualRecoveryRestartClearsOnlyAfterHoldRescue` |
| J18 Recovery hold disappears or is resumed mid-rescue | RecoveryHold or its RemovalCommitted decision | Resume cannot bypass rescue. Unexpected absence needs a new ACK for that hold; committed removal can finish normally. No repeated adoption or stale ACK. | `TestCodexStopRecoveryHoldLossNeedsFreshEvidence` |
| J19 Second suspend after completed replacement | Complete, fresh owned executor on original HOME | New operationID and target UID on same HOME. No inheritance of the old receipt or removal decision. | `TestCodexStopSecondLifecycleCannotReusePriorReceipt` |

Keep the old M4 characterization for **unnamed/unsupported** fixtures and add
J15 for the qualified fixture; do not rewrite the old-host expectation globally.
The pinned test already exposes the original UID/status/no-retry behavior.
([codex_remote_stop_resume_test.go:48–80][m4-test])

Additional build checks: capability absent/unsupported/unknown; disabled names;
matching/absent explicit pod decision; old c1 compatibility; status conflicts,
lost ACK and CRD pruning; malformed/oversized/unknown version records; receipt
identity and operation replay; changed ownership/PVC UID; failed Git rescue
versus stop error; normal and empty holds; heartbeat cannot set/erase record;
all other status writers preserve it; old typed writer drops the field in a
mixed-version fixture and activation is refused. Inventory all production
wantsPodGone callers and show their answers unchanged. Preserve existing guard
and pod goldens. Mutate each new guard and name its failing test in the build PR.
These tests do not prove Linux signal delivery, phone rendering or enrollment.

## 9. Answers to recheck 1 part D

| Objection | Ruling | Where and why |
| --- | --- | --- |
| D1 Old images become unsuspendable | **Accepted.** | Section 2.1 checks non-mutating capability before any stop. Confirmed old images keep today's route and need no receipt; unknown transport is retryable. Never replace a running host for version detection. |
| D2 All hosts and undelivered exec | **Accepted.** | Section 2 requires current operator name plus pod construction decision and capability. Section 3 states replacement cost after confirmed intent, and no cost before it, including cache-behind requeues. |
| D3 Manual recovery cannot clear | **Accepted.** | Section 5 names Tom/authorized custodian, exact exit evidence, audited bound ACK, original-HOME hold rescue and controller completion. Section 3.1 distinguishes clearing active state from retaining bounded evidence. |
| D4 Holds missing | **Accepted.** | Sections 4/5 and J17/J18 cover normal/empty/recovery holds, freeze, generation races, lost holds and resume's usual shortcut. |
| D5 wantsPodGone changes 37 callers | **Accepted.** | Section 4 leaves the function unchanged, supplies a pod-aware predicate and an independent pending lane, and names the guard/deletion/construction integration points. |
| D6 Anchors inaccurate | **Accepted.** | All source citations use the freshly fetched pin and actual numbered source. Private-history eligibility cites executable lines 45–46 as well as its comment; no uninspected haynes-ops line number is asserted. |
| D7 Marker timing | **Accepted.** | Section 1 says HOME fence precedes quiesce, with agent-stop lines 213–217. Section 3 needs controller memory before exec because it cannot rely on receiving any agent report. |
| D8 Receipt trust and old decode | **Accepted.** | Section 3.2 calls it an agent-reported result, not kernel attestation, and cites the lenient decoder. Section 7 prohibits mixed-version activation and explains unknown-key loss. |
| D9 RBAC/admission unverified | **Accepted.** | Section 7 makes permission/admission proof an installer gate. This worker has inspected neither live policy nor the haynes-ops copy and claims no verified absence of permission changes. |

## 10. Later disposable-host proof — written, NOT RUN

This procedure is for a separate, explicitly authorized proof story. It touches
only an ordinary disposable Codex host named in that story, never the coordinator
or Studio. The story must authorize suspend/resume, the one interrupted stop,
read-only exec observation and the named custodian. It must record disk reserve,
CPU limit, installed pins/schema, all replica versions, current RBAC/admission,
HOME UID, rescue destination and cleanup. A denial stops its action. No host
creation, flag or manifest is authorized here.

Run from the authorized custodian's shell. Set these values from the proof story,
not by searching for a convenient host. Commands in this section are instructions
for later, not commands executed by this design worker. The two new ctl verbs
are proposed build outputs from sections 2.1 and 3.2.

```sh
PROOF_HOST='ordinary-disposable-host-from-proof-story'
PROOF_NS='dev-agents'
PROOF_DIR="$XDG_RUNTIME_DIR/op877-proof"
mkdir -m 700 "$PROOF_DIR"
agent-run help suspend
agent-run help resume
agent-run show "$PROOF_HOST" -o json > "$PROOF_DIR/before.json"
kubectl -n "$PROOF_NS" get agentsession "$PROOF_HOST" -o json > "$PROOF_DIR/session-before.json"
kubectl -n "$PROOF_NS" get pod "$PROOF_HOST" -o json > "$PROOF_DIR/pod-before.json"
kubectl -n "$PROOF_NS" get pvc "home-$PROOF_HOST" -o json > "$PROOF_DIR/home-before.json"
kubectl -n "$PROOF_NS" exec "$PROOF_HOST" -c agent -- agentd ctl codex-stop-capabilities > "$PROOF_DIR/capability.json"
```

Require version 1, matching UIDs, enabled decision and release-qualified imageID.
Require the Session has no prior active stop, ordinary account and a dedicated
PID namespace. Record only object identities, image digests, versions and bounded
conditions in public evidence. These JSON files stay private; never publish their
raw content. Confirm native history on the paired phone privately if the proof
story includes pairing; do not request/copy a login as a shortcut.

### 10.1 Deterministic interrupted stop on the disposable host

Use a bounded sleeping writer to hold the normal 30-second grace open. It does
no CPU work and creates no native conversation. The future build supplies
`scripts/proofs/op877-interrupt.py`, a reviewed **proof-only** helper. This is a
build deliverable, not a file that exists at this pin. Its exact contract: one
`--writer` invocation installs its TERM-ignore handler, prints and flushes only
`writer-ready`, then sleeps at most 120 seconds; one
`--interrupt` invocation reads the source-bound HOME fence, verifies the exact
boot supervisor is frozen, finds exactly one `agentd ctl rescue --stop-agent
--codex-stop-operation` with the stored operationID, opens a pidfd, rechecks its
identity, then sends KILL only to that ctl process. It never thaws anything,
modifies a fence, kills tini/supervisor, invokes a provider or writes HOME. If
any identity/check is ambiguous it exits 2 without signalling. It prints only
bounded JSON with result `interrupted` and the operation/pod UID on success. Its own interruption is an
inconclusive attempt, never proof. No general PID-selection argument is allowed.
Install/copy this reviewed helper privately only within the later proof authority.

The operator's retry is one minute; a proof runner must capture the pending
state before the retry. The helper must enforce a 20-second total interrupt
budget with modest sleeping reads; it cannot busy-loop. Start it only after the
writer is in grace, so it does not get frozen in the first process inventory.
A later build must measure this timing against the qualified binary. If this
window cannot be observed safely, stop and revise the proof; do not shorten
production grace or add a production fault-injection flag.

```sh
kubectl -n "$PROOF_NS" exec -i "$PROOF_HOST" -c agent -- python3 - --writer < scripts/proofs/op877-interrupt.py > "$PROOF_DIR/writer.txt" 2>&1 &
PROOF_WRITER_CLIENT=$!
sleep 1
test "$(cat "$PROOF_DIR/writer.txt")" = writer-ready
agent-run suspend "$PROOF_HOST" -o json > "$PROOF_DIR/suspend.json"
# One deliberate observation delay, within the helper's bounded proof window.
sleep 3
agent-run show "$PROOF_HOST" -o json > "$PROOF_DIR/stopping.json"
kubectl -n "$PROOF_NS" exec -i "$PROOF_HOST" -c agent -- python3 - --interrupt < scripts/proofs/op877-interrupt.py > "$PROOF_DIR/interrupted.json"
agent-run resume "$PROOF_HOST" --wait 1s -o json > "$PROOF_DIR/resume.json"
agent-run show "$PROOF_HOST" -o json > "$PROOF_DIR/pending.json"
```

Pass this stage only if the helper proves the source-bound fence and frozen
supervisor before the interruption, and API observation shows the same source
pod/HOME UID, active operation, PodReady=False and the section 6 pending message
after resume. A wait timeout is an observation limit, not recovery success.
No completed rescue may be invented from the killed ctl process. The writer
client may end by signal during the later normal retry; record its exit privately.
No repeated interruption is allowed by this recipe.

Let the controller's ordinary retry finish without another stop request. Observe
with a single bounded Kubernetes wait, then read the final objects:

```sh
kubectl -n "$PROOF_NS" wait agentsession/"$PROOF_HOST" --for=jsonpath='{.status.codexRemoteStop.phase}'=Complete --timeout=10m
kubectl -n "$PROOF_NS" wait agentsession/"$PROOF_HOST" --for=condition=PodReady --timeout=5m
agent-run show "$PROOF_HOST" -o json > "$PROOF_DIR/after.json"
kubectl -n "$PROOF_NS" get agentsession "$PROOF_HOST" -o json > "$PROOF_DIR/session-after.json"
kubectl -n "$PROOF_NS" get pod "$PROOF_HOST" -o json > "$PROOF_DIR/pod-after.json"
kubectl -n "$PROOF_NS" get pvc "home-$PROOF_HOST" -o json > "$PROOF_DIR/home-after.json"
```

Require exactly one new pod UID, same Session and HOME UID, a matching operation
receipt and completed rescue persisted before the old-pod delete, ordinary grace,
no overlap of writers, and no HOME/finalizer deletion. API audit plus bounded
operator events and helper observations establish ordering; final snapshots
alone do not. If audit cannot establish order, proof remains unconfirmed. Tom's
phone must show the pending words during the wait, then the retained computer
and history afterward. Do not claim a native thread automatically reattached.

Any missing receipt, false Running, original pod reuse, changed HOME, unexpected
same-name pod, missing rescue order, duplicate executor or timeout is a fail or
unconfirmed proof. Preserve evidence and checkpoint before authorized recovery.
Do not clear status or delete a pod to turn a failing test green.

### 10.2 Additional proof gates

On a separately named old-image disposable fixture, run the same initial reads,
capability probe, `agent-run suspend` and `agent-run show`. Require the exact
unsupported response, legacy suspend/rescue and no new operation. The proof
custodian checks its completed rescue before any normal cleanup; receipt absence
must not make this fixture unsuspendable. Do not run the interruption helper there.

Operator/agentd restart and node-loss permutations are first covered by section
8. Do not kill a node or the shared operator for this live proof. A real manual
recovery needs its own incident authority and measured node/container-exit
facts. Its exact status write is a resourceVersion-tested JSON patch of only
recoveryAck, prepared privately from the incident, then:

```sh
kubectl -n "$PROOF_NS" patch agentsession "$PROOF_HOST" --subresource=status --type=json --patch-file "$PROOF_DIR/recovery-ack.patch.json"
agent-run show "$PROOF_HOST" -o json > "$PROOF_DIR/recovery-observed.json"
```

The patch file must start with `test` operations for resourceVersion and every
bound identity, followed by one `add` of the appropriate acknowledgement. It may contain no phase,
receipt, rescue or deletion edit. Pass only after the original-HOME hold has its
successful rescue, is removed normally, and Complete is observed. Forged/stale
identity cases belong in envtest, never experiments against a real lost writer.

Qualify the real-kernel points inherited from c1 with reviewed bounded fixtures:
whole-thread-group pidfd readiness, multi-thread SIGSTOP, grace-born descendants,
tini reaping, same-pod restart fence persistence including runtime-dir loss, and
pod-delete time. A D-state writer that cannot be made safely on a disposable host
stays an explicit unproved limit; never manufacture storage failure on a shared
node. Raw exec after final inventory remains outside the supported-writer fence,
as the source's process scan and returned stop report alone cannot prevent it.
([codex_remote_processes.go:255–289,568–592][final-inventory])

End only with the proof story's separately authorized supported suspend and
cleanup after completed rescue, or a recorded pending-recovery handoff. No
force-delete command appears here. Installed proof is not Tom's acceptance of
OP858's coordinator host, phone thread or custody transfer.

## 11. Build slices, sizes and stop gates

Execute these as serial ready stories under OP877. Sizes are source-work
estimates, not permission or a promise about provider behavior. Give each a
seven-field tracker record, budget and exact-head cross-family review. Split a
slice that exceeds one session. All local Go commands use GOMAXPROCS=2,
`nice -n 19` and `-p 2`; one suite at a time, no timing stress loops.

| Slice | Size / estimate | Deliverable and acceptance boundary |
| --- | --- | --- |
| A Protocol and schema | M, 4–6 hours; split API and agent work if needed | Capability command, operation-bound receipt/request, bounded API types, deepcopy/CRD, validation and protocol tests. No activation. Prove old command response fixtures and named-only gates. |
| B Reconcile and guards | L, 6–10 hours, split before pickup | Durable operation writes, acknowledgement, retry, exact target removal branch, generation boundary, ensure/archive/finalizer fences, Complete HOME binding, ordinary/empty hold behavior. Implement J1–J16/J19 and old-host regression checks. Remains inactive until all pieces/review are ready. |
| C Manual recovery | M, 4–6 hours | Custodian ACK validation/audit contract, same-HOME recovery holds, lost-hold binding and J17/J18. Q-068-01 gates first use, not the independent source checks. No new credential or permission. |
| D Phone wording and proof preparation | S/M, 2–4 hours | Plain pending/rescue messages through show/API/phone detail, reviewed proof-only helper, private evidence checks and exact procedure validated against build output. No live run. |
| E Cross-family review and source delivery | M, 2–4 hours | Review exact integrated head, mutate new guards, required CI green, layout check, schema copy PR prepared before operator pin. Coordinator arranges model family. Source merge alone closes no installed gate. |
| F Authorized install and disposable proof | M, 2–4 hours plus explicit uncertainty | Separate install story and owner decision; schema first, every replica compatible, empty-list upgrade, named disposable fixture only, section 10 proof. No production coordinator until OP877's full acceptance is closed. |

This docs worker runs only `make check-layout` and document/diff checks locally.
It opens a ready PR and does not merge. Required CI must pass on its exact head.
The coordinator arranges another-model-family review before any merge. Nothing
here labels source work as installed or accepted.

## 12. Owner-only open questions

These questions are recorded for the coordinator; this worker does not ask Tom.
No answer is assumed, and no pending question authorizes an installation.

- **Q-068-01 — Who may acknowledge a lost writer?** Recommendation: Tom or one
  custodian he names for that incident, using existing authorized status access.
  Require the exact writer-exit evidence and successful same-HOME hold rescue in
  section 5. Hold the first manual acknowledgement until that custodian and
  evidence are explicitly approved. A name in a note is not that authority.
- **Q-068-02 — Which disposable host and disruption may prove this?**
  Recommendation: a newly authorized ordinary host with no real work, qualified
  agent image, bounded sleeping writer and one interrupted ctl stop. Approve the
  exact host, pins, retained HOME and cleanup in a separate proof story. Do not
  use the future coordinator as its own first recovery test.

The conservative replacement cost and named-only compatibility rules are proposed
engineering decisions for review in ADR-031. They do not request broader access.
OP858's existing owner questions and its host installation/custody gates remain
with that story.

[agent-stop]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/agentd/codex_remote_stop.go#L172-L239
[freeze]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/agentd/codex_remote_processes.go#L387-L425
[agent-rescue]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/agentd/rescue.go#L49-L66
[reconcile]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L584-L888
[ensure]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L1395-L1434
[conditions]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L1767-L1801
[phase]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L1898-L2009
[m4-test]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/codex_remote_stop_resume_test.go#L18-L80
[names]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/codexworkbench/remote_stop.go#L13-L48
[decision]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/codex_remote_stop.go#L14-L39
[agent-decision]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/agentd/codex_remote_stop.go#L27-L46
[private-history]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/private_history.go#L34-L46
[old-ctl]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/cmd/agentd/main.go#L849-L947
[cache]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L845-L1058
[status-update]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L805-L815
[ack]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/private_history.go#L63-L92
[heartbeat]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/apiserver/heartbeat.go#L155-L175
[status-type]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/api/v1alpha1/agentsession_types.go#L915-L947
[stop-type]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/agentd/protocol/rescue.go#L131-L143
[decode]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/rescue.go#L207-L223
[final-inventory]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/agentd/codex_remote_processes.go#L255-L592
[record]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L1018-L1105
[verdict]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/rescue.go#L261-L386
[rescue-predicates]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/rescue.go#L520-L536
[pod-guard]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/guard.go#L75-L365
[volume-guard]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/guard.go#L41-L345
[retry]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L95-L96
[hold]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/pod.go#L303-L315
[hold-supervisor]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/agentd/codex_remote_processes.go#L173-L227
