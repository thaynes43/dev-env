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

Review correction `REMOTEPILOT286-FIX-20261030-03` consumes PR #713's
immutable `997003c65fe50c82a68c2a29dc0ee9e62b70b70c` checkpoint and rebases on
fresh `origin/main` (`94b3b09`), without conflict. OP286 journal 3210 corrects
the lead's live non-secret field read to about **07:44Z**, rather than the
estimated 07:47Z in the order. All six findings are addressed below; this round
changes production behavior only by requiring the remote image's release tag.

The shared test-only witness is
[`internal/homeguard/testutil/testdata/root.json`](../../../../internal/homeguard/testutil/testdata/root.json)
(SHA256 `299a34acc4dc98208eb4c69a8fb39480d44d83c2ce7d2409f64c00eea651ec0f`).
It carries Root's specified non-secret namespace/name/UID, parent/key/depth,
repo and Codex remote mode, original Bound PVC/PV, owner/claimRef, reclaim
policy and existing finalizers. Each stage reads this one fixture; the six-field
binding is derived from its objects. These are fixture witnesses, not live
observations. Its covered Session fields now match the lead's supplied values:
`codex`, `origin/main`, `remote`, `gpt-6-astra`, `Running`, parent `dev/dev-env`,
repo `dev-env`, size `S`, **no effort and no profile**. Its labels are the given
depth `0` and idempotency key; its Session rescue finalizer is unchanged. PVC
storage remains `40Gi` on `gasha01-rbd`, with both rescue and PVC-protection
finalizers. PV storageClass is `gasha01-rbd`, reclaim `Delete`, phase `Bound`,
with all three supplied provisioner/protection/attacher finalizers. Session,
PVC and PV UIDs, PV name, PVC name and ownership/claimRef match the supplied
identities. No supplied live value required a local substitution.

Local boundaries remain explicit: this is a projection of the supplied fields,
not a complete live-object dump. Live resourceVersions, timestamps, other status
fields and the CSI volume source are not reproduced. The templates ConfigMap is
`internal/templates/testdata/templates.yaml`, including its synthetic default
2.0.0 image, default profile `full` and size-S resources; it is not the live
ConfigMap. The task-pilot target/image is synthetic. These do not change the
covered Root spec fields: the real `sessionDocument` renders **no `effort` key**
in `AGENTD_SESSION` when Root's effort is absent. Stage A checks size S and the
omitted effort/profile, and Stage B consumes that rendered document.

| Stage | Named test and actual boundary | Link/evidence |
| --- | --- | --- |
| A, operator | `cmd/dev-env-operator: TestRemotePilotRootStageA` (and `TestRemotePilotRootStageANegatives`) uses real `parseFlags`, `reconcilerForOptions` shared with `run`, and exported `Reconcile` on a controller-runtime fake client. The real parser accepts the templates ConfigMap. | Starts after sticky API admission with the Pod absent. Asserts original HOME claim/owner, Session/PVC/PV identity and retention, explicit binding, main and read-only init on the specified 2.69.0 digest, exact init command/args, usage target `dev-env-1028-031951` on 2.52.0, task pilot preserved, and default bytes with all pilot flags unset. |
| B, agent | `internal/agentd: TestRemotePilotRootStageB` reads A's golden, loads its actual `AGENTD_SESSION`, and runs the existing `verifyOriginalHomeAPI` implementation against a local TLS metadata server, then `Daemon.startAgent` → `codexRemote.ensure/start` with the existing Codex process/remote and tmux fakes and a local argv-recording executable. | The init check and both native-start checks read the same three identities. Exact process argv is `remote-control start --json`, with no prompt/turn/pairing argument. Unsafe/deleting/replaced identities and PV 403 cause no provider call. No real Codex binary runs. |
| C, CLI | `internal/agentrun: TestRemotePilotRootStageC` uses real CLI parsing/request serialization/response decoding through the existing local TLS harness. | The same fixture links A to C. A suspend ACK still has a simulated Pod; two `show --json --once` observations expose the transition to stopped/Suspended before resume. Each request extracts its target name from the route and resolves it in the fake API's object map; the recorded UID is that resolved object's UID. The success case resumes the original object. In the negative case a same-name replacement resolves to a different UID at resume, the fake returns 409 with the original-HOME refusal, and agent-run exits nonzero without polling or printing success. No local/native process runs. |

A compares the rendered fields byte for byte against
[`internal/homeguard/testutil/testdata/root-pod.golden.json`](../../../../internal/homeguard/testutil/testdata/root-pod.golden.json)
(SHA256 `e5673459639b04e77315ad97ff17fa28bd478f6e3635786f2c323d39df40692e`).
That committed golden is the A-to-B link: images, command/args, actual Session
and binding env, HOME claim, owner UID, and read-only init mount. It also carries
the rendered result after removing the remote pair AND startup binding while
the admitted annotation remains. Assertions now inspect Stage A's actual
Reconcile render: `original-home-verify` stays on the templates image with
command `[/usr/local/bin/agentd]`, args `[home-verify]` and a read-only HOME.
The complete main container is unchanged after accounting for the expected
image change back to the templates image. The prior self-written old-agentd
script has been removed; an old image's **unknown-command refusal is UNVERIFIED
locally**. Kubelet ordering and old-image qualification remain untested.

