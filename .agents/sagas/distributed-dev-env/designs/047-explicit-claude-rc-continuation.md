# DESIGN-047: Explicit Claude Remote Control continuation

Status: implementation in progress, default off. Source, publication,
installation and real user acceptance are separate gates.

## Outcome and boundaries

An authorized owner can deliberately continue one completed private Claude
project task as a supervised interactive Remote Control conversation. Preserve
its Session UID, original HOME claim, native conversation, project rules and
initial task outcome. The daemon that starts the provider also owns heartbeat
and process supervision. An intentional terminal TUI exit ends this generation.
A network disconnect does not create another provider invocation.

This does not relax ordinary retained-task automatic-resume refusal. It adds
no principal, grant, credential, login, network permission, shared repository,
global worker image change or mutable task claim. Existing user sessions and
other providers remain outside the selected scope. A separate sequential UI
handoff may meet immediate user access needs while this v2 integration ships.

## Pod-local supervisor

The source in `docs/explicit-claude-rc-continuation.md` journals one spawn
intent before changing resume state or starting a pane. The request has one
writer generation. It binds exact original launch/result identity and hashes,
HOME, conversation, boot, resume hash and subsequently the actual PID/start.
A missing or uncertain result never permits another spawn. Daemon restarts
reconcile the existing process or exit; they do not replay it.

Caller descriptions and freeze text are audit material, not authorization.
The pod-local control command requires the actual same running daemon binary.
Older daemons and side binaries fail closed. Accepted project rules and the
interactive guard remain present. The child uses its existing usable Claude
login with static OAuth overrides removed. Transcript checks inspect metadata
only at the exact worktree project key.

The implemented pod-local continuation runs in the original pod, whose whole
executor has not terminated. While RC is live or uncertain, agent status is
not exited and terminal-task compute reclaim refuses it. HOME expiry needs
the whole original pod's terminal proof, not its earlier native task result.
An explicit suspend/reap follows the existing guarded whole-pod stop path.
This adds no authority to stop another owner. A later replacement pod is a
separate unimplemented generation and needs the integration below.

## Explicit operator generation

A future source integration selects one exact Session name/UID and one
released digest-pinned Claude agent image through independent, default-off
configuration. It must not borrow a Codex or pilot selector. The normal
project/task image mapping stays unchanged.

The authenticated request comes from the selected live Session's actual owned
pod. It names a stable request key, expected Session/pod identity and title.
The server derives the parent UID, old executor UID, accepted project,
conversation and original HOME claim UID from live objects. An audit sentence
cannot choose those bindings. A different request after one intent conflicts;
repeating an identical intent returns its existing receipt.

Only the same recorded live parent, or the same owner while its original token
is still valid, can activate that intent. Activation is a separate supported
control operation. It needs the original owner's cooperative checkpoint and
actual terminal task result, the recorded whole-executor stop proof, absence
of the old pod, unchanged original HOME claim, and an unexpired request. Pod
absence or a timeout alone cannot prove stop. Existing owner holds, archive
work, deletion, foreign mounts and uncertain children block activation.

A separate typed continuation record must preserve the original private
executor record. In particular, never replace `privateHistory.lastExecutor`,
its stop/terminal receipt or its initial task outcome with a new pod UID.
The continuation record binds:

- One immutable request, Session UID, original pod UID, HOME UID, conversation,
  parent UID, selected image, title, request/activation times and generation.
- An immutable snapshot of the actual original task outcome at activation.
- One durable new pod-creation intent, followed by its actual owned pod UID.
- Current continuation state and its separately bound provider/whole-pod exit
  evidence. Unknown outcomes preserve the outstanding intent.

The controller confirms the creation intent before calling Create. It calls
Create at most once for that intent. An uncertain response can be reconciled
only by finding the same owned pod with the same creation nonce and image;
absence does not authorize another attempt. Before initial admission it
rechecks the Session resourceVersion, HOME claim and absence of other mounts.

The new pod receives the operator-authored old-stop/HOME/generation binding
and its actual pod UID from the downward API. Its reviewed daemon validates
the old original launch/result against that binding and starts the one explicit
interactive continuation. It does not relabel an original launch as a new
task or rewrite historical files. A stale PID from another pod is not a live
process identity in the new pod; whole-old-executor fencing is mandatory
before that distinction may be used.

## Observation, stop and retention

A heartbeat must authenticate the actual new owned pod and bind its request,
creation intent and generation before changing continuation state. The
original task outcome stays immutable. A live matching PID may supply a CLI
bridge URL; fixture files and PodReady do not establish user readiness.

Controller restarts observe the same stored creation intent and pod. A lost
heartbeat, missing PID, foreign pod, expired request, changed HOME or unknown
wrapper result preserves work and reports an accountable reason. None grants
another launch.

An intentional TUI exit is distinct from a disconnect. Its bound Wait result
ends provider execution and cannot restart automatically. Suspending or
reaping an active continuation must use its own exact controlled pod/stop
intent and fresh whole-executor terminal proof before deletion. It cannot
reuse the original task's stop receipt to delete a newer executor. There is
no permanent task-reclaim exemption.

HOME expiry must wait for every continuation executor to have confirmed stop
proof, and start the applicable grace no earlier than the latest confirmed
activity/suspension. An active or uncertain continuation cannot be made safe
for expiry by an older original task receipt. Schema invariants must preserve
these facts across older-controller writes and rollback. These retention
requirements are integration work, not established by the pod-local patch.

## Validation and delivery

Meaningful isolated fixtures must cover: exact target isolation; forged,
sibling and replaced-parent refusal; immutable original outcome; HOME/pod
replacement and foreign mounts; crash before/after creation-intent persistence;
uncertain Create without replay; original owner still live; new pod identity;
controller/daemon restart; missing/changed PID; network reconnection while the
owned process persists; intentional exit without replay; guarded stop/delete;
and retention refusing an active or uncertain newer executor.

Run one suite at a time with bounded CPUs/package workers. No stress or wide
reproduction loop. Current source and changed deltas need independent native
review, required CI, matching generated schema and signed image publication.

Only after those gates should an exact runtime packet name the selected
Session/parent/pod/HOME identities, signed images, request key, scope, health
checks and rollback behavior. Activation needs its specific authorization and
cooperative owner evidence; source configuration grants none. Preserve the
original HOME/history and all unrelated sessions on every failure path.
Real acceptance additionally needs the actual URL, title and account listing,
a live native acknowledgement, multiple turns and disconnect/reconnect proof.
