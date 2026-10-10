# Managed child decision source contract

D-83 extends the managed shared Codex task route from D-80. Operator
`--enable-managed-child-decisions` and agentd `AGENTD_ENABLE_CHILD_DECISIONS`
default false. The operator requires configured coordinator callers and managed
Codex task support. The controller's final flag enables only eligible managed
shared Codex task pods whose exact direct Parent is a validated configured
coordinator ServiceAccount reference. Human, client, session and absent parents
are ineligible. Templates cannot widen it to private, hold or Claude pods.

`agentd ask-decision` accepts bounded question, options and context input. Platform
identity comes from the private current launch and current launched shared writer.
The model cannot supply Session, Pod, native thread, generation or path authority.
One unresolved record is retained in private 0600 state; a different second answer
is rejected. Completed context is archived privately before the next question.
The existing status/Outcome discovery route publishes only the decision reference.
Private files share the native child's Unix identity. Their permissions protect
privacy, but do not establish who authorized an answer.

GET/POST `/v1/sessions/{name}/decision` requires the configured live direct parent.
The API repeats uncached Session/Pod ownership checks and executes target commands
with expected Session UID and Pod UID. Target checks precede private context reads
and answer stdin consumption. Before recording an answer in the child, the API
reserves a bounded authority record in operator-owned Session status. Its digest
binds the exact question, context, options and answer to the Session, Pod, native
thread and writer generation. It contains no question or answer text. The API
confirms that authority only after reading a matching durable child response.
POST 202 acknowledges both confirmations. A lost response leaves a reserved,
uncertain operation and does not authorize another answer exec.

The daemon checks the live authority through its authenticated API before answer
reservation and final paste. A locally written answer cannot authorize delivery.
A heartbeat cannot write that authority. Omission or a local delivery claim
lacking matching confirmed authority cannot clear escalation. This establishes
direct-parent authorization.
It does not attest that an answer came from Tom's phone: the parent must perform
the native question round trip, which still requires real-client acceptance.

The owning daemon waits for an initial native invocation's genuine owned Wait and
separate bounded observation of exact process-group absence. Its fsynced receipt
binds invocation ID, launch digest, confirmed native UUID, boot, Session UID, Pod
UID and writer generation. A private noninherited lifetime flock protects producer
and consumer; a free lock or missing PID is never an exit receipt.

Before reserving an answered decision, the daemon uses a five-second deadline for
the prior wrapper's lifetime lock to be available and exact tmux session to be
absent. These observations establish teardown readiness only. Unknown status or
an expired wait retains Answered without consuming the receipt or starting a
successor; continuation admission still requires the genuine causal receipt.

Continuation admission and final one-use consumption run under the common Git to
supervisor-stop ordering. They preserve the same launched shared writer and its
unchanged generation. The consumer durably replaces the exact Exited receipt with
the next Running invocation before native spawn. It resumes the confirmed UUID as
an empty-prompt TUI. The original task prompt is never replayed.

Answer delivery reserves ResumeStarting, confirms exact owned native startup, then
rechecks identity and stop under the supervisor and private record locks. Dispatching
is durable before the owned TUI's bracketed paste and one Enter. Unknown startup,
identity, stop, receipt or acknowledgment retains a nonreplayable uncertain state.
Delivered acknowledges local transport only; it does not prove model action.

Source tests use synthetic credentials and fake native CLIs only. The actual tmux
journey is a same-Pod proof. Native parent relay, owner phone prompt delivery,
cross-Pod continuation and real provider acceptance remain required separately.
The accepted 60-minute no-progress/three-identical-blocker watchdog in issue #154
is not implemented by this unit.
