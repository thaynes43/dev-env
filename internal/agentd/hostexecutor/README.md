# Owned native executor contract

This Linux-only package is an isolated candidate for D-84. The legacy `codex-host`
implementation is unchanged. `agentd owned-codex-host` refuses execution until a
trusted off-pod authority adapter is wired; local flags and environment variables
cannot turn that command on. The package refuses a nil `Gate` before writing a
receipt. This source unit does not enroll a computer or run a real Codex turn.

The adapter supplies an immutable `Binding` (`TaskUID`, `Epoch`, `Deadline`,
`HostID`, `PodUID`), admits that exact binding, and observes its durable latch.
Admission errors refuse launch; observation errors, a latch, cancellation and the
absolute deadline stop the owned executor. Admission and observation calls have
bounded cancellation. The dedicated helper also enforces the deadline and parent
control loss independently. An adapter must reject identity/epoch mismatch and
must not interpret an arbitrary HTTP 200 as admission. The MVP assigns one logical
campaign and its children to this host; this is a whole-executor fence, not
per-thread concurrency or a native prompt hook.

`Run(ctx, Config, Gate)` uses the current agentd executable as `HelperBinary` and
an absolute pinned Codex executable as `NativeBinary`. The private home and receipt
parent must already exist. The native command is fixed:

```
codex app-server --remote-control --managed-daemon --listen unix:///PRIVATE/socket
```

The helper accepts only an inherited socket from its parent with the same UID and
executable. It owns `cmd.Start`/`cmd.Wait` and enables/reads back Linux child
subreaper mode. Before stopping, it captures boot ID/start ticks, opens pidfds,
checks identity and ancestry again, freezes all threads before enumerating their
children, and signals exact pidfds. Process and thread enumeration have caps.
After termination it observes the root's `Wait`, reaps adopted descendants until
`ECHILD`, and exits. The supervisor must also observe successful helper `Wait`
before writing `Stopped`. No process-group disappearance is accepted as proof.
A reused root number is recognized as the original root being gone, and its
replacement is never opened or signaled; original native Wait and ECHILD remain
mandatory. Unsupported pidfds, permission errors, changed descendant identity/ancestry, enumeration caps
or elapsed stop budget produce an uncertain stop, retained as `Stopping` rather
than `Stopped`. Independent helpers cannot reap another executor's descendants.

Receipts are private, atomically replaced and fsynced at `Attempting`,
`Confirmed`, `Stopping` and `Stopped`. `Confirmed` means owned process launch;
it does not mean native RPC readiness. Every receipt path is one-use, including a
completed one. An interrupted receipt is retained byte-for-byte and requires
explicit review; neither a restart nor a fresh epoch automatically rearms it.
Home, enrollment, native snapshots and shared WIP are never removed or rewritten
by this package. A forced tree stop does not promise a completed native recovery
snapshot. Pairing, native version/socket-peer verification, normal conversation
recovery, owner notification and authentic progress/failure accounting remain
separate acceptance work. The native capability gate stays off until those
required seams are integrated and proved.

The finite test binary supplies an inert foreground process, a session-detached
intermediate and a second session-detached orphan. Tests stop the active tree via
latch, authority failure, deadline and an actual supervisor SIGTERM, then check
immutable root/leaf identities are gone and fixture WIP/enrollment/snapshot bytes
remain unchanged. Tests also refuse missing authority, interrupted receipts,
automatic rearm, changed start identity and uncertain stop proof. They perform no
network, credential, model, real-provider or cluster operations. Run serially:

```
nice -n 19 env GOMAXPROCS=2 go test -p 2 ./internal/agentd/hostexecutor -count=1 -timeout=35s
```

The caller must bind this private home's preexisting native recovery state to the
same admitted campaign. The pinned managed daemon can consume a saved snapshot
at startup; this package does not interpret that snapshot or authenticate its
thread/task provenance. A fresh receipt must not license unrelated saved work.
