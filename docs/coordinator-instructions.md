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
row. The response is a receipt: message identity, digest and `consumed: false`.
It does not include the nonce. Repeating the same intent returns that receipt.
A different intent for the same key conflicts. The client does not choose a
new key after an uncertain response.

`GET` is a read. It does not create an inbox read acknowledgement and does not
mark the instruction consumed. The target session's read includes the text and
nonce. `POST /v1/inbox/{message}/ack` remains the inbox read acknowledgement.

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
written by `beginNativeInvocation`. `RunAgent` and `runTUI` register that
binding after the owned process is recorded and the local phase is `Running`.
They post the end only after `afterWait` has verified `Exited`, leader reaped
and process-group absence. Both calls use the existing inbox client. An empty
`AGENTD_API_URL` does not register, does not end, and does not consume. A
configured origin that fails is unconfirmed and the native exit is not reported
as success.

`ReadInstruction` is a GET of the stored instruction for that active attempt.
It requires the project, repository, task key, message UID, text digest, text
and nonce from the reply, and it does not consume. The read view does not
carry the sender UID; that attribution stays on the server. The scope the
target can show the model is the project, repository, task key, target session,
target pod and attempt on that read.

`AcknowledgeReadInstruction` consumes only when the model-issued message UID
and text digest equal that exact read. It then calls `ConsumeInstruction`,
which also checks the consumption reply against those values. An empty field,
a different field, a daemon file, or the read itself is not consumption. The
one-shot runner does not call this function.

`internal/agentd/runagent.go` starts a non-TUI native exec with
`cmd.Stdin = strings.NewReader(l.Prompt)`. That stdin is closed after the
original prompt. There is no subscriber, no second prompt, and no steer into
the running process. A resumed native TUI takes the terminal, which is not a
coordinator push. Launch guidance for an enabled child decision tells the
model to pull a key it already knows with `agent-run instruction read` and to
acknowledge with `agent-run instruction consume`. It does not claim that a
correction stored after launch appears in the exec.

The missing receiver is that input seam. A correction that arrives after the
one-shot prompt has no supported way to be presented to the model, and there
is no list route in this client. This source does not invent an acknowledgement
or a polling loop for that gap.

`agent-run instruction send` accepts HTTP 200 `Confirmed` with the stored
`consumed` value, including true. That retry does not create consumption. HTTP
201 `Created` is unconfirmed when `consumed` is true. `instruction consume`
accepts an acknowledgement only when its message UID and text digest equal the
`--message-uid` and `--text-sha256` copied from the read.

A legacy session with no `spec.coordinatorInstructionParentUID` fails closed
on delivery. The field is copied only at create from a live parent read. This
slice does not patch an existing spec and does not inherit another claim.
