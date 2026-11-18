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
  and install gates. Fix worker `dev-env-1118-180158` owns this docs branch. The reviewed
  head `00317202a9e2ca5f6270ab690c7d72258da762d2` remains an ancestor;
  main `4f3ef3d9bf02ec9b9f4172b06345751d15389be2` was merged without rebase.
  The cited lifecycle files are unchanged between that main and the source pin.
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
and removal. A positively verified ended pod instead follows section 5: remove
the ended pod, rescue in a hold on the original HOME, then complete recovery.
A resume cannot return the original pod to service. A completed operation permits
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

Proposed naming is a **pod construction decision**, read from the owned pod's
explicit agent-container `AGENTD_CODEX_REMOTE_STOP_SESSION_UID` entry equal to
this Session UID. For an existing pod, never consult the current operator flag
to decide whether it is named. Adding/removing a name affects only later builds.
The existing pod-spec entry is the operator-owned construction record; no image
release declaration is substituted for a probe of the actual binary.

A task, local session, shared workspace or private-history lifecycle is outside
this operation. A missing/empty entry or a different nonempty Session UID is not
a named pod: send no bound request and preserve today's route. A matching capable
status binding with contradictory pod identity/env is instead a conflict: retain
the block, never fall back or stop the different target. An envFrom value is not
an operator-owned explicit construction decision; the existing raw pod/config
writer boundary remains, rather than becoming a new containment claim.
([controller/codex_remote_stop.go:14–39][decision],
[agentd/codex_remote_stop.go:27–46][agent-decision])

For a named pod, verify the actual binary with section 2.1's version probe. Before
**any** stop request after a successful qualification, persist and confirm
`status.codexRemoteStopCapability`, bound to the exact pod and original HOME
(section 3.1). A recovery hold admitted by an active record has no legacy
fallback while its own version confirmation waits. Once capable, the pod stays
on this route for its lifetime,
including before its first stop and after flag removal. The operation then
requires uncached Session UID/generation, owned pod UID, original owned HOME
claim name/UID and node name/UID. Never acquire the original claim UID from a
later same-name replacement. An active operation is authoritative even if a
later capability probe fails or the current capability binding names its
recovery hold.

The private-history path stays separate; remote sessions are not newly latched
by that feature today.
([private_history.go:34–46][private-history])

### 2.1 Version before stop, never a destructive probe

Use the existing `agentd version`, not a new capability command or an exact
unknown-command diagnostic. At the pin, `version`/`--version` returns build
identity without rescue. Its common formatter reports a release or `dev`.
The code at this pin proves this command/format; fixtures for the supported old
releases must establish compatibility across those releases in the later build.
([cmd/agentd/main.go:217–223][version-command],
[version.go:34–71][version-format])

The proposed operator has **one** `minimumCodexStopRecoveryVersion` constant,
set by the build to the first released agentd version that implements this
complete contract (the actual release number is not assigned by this docs PR).
Parse the command's bounded one-line output with a version parser; accept only
a recognised released semver at/above that constant. `dev`, malformed output,
prerelease/unknown formats and exec errors are unrecognised, never invented
versions. Keep fixtures below, exactly at and above the minimum. This is a
binary's assertion of compiled support, not independent kernel attestation.

Exec through the existing route with a 15-second bound, 4 KiB stdout cap and no
logged raw output. Read pod UID, explicit decision, container imageID and restart
count uncached before and after; confirm the same target/image during the probe.
A changed target discards the result and is reread before any request. A template
revision, heartbeat, current template image or missing receipt proves no support.

There are two cases, distinguished **before** dispatch:

- **No capable binding or active recovery operation admits this pod:** a
  recognised below-minimum version,
  failed probe or unrecognised answer attempts exactly today's rescue/stop command,
  after fresh same-target reads. Do not require a receipt, create an operation or
  block indefinitely on probe classification. An old binary runs its current stop;
  that includes c1's known M4 limit, not a repair of old code. A new recovery binary
  receiving this unbound command must refuse before any lock, fence, freeze or
  Git operation, returning no report. The suspend stays pending with that clean
  refusal; nothing is frozen without a record. Retry can later qualify it.
- **A capable binding already exists:** never downgrade. Confirm/reuse the
  operation before any stop-bearing dispatch. A failed version probe or request
  is the conservative case “the request may not have reached the agent”. Retain
  StopPending and the replacement cost in section 3; use the confirmed binding
  for original node identity if that Node is now unreadable. Resume cannot
  reuse the possibly stopped pod. Retry at the normal cadence. A correctly bound request,
  not today's unbound command, is the only possible stop for that pod.

No upgrade restarts a host to learn its version. Unnamed old pods need no probe.
Installation must not rely on an unqualified c1 pod: suspend by its existing
route and later resume onto a qualified image only when separately authorized.
This design cannot eliminate M4 in an old binary's legacy implementation.

**Coordinator clarification, 2026-11-18:** in reply to `op877f1-q1`, verified
parent session `dev-env-fable-coordinator-1109` confirmed this reading of R1:
naming is recorded at build in pod spec; actual capability is first verified and
persisted after the binary answers, before any stop. No build-time image-version
declaration is required. This is the coordinator's design ruling, not Tom's
approval. J21/J28/J29/J33/J34 cover the four clarified boundaries.

## 3. Cost of an undelivered stop and the durable contract

Check cache currency **before** creating an operation. Today cacheBehind causes
a requeue before rescue; the existing writer uses whole-status Update, and a
separate private-history helper already compares intended status with both the
write response and an uncached read.
([reconciler.go:845–851,1047–1058][cache],
[reconciler.go:805–815][status-update], [private_history.go:63–98][ack])

Proposed order for a newly qualified target: uncached eligibility reads;
optimistic status write; compare the intended record with the write response; uncached confirmation of the record and
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

Add optional operator-owned `status.codexRemoteStopCapability`, one bounded
current-pod binding, independent of whether a stop has begun:

| Member | Type and validation |
| --- | --- |
| `version` | Required integer enum 1 (recovery protocol). |
| `sessionUID`, `podUID`, `homeClaimUID` | Required strings, min 1, max 128; exact owning Session, current pod and original owned HOME. |
| `homeClaimName` | Required DNS subdomain string, min 1, max 253. |
| `nodeName` | Required DNS subdomain string, min 1, max 253; node at qualification. |
| `nodeUID` | Required string, min 1, max 128; exact node identity, not exit evidence. |
| `agentVersion` | Required recognised release string, min 1, max 64, at/above the one minimum constant. |
| `imageID` | Required string, min 1, max 512; observed container image identity. |
| `containerRestartCount` | Required integer, minimum 0; observation when verified, never permission to forget capability after restart. |
| `verifiedAt` | Required date-time string, max 64; observation time, not exit evidence. |

