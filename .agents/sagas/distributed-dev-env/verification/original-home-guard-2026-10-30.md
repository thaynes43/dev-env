# OP252 original HOME guard: source contract and qualification

2026-10-30. Author continuation `dev-env-1030-042543`, fresh source base
`9377637b8a1f253521118af6d2f934d7dbf27a3a`, includes recovery packet `34c1e02`.
[OP252](https://work.haynesnetwork.com/work_packages/252) journals 3041/3046
select this SOURCE increment; the current direct work order transfers the
stopped 032005 author's custody. Q-252-01 no longer blocks the source choice.
The runtime decision remains held. No runtime check or action is evidence here.

## Contract

An omitted `--original-home-binding` leaves existing behavior unchanged. The
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
