# OP252 original HOME guard: source contract and qualification

2026-10-30. Author continuation `dev-env-1030-042543`, fresh source base
`9377637b8a1f253521118af6d2f934d7dbf27a3a`, includes recovery packet `34c1e02`.
[OP252](https://work.haynesnetwork.com/work_packages/252) journals 3041/3046
select this SOURCE increment; the current direct work order transfers the
stopped 032005 author's custody. Q-252-01 no longer blocks the source choice.
The runtime decision remains held. No runtime check or action is evidence here.

[OP286](https://work.haynesnetwork.com/work_packages/286) adds the separate default-off [remote pilot image selector](../../../../docs/portal-session-runtime.md#separate-codex-remote-pilot-image-default-off-source-only) for OP252's slash-parent image path (Option B); activation remains held.

## Contract

An omitted `--original-home-binding` leaves existing behavior unchanged. Unguarded
Pod bytes remain identical: no empty guard environment entry or init container
is added. Only an admitted guarded Pod receives the explicit binding. The
optional single JSON tuple has namespace, session name, exact Session UID,
original PVC UID, PV name and exact PV UID. It grants no read or write access.
Only an ordinary remote Codex Session without workspace, pilot, private history
or task budget is eligible. The flag is configuration, never request input.

The existing authenticated resume API checks uncached Session/PVC/PV
objects. It rejects missing, replaced, foreign, deleting, unbound or unprotected
objects, a changed volumeName or PV claimRef, and a Session that changed during
admission. Its existing optimistic Session patch durably records the exact
binding annotation with the operating-mode change. Suspend remains available to stop a session
even if storage has become unsafe; it preserves any admitted annotation.
Even a Running resume no-op
must first validate and, if needed, admit the configured tuple. A repeated
identical admitted no-op keeps its existing 200 behavior. The annotation remains
effective after the flag is removed; removal is not an implemented reset route.
No live flag or annotation is set by this source change.

Admission deliberately refuses a changed Session resourceVersion, including an
unrelated status write between its two reads. That conservative 409 causes no
lifecycle mutation or provider dispatch. The existing loop still retries Patch
conflicts; it does not automatically reinterpret an uncertain admission snapshot
as safe. This availability limit is retained for this preservation increment.


Unavailable Kubernetes reads return to the controller’s existing error backoff;
missing or mismatched identities remain preservation refusals. This also keeps
unguarded legacy creation from stalling after transient read failures.

Controller recreation checks the durable binding with uncached reads before
building and immediately before Pod Create. Cached claim absence cannot enter
the replacement-PVC branch. It never creates a missing guarded HOME. Existing
active Pods are left alone. The existing archive/delete guard retains the
original rescue finalizer and refuses guarded HOME deletion. A configured target
is protected even before the API has admitted it; archive also reads the latest
Session annotation to avoid a stale cache hiding a durable binding. No size,
storage class, reclaim policy, archiveAfter or expiry annotation is changed.

OP285 source follow-ups reserve `AGENTD_ORIGINAL_HOME_BINDING` against explicit
template/profile env and list `home-verify` in agentd help. The unused binding
remains absent from unguarded Pods; this reservation does not mask `envFrom`.
Operator startup rejects a binding namespace different from `--session-namespace`,
after parsing all flags so argument order does not matter.

The proposed OP285 Session GET optimization is deferred pending a scope decision
on the canonical item. Native independent review found that even immutable
agent/mode cannot prove a cached Session is unguarded: a same-name replacement
has a different UID and can be guarded while the cache still holds the old
unguarded Session and HOME. Skipping the live identity check could create an
unguarded Pod mounting the replacement HOME by name. Merely checking cached
annotations or startup configuration also fails the existing sticky-binding
regression after flag removal. No read or guard check is removed. New regression
tests count the existing unguarded Session GETs and require stale unguarded state
to refuse creation/archive against a guarded replacement. Existing guarded,
stale-cache and backoff tests remain unchanged. A safe optimization needs an
explicit fresh identity observation to reuse, beyond a cached eligibility test.
Q-285-01 was emitted through the native async question tool: ship the three safe
fixes and defer the optimization (recommended), or expand OP285 for fresh
observation reuse. Emission does not prove phone delivery or an owner answer;
none has been observed at this source checkpoint. OP285 remains incomplete.

## Actual mount and provider boundary

Two API reads are not an atomic mount fence. Kubernetes Pods reference PVCs by
name, with no PVC-UID precondition. This increment therefore requires the
**existing** rescue finalizer on the original PVC and PV protection finalizer on
the original Bound PV. Neither finalizer is added, released or repaired here.
The bound claim's volumeName and PV volume source are immutable in the supported
Kubernetes lifecycle. Keeping those objects anchored prevents ordinary deletion
and name reuse from silently substituting a different HOME. A privileged writer
that strips finalizers or changes binding metadata is outside that assurance;
this source does not install an admission policy to constrain one.

A new guarded Pod also has a small init container from its selected agent image.
It mounts the same `home` volume read-only and runs `agentd home-verify` before
main boot writes. It has the same non-root security settings and bounded
resources, no profile env/Secret mounts, and uses the Pod's **existing**
automounted ServiceAccount token. It performs only three bounded GETs against
the fixed in-cluster Kubernetes API: its Session, original PVC and named PV.
It never uses HOME kubeconfig, grant contexts, proxies, redirects, provider RPC,
credentials from HOME or storage contents. Missing permission fails closed.
An older agent image without the command also fails closed; activation needs
an explicitly qualified guard-capable agent image, not just an operator update.

`codexRemote.start` repeats the read-only verification before its auth sync, stale
cleanup or settings writes, and again immediately before each native start.
Existing bootstrap Render and supervisor-loop credential sync can still write
the anchored original HOME before this start-specific gate. This increment does
not change those paths or promise a write-free original HOME.
The repeated check is necessary because supervisor retries and main-container restarts do
not rerun init. An already live daemon is neither gated nor restarted.
The native argv stays exactly `remote-control start --json`; no old prompt,
conversation, turn, pairing, host reset or new identity is dispatched.

**Precise limit:** a DELETE can set deletionTimestamp after the final GET and
before native spawn. Read-only verification cannot atomically exclude this.
The existing finalizers preserve the original object/data identity during that
window; the next verification refuses observed deletion. This is an original
identity fence with observed-deletion refusal, not proof of a nondeleting
object at the CPU instruction that spawns the provider. An absolute exclusion
of that race would need a separately reviewed coordination/admission mechanism
and is not smuggled into this source increment. The same limit applies to a
privileged claimRef change after the last observation. No runtime approval
packet may promise a stronger guarantee.

The copied historical operator RBAC fixture lacks PV GET. Current installed
operator authority has not been checked here. Do not activate or expand RBAC to
make the guard pass: verify the existing authority as a separate read-only
prerequisite and preserve any denial. The existing session read identity is
reused without a new SA, role, policy or credential.

## Focused evidence and remaining gates

Tests cover default-off admission, unchanged original HOME/PV, absent/replaced/
foreign/deleting claims, changed PV/claimRef, missing protection, sticky binding,
acceptance-to-recreation and final-create races, a real-API DELETE/name-reuse
race with the existing finalizer and bound-volume immutability, read-only startup checks,
mutation after successful init and immediately before native start, forbidden
reads, retry refusal, unchanged active RC and exact native argv. They inject
real state changes into fake Kubernetes readers and an HTTP API fixture; they
are source race tests, not a CSI/runtime race trial. HOME spec and existing
finalizers remain unchanged and no replacement claim is created.

Validation and independent exact-head review receipts belong in the PR and
canonical OP item. Required hosted `CI - Success` must be green on the final
head. No CRD or generated API changes are part of this increment. No operator,
agent image or template rollout follows from the PR. Built, published,
installed and usable/accepted remain separate states.

## OP286 linked local proof (2026-10-30 continuation)

Work order `REMOTEPILOT286-FIX-20261030-02`, OP286 journal 3189, consumes
PR #712's immutable `5ece982dd41d091d74ffd374b78e5df1c93ac6fe` checkpoint.
The continuation rebases onto `94b3b09` (#710); its namespace refusal and
replacement checks remain intact. Startup now refuses a remote parent/key
tuple equal to either legacy or task pilot tuple, in either flag order
(`TestRemotePilotAgentFlagsRefuseCollisions`). Legacy precedence in
`sessionPod` remains defense in depth; startup flags cannot reach overlap.

The shared test-only witness is
[`internal/homeguard/testutil/testdata/root.json`](../../../../internal/homeguard/testutil/testdata/root.json)
(SHA256 `d1edc5eec4e4d20314356d75c03ab6a8b907059df7eb5b113c2152f53863bbc8`).
It carries Root's specified non-secret namespace/name/UID, parent/key/depth,
repo and Codex remote mode, original Bound PVC/PV, owner/claimRef, reclaim
policy and existing finalizers. Each stage reads this one fixture; the six-field
binding is derived from its objects. These are fixture witnesses, not live
observations. The fixture also supplies model/size/profile values needed for
valid local rendering, and a synthetic task-pilot target/image.

| Stage | Named test and actual boundary | Link/evidence |
| --- | --- | --- |
| A, operator | `cmd/dev-env-operator: TestRemotePilotRootStageA` (and `TestRemotePilotRootStageANegatives`) uses real `parseFlags`, `reconcilerForOptions` shared with `run`, and exported `Reconcile` on a controller-runtime fake client. The real parser accepts the templates ConfigMap. | Starts after sticky API admission with the Pod absent. Asserts original HOME claim/owner, Session/PVC/PV identity and retention, explicit binding, main and read-only init on the specified 2.69.0 digest, exact init command/args, usage target `dev-env-1028-031951` on 2.52.0, task pilot preserved, and default bytes with all pilot flags unset. |
| B, agent | `internal/agentd: TestRemotePilotRootStageB` reads A's golden, loads its actual `AGENTD_SESSION`, and runs the existing `verifyOriginalHomeAPI` implementation against a local TLS metadata server, then `Daemon.startAgent` → `codexRemote.ensure/start` with the existing Codex process/remote and tmux fakes and a local argv-recording executable. | The init check and both native-start checks read the same three identities. Exact process argv is `remote-control start --json`, with no prompt/turn/pairing argument. Unsafe/deleting/replaced identities and PV 403 cause no provider call. No real Codex binary runs. |
| C, CLI | `internal/agentrun: TestRemotePilotRootStageC` uses real CLI parsing/request serialization/response decoding through the existing local TLS harness. | The same fixture links A to C. A suspend ACK still has a simulated Pod; two `show --json --once` observations expose the transition to stopped/Suspended before resume. Every request resolves the same fixture name/UID, and resume returns Running on it. No local/native process runs. |

A compares the rendered fields byte for byte against
[`internal/homeguard/testutil/testdata/root-pod.golden.json`](../../../../internal/homeguard/testutil/testdata/root-pod.golden.json)
(SHA256 `7a1ed022f42117d2fef7af26dfcdcba43ad0ccf5a5f7aa8483d67f00139be45e`).
That committed golden is the A-to-B link: images, command/args, actual Session
and binding env, HOME claim, owner UID, and read-only init mount. It also carries
the rendered result after removing the remote pair AND startup binding while
the admitted annotation remains. The guard is still explicit; a local simulated
legacy agentd without `home-verify` exits 2 and does not enter main/native.
This simulation does not qualify an old image or prove kubelet ordering.

The negative subtests cover Session/PVC/PV UID mismatch, deleting Session/PVC,
same-name replacement, final native-check replacement, forbidden PV GET, binding
namespace mismatch in either flag order, and parent/key mismatch (no pilot
image). Collision refusal is tested for both other selectors in both orders;
changing either half permits coexistence. The existing
`TestTmpDefaultPreservesPodBytes` still requires the original
`c91e28583563011e5e83da0056e2965be4d79bc44212d4dafd2d494029e75e1c` golden.

**Limits and untested boundaries:** this is a linked local proof, not full
end-to-end or runtime acceptance. The three linked stages do not exercise the
real API server/admission (including actual lifecycle admission), real kubelet
or init ordering, CSI attachment/mounts, the real Codex client or pairing,
installed PV GET RBAC, or the phone client. Existing focused controller envtests
cover some API resource semantics separately; they do not close these live
gaps. The in-cluster verifier's service-account token/CA loading is also outside
the injected HTTP verifier boundary. The rest of boot and the supervisor
poll/retry loop remain outside this `startAgent` boundary. Native remote starts do not use tmux; the existing tmux
fake records no call, and no real tmux runs here. Agent-run lifecycle wire requests
contain the session name, **no UID precondition**; C proves stable resolution and
UID observations in its fake API, not replacement fencing on that wire.
Queued remote-client tasks remain **UNKNOWN**, outside the local no-replay
proof. No live API, native client, RBAC, image, flag or lifecycle action occurs.

Focused local results and the independent native review receipt are recorded
on OP286 and in the superseding PR. Hosted checks, publication, installation
and owner acceptance remain separate.

## Deferred exact recovery approval packet

After source qualification, first establish guard-capable code and consistent
exact configuration across all API/controller replicas in an exclusive lifecycle
window. A mixed old/new operator rollout is not an activation fence.
Separately verify existing permissions, compatible
image, exact tuple, original HOME protection, ordinary-remote eligibility and
the target's stopped state. The recorded witnesses in the prior recovery packet
must be read again, never treated as current proof. Activation of the exact
binding and a single supported suspend/wait/resume cycle need explicit runtime
authorization. The current work order authorizes none of those actions.

The supervisor retries with 5-second exponential delay capped at 5 minutes,
indefinitely; it resets the failure count after one minute stable. Approval of
one lifecycle cycle therefore covers multiple possible daemon starts unless a
separate bounded-start source change is reviewed. Supported hold is the normal
Suspended mode after the controller removes the old Pod. There is no existing
independent no-autostart hold for a Running ordinary-remote supervisor. Do not
substitute a host receipt reset or direct Pod deletion. Client-side confirmation
of the old paired identity and history is still required; connected/code-issued
status alone is insufficient. A missing identity stops the cycle, with no re-pair.