Confirm this intended binding in both the write response and an uncached read,
with the same pod/HOME checks, before a stop. Once confirmed it is immutable for
that pod UID; a later restart/probe failure cannot erase/downgrade it. A matching
active stop independently preserves capability even if this current-pod slot
later binds a recovery hold. Replace the slot only for a newly owned current
pod, after exact previous-pod absence and completion of any active stop, or for
the one recovery hold explicitly admitted by the active record. Ordinary-hold
resume with no stop retains its D-55 removal exception. Keep the old slot until
replacement; never delete it to reclassify a still-existing pod. An invalid or
unknown-version capable binding blocks with a reason, not legacy fallback.

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
| `terminated` | optional object, exact shape below | Controller-confirmed kubelet termination evidence for the original pod; not a rescue. |
| `recoveryHoldTerminated` | optional object, same shape | Separate termination evidence for the current recovery hold. |
| `recoveryAck` | optional object, exact shape below | Authorized human assertion of the old writer's exit when machine evidence is unavailable. |
| `recoveryHoldRescueAttempted` | optional boolean; required for RecoveryHold, explicitly initialized false before hold Create | Durable writer-admission bit; set true before any hold rescue dispatch. Absence is unknown. |
| `recoveryHoldUID` | optional string, min 1, max 128 | Distinct current rescue hold; cannot equal original pod UID. |
| `recoveryHoldNodeName` | optional DNS subdomain string, min 1, max 253 | Current recovery hold's node name. |
| `recoveryHoldNodeUID` | optional string, min 1, max 128 | Current recovery hold's node UID. |
| `recoveryHoldAck` | optional object, same members and bounds as recoveryAck | Separate assertion for an unexpectedly lost recovery hold. |

`terminated` has required `version` (integer enum 1), `podUID`, `nodeUID`,
`podResourceVersion`, `nodeResourceVersion` (strings, min 1, max 128),
`observedAt` (date-time string, max 64), `nodeReady` and
`allContainersTerminated` (booleans, only true). Pod/node UIDs equal the original
binding. `recoveryHoldTerminated` uses the current recovery-hold binding instead.
No raw container list is retained. Section 5 defines the uncached checks that
must precede this controller assertion; the timestamp never proves exit.
Keep confirmed source termination evidence through Complete. Replace a current
hold's termination evidence only when section 5 permits replacing that hold.

`recoveryAck` has required `operationID`, `sessionUID`, `podUID`,
`homeClaimUID`, `nodeUID` (same bounds and equality to the containing record),
`at` (date-time), `actor` (string, 1..253), `evidenceRef` (string, 1..512),
and `writerExit` (boolean, only `true`). The reference names a private incident
record, never credential material, a transcript or a phone session link. An
actor string grants no authority. Kubernetes authorization and the incident's
recorded authority govern the write; the audit record identifies its caller.

For `recoveryHoldAck`, the pod/node UIDs equal recoveryHoldUID and
recoveryHoldNodeUID; operation/Session/HOME IDs still equal the record. Require
both recovery-hold node members before recoveryHoldRescueAttempted becomes true;
a newly created hold may not yet be scheduled. Retain the
original recoveryAck after a lost hold; it is not overwritten by recoveryHoldAck.

The controller owns the capability binding and all operation members except `recoveryAck` and `recoveryHoldAck`, which
the incident actor writes only under section 5 after the owner
and access decisions described there. No new custodian role is assumed. Heartbeat
input cannot write any of them. Preserve them through status observation and every status writer.
At the pin heartbeat uses a merge patch; this does not make other whole-status
writers safe with an unknown field.
([heartbeat.go:155–175][heartbeat], [agentsession_types.go:915–947][status-type])

Add the same two removal latches used by comparable durable records today:
([agentsession_types.go:807–814][removal-latches],
[agentsession_types.go:2210–2217][root-latches])

```text
# On AgentSessionStatus:
!has(oldSelf.codexRemoteStop) || has(self.codexRemoteStop)
!has(oldSelf.codexRemoteStopCapability) || has(self.codexRemoteStopCapability)
# On AgentSession (so deleting all status cannot erase the record):
!has(oldSelf.status) || !has(oldSelf.status.codexRemoteStop) || (has(self.status) && has(self.status.codexRemoteStop))
!has(oldSelf.status) || !has(oldSelf.status.codexRemoteStopCapability) || (has(self.status) && has(self.status.codexRemoteStopCapability))
```

These are proposed transition rules. The operation messages are “Codex stop
evidence cannot be removed” and “Codex stop evidence cannot be erased with
status”; the capability twins say “Codex stop capability” in place of “evidence”.
They also protect Complete. A remove/null patch and an older typed status Update
that omits the key are rejected by the API server, rather than silently losing
it. A status patch which leaves it untouched can still succeed (section 7).

Validation must bound every string used by CEL. Within an active operation,
source identity members, role and startedAt are immutable; lastAttemptAt cannot
move backwards. Only section 5's confirmed lost/ended-hold recovery may change the
current recovery-hold identity. Receipt operation/Session IDs equal the record. Its pod UID is either
the original pod or the bound recovery hold. Rescue and receipt pod UIDs agree.
RemovalCommitted requires receipt, rescue and removalCommittedAt in one write;
StopPending and RecoveryHold contain no receipt/rescue reference authorizing
removal. RecoveryHold requires either matching source termination evidence or
recoveryAck. A recovery hold additionally requires a successful rescue before
RemovalCommitted, whatever the requested mode. An original ordinary hold keeps
the D-55 distinctions in section 5. Complete after the ended/lost-source route
requires the recovery hold's successful rescue and confirmed removal. Complete
requires the completed removal evidence. A new operation may replace a Complete
record only with the same Session and HOME
binding, a fresh operationID and a different currently owned pod UID. An invalid
or unknown-version record fences construction and deletion; it never means absent.
Every date-time string has MaxLength 64; role, phase and result enums also have
MaxLength (8, 16 and 14 respectively). All other strings, including nested
receipt, rescue, acknowledgement and termination evidence, have the bounds in
this section. There are no maps or lists; each nested object has only the listed
members. Test the root and status rules against the generated CRD in envtest,
including its API-server CEL cost budget. Tests must show legal transitions and
rejected contradictions. Do not use CEL alone to infer process exit.

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
exactly as the status receipt above. The qualified stop request is:

```text
agentd ctl rescue --stop-agent --codex-stop-operation OPERATION_UUID --expected-session-uid SESSION_UID --expected-pod-uid POD_UID
```

All three identity arguments come from the confirmed operation, not a new pod
read after a failed exec. OPERATION_UUID is operationID and SESSION_UID is
sessionUID. For the original executor or original hold, POD_UID is podUID. For
rescue in a confirmed recovery hold, POD_UID is recoveryHoldUID; its own agent
must match that target. Never send the original podUID to a recovery hold or
replace the immutable source podUID with the hold's UID.

Before acquiring a lifecycle lock, writing a fence or
signalling, the new agent validates their bounded syntax and compares both
expected UIDs with its actual Session document and Settings.PodUID. A mismatch
returns no report and performs no stop. This prevents a name-based exec that
lands in a replacement pod from freezing it before the controller notices the
UID change. A controller post-exec UID check alone would be too late. Keep that
existing check as well, and keep the legacy RescueCommand unchanged. The
qualified rescuer must receive the confirmed request binding explicitly.

A recovery-capable agent with its construction decision enabled refuses **any
stop-bearing rescue without all three valid binding arguments** before acquiring
a lock, publishing any fence, freezing/signalling or running Git. Return a fixed
bounded refusal and no report. This includes the legacy RescueCommand received
after a failed first version probe. It cannot create an unrecorded frozen pod.
An absent/different decision retains the legacy agent path and rejects a bound
request as ineligible before mutation.