Golden regeneration method for this round: temporarily insert an
`OP286_GENERATE_ROOT_GOLDEN=1` branch immediately before Stage A's final golden
comparison. It writes the `json.MarshalIndent(fixture.Golden{Selected: witness,
SelectorRemoved: removed}, "", "  ")` bytes plus a newline to
`/tmp/t/root-pod.generated.json`; both witnesses come from the test's real
`parseFlags` → `reconcilerForOptions` → `Reconcile` path. Run exactly:

```sh
GOTOOLCHAIN=local GOMAXPROCS=2 GOTMPDIR=/tmp/t TMPDIR=/tmp/t \
  OP286_GENERATE_ROOT_GOLDEN=1 nice -n 19 go test -p 1 -count=1 \
  ./cmd/dev-env-operator -run '^TestRemotePilotRootStageA$' -timeout=5m
```

Copy that generated file to `internal/homeguard/testutil/testdata/root-pod.golden.json`,
remove the temporary generator branch, then run the required operator filter
without the generation environment. No handwritten JSON substitution or
permanent update switch is used. The test again byte-compares the committed
artifact consumed by Stage B.

Stage A's UID/deleting-PVC/replacement negatives assert the recorded
`RemovalBlocked=True`, reason `SuspendNeedsRescue`, and exact original-HOME
unsafe message. Its Session deletion case is honestly named `Session deletion
path`: it reaches the archive retention guard and asserts `DeleteNeedsRescue`
and the exact original-HOME message. The PV-read denial asserts the returned
Forbidden error names `persistentvolumes` and the injected `offline denied`
reason; that error returns before a status write. Parent/key mismatches assert
successful default rendering with sticky guard and no RemovalBlocked condition.
Stage B separately verifies deleting Session/PVC and final-native-check
replacement. Additional negatives cover binding
namespace mismatch in either flag order, and parent/key mismatch (no pilot
image). Collision refusal is tested for both other selectors in both orders;
changing either half permits coexistence. Remote image parsing now requires
`:2.X.Y@sha256:<64 lowercase hex>`; a digest-only refusal test protects the
signature-audit reference requirement, and help/docs require both tag and digest.

`internal/apiserver: TestOriginalHomeLifecycleReplacementUID/startup_binding_refuses`
uses the shared Root fixture with a different Session UID and no sticky annotation.
Valid original Root PVC/PV fixtures are present. With the startup binding
configured, the real lifecycle handler returns 409 with the original-HOME refusal
**before any storage GET or Patch**; the test counts both and compares the stored
object unchanged. This isolates the Session UID rejection in `homeguard.Select`
from a missing-storage or later binding-verification failure. Its `no_binding_is_unguarded` case
explicitly proves the limit: if the startup binding is removed and a replacement
has no sticky annotation, it is treated as unguarded and API resume is accepted.
Protection then falls back to the controller's PVC ownership check.
`internal/controller: TestOriginalHomeReplacementWithoutBindingKeepsForeignPVC`
proves that the original Session's PVC is recognized as foreign, computes the
`PodReady=False/NameTaken` condition through the real `podPhase` function,
creates no Pod and preserves the claim.
The existing stale-cache test
`TestOriginalHomeUnguardedCacheCannotBypassGuardedReplacement` covers the separate
case of a guarded replacement with stale unguarded cached metadata.

The existing
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

## OP288: suspend a verifier-only executor

