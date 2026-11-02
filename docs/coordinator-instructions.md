# Coordinator instructions

Source contract for a designated parent session to deliver one bounded
correction to the active native attempt of one child task, and for that task
to acknowledge consumption. This document describes the source routes. It is
not an installed operator, a published image, a live delivery, or a grant.

The parent is `spec.coordinatorInstructionParentUID`, copied at create from a
live read of the creating session. The request body cannot set it. Usage
capture is a different field and is not consulted. An absent UID fails closed.
A later session with the same name and a new UID is not the designated parent.

Delivery is `POST /v1/sessions/{name}/instructions`. The API derives the sender
from the caller's token. The body names the key, the target session UID, the
current pod UID, the registered attempt, the project, the repository, the task
key and the text. Those scope fields must match the target's stored snapshot
and its single owned task claim. A different value is refused. Text is not
parsed for authority.

The delivery record is an ordinary inbox message plus a server-authored status
row. `status.coordinatorInstructions` is a map list keyed by the immutable
delivery key, at most four records. Kubernetes can correlate each item, so
the delivery fields stay immutable and consumption can be set once. A later
update cannot drop the list, remove an old key, or replace an old key with a
different key. Duplicate keys are rejected. A new key is refused when four
records are already stored, and that refusal happens before an inbox message
is created. Repeating a key that is already stored still returns that receipt
and does not create another message. The response is a receipt:
message identity, digest and `consumed: false`. It does not include the nonce
or the sender. Repeating the same intent returns that receipt.
A different intent for the same key conflicts. The client does not choose a
new key after an uncertain response.

`GET /v1/sessions/{name}/instructions/{key}` is a read. It does not create an
inbox read acknowledgement and does not mark the instruction consumed. The
target session's read includes the server-authored sender UID, the text and
the nonce. `POST /v1/inbox/{message}/ack` remains the inbox read acknowledgement.

`GET /v1/sessions/{name}/instructions` is the checkpoint for the caller's own
active attempt. It takes no key. It returns that attempt's unconsumed
corrections, each with the sender UID, text, message UID, text digest and
nonce, or an empty pending list. The designated parent is not the recipient
and cannot use this list. The call does not consume.

`POST /v1/sessions/{name}/instruction-attempt` lets the authenticated target
pod register an attempt, or end that same attempt. The server copies the
session and pod from the token. It authenticates that pod-authoritative
claim. It does not independently attest that the platform selected or ran
the attempt. The attempt ID is a lowercase UUID or 64 lowercase hex
characters. That is a shape check. A different ID is refused while the
current attempt is active.
An active attempt stays bound to the pod UID that registered it. A later pod
with the same session name and a new UID is a replacement, not that executor.
The replacement cannot register a new attempt, cannot register the active
attempt again, and cannot end it. The active record is not retired and the
unknown executor is not marked ended. That refusal stays in place until the
original pod ends the attempt. This source does not treat the replacement as
the lost executor.
Ending retains that ID in `status.coordinatorInstructionEndedAttempts`, an
append-only list of at most four. A retained ID cannot be registered again.
A full list refuses another attempt. Removing or rewriting a retained ID is
rejected. The delivery list's per-key immutability rules are unchanged.

For one enrolled session those four entries are the whole native registration
budget: the initial invocation and three continuations. Each enabled
child-decision continuation registers its own attempt ID. After four attempts
have ended, the fifth registration is refused and the ledger is unchanged.
`TestInstructionEndedLedgerRefusesWhenFull` in
`internal/apiserver/coordinator_instruction_test.go` registers and ends four
attempts, then refuses a fifth registration and an ended-ID replay. The
refusal stays unconfirmed. `runTUI` stops that enabled continuation. The
session stays enrolled, with no unregistered fallback and no fabricated
acknowledgement. This is the current source limit. It is not full native
acceptance, and it is not a claim that an agentic workflow is fully
unblocked. A disabled or legacy launch makes no registration request and is
outside this ledger.

`GET /v1/sessions/{name}/instruction-attempt` is the designated parent's
address read. It returns the current attempt ID, phase, target session UID
and live pod UID. It returns no nonce and no instruction text. The caller
must be the session whose UID was stored in
`spec.coordinatorInstructionParentUID` at the child's create, and both
sessions must pass the same accepted-project snapshot and single owned task
claim checks as send. `claimSnapshot` still requires the annotation and still
rejects `spec.workspace`. An ended attempt, a stale pod, a stopped agent, a
sibling, a human caller, or a parent with no accepted project snapshot is
refused. `agent-run instruction address` is that read. Send copies
`--attempt`, `--expect-uid` and `--expect-pod` from it. The recipient
checkpoint stays a different route and remains own-only.

## Who can send

The sender is a Session. The parent UID is copied only at the child create,
from a live read of that authenticated session. A human ServiceAccount, a
client, or a later session with the same name is not that parent. The sender
and the child each need an accepted project snapshot: the annotation is
present, the selected repository equals `spec.repo`, and `spec.workspace` is
absent. The child owns exactly one task claim for that project and
repository. Depth follows DESIGN-001 3.4. A depth-0 session may start a
depth-1 child. A depth-1 session may start a depth-2 child. A depth-2
session cannot start another session. A legacy depth-0 root with no accepted
project snapshot fails `claimSnapshot` and cannot publish or address. This
source does not admit that root, does not drop the snapshot check, and does
not admit a shared workspace.

