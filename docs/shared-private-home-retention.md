# Preserve private homes when a shared task is reaped

**Implemented in source; runtime acceptance pending.** A verified shared-task rescue
preserves task Git state. It does not preserve the private provider home,
credentials, enrollment, conversation database or other private files. Shared
archive/reap must therefore refuse the legacy private-home deletion path.

The supported completion route retains that private PVC independently
of its AgentSession. After a verified task rescue and uncached absence of both
executor and hold, the controller records a versioned retention receipt bound
to Session UID, original executor UID, private PVC UID, writer generation and
rescue result/locator. A `NoWorkAdmitted` result is distinct from an empty home;
its generation may be zero only for verified no-owner admission.

Bind the private PVC UID durably when the original executor is admitted. Check
that binding before and after hold rescue; capturing a UID only when the hold is
created cannot detect an earlier replacement. Pod annotations are mutable and
cannot supply that authority by themselves. Old untyped rescue evidence or a
missing original home binding refuses retention rather than inventing a proof.

Under a conditional write, remove only that exact AgentSession controller owner
reference from its owned PVC. Preserve all other metadata, owner references and
data. Discover retained homes by the label
`dev-env.haynesops.com/retained-private-home=true`; the companion
`dev-env.haynesops.com/retained-session-uid` label identifies the original Session.
The `dev-env.haynesops.com/private-home-retention` annotation holds the versioned
receipt without credentials or provider transcripts. Confirm the write durably
and recheck both pod names
before permitting Session finalization. Foreign/replaced/deleting PVCs, new pods,
unconfirmed writes or mismatched evidence refuse and preserve the Session/PVC.

For a genuinely new task, `status.sharedAdmission` records version 1, its exact
Session UID and `NeverStarted` before the controller adds its finalizer. Before
attempting any shared private-home, executor or hold creation, the controller
durably confirms the one-way transition to `Started`. Missing history is unknown;
deleting or previously finalized tasks never receive a backfilled marker.

A deleted task with confirmed `NeverStarted` may finish only after fresh,
repeated checks show no executor, hold, private home or resource/provider/rescue
history, followed by a conditional Session update. This creates no home-retention
receipt and makes no empty-home claim. It is separate from `NoWorkAdmitted`, which
describes verified task admission after resources exist. `Started` or unknown
tasks still require the retained-home proof above.

Do not call the home archived, empty or backed up. This is retained storage whose
contents are unchanged. No automatic retention expiry, home destruction, native
enrollment retirement or reuse by another task is authorized by this source
unit. Those operations need their own explicit supported policy and acceptance.
Shared workspace and peer/project data remain outside Session deletion.

Source fixtures cover lost write acknowledgements, replacement UIDs, changed
owners, new executor/hold races, nonempty provider homes and no-work admission.
The original home binding is `status.sharedPrivateHomeUID`; typed rescue evidence
is `status.rescue.sharedProof`. Install the matching generated CRD before enabling
this behavior in an operator or executor image. Existing untyped records remain
blocked until a supported proof exists.
Runtime acceptance must show a reaped task leaves its private home discoverable
and intact, with its workspace and peers preserved. This closes the narrow reap
preservation gap; it does not close full restore, backup or cutover acceptance.