The new agent accepts a bound request only for an enabled, identity-matched
remote stop. Emit the receipt only after c1's final inventory and fence recheck, then return
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
For a report eligible for removal, persist `status.rescue`, the validated receipt,
its reference, `phase: RemovalCommitted` and removalCommittedAt in **one**
optimistic status transaction, then compare the response and uncached read with
the intended whole decision. There is no durable StopPending-with-receipt middle
state. Before this write there is no removal decision; after it the whole
RemovalCommitted decision exists. If a hold's completed report is ineligible
because a successful rescue is required, store only `status.rescue` for its
failure/retry reason, leaving the operation pending with no receipt/reference
that could authorize deletion. A report for an older generation after a concurrent resume
is discarded and retried. No transport error, nil report, missing receipt,
contradictory report or failed status write authorizes delete.
([reconciler.go:1024–1044,1084–1105][record],
[controller/rescue.go:261–289,375–386][verdict])

A completed report with a **failed Git rescue** is distinct from stop refusal.
Preserve D-10 for an executor: that pod may go after its completed stop/rescue,
while HOME stays and RescueFailed blocks archive. Today an ordinary hold goes
on resume even without a successful rescue; while suspension still needs the
hold, a failed rescue retains it. Preserve that distinction after a recorded
ordinary-hold stop finishes. Only a recovery hold has an unconditional
successful-rescue gate, including on resume. The wait for a recorded stop is a
**new** block, not a retained D-55 resume requirement. The new receipt is
additionally required for a qualifying recorded stop. Do not silently require every Git rescue to be Verified in
the normal removal path. The stricter manual recovery path in section 5 requires
a successful hold rescue because it repairs missing original evidence.
([controller/rescue.go:520–536][rescue-predicates],
[guard.go:137–150,289–300][pod-guard])

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
lane, including when spec says Running. Also guard ensure, **hold creation**,
pod deletion, volume deletion and finalizer-release boundaries so another caller
cannot bypass it. Both startHoldPod callers (archive and transient retry) must
refuse creation until section 5 has exit evidence and original-pod absence.
([reconciler.go:988–1014][hold-create],
[reconciler.go:1227–1230][archive-hold],
[rescue_retry.go:193–214][retry-hold-create])
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
caller's `wantsPodGone` answer changes. Other podRemovalAllowed entry points are
revision drain (remote mode is excluded), shared-workspace removal (only shared
sessions), and terminal-task reclaim through deletePod (task/private-history
eligibility). The new branch requires this remote operation; it grants none of
those callers an exception. The later build inventories the full call surface.
([drain.go:62–72][drain-callers],
[workspace_lifecycle.go:316–322][shared-caller],
[terminal_task_reclaim.go:194–199][task-caller])

Confirm the single atomic rescue/RemovalCommitted write from section 3.2 with
the exact report reference and receipt, while that report's generation is
current. That is the durable removal decision. `podRemovalAllowed`
gets an explicit narrow branch for this target before the generic Running/hold
shortcuts. It validates the operation and its recorded decision. No generic
podEnded/podNeverStarted shortcut bypasses the operation. There is one separate
ended-pod branch: section 5's positively verified termination, durably recorded
before ordinary deletion, permits removing the ended original pod so a recovery
hold can rescue its HOME. That is not RemovalCommitted or Complete, and no
rescue is invented for the ended pod. `deletePod` remains the sole ordinary
deletion primitive with pod UID/resourceVersion preconditions and normal grace. It also rereads Session and HOME at this
boundary.
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

A retry may meet an earlier ctl still executing after its reply was lost. Today
rescue has a ten-minute timeout but execution retries are one minute, and the
exclusive lifecycle lock refuses an overlapping stop as busy. Treat busy as a
safe retryable refusal with no report or delete; never unlock or kill the
previous ctl to advance. J30 tests this overlap with fake time.
([rescue.go:63–65][rescue-timeout],
[codex_remote_stop.go:145–160][busy-lock])

## 5. Ended pods, missing pods, manual recovery and holds

A pod NotFound, missing Node object, NotReady node or elapsed time is not writer
exit. If the source vanishes before a confirmed RemovalCommitted, retain the
binding in RecoveryRequired. A different same-name pod is a conflict, never an
inheritor of the old receipt. The controller creates no executor or recovery hold
until original-writer exit is established. Existing finalizers protect retention;
the operation must also fence archive while this uncertainty remains.
([guard.go:41–52,312–345][volume-guard])

### 5.1 A positively verified ended pod

Today's guard permits removal of a Failed or Succeeded pod without rescuing
inside it; needsRescue returns false and a D-55 hold can later rescue its volume.
The phase-only podEnded helper is weaker than the evidence required here.
([guard.go:128–150,278–300,334–341][pod-guard],
[reconciler.go:988–1014][hold-create],
[reconciler.go:1227–1230][archive-hold])

Proposed: for a still-present original pod in Failed or Succeeded, read the exact
pod and bound Node uncached. Require the original Session/HOME ownership and
UIDs, the original node UID with Ready=True, and one current **terminated**
status with a nonzero FinishedAt for every admitted regular, init and ephemeral
container, matched by name with no missing, duplicate, running or waiting entry.
Reject ContainerStatusUnknown and missing status. Re-read the Node to confirm
the same Ready identity before recording `terminated` and RecoveryRequired in
one acknowledged status write. No elapsed interval, NotFound, phase alone or
request to shut down a node is termination evidence.

The existing allContainersTerminated helper supplies strict named-container
checks and rejects per-container restart overrides/rules. It does not itself
check the pod's restart policy or terminal phase; its callers supply their own
lifecycle proof. Ordinary remote pods use Always. Reuse its strict container
checks with terminal phase and the Ready-node condition in a new remote-ended
predicate; do not relax its existing callers. That combination is a proposed
proof, not something the helper alone establishes. The kubelet's report is
trusted within Kubernetes' normal status-writer boundary; RBAC alone is not a proof of its provenance.
([workspace_stop.go:123–202][terminated-check], [pod.go:583–595][pod-shape])

After confirming the termination record, revalidate the same live evidence at
the delete boundary and remove only that ended pod with ordinary grace and
UID/resourceVersion preconditions. A changed/unreadable status or non-Ready node
before delete keeps it present and explains the missing evidence. Persisted exit
evidence survives a lost Delete reply or later disappearance. After uncached
absence, enter RecoveryHold from that evidence without a human acknowledgement.
A successful rescue in a capable hold on the original HOME and its normal
removal are required before Complete. Thus the ended object can leave, but its
unrescued HOME cannot. This is the narrow ended-pod exception to the ordinary
stop-receipt-before-pod-delete rule; no live or uncertain writer gets it.

Apply the same positive-termination rule to an ended recovery hold, recording
recoveryHoldTerminated before removal. Its failed/incomplete rescue is not
success; only a replacement capable hold's successful rescue can complete the
operation. A missing admitted hold with no such evidence still needs its own ACK.

