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
CODEX_INTERNAL_APP_SERVER_REMOTE_CONTROL_DISABLED=1 codex app-server --managed-daemon --listen unix:///PRIVATE/socket
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

The production GitOps enablement remains OFF. The operator still requires an independent inspector of the finite campaign,
exact host/pod deadline, owned readiness/stop evidence and native home provenance.
A nonnil authority callback alone is insufficient. Native/provider internal usage
may remain unknown or estimated and must be reported that way; missing estimates
must never disable time or attempt limits. Real lifecycle
acceptance requires a separately reviewed finite authority whose admissible
workload excludes owner clients, prompts, model turns and child inference. A
fresh local permit file or permissive fake Gate is not that acceptance. Source
checks use only synthetic TLS projections and inert native socket/process
fixtures. They do not close the real native or accounting gates.

## Explicit ordinary root source adapter

`--owned-root-policy=<ConfigMap>` selects the separate ordinary root inspector,
with exactly one configured coordinator and assigned campaign; default remains
off and the fixed zero-task fixture remains separate. `OwnedRootPod(policy)`
constructs the exact source profile without creating a Pod. The operator-owned
`owned-root.json` binds the task, finite spec, immutable agent image, original
HOME PVC UID, authorized `.codex` baseline and approved network-policy UID/spec
hash. Signature/source review, sole trusted exec-client ownership, effective
Cilium/storage isolation, credentials and installation remain separate gates.

The original supervisor must be direct namespace PID1, nonroot1000, with no
sidecars/init containers, host namespaces, elevated capabilities or writable
image. PID1 and its authenticated helper become nondumpable before native starts.
The helper uses inherited executable FD4 when protected PID1's proc executable
is inaccessible. The server bounds concurrent connections to four and each connection to500ms;
one idle peer cannot serialize all probes. A fresh bounded challenge probes the original PID1 Unix peer,
boot/start identity and pidfd liveness, then reads protected memory. Ready proof
also rechecks the original native peer and pinned initialize exchange. Receipts
and worker-reported readiness are never inputs to operator authority.

The exec-to-main interval keeps Yama ptrace_scope1/2/3 and nondumpable
supervisor/helper/probe memory. CRI stdout/stderr are deliberately untrusted:
some runtimes create UID1000 FIFOs, and a sibling can preopen proc FDs before
probe userspace hardening. The operator discards those streams entirely. The
pinned immutable probe locally authenticates original PID1/native peer/RPC,
fresh challenge, exact binding/HOME/baseline and phase predicates. Only dedicated
nonzero process exit statuses40..44 (Prepared/Starting/Ready/Stopped/Continuable) traverse the
separate CRI status channel. Nil/generic success, absent/empty/truncated status,
signals, transport errors and cancellation refuse. No process identities are
reconstructed from output. Existing trusted exec-client ownership, fixed immutable
command/image, Yama and kernel proof remain necessary; no privilege or new keys.

The HOME claim must be Bound ReadWriteOncePod, rather than node-shared RWO. The
startup baseline traverses directory FDs without symlinks, bounds entries/bytes,
checks stable identities/content, and excludes only regular single-link rotating
`auth.json`. Owner-approved digest comparison happens before admission; native
mutations after launch are not rehashed as provenance. Unrelated recovery input
or aliases refuse. The private directories and approved recovery baseline must
already exist; this route does not prepare or erase an existing enrollment.

The guarded bootstrap preflights HOME authority before consuming an intent, then
writes and syncs a one-use intent and its parent directory
before one authenticated budget create. Conflict, unknown acknowledgement or
restart never triggers recovery/replay. The returned exact binding is frozen; its clock sanity check permits at most a
fixed five-second operator/node skew without rewriting the deadline.
Prepared permits initial Admit; Starting denies work without an authority-loss
latch; dispatch, launch reservation and private continuation require fresh Ready.
Unknown evidence stops work and retains accounting. Original native Wait, ECHILD
and successful helper Wait advance protected state to Stopped. The still-live
original guard supplies that proof for one stop-event request. An exact event
acknowledgement is required; it atomically latches the campaign while each child
stays charged until its own independent stop. Unknown replies remain unknown.

The source integration uses actual inert guard/native/helper processes, Unix
peer credentials/RPC, the production operator inspector, real TLS budget API and
isolated Kubernetes admission/storage. Only Cilium's stored object and token
identities are synthetic; no kubelet/datapath/provider exists there. A separate
hosted CI test uses nonroot PID1 in a bounded fresh PID namespace, including the
protected-parent FD4 path. Neither test establishes real Codex recovery, CRI
pipe compatibility, phone pairing or installed/native usable acceptance.


