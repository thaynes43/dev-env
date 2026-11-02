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
different key. Duplicate keys are rejected. The response is a receipt:
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

`POST /v1/sessions/{name}/instruction-attempt` lets the target pod register the
attempt it is running, or end that same attempt. The server copies the session
and pod from the token.

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

The platform invocation id, session UID and pod UID are the binding already
written by `beginNativeInvocation`. When instruction registration is enabled,
`RunAgent` and `runTUI` register that binding after the owned process is
recorded and the local phase is `Running`. They post the end only after
`afterWait` has verified `Exited`, leader reaped and process-group absence.
Both calls use the existing inbox client. An empty `AGENTD_API_URL` does not
register, does not end, and does not consume.

Registration is not implied by child decisions or by `AGENTD_API_URL`. The
launch records `coordinatorInstructions` only when
`AGENTD_ENABLE_COORDINATOR_INSTRUCTIONS=true` at launch build, and run-agent
posts the attempt only when that launch flag and the pod setting are both
set. A legacy or disabled native task makes no instruction request, so a
missing route does not stop it. An enabled launch with a configured origin
that fails registration or end is unconfirmed and that native attempt stops.
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
Guidance for an enabled instruction launch tells the model to run
`agent-run instruction checkpoint` at a meaningful checkpoint: before an
irreversible step, before declaring the task done, and when resuming work.
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