The Kubernetes **1.35** documentation confirms that failed pod objects remain
until a human/controller removes them, including PodGC; an ended object need not
disappear by itself. It also documents graceful shutdown terminating pods and
not restoring them if shutdown is cancelled. The node-shutdown page describes
the displayed status as Terminated; its exact API phase being **Failed** is
**Unknown from those docs** and is not assumed by this design. Both statements
were checked 2026-11-18, not tested in this task.
([Pod lifecycle, garbage collection][k8s-pod-gc],
[Node shutdown, graceful shutdown][k8s-shutdown])

For a node that stays NotReady, retain the present pod and explain why neither
phase nor absence proves exit. The documented out-of-service taint can trigger
forceful pod deletion and immediate detach; this design authorizes neither that
taint nor force deletion. J12 can reach J13 if an independently authorized
incident or Kubernetes removes the object; that event still needs bound human
exit evidence, not an inferred success. If the node never becomes Ready and the
object remains, the incident stays held. There is no automatic escape by time.
([Node shutdown, non-graceful shutdown][k8s-nongraceful])

### 5.2 Missing evidence and the human acknowledgement

Normal manual repair of a still-runnable pod keeps the original pod: Tom or his
expressly authorized custodian resolves the signal/node fault. The controller retries the same stop,
gets its completed rescue, removes it and marks Complete. No human status edit
is needed and a second suspend is not required.

If the original pod has already gone without that evidence, the custodian must:

1. Keep the original Session and HOME. Verify their exact UIDs. Record evidence
   that the original container cannot run again or write HOME: for example the
   node's confirmed power fencing plus storage detach, or verified container
   termination on that node. API absence or an action request is insufficient.
2. Have **Tom or his explicitly authorized disruptive-recovery custodian**, with
   the required permission to update this status (section 7.1), write only
   recoveryAck using a resourceVersion precondition. It binds this operation, source pod, Session,
   HOME and node UIDs to the incident evidence. It does not set Complete or delete
   the field. An ordinary host thread, heartbeat or parent note is not authority to
   acknowledge its own stop; the later access review must prove enforcement. A cluster custodian who lacks status permission stops; this
   design grants neither credentials nor a new route.
3. The controller validates either the termination record of section 5.1 or
   the human acknowledgement, plus uncached absence, then enters RecoveryHold.
   Before Create it confirms recoveryHoldRescueAttempted=false. It creates a
   D-55 hold without agent credentials on the **same original HOME**, annotated
   with the
   operationID, then captures its UID and, once scheduled, node name/UID.
   **Build this recovery hold capable from the active record**, including its
   Session-UID decision and a recovery-qualified agent image. Never consult the
   current stop-name list to disable it. An unavailable qualified image waits
   with a reason; it never builds an old/incapable recovery hold. A lost
   Create ACK requires uncached discovery of the one owned, operation-bound
   hold; never adopt an executor or different HOME. It verifies/persists the
   hold's version-capability binding, then
   confirms recoveryHoldRescueAttempted=true before any qualified rescue exec.
   A failed hold rescue retains the hold and active record. A mismatching/stale acknowledgement retains the block.
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

Proposed: an ordinary hold with **no recorded stop** keeps today's D-55 resume
shortcut, even if its host was named at construction. Do not create a stop
operation solely to remove that untouched hold on resume. A normal hold rescue
that will send stop must first confirm its operation. A resume racing that
confirmed record treats the stop as possibly dispatched, even before a receipt:
it cannot prove nothing ran. Finish the recorded stop and completed report;
then ordinary D-55 permits removal on resume even if the Git rescue failed,
retaining HOME and RescueFailed. If still suspending, D-55 requires a successful
rescue before ordinary hold removal. **Only a recovery hold** built to finish an
existing recorded stop must successfully rescue regardless of desired mode.
This adds a wait for a recorded stop, not a success requirement for every resume.
Unnamed/unsupported ordinary holds retain today's behavior. A recovery hold is a distinct
target within the original operation; it never overwrites the original pod UID.
Once a **recovery** hold rescue may have been dispatched, its disappearance before a
committed rescue returns to RecoveryRequired, with a new recoveryHoldAck
required for that hold before another hold. Do not reuse the source acknowledgement to assert the missing hold stopped. The second
acknowledgement binds the lost recoveryHoldUID and recoveryHoldNodeUID, while
retaining the source recoveryAck. Only a matching fresh hold acknowledgement and
confirmed absence permit replacing that hold's binding. Section 5.1's matching
positive termination evidence is the machine-verified alternative to that ACK.
Persist and confirm the new hold binding, and clear the consumed recoveryHoldAck, before its rescue.
No active source/hold binding is rewritten merely because a pod name is free.
Status retains one source and one current hold, not an unbounded incident ledger.
A lost replacement hold needs its own new acknowledgement under the same rule.

There is one narrower pre-dispatch case. If the stored admission bit is explicitly
false, no hold rescue could have run: its only supported command, `agentd hold`,
writes nothing. An uncached missing hold after a lost Create ACK may therefore
be replaced without asserting that its process exited. No writer was admitted,
no executor starts, and the original source exit evidence still applies. An
absent bit is not false. Before the bit becomes true, confirm hold UID, node UID,
HOME and operation annotation durably. A restart after true but before exec has
the same conservative recovery cost as any uncertain dispatch. Bind a replacement
hold and reset the bit only after these rules permit it. This is no permission
to delete a present hold without rescue, except for section 5.1's independently
verified ended-hold path.
([cmd/agentd/main.go:331–347][hold-command])

### 5.3 Visible unresolved retention

RecoveryRequired protects the original HOME while writer/preservation evidence
is missing; it does not create a hidden indefinite retention class. Under
accepted [ADR-018](../adrs/018-two-tier-home-retention.md), unresolved
work reaches the visible owner decision batch at 168 hours using its applicable
recorded completion/terminal clock. If no such clock exists, show a named
missing-clock exception rather than inventing one from operation or PVC age.
Show this recovery record, missing evidence, retained HOME and next safe action
in that batch/exception. The batch is an owner decision, not permission to delete,
complete the stop or bypass rescue. Delivery of that visibility is a later build
requirement, not an installed collector claimed here.

## 6. What Tom sees on the phone

Proposed session detail and `agent-run show` wording, with no native conversation
or enrollment link added:

> Resume is waiting. This computer started stopping and cannot restart in the
> same running instance. We are finishing its stop and saving its work. Your
> saved files and history are kept.

On refusal append a bounded plain reason, for example:

> A process has not stopped. We will try again. Resuming again will keep waiting.

For lost evidence:

> Resume needs recovery. We cannot confirm the old computer stopped. Your saved
> files and history are kept. Tom must arrange confirmation that its processes
> are gone and its work is saved before a new computer starts.

While active, PodReady=False regardless of kubelet readiness. When desired mode
is Running and recovery blocks service, phase is Pending. When Suspended, retain
the Suspended phase and show the stop/recovery detail there; a hold still has the
HoldPod detail. Deletion keeps its requested intent with the pending work visible.
Do not claim one Pending phase for all requested modes.
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
At the pin, show prints condition messages, but resume's initial text can say
“is not suspended” or promise a new pod without considering this operation.
The later build must show the waiting message even for an already-Running
requested mode and must not print attach/success from stale kubelet readiness.
([sessions.go:386–389][show-conditions], [lifecycle.go:89–112][resume-text])