`POST .../instructions/{key}/consume` is the target task's consumption
acknowledgement. It must present the nonce, the live pod and the active
attempt. The stored consumption receipt is immutable and distinct from the
inbox read acknowledgement. An exact retry returns the same receipt. Ending
the attempt, replacing the pod, or exiting the agent refuses a new delivery
and a new consumption. A daemon file is not a consumption receipt.

These routes do not grant credentials, widen repository or task scope, clear a
hold, extend a lifetime, or record a human decision. Source tests exercise the
handlers with fake tokens. They do not prove a deployed operator or a live
native task.

## Native lifecycle and explicit pull

A child-decision launch still binds the platform invocation id, session UID
and pod UID written by `beginNativeInvocation`. agentd reads that local file
before it posts a child-decision attempt. An ordinary private task does not
write `native-invocation.json` and does not enable child decisions. Its
attempt ID is `Launch.UsageSource.AttemptID` from the initial-usage binding.
agentd's local provenance check, before its own post, requires that id, the
exact `initial-usage/intent.json` launch digest, the current private launch,
and the live owned `agent.pid` process group. The local end check requires
this process's own Wait, the recorded pid no longer alive, and process-group
absence. A stopped, replaced, or stale local record is not posted by that
path. The registration server does not read the local invocation file, the
intent digest, or the process.
The stored attempt ID remains the pod's claim under the registration guards
above. The usage source is attempt metadata. It is not allowance ingestion.

`agent-run instruction attempt` is the model-facing command. It can register
or end its own pod record. It posts `--attempt` and optional `--ended` and
does not run the agentd local provenance check. The server authenticates the
caller's pod and still applies the attempt guards above. The command does
not consume.

When instruction registration is enabled, `RunAgent` and `runTUI` register
that binding after the owned process is recorded and the local phase is
running. A child-decision end is posted only after `afterWait` has verified
`Exited`, leader reaped and process-group absence. An ordinary end is posted
only after the owned Wait evidence above. Both calls use the existing inbox
client. An empty `AGENTD_API_URL` does not register, does not end, and does
not consume. Registration failure still stops that attempt. An end POST that
does not confirm is recorded as local protocol state
`instruction-end-unconfirmed.json` and does not change the CLI's exit code.
That file is not server acceptance, not a consumption receipt, and not
permission to start another attempt. Neither the registration POST nor the
end POST is retried. An unconfirmed registration is not acceptance: the
attempt is stopped. An unconfirmed end keeps the CLI exit result that Wait
already reported.

Registration is not implied by child decisions or by `AGENTD_API_URL`. The
launch records `coordinatorInstructions` only when
`AGENTD_ENABLE_COORDINATOR_INSTRUCTIONS=true` at launch build, and run-agent
posts the attempt only when that launch flag and the pod setting are both
set. A legacy or disabled native task makes no instruction request, so a
missing route does not stop it. An enabled launch with a configured origin
that fails registration is unconfirmed and that native attempt stops. A full
ended-attempt ledger is that failure for a child-decision continuation: after
four ended attempts the fifth registration is refused, and `runTUI` stops
that continuation. The launch stays enrolled. The count and the server test
are stated with the ended-attempt ledger above. An unconfirmed end preserves
the true CLI exit Wait already reported and records
`instruction-end-unconfirmed.json`.
An enabled launch with an empty origin makes no request and does not consume.

`CheckpointInstructions` and `agent-run instruction checkpoint` are a GET of
the pending list for the active attempt. The model does not need the new key.
The reply includes the server-authored sender UID. The daemon lifecycle path
does not call it, and a daemon file is not consumption. `ReadInstruction` is
the keyed GET for a key the task already has. It requires the sender UID,
project, repository, task key, message UID, text digest, text and nonce, and
it does not consume.

`AcknowledgeReadInstruction` consumes only when the model-issued message UID
and text digest equal that exact read. It then calls `ConsumeInstruction`,
which also checks the consumption reply against those values. An empty field,
a different field, a checkpoint, a daemon file, or the read itself is not
consumption. The one-shot runner does not call this function.

`internal/agentd/runagent.go` starts a non-TUI native exec with
`cmd.Stdin = strings.NewReader(l.Prompt)`. That stdin is closed after the
original prompt. There is no subscriber, no second prompt, and no steer into
the running process. A resumed native TUI takes the terminal, which is not a
coordinator push. Codex exec cannot be interrupted with a later correction.
Claude Code is not enrolled by this launch and is not interrupted either.
Guidance for an enabled instruction launch, including an ordinary private
task that is not a child-decision launch, tells the model to run
`agent-run instruction checkpoint` at a meaningful checkpoint: before an
irreversible step, before declaring the task done, and when resuming work.
The ordinary guidance names that command and states that it takes no
correction key. A disabled or legacy launch does not add the guidance.
The model sees a later correction only by running that command. The guidance
does not claim the provider will pause or that a stored correction appears
in the process by itself.

`agent-run instruction send` accepts HTTP 200 `Confirmed` with the stored
`consumed` value, including true. That retry does not create consumption. HTTP
201 `Created` is unconfirmed when `consumed` is true. `instruction consume`
accepts an acknowledgement only when its message UID and text digest equal the
`--message-uid` and `--text-sha256` copied from the checkpoint or the keyed read.

A legacy session with no `spec.coordinatorInstructionParentUID` fails closed
on delivery. The field is copied only at create from a live parent read. This
slice does not patch an existing spec and does not inherit another claim.