The optional owned-root continuation keeps the original living guard/Pod/HOME.
Its read-only frozen-startup check distinguishes stop43 from continuable44;
changed config/rules cannot advance the owner epoch. The existing authenticated
human route can authorize one epoch within the unchanged finite budget after all
stops are confirmed. Original intent, receipts, stop history and counters remain.
The next epoch gets a distinct receipt, and actual startup measurement is separate
from the original policy baseline. No automatic Pod-loss or exhaustion recovery
is implemented. Tests use inert helper/native processes, not provider sessions.

## Trusted owner thread/turn control

The existing authenticated-human API adds
`POST /v1/task-budgets/{uid}/native`, selected only when the existing explicit
owned-root policy configures this adapter. It accepts an exact immutable binding,
an explicit request ID, and one operation: `thread/start`, `thread/resume`,
`thread/read` (including turns), `turn/start`, `turn/interrupt`, or `host/stop`.
No arbitrary RPC/path, model/provider, approval, sandbox or credential override
is accepted. Thread creation/resume and turns pin `gpt-6-luna`.

Use `agent-run budget native TASK --once < request.json` as the existing human
caller. Request JSON contains `binding`, `id`, `operation` and only the relevant
`threadID`, `turnID`, or `text`. Prompts travel on stdin, never exec argv/logs.
Every mutation is reserved durably on the original ledger before dispatch. The
immutable exec command includes the canonical request SHA256; the helper checks
it, authenticates original guard/native peer evidence and independently consumes
that exact human reservation through the root-only `/native-consume` endpoint.
Consumption follows passive native initialization immediately before the RPC.
A same-UID process cannot create authority by invoking the helper directly:
unreserved, modified, already-consumed and ambiguously acknowledged requests
refuse. A racing copy of the exact approved payload can only consume that same
one-use authority. Never replay an unconfirmed mutation or substitute a new ID
for an attempted turn without a new explicit owner decision.

The audit reuses existing `activity` event envelopes with private evidence-kind
markers, bounded hash event IDs and existing history limits. External activity
submission only observes; it cannot append a forged owner receipt. Old ledger
serializers preserve these inert fields. This does NOT make older operators
understand the new stop-request status; mixed-version enforcement remains
unaccepted. No original deadline, effort, checkpoint, worker, stop receipt or
failed attempt is cleared. Passive history reads reserve/consume nothing.

`host/stop` commits an owner intent; the original living guard sees the request
and stops its owned native tree. The intent is not termination proof and frees
no charged worker. Observing expected Stopping readiness loss does not poison
continuation with a new authority-failure latch. Only the existing independent
Wait/ECHILD/helper/CRI stop validation records `NativeRootStopped`. The existing
human `budget continue --decision ID TASK` can then authorize one epoch on the
same guard/Pod/HOME inside the original budget. Old audit and stop events remain.
Unknown/exhausted/deadline-expired campaigns and Pod/container loss refuse.
Turn interrupt is a native turn operation, not whole-tree stop.

Native result/history stdout is **untrusted content**, including against a
same-UID preopened-CRI-FD forgery. It is never used as instruction-loading,
served-model, readiness, stop, budget or continuation acceptance evidence.
The existing dedicated CRI evidence statuses and kernel/native checks remain
unchanged. Response bounds retain writer errors even when CRI drains them.
A `turn/start` result is an acceptance acknowledgement; use supported
`thread/read` to observe completion/history. No native JSONL/SQLite/auth store
is read by this adapter and no interactive client or pairing is opened.

The actual pinned0.160.1 test uses one scripted loopback Responses exchange,
private HOME, no credentials and no real provider/model. It proves a turn stays
on the original managed daemon when its control WebSocket closes and that a
later connection reads completed history/resumes the same thread. It does not
prove owned-guard integration, remote enrollment, live model/rule loading,
phone steering or an active-session operator upgrade. Run serially with
`DEV_ENV_PINNED_NATIVE_BINARY` pointing to verified0.160.1; ordinary CI without
that artifact reports the optional local protocol proof as skipped.

Operator and task-agent publications must match before live use. Image adoption
is separate from the exact root profile, existing credential-reference consumer
and provider/network authority. Per-attempt policy/input ConfigMap names must
match the existing operator Roles; this does not grant general admission.