These words belong in the platform's phone-visible session detail and supported
command result. The native Codex app may show the stopped computer as offline;
this design does not invent an unsolicited push or claim control of its UI.
The phone proof must record where Tom actually saw the message.
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
before any real record. Section 7.1 records the live RBAC read performed for
this revision. Live admission, pins and the haynes-ops CRD copy were not inspected;
those remain installer checks, not installed evidence.

An older operator decodes neither this status member nor the new receipt. Its
typed whole-status Update omits an unknown member. With section 3.1's installed
status/root removal latches, that update is **rejected** once a record exists;
it cannot erase the record, including by replacing all of status. A merge patch
that does not touch the unknown field may still update unrelated fields. An old
replica can nevertheless ignore the operation in its Running/removal paths and
attempt unsafe actions without a status write first. The latch prevents data
loss, not unsafe old control flow. An older agent ignoring a key does
not make this safe. The known source points are whole-status Update, the Running
branch and the non-strict report decoder; mixed-version effects must be tested,
not merely assumed safe.
([reconciler.go:712–728,805–815][reconcile],
[controller/rescue.go:207–223][decode])

Inventory both relevant whole-status surfaces: the reconciler's writeStatus and
recordRescue, and the API's sessiondirectory.KubeStore.writeReceipt, which does
Status().Update on AgentSession. An older API-serving replica has the same
unknown-member omission and rejected-write risk, even when not leader. The
heartbeat path uses a merge patch and is tested separately; it must never accept
this record from heartbeat input. Audit all other status writers in the build.
([reconciler.go:808–815][status-update],
[reconciler.go:1024–1044][status-writers],
[sessiondirectory/store.go:267–275][directory-status],
[heartbeat.go:155–175][heartbeat])

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
status bindings through a separately reviewed schema migration and downgrading;
the removal latches reject ordinary status erasure, and reconcile never deletes them. Reinstalling
an old schema or binary during recovery is not a recovery method.

### 7.1 Live RBAC observation — 2026-11-18

This revision read only `kubectl get roles,rolebindings --all-namespaces -o json`
and `kubectl get clusterroles,clusterrolebindings -o json` (711 objects). Rules
and bindings grant the following identities update/patch on AgentSession status
in dev-agents, directly or through wildcards. These are live-object references,
not claims about source at the dev-env pin:

| Live binding / role | Bound subjects with matching status-write rules |
| --- | --- |
| RoleBinding `dev-agents/dev-env-operator` → Role `dev-env-operator` | ServiceAccount `dev-env-system/dev-env-operator`; explicit agentsessions/status get/update/patch. |
| ClusterRoleBinding `cluster-admin` → ClusterRole `cluster-admin` | Group `system:masters`; all groups/resources/verbs. The read does not enumerate human members or prove Tom's login membership. |
| ClusterRoleBinding `cluster-reconciler-flux-system` → `cluster-admin` | ServiceAccounts `flux-system/kustomize-controller` and `flux-system/helm-controller`; the same wildcard rights. |
| ClusterRoleBinding `headlamp-admin` → `cluster-admin` | ServiceAccount `frontend/headlamp`; the same wildcard rights. |
| ClusterRoleBinding `system:controller:generic-garbage-collector` → its same-named ClusterRole | ServiceAccount `kube-system/generic-garbage-collector`; wildcard groups/resources, including update/patch. |

The reviewer’s narrower premise (only operator plus Tom) is **not established**:
these infrastructure identities also have RBAC grants. None is a custodian role
or permission for a worker to borrow its identity. No matching grant to ordinary
session or workbench ServiceAccounts was found. One unrelated binding,
`smartctl-exporter`, names a missing role and grants no rights through it.

RBAC is the observed authorization layer only. Admission may further refuse a
write; it was **not read** because this order permits RBAC objects only. Thus
this design does not claim which writes pass live admission, or that a session
can or cannot free itself under all policy. A later access review must prove
that boundary before manual use. No status patch or impersonation probe ran.

