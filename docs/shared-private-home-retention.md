# Preserve private homes when a shared task is reaped

**Source contract; not implemented or accepted.** A verified shared-task rescue
preserves task Git state. It does not preserve the private provider home,
credentials, enrollment, conversation database or other private files. Shared
archive/reap must therefore refuse the legacy private-home deletion path.

The first supported completion route will retain that private PVC independently
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
data. Label the retained home for discovery; record its receipt without secrets
or provider transcripts. Confirm the write durably and recheck both pod names
before permitting Session finalization. Foreign/replaced/deleting PVCs, new pods,
unconfirmed writes or mismatched evidence refuse and preserve the Session/PVC.

Do not call the home archived, empty or backed up. This is retained storage whose
contents are unchanged. No automatic retention expiry, home destruction, native
enrollment retirement or reuse by another task is authorized by this source
unit. Those operations need their own explicit supported policy and acceptance.
Shared workspace and peer/project data remain outside Session deletion.

Source fixtures must cover lost write acknowledgements, replacement UIDs, changed
owners, new executor/hold races, nonempty provider homes and no-work admission.
Runtime acceptance must show a reaped task leaves its private home discoverable
and intact, with its workspace and peers preserved. This closes the narrow reap
preservation gap; it does not close full restore, backup or cutover acceptance.
