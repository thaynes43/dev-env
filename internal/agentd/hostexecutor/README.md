# Owned native executor contract

This Linux-only package is an isolated candidate for D-84. The legacy `codex-host`
implementation is unchanged. `agentd owned-codex-host` is selected only by explicit pod configuration and
requires the real operator HTTPS authority. Configuration selects a route, not
admission. No local flag or permit file can replace authority. The package refuses a nil `Gate` before writing a
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
snapshot. Pairing, real native lifecycle acceptance, normal conversation
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


## HTTPS authority and authenticated readiness

The CLI accepts no arguments. It requires `AGENTD_OWNED_CODEX_HOST_ENABLED=true`,
the fixed operator Service HTTPS URL, `/var/run/secrets/dev-env/token`,
`/opt/dev-env/api-ca/ca.crt`, and the read-only keeper access-only projection at
`/opt/dev-env/codex-access/access.json`. `AGENTD_API_URL`,
`AGENTD_API_TOKEN_FILE`, `AGENTD_API_CA_FILE` and `AGENTD_CODEX_ACCESS_FILE` must
match those projections. The API token is reread for rotation; token and CA
reads accept bounded regular files only. The HTTPS client trusts the projected
CA, refuses redirects and proxies, and bounds every request.

`AGENTD_OWNED_TASK_UID`, `AGENTD_OWNED_TASK_EPOCH`,
`AGENTD_OWNED_TASK_DEADLINE`, `AGENTD_OWNED_HOST_ID` and downward-API
`DEV_ENV_POD_UID` declare the expected immutable binding. The adapter first reads
`GET /v1/task-budgets/{uid}` and requires observed, unlatched authority with all
five fields equal. It never creates a ledger or substitutes a later deadline.
`Run` then acquires its receipt lock and refuses old receipts before the real
`Admit` call. Only positive admission permits keeper access adoption. Each valid,
unlatched observation revalidates access; any authority or access error stops.
This adapter cannot create provider refresh credentials. The helper inherits
only PATH, private HOME/CODEX_HOME, fixed TMPDIR and locale, excluding alternate
ambient auth, storage and transport settings.

The CLI forces the image's `/usr/local/bin/codex` and version `0.160.1`. After
confirmed launch, a two-second readiness context permits at most 40 paced startup
socket connects, retrying only absent/refused sockets. It authenticates Unix
SO_PEERCRED UID/PID against the original boot/start identity and verifies the
original process is still live through a pidfd. It then performs the pinned Unix
WebSocket `initialize` / `initialized` exchange and verifies the version in
`userAgent`. RPCs are never retried. Only this success sets `NativeReady` and
`NativeVersion` in the private receipt. Readiness failure stops the owned tree,
retains the receipt, and returns failure even if termination is proven.
`NativeReady` records this bounded observation; it does not assert current
readiness, remote connection, enrollment, pairing or model/task recovery.

The production GitOps enablement remains OFF. The operator has no production
independent validator for campaign/snapshot provenance or complete native worker
effort. A nonnil authority callback alone is insufficient. Real lifecycle
acceptance requires a separately reviewed finite authority whose admissible
workload excludes owner clients, prompts, model turns and child inference. A
fresh local permit file or permissive fake Gate is not that acceptance. Source
checks use only synthetic TLS projections and inert native socket/process
fixtures. They do not close the real native or accounting gates.