No new role is taken by this design. Q-068-01 asks whether Tom will acknowledge
with his own administrator login, or approve a separate narrow right for a named
account. A future right also needs an admission fence restricting that account
to the two acknowledgement members and the bound incident, like ADR-029's
explicit writer fence; an actor string is no fence. Until that decision and
verification, manual acknowledgement is held. Do not borrow an infrastructure
account, credential or route. The later proof also needs its own authorized exec
and status-write rights; ordinary worker rights are not assumed sufficient.

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
| J3 Operator restart during exec or after lost reply | StopPending; no confirmed completed report | Retry same operation/target after interval, including safe busy refusal if earlier ctl still runs (J30). A partial/late/missing receipt cannot authorize deletion. | `TestCodexStopRestartAfterLostReplyRetriesSameOperation` |
| J4 Operator restart after report before status ACK | StopPending with no receipt/reference, or the entire atomically stored RemovalCommitted decision | No delete from process memory. Reread and either retry stop/rescue or use exactly the stored RemovalCommitted evidence. No report-only middle state. | `TestCodexStopRestartBeforeRescueAcknowledgementDoesNotDelete` |
| J5 Operator restart after removal decision, before Delete | RemovalCommitted, receipt and completed rescue | Revalidate bindings and normally delete exactly the committed UID. A later desired-generation change cannot cancel this decision. | `TestCodexStopRestartAfterRemovalCommitFinishesExactPod` |
| J6 Operator restart during termination | RemovalCommitted, pod deletionTimestamp | Wait for actual absence. No replacement, even on repeated resume; no grace override. | `TestCodexStopRestartWhilePodTerminatesKeepsHomeAndWaits` |
| J7 Operator restart after absence before Complete ACK | RemovalCommitted, matching receipt/rescue, original HOME | Confirm absence uncached and Complete durably; no ensure in the same reconcile. Pruning/conflict sends no creation. | `TestCodexStopRestartBeforeCompleteAcknowledgementStartsNothing` |
| J8 Operator restart after Complete | Complete, retained original HOME binding | If Running, ensure only that HOME; if Suspended, keep normal retention rules. Same-name replacement/missing PVC refuses creation. | `TestCodexStopRestartAfterCompleteUsesOriginalHomeOnly` |
| J9 Agentd restart in the same pod | StopPending or RemovalCommitted plus HOME fence | Do not infer service from heartbeat or restart count. Fence rejects supported launch; retry/finish same target. Envtest proves controller behavior, agent tests prove fence refusal. | `TestCodexStopAgentdRestartDoesNotRestoreReadiness` |
| J10 Pod gone after authorized removal | RemovalCommitted, matching completed stop/rescue | Absence completes the operation; exactly one fresh pod may follow on original HOME. | `TestCodexStopPodGoneWithCompletedRescueCompletes` |
| J11 Pod gone unexpectedly, or same name/different UID | StopPending or inconsistent RemovalCommitted | RecoveryRequired. No hold/executor, no adopting the different UID, no clear from NotFound. | `TestCodexStopPodGoneWithoutProofRequiresRecovery` |
| J12 Node gone or NotReady; pod still present | Any active state, no confirmed writer-exit evidence | Retain pod/HOME with reason; terminal phase alone cannot permit delete. If node returns Ready, test J23. If a separate incident removes the object, test J13; no forceful-removal action is authorized here. | `TestCodexStopNodeGoneNeverBecomesExitProof` |
| J13 Node gone and pod gone | Active record without completed removal | RecoveryRequired; only correctly bound custodian ACK permits same-HOME recovery hold. | `TestCodexStopNodeAndPodGoneNeedCustodianAcknowledgement` |
| J14 Second suspend of the same target | StopPending or RemovalCommitted | Same operationID, no second record. Before commit generation change discards stale report; after commit finish the decision. | `TestCodexStopSecondSuspendKeepsOperationAndChecksGeneration` |
| J15 Resume after refusal | StopPending | Pending, PodReady=False, RemovalBlocked retained. Retry stop; later completed rescue/removal permits a fresh UID on same HOME. | `TestCodexStopResumeAfterRefusalQueuesFreshPod` |
| J16 Resume races report or committed delete | StopPending then RemovalCommitted | Stale-generation report before commit is discarded; resume after commit waits through delete and Complete. Never relabel old report generation. | `TestCodexStopResumeRaceHonorsCommitBoundary` |
| J17 Restart during manual recovery | RecoveryRequired/RecoveryHold, bound ACK or terminated record/hold | Recover only original HOME after verified exit evidence; discover the one owned hold after lost Create ACK; receipt plus successful hold rescue precede hold deletion and Complete. | `TestCodexStopManualRecoveryRestartClearsOnlyAfterHoldRescue` |
| J18 Recovery hold disappears or is resumed mid-rescue | RecoveryHold with admission bit true/unknown, or its RemovalCommitted decision | Resume cannot bypass rescue. Unexpected absence after admission is true/unknown needs new evidence for that hold; committed removal can finish normally. An explicitly false admission bit follows J20. No stale ACK; positive ended-hold evidence takes J31. | `TestCodexStopRecoveryHoldLossNeedsFreshEvidence` |
| J19 Second suspend after completed replacement | Complete, fresh owned executor on original HOME | New operationID and target UID on same HOME. No inheritance of the old receipt or removal decision. | `TestCodexStopSecondLifecycleCannotReusePriorReceipt` |
| J20 Restart after recovery-hold Create or before rescue dispatch | RecoveryHold, explicit admission bit and possibly observed hold binding | Lost Create ACK discovers only the operation-bound hold. Missing hold with false bit permits only another no-writer hold; true/unknown bit requires recovery evidence. True is persisted before exec, after exact pod/node/HOME reads. | `TestCodexStopRecoveryHoldAdmissionSurvivesLostCreateAndRestart` |
| J21 Name removed while capable original pod still exists, no stop yet | Pod construction decision and confirmed capable binding, no active operation | First suspend/idle/reap still records and sends qualified stop to that UID. No current-flag legacy fallback. | `TestCodexStopNameRemovalBeforeFirstStopKeepsPodEligibility` |
| J22 Recovery agent receives unbound stop | No operation binding in request | Agent refuses before lock/fence/freeze/Git; no report or mutation. Envtest checks controller handles refusal without deleting; agent unit test proves zero signals/files. | `TestCodexStopUnboundRequestCannotFreezeUnrecordedPod` |
| J23 Original pod ended and still present | Active record, Ready same node UID, every container positively terminated | Confirm terminated+RecoveryRequired before ordinary UID/RV delete; after absence build capable original-HOME hold; successful rescue/removal before Complete. | `TestCodexStopEndedOriginalUsesVerifiedTerminationThenHold` |
| J24 Ended phase with non-Ready node, unreadable Node/pod or incomplete/unknown container status | Active record, no valid positive termination evidence | Wait with precise missing-evidence reason, no delete/hold/executor/Complete. A later valid observation may take J23; no deadline. | `TestCodexStopEndedOriginalWithoutReadyTerminationEvidenceWaits` |
| J25 Older replica or human tries to erase operation/status | StopPending or Complete already stored | Both removal latches reject Update, remove/null patch or whole-status erasure; a preserving merge patch succeeds. Record survives; no legacy fallback. | `TestCodexStopRemovalLatchesRejectOldWriterAndStatusErase` |
| J26 Ordinary hold resumed before any recorded stop | No active operation for hold, even if named at construction | Today's D-55 shortcut removes hold without a rescue; no stop is manufactured. Failed prior generic Git rescue cannot create a new resume gate. | `TestCodexStopUntouchedOrdinaryHoldResumeKeepsD55` |
| J27 Recovery hold built after name removed | Active operation plus source exit evidence, current flag omits host | Construction uses record and qualified image, not flag; exact hold binding receives capable stop and successful rescue. Image unavailable waits visibly. | `TestCodexStopRecoveryHoldCapabilitySurvivesFlagRemoval` |
| J28 Version probe fails or is unrecognised before capable binding | No capable binding for this pod, no active operation | Today's stop command is attempted: an old agent runs it as today; a recovery agent refuses before fence/freeze and leaves suspend pending with a reason. No receipt requirement or permanent probe-only block; no unrecorded frozen recovery pod (J22). | `TestCodexStopUnrecordedUnknownVersionAttemptsLegacyStop` |
| J29 Version probe fails after pod recorded capable | Confirmed capable binding; operation absent or active | Confirm/reuse StopPending before any possible stop; treat as uncertain delivery, never downgrade to legacy or reuse pod on resume. | `TestCodexStopRecordedCapabilityNeverFallsBackOnProbeFailure` |
| J30 Retry while earlier ctl still holds lifecycle lock | StopPending after lost reply, clock reaches retry while old exec remains | Busy refusal records no rescue/receipt/decision; original target and HOME stay; later ordinary retry can complete. No simultaneous rescue. | `TestCodexStopLostReplyRetryBusyKeepsOperationPending` |
| J31 Recovery hold ended but remains present | Active record, Ready bound node, strict current termination of all hold containers | Confirm recoveryHoldTerminated before ordinary removal; bind a new capable hold; successful rescue still required. Missing evidence follows J18/J24. | `TestCodexStopEndedRecoveryHoldRequiresReplacementRescue` |
| J32 Ordinary source hold report completes after resume | StopPending for hold, valid receipt, completed failed Git rescue, desired Running | Atomic RemovalCommitted may remove under ordinary D-55 resume rule; HOME and RescueFailed stay. Recovery hold with same failed report stays. | `TestCodexStopOrdinaryHoldResumeDoesNotRequireSuccessfulGitRescue` |
| J33 Pod's explicit decision has a different Session UID | No matching capable binding/active operation | Treat as not named; no bound request. Today's unbound path only, without inheriting another Session's capability/receipt. A contradictory existing capable binding instead blocks. | `TestCodexStopMismatchedConstructionDecisionNeverGetsBoundRequest` |
| J34 Restart around first successful version/capability write | Correct pod construction decision, probe at/above minimum; capable binding absent/unconfirmed or confirmed | No stop before confirming the intended capability binding. If stored, use capable route even after flag removal and restart; a lost ACK is reread, not assumed absent. | `TestCodexStopCapabilityAcknowledgementPrecedesEveryQualifiedStop` |

Keep the old M4 characterization for **unnamed/unsupported** fixtures and add
J15 for the qualified fixture; do not rewrite the old-host expectation globally.
The pinned test already exposes the original UID/status/no-retry behavior.
([codex_remote_stop_resume_test.go:48–80][m4-test])