Work order `INITSTUCK288-20261030-01`, [OP288](https://work.haynesnetwork.com/work_packages/288)
journal 3240, starts from `6af7f59a` (includes OP286 PR #715). The bounded review
fix round `INITSTUCK288-FIX-20261030-02` supersedes PR #719's immutable head
`2dace79b9f8ac949a201d545a1b776416df2214e`, rebased onto `38ffdf2`.
Suspend may use
the existing guarded Pod deletion without a rescue exec when the Session's
admitted binding and exact Pod ownership match, every named main container is
Waiting with an empty ContainerID, zero restarts and no current or previous run/termination, and the
sole init container is the operator's `agentd home-verify` command with the full
read-only HOME mount and no writable mounts or volume devices. Additional
read-only mounts, including ServiceAccount admission's token/CA/namespace mount,
are allowed. The verifier must still be Running or have FAILED, never succeeded:
its exclusive state is Running, Waiting after a nonzero last termination, or
Terminated with a nonzero exit code. It must not be Ready, and the Pod must
not report Initialized=True. A successful termination, including a previous
successful termination while Waiting, or Waiting with restarts but no last
termination retains the rescue requirement. The verifier image must equal
the named agent container's image. Its security context must match the agent's,
with no privileged mode, no added capabilities and explicit refusal of privilege
escalation; missing or changed security context fails closed. Missing,
duplicate or unknown main status, another init, writable HOME, altered init
command, ephemeral containers or uncertain ownership cannot use this exception.
This conservative exception does not apply to reap, active drain or hold Pods.
A stale Draining phase with Suspended mode still uses the suspend guard and
requires the same evidence. Deletion
still uses the existing UID and resourceVersion preconditions; no force deletion
or other resource mutation is added.

`TestOriginalHomeInitFailedSuspend` exercises the real Reconcile/removePod/deletePod
path using the existing fake rescuer and an admission-shaped projected-token
fixture. It starts with RemovalBlocked set, checks
zero rescue calls and condition removal on success, checks the normal delete
preconditions, and covers the unsafe/uncertain cases and unchanged unguarded and
reap behavior. Each review gap has a separate case: successful or uncertain
init states, Initialized=True, Running/Unknown Pod phases, native sidecar init,
subPath HOME, hold Pod, contradictory states, spoofed Command, Lifecycle hooks,
VolumeDevices, main ContainerID, Draining plus Suspended, and altered init image
or privilege settings. Contradictory main Started/Ready reports also require rescue.
Foreign Pods remain untouched without exec.
`TestOriginalHomeInitFailedArchiveStillRetainsHOME` proves the stronger existing
original-HOME archive refusal even with a verified rescue, and separately checks
that the legacy volume guard still requires verified rescue when the Pod is gone.
Existing hold-Pod archive/rescue tests cover the legacy rescue execution path.
The default Pod golden remains
`c91e28583563011e5e83da0056e2965be4d79bc44212d4dafd2d494029e75e1c`.

PR #719's initial local qualification used checksum-verified Go 1.27.3, envtest Kubernetes 1.35.0,
`GOTOOLCHAIN=local GOMAXPROCS=2 GOTMPDIR=/tmp/t TMPDIR=/tmp/t`, and
`nice -n 19 go test -p 1 -count=1`. The required controller filter plus all tests
in the existing guard/rescue/bootstrap test files passed: 325 test/subtest
events, 41.691 seconds. Native Sol review caught the admission mount mismatch;
that round's reviewer reported no remaining source findings after its correction.
The later independent adversarial Opus review found the succeeded-init regression
and the ContainerID/image/privilege gaps addressed by this fix round.
The initial reviewer did not run tests or runtime checks.
`make GO_PARALLELISM=1 GOMAXPROCS=2 check-generated check-layout lint` also
passed with the same environment; lint reported zero issues.

The fix-round focused run on code head `7be59054ca91dd80960452875cb38c8c3b128c4b`
passed the exact requested controller filter: 88 top-level tests, 286 test/subtest
passes, zero skips or failures, 41.106 seconds. All 52 suspend fixture cases
passed, including the review gaps above, as did `TestTmpDefaultPreservesPodBytes`
with its unchanged hash. It used the same caps and `/tmp/t` after a fresh
`make GO_PARALLELISM=1 GOMAXPROCS=2 envtest` setup.
The fix round also passed
`make GO_PARALLELISM=1 GOMAXPROCS=2 check-generated check-layout lint` with
the same environment, serially after that suite; lint reported zero issues.
Independent native `gpt-6.1-sol`, `xhigh`, reviewer `/root/op288_review` reviewed
the three-file delta from rebased predecessor `74134bc` and found no in-scope
source findings. It ran no tests or runtime checks and changed no files.
Exact securityContext equality is intentionally conservative: harmless admission
differences also retain the Pod.

The reviewer also identified an unchanged legacy limit: `podNeverStarted` can
accept contradictory zero-restart status (for example Waiting plus Terminated,
Ready or Initialized=True), independently of this exception. Those synthetic
shapes were already accepted before PR #719; changing that helper would extend
this round into excluded legacy never-started/reap/hold behavior. This is recorded
on OP288 for a separate coordinator scope decision, with no follow-up execution
here. The new negative cases retain the failed/restarted verifier fixture to
isolate this exception; they do not claim to harden the legacy helper.

Residual risk shared with the existing `podNeverStarted`: kubelet status lag
can briefly hide a just-changed container state, even before this API read.
Deletion retains its UID and resourceVersion preconditions, which fence API
changes after the read but cannot eliminate kubelet reporting lag. Suspend never
deletes the HOME volume, and any later archive still requires a verified rescue
under the existing archive guards.

This increment is **source-tested, not runtime-accepted**; local results and the
independent native review receipt are recorded on OP288 and its PR. No installed
operator, HOME, RBAC, finalizer, history or lifecycle action is part of this work.

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