The fixed helper omits `--remote-control` and sets the pinned internal startup
environment `CODEX_INTERNAL_APP_SERVER_REMOTE_CONTROL_DISABLED=1`. This selects
`DisabledEphemeral`, ignoring retained enablement without editing native stores.
Pinned0.160.1 can automatically enroll
a fresh backend computer under that flag before any pairing-code request. The
approved bounded root uses the local managed Unix RPC daemon only, matching the
actual pinned loopback test. It does not create/copy native remote enrollment.


Cold native catalog startup is bounded separately from evidence probes. Pinned
0.160.1 allows five seconds for model-catalog refresh before fallback; the owner
command must leave time for its preceding fresh status/proof/initialize/consume.
The finite command/CRI/API limits are 12/13/16 seconds, each capped at the original
campaign deadline and caller cancellation. This changes no campaign allowance,
consume/retry behavior or process-proof predicate. A caller should allow eighteen
seconds for this response while preserving its original credential expiry and
cleanup reserve.

The opt-in `TestPinnedNativeColdCatalogFallbackWithinOwnerBound` uses the verified
`DEV_ENV_PINNED_NATIVE_BINARY` and a public frozen inputs ConfigMap JSON supplied
as `DEV_ENV_FROZEN_INPUTS`. It retains the original config/common-rule/HOME bytes,
adds only loopback network overrides and synthetic access-only authentication,
and delays catalog metadata past the pinned five-second fallback with realistic
preflight/CAS latency. No inference is requested. The inherited host requirements
are not a substitute for the task's mounted system requirements; this test proves
the protocol timing seam, while live hash receipts prove the actual task floor.
Fixed protected helper exit diagnostics80–88 describe refusal phase only; stdout,
stderr and native content cannot grant budget or stop/continue authority.


### Complete timing composition

The owner-operation budget covers **all** preceding work, not just the final
native RPC. Two authority calls can each take most of two seconds; a local
proof allows800ms and the pinned catalog fallback takes five seconds. The
12-second command bound includes those phases. CRI allows another second for
exec transport; the16-second API includes reservation and ControlTarget proof
before that exec. An18-second external caller retains its original credential
expiry, cleanup reserve and no-replay rule. These are finite processing bounds,
not additional campaign allowance or permission to accept stale evidence.

| Path | Bound and cancellation owner |
| --- | --- |
| Bootstrap/admit/binding | Existing2s authenticated HTTP envelope; one bootstrap intent, no create replay. Admission is capped at the original binding deadline. |
| Guard heartbeat | Cadence1s; complete observation2s capped at original deadline/caller cancellation. The inner CRI proof alone allows1s, so a1s outer context left no room for auth, metadata or response. Any error/refusal stops immediately; no grace, cached success or automatic operation retry. |
| HTTP connection | Dial/TLS1s each, header/client2s total; idle connection timeout1s is connection reuse, not authority lifetime. Cold/reused connections remain subject to the same complete call deadline. |
| Independent proof | Native readiness300ms inside server500ms, local helper800ms, CRI1s. Original peer/PID/challenge/binding and explicit exit40–44 remain required; missing/truncated status never proves success. |
| Initial readiness | Existing2s within original deadline. Absent/refused startup sockets may be observed; initialized RPCs are never replayed. |
| Helper launch/confirmation | Existing5s backstops; helper validates original binding and independently enforces its absolute deadline. No launch/confirmation limit extends that deadline. |
| Stopped continuation | GET2s capped at original deadline; failed GET leaves the original process stopped. Only exact fresh human-authorized epoch2 permits once-only continuation. |
| Stop | Existing configured3s tree stop plus2s helper wait; independent Wait/reap required. Receipt reporting has2s after stop, intentionally allowing evidence retention after deadline. It grants no running authority. |

The latency regression uses real TLS/API/Kubernetes storage, original guard and
kernel evidence, with bounded metadata/exec scheduling delays. The old outer
context canceled at1.001s; the corrected fresh response completed at1.273s,
then actual authority revocation produced original Wait/reap in265ms. Ordinary
unchanged observations do not write ledger accounting or create CAS contention;
this test does not invent an observation write conflict. A second regression
uses the real pinned native process/frozen public inputs and two1.6s authority
phases: old8s command timed out after consuming once; corrected command returned
one thread acknowledgement at8.236s, with zero inference. Eight concurrent
real native readiness handshakes completed within the original300ms limit
(maximum5.8ms) during that cold fallback. These tests diagnose
processing composition, not the unobserved historical source of live latency.
They do not prove rolling-upgrade, live-model, continuation or phone acceptance.