Additional build checks: version below/at/above minimum, absent/unrecognised/failed probe; disabled names;
matching/absent/mismatched explicit pod decision; old c1 compatibility; status conflicts,
lost ACK and CRD pruning; malformed/oversized/unknown version records; receipt
identity and operation replay; exec-name reuse after the pre-read (agent refuses
before any fence or signal); changed ownership/PVC UID; failed Git rescue
versus stop error; normal and empty holds; heartbeat cannot set/erase record;
all other status writers preserve it; old typed writer omits the field and is
rejected by both latches in mixed-version fixtures, while activation remains held
because old control flow still ignores it. Inventory all production
wantsPodGone callers and show their answers unchanged. Preserve existing guard
and pod goldens. Mutate each new guard and name its failing test in the build PR.
These tests do not prove Linux signal delivery, phone rendering or enrollment.

## 9. Answers to recheck 1 part D

| Objection | Ruling | Where and why |
| --- | --- | --- |
| D1 Old images become unsuspendable | **Accepted.** | Section 2.1 uses agentd version, not unknown-command text; before any capable binding, below-minimum/failed/unrecognised answers attempt today's stop with no receipt requirement (J28). The pinned version command is [cmd/agentd/main.go:217–223][version-command], and current rescue dispatch is [agentd/rescue.go:54–66][agent-rescue]. No record means no new receipt requirement for an old binary. Successful suspension remains subject to today's existing rescue failures; it is not promised for an unreachable agent. Older-release fixtures and the minimum release are future build evidence, not established by reading one pin. |
| D2 All hosts and undelivered exec | **Accepted.** | Section 2 uses the pod's construction name decision plus its bound capability, never the current flag for an existing pod. Section 3 states replacement cost after confirmed intent, and no cost before it, including cache-behind requeues. |
| D3 Manual recovery cannot clear | **Accepted.** | Today [guard.go:130,278–300][pod-guard] allows an ended original pod to go, and [reconciler.go:988–1014][hold-create] and [1227–1230][archive-hold] supplies the volume-rescue hold. Section 5 adds the stronger Ready-node/container evidence, or bound human ACK after unexpected absence, and completion only after successful original-HOME recovery-hold rescue. That controller flow is proposed, not already present. Section 7.1 records actual RBAC, including infrastructure wildcard grants; effective admission and any new named-account permission remain open under Q-068-01. |
| D4 Holds missing | **Accepted.** | Today [guard.go:145,295][pod-guard] permits ordinary-hold resume without rescue, [pod.go:303–315][hold] defines the hold, and [controller/codex_remote_stop.go:30–39][decision] currently uses the flag for its build. Section 5 preserves the no-record resume route (J26), finishes a recorded ordinary stop without imposing Git success on resume (J32), and builds recovery holds from the record (J27). Successful rescue independent of desired mode is a new recovery-hold rule. J17/J18/J20/J31 cover loss/restarts; real hold freeze remains unproved. |
| D5 wantsPodGone changes 37 callers | **Accepted.** | Section 4 leaves the function unchanged, supplies a pod-aware predicate and an independent pending lane, and names the guard/deletion/construction integration points. |
| D6 Anchors inaccurate | **Accepted.** | All source citations use the freshly fetched pin and actual numbered source. Private-history eligibility cites executable lines 45–46 as well as its comment; no uninspected haynes-ops line number is asserted. |
| D7 Marker timing | **Accepted.** | Section 1 says HOME fence precedes quiesce, with agent-stop lines 213–217. Section 3 needs controller memory before exec because it cannot rely on receiving any agent report. |
| D8 Receipt trust and old decode | **Accepted.** | Section 3.2 calls it an agent-reported result, not kernel attestation, and cites the lenient decoder. Section 7 prohibits mixed-version activation and distinguishes rejected unknown-key omission from control flow that still ignores the operation. |
| D9 RBAC/admission unverified | **Accepted.** | Section 7.1 supplies live RBAC objects read for this revision, not a stale clone. Admission was outside the permitted reads and remains unverified; no new access or effective admission permission is asserted. Q-068-01 takes neither access choice. |

## 10. Later disposable-host proof — written, NOT RUN

This procedure is for a separate, explicitly authorized proof story. It touches
only an ordinary disposable Codex host named in that story, never the coordinator
or Studio. The story must authorize suspend/resume, the one interrupted stop,
read-only exec observation and the named custodian. It must record disk reserve,
CPU limit, installed pins/schema, all replica versions, current RBAC/admission,
HOME UID, rescue destination and cleanup. A denial stops its action. No host
creation, flag or manifest is authorized here.

Run all blocks in the same authorized custodian shell, with `set -eu` as below;
a failed prerequisite stops before the next action. Set these values from the
proof story, not by searching for a convenient host. Commands in this section are instructions for later, not commands executed by
this design worker. The version command exists at the source pin; the qualified
rescue arguments and the proof helper are proposed build outputs.

```sh
set -eu
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
kubectl -n "$PROOF_NS" exec "$PROOF_HOST" -c agent -- agentd version > "$PROOF_DIR/agent-version.txt"
```

Use the reviewed build's version parser/minimum constant for this comparison.
Require a parsed release at/above that minimum, matching
UIDs, the pod's recorded construction decision, confirmed capability binding and
release-qualified immutable imageID.
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
`writer-ready`, prints `writer-grace` on its first ignored TERM, and sleeps
at most 120 seconds; one
`--interrupt --operation OPERATION_UUID` invocation validates the supplied UUID,
reads the source-bound HOME fence, verifies the exact boot supervisor is frozen,
and finds exactly one qualified rescue command with that operationID and the
matching expected Session/pod UID arguments. It opens a pidfd, rechecks its
identity, then sends KILL only to that ctl process. It never thaws anything,
modifies a fence, kills tini/supervisor, invokes a provider or writes HOME. If
any identity/check is ambiguous, or the grace interval has already ended, it
exits 2 without signalling. It prints only
bounded JSON with result `interrupted` and the operation/pod UID on success. Its own interruption is an
inconclusive attempt, never proof. No general PID-selection argument is allowed.
Install/copy this reviewed helper privately only within the later proof authority.

The operator's retry is one minute; a proof runner must capture the pending
state before the retry. The helper must enforce a 20-second total interrupt
budget with modest sleeping reads; it cannot busy-loop. Before signalling it verifies that the writer is
still alive inside the same 30-second grace interval, and records measured
elapsed time. Its proof-only writer exposes a private monotonic TERM-received
marker outside HOME; --interrupt reads that exact writer-bound marker, rejects
an absent/expired marker, and verifies it again before KILL. No production fault
injection or change to the stop grace is introduced. Start it only after the
writer is in grace, so it does not get frozen in the first process inventory.
A later build must measure this timing against the qualified binary. If this
window cannot be observed safely, stop and revise the proof; do not shorten
production grace or add a production fault-injection flag.

```sh
kubectl -n "$PROOF_NS" exec -i "$PROOF_HOST" -c agent -- python3 - --writer < scripts/proofs/op877-interrupt.py > "$PROOF_DIR/writer.txt" 2> "$PROOF_DIR/writer-stderr.txt" &
PROOF_WRITER_CLIENT=$!
PROOF_WRITER_READY=false
for PROOF_READY_TRY in 1 2 3 4 5 6 7 8 9 10; do
  PROOF_READY_LINE=''
  IFS= read -r PROOF_READY_LINE < "$PROOF_DIR/writer.txt" || true
  if test "$PROOF_READY_LINE" = writer-ready; then
    PROOF_WRITER_READY=true
    break
  fi
  sleep 1
done
test "$PROOF_WRITER_READY" = true
agent-run suspend "$PROOF_HOST" -o json > "$PROOF_DIR/suspend.json"
# Bounded, sleeping poll for the actual record, not an assumed three-second write.
PROOF_OPERATION=''
for PROOF_RECORD_TRY in $(seq 1 25); do
  kubectl --request-timeout=2s -n "$PROOF_NS" get agentsession "$PROOF_HOST" -o json > "$PROOF_DIR/stopping-session.json"
  PROOF_OPERATION=$(python3 -c 'import json,sys; r=json.load(open(sys.argv[1])).get("status",{}).get("codexRemoteStop",{}); print(r.get("operationID","") if r.get("phase")=="StopPending" else "")' "$PROOF_DIR/stopping-session.json")
  test -z "$PROOF_OPERATION" || break
  sleep 1
done
test -n "$PROOF_OPERATION"
agent-run show "$PROOF_HOST" -o json > "$PROOF_DIR/stopping.json"
# Observe the writer's TERM before starting any new process in its container.
PROOF_GRACE_READY=false
for PROOF_GRACE_TRY in $(seq 1 25); do
  if rg -qx writer-grace "$PROOF_DIR/writer.txt"; then
    PROOF_GRACE_READY=true
    break
  fi
  sleep 1
done
test "$PROOF_GRACE_READY" = true
# The helper independently rejects an expired grace marker before signalling.
kubectl --request-timeout=25s -n "$PROOF_NS" exec -i "$PROOF_HOST" -c agent -- python3 - --interrupt --operation "$PROOF_OPERATION" < scripts/proofs/op877-interrupt.py > "$PROOF_DIR/interrupted.json"
agent-run resume "$PROOF_HOST" --wait 1s -o json > "$PROOF_DIR/resume.json"
agent-run show "$PROOF_HOST" -o json > "$PROOF_DIR/pending.json"
```

Pass this stage only if the helper proves the source-bound fence, frozen
supervisor and live sleeping writer within the measured 30-second grace before
the interruption, and API observation shows the same source
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
version probe, `agent-run suspend` and `agent-run show`. Require a recognised
below-minimum release, legacy suspend/rescue and no new operation. Envtest also
covers failed/unrecognised version answers taking the legacy command (J28). The proof
custodian checks its completed rescue before any normal cleanup; receipt absence
must not make this fixture unsuspendable. Do not run the interruption helper there.

Operator/agentd restart and node-loss permutations are first covered by section
8. Do not kill a node or the shared operator for this live proof. A real manual
recovery needs its own incident authority and measured node/container-exit
facts. An ended-pod proof first captures uncached source pod/Node snapshots,
checks Ready on the bound node and strict termination of every container, then
observes the controller's termination record, ordinary removal, capable hold and
successful rescue before Complete (J23/J24). No status ACK is needed for that
machine-evidence route. Exercise flag removal only in a separately authorized
fixture: the same existing capable pod still receives the bound stop (J21), and
the later recovery hold is built from the record (J27). Do not mutate a real
coordinator's setting for proof.

For unexpected absence without positive termination evidence, the human path's
exact status write is a resourceVersion-tested JSON patch of only
recoveryAck, prepared privately from the incident, then:

```sh
kubectl -n "$PROOF_NS" patch agentsession "$PROOF_HOST" --subresource=status --type=json --patch-file "$PROOF_DIR/recovery-ack.patch.json"
agent-run show "$PROOF_HOST" -o json > "$PROOF_DIR/recovery-observed.json"
```

The incident actor first verifies the section 7.1 access/admission gate under
its own identity. The patch file must start with `test` operations for resourceVersion and every
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
| A Protocol and schema | M, 4–6 hours; split API and agent work if needed | Version parser and one minimum constant, durable capable binding, operation-bound receipt/request, unbound refusal, bounded API types and root/status removal latches, deepcopy/CRD and cost/validation tests. No activation. Prove old release fixtures and construction-bound naming. |
| B Reconcile and guards | L, 6–10 hours, split before pickup | Durable operation writes, acknowledgement, retry, exact target removal branch, generation boundary, ensure/archive/finalizer fences, Complete HOME binding, ordinary/empty hold behavior. Implement J1–J16/J19/J21–J26/J28–J30/J32–J34 and old-host regression checks. Remains inactive until all pieces/review are ready. |
| C Manual recovery | M, 4–6 hours | Custodian ACK validation/audit contract, same-HOME recovery holds, lost-hold binding/admission, verified-ended source/hold recovery and J17/J18/J20/J27/J31. Q-068-01 gates first use, not the independent source checks. No new credential or permission. |
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

- **Q-068-01 — Will you confirm a missing computer's processes have stopped
  yourself, using your administrator login, or approve a new narrow right for
  one named account to record that confirmation?** Recommendation: use Tom's own
  administrator login for the first incident, after verifying its access. Giving
  a named account that right is a separate access decision with its own review;
  this design takes neither choice. The live RBAC read found no existing session
  or workbench custodian right; infrastructure wildcard accounts are not a route
  to borrow. Either choice still needs verified exit evidence and a successful
  save of the original files in the recovery hold. Hold first manual use until
  the owner decides and effective authorization/admission are verified.
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
[cache]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L845-L1058
[status-update]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L805-L815
[ack]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/private_history.go#L63-L98
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
[hold-command]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/cmd/agentd/main.go#L331-L347
[show-conditions]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/agentrun/sessions.go#L386-L389
[resume-text]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/agentrun/lifecycle.go#L89-L112
[removal-latches]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/api/v1alpha1/agentsession_types.go#L807-L814
[root-latches]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/api/v1alpha1/agentsession_types.go#L2210-L2217
[version-command]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/cmd/agentd/main.go#L217-L223
[version-format]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/version/version.go#L34-L71
[hold-create]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L988-L1014
[archive-hold]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L1227-L1230
[retry-hold-create]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/rescue_retry.go#L193-L214
[rescue-timeout]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/rescue.go#L63-L65
[busy-lock]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/agentd/codex_remote_stop.go#L145-L160
[terminated-check]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/workspace_stop.go#L123-L202
[pod-shape]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/pod.go#L583-L595
[status-writers]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/reconciler.go#L1024-L1044
[directory-status]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/sessiondirectory/store.go#L267-L275
[drain-callers]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/drain.go#L62-L72
[shared-caller]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/workspace_lifecycle.go#L316-L322
[task-caller]: https://github.com/thaynes43/dev-env/blob/ffb6af5ce118b243c5c6820c235f104f4159c08b/internal/controller/terminal_task_reclaim.go#L194-L199
[k8s-pod-gc]: https://v1-35.docs.kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#garbage-collection-of-pods
[k8s-shutdown]: https://v1-35.docs.kubernetes.io/docs/concepts/cluster-administration/node-shutdown/#graceful-node-shutdown
[k8s-nongraceful]: https://v1-35.docs.kubernetes.io/docs/concepts/cluster-administration/node-shutdown/#non-graceful-node-shutdown
