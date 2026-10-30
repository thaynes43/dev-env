# Bounded maintenance source preparation (OP233 / OP219)

Source base: dev-env 030133dc9dd9ac24a2c61f30f12ee8e402bd4bf2.
Initial source author: dev-env-1030-170111; bounded policy-correction custodian:
dev-env-1030-184308, after180930 released source at709f4007 (OP233/3548). Lead dev-env-1029-132039 owns sequencing
and release. This packet authorizes no merge, live policy, grant, adapter activation,
node command, hardware operation or pilot. All execution defaults stay off.

Tom's October 30 16:52/16:53 direction approves source preparation for a generic
bounded task: the user approves its envelope once; agents choose commands inside
it. The historic OP233 fixed 300W helper is superseded for this source model.
OP304 automated approval review is backlog only. No fixed command allowlist,
new task database, orchestration framework or repeated worker approval.

The immutable AccessGrant scope pins coordinator/session and pod UIDs, explicitly
allowed direct children, target node UID, resource identity, capabilities, selected
device/mount paths, digest image, CPU/memory/runtime bounds and restore intent.
The API authenticates the configured existing human identity for approval; a bound immutable
annotation hands that decision to the existing broker.Decide. It is distinct from
unverified coordinator:string attestation. Asks remains held. Delegation uses the
same approved grant; each command rechecks authoritative parent/session/pod UIDs,
and uses only that grant's broker-owned executor. No token or general pod creation
privilege is given to a session. Extensions require a new immutable request and
approval, never an in-place expiry mutation.

Actual enforcement limits: SYS_ADMIN permits broad administration inside the
container and interaction with exposed devices/drivers. Neither an operation-class
label nor a task description enforces semantic command intent in the kernel.
Node/driver compromise remains possible. Host PID/network/IPC, privilege, arbitrary
host mounts, credentials and API tokens are excluded. Network is denied by the
executor's own NetworkPolicy. Explicit device files still require device/runtime
policy qualification: a hostPath file alone does not establish a working GPU
cgroup allocation. This source must refuse activation until those policies are
reviewed. No assertion of live GPU usability is made.

[ADR-012](../../adrs/012-bounded-maintenance-task-grants.md) records the dated owner ruling.

Expiry/release refuses new API commands immediately and requests deadline stop.
Kubernetes deletion or a missing/unreachable pod is not proof of termination.
Cleanup stays visible and pending until a terminal executor observation. Persistent
hardware effects do not revert with TTL. Restore/readback must be separately in
the approved scope and performed while authorized; status records evidence, not
an inferred rollback. No generic restoration engine is introduced.

Overlap preserved: PR597 50945ca3 touches CLI show/readiness/help; PR563 ab788643
adds general decision summaries and handoff; PR565 104d06d9 builds on563 and adds
selected credential sources plus fixes. None is closed, replaced or imported.
This task adds maintenance-specific gated routes/adapter; shared-file conflict
resolution belongs to the lead. Credential semantics remain unchanged.

Acceptance: offline fake/envtest constraints, authenticated decision replay fences,
worker replacement/parent checks, command freedom, lifecycle cleanup and truthful
restore status; immutable-head native review and required hosted CI. Built/tested,
installed and accepted are separate states. Policy packet is UNAPPLIED, against
haynes-ops f12bbf60057e1a09d654143ad5c3b54058571dcb (read-only reference).

## Policy custody and activation hold

`haynes-ops-UNAPPLIED.patch` is the exact proposed CRD/RBAC/admission/exec delta
against the commit above, generated without writing that checkout. Its adjacent
YAML files are review inputs, never applied fixtures or deployment authorization.
The patch adds a tokenless, unbound executor ServiceAccount; broker-only pod/network
management narrowed by grant ownership/shape; authenticated annotation provenance;
and an exec guard for ALL other baseline, workbench and grant identities. It leaves
the global baseline policy unchanged and adds only the broker-specific, Pod-only
partial exception described below; it creates no public endpoint or namespace.
The operator's existing API and pods/exec authority are reused; no grant identity
receives pods/create or pods/exec. Node and node-lease reads by the broker are stop
evidence only. Unrelated v1 behavior is preserved.

The pinned platform and web CCNPs select dev-agents broadly. The patch explicitly
excludes the maintenance label from both: Kubernetes NetworkPolicy isolation is
additive and cannot override an already matching allow policy. Offline selector
tests prove ordinary session selection remains and maintenance selection is refused.
This does not establish runtime Cilium enforcement.

The proposed Kyverno policy requires isolated policy qualification in the lead's
separate policy PR, including defaulting, update metadata and resource lookup RBAC.
This source's fake/API tests do NOT prove live Kyverno enforcement. Neither flag
may be enabled based on this source PR or this unapplied review packet alone.
Device cgroup/runtime allocation and digest image tooling are also not qualified
for actual hardware. Resource UUID and operation class are approved intent; driver
ioctls through shared control device files can affect more than that UUID. This
residual risk must be reviewed honestly before activation.

Rollback of source activation means disable the API (no new execution), disable
new broker materialization while preserving this broker's cleanup path, then wait
for pinned node/lease/container termination evidence and network cleanup. Do not
replace the broker with an older binary until every maintenance lease is cleaned
up. A definitive admission/auth/body create refusal records NotCreated and cleans the
owned network policy; ambiguous create errors remain Unknown and never recreate.
A stale/unreachable node keeps Unknown/StopPending visible and the finalizer
held. TTL begins when broker.Decide records approval; Kubernetes deadlines are relative to pod start and
are a backstop, not a promise of on-time termination on an unreachable node.
Persistent effects need the approved restore/readback, independently recorded.

CLI source paths (held until reviewed activation):
`agent-run grant maintenance-request --task-json '<envelope>' --reason '<task>'`
requests a 30 minute lease. Coordinator identity is replaced by authenticated
session/pod UIDs; explicitly named workers must already be authoritative children.
`grant show -o json` exposes the entire envelope, grant UID and maintenanceDigest.
The authenticated human calls `grant maintenance-decide --grant-uid <UID>
--digest <maintenanceDigest> --approve <grant>`; omission of --approve denies.
`grant maintenance-exec <grant> -- <chosen-command> [args...]` uses the same lease
for an approved participant. Extensions are new maintenance requests with
extensionOf pointing to the earlier record and a separate explicit approval;
no actor can mutate the original grant TTL or scope. Command content and output
are not written into operator logs; audit records actor, grant UID, argument count,
completion and optional bounded attributed restore report. Output is capped64KiB
per stream. The broker --maintenance-human-service-account must match operator --human-service-account.
The image's writable root is disabled and no credential is injected.

## Deadline correction and exact baseline proposal (OP233/3527)

The UPDATE rule explicitly preserves UID/name/namespace and optional labels,
annotations, ownerReferences and finalizers. All remaining metadata is also
preserved except server-managed generation, managedFields and resourceVersion.
This admits Kubernetes1.35 normalization before validating admission. Only an
old deadline greater than1 becoming1 is allowed; every other spec field and
its key set must match. The actor, grant ownership, PodUID patch test and
resourceVersion patch test remain. CREATE node UID/control-plane restrictions,
network exclusions, decision authentication and stop-proof guards are unchanged.

The pinned global `pod-security-baseline` rejects SYS_ADMIN/SYS_RAWIO and hostPath.
`maintenance-baseline-exception.yaml` proposes a `kyverno.io/v2` PolicyException
for only bare Pods in dev-agents with the maintenance label, broker admission
subject, tokenless executor ServiceAccount name and matching controller-owner /
grant-label metadata. It waives only Capabilities values SYS_ADMIN/SYS_RAWIO and
HostPath Volumes for the existing device/input path classes. No autogen rule,
ordinary session or other baseline control is exempted. Those path patterns are
not exact grant authorization: the separate fail-closed executor CREATE policy
must perform authoritative AccessGrant lookup and exact capability/image/node/
mount comparisons. Owner/label claims alone cannot authenticate an AccessGrant.
All policies must be reviewed and qualified together, before enabling either flag.

`maintenance-admission-lookups.yaml` proposes only GET AccessGrants in dev-agents
and GET nodes for kyverno/kyverno-admission-controller. Pinned chart3.9.2 already
binds that identity to `view` for Pod GETs, but does not supply node/custom-grant
reads. Broker RBAC does not authorize admission lookups. The pinned1.19.2
[resource.Get implementation](https://github.com/kyverno/kyverno/blob/v1.19.2/pkg/cel/libs/context.go)
uses direct named dynamic-client GETs, so these calls require no list/watch/write
permissions. Executor/session identities receive none of these permissions.

The pinned [chart3.9.2 package](https://kyverno.github.io/kyverno/kyverno-3.9.2.tgz)
declares appVersion1.19.2; SHA256
`f6fe7184544d5e2ee33a77808c09e67aa603ce6560cc197038fde31d727c861e`.
Its exception schema and source support background:false actor matches,
conditions and control-specific podSecurity exclusions; the global baseline can
retain background:true. HostPath's reported restrictedField is
`spec.volumes[*].hostPath` with path-valued exclusions, not `.hostPath.path`.
Capabilities requires images; pod-level HostPath must omit images.

Qualification gap: direct CEL tests execute the packet ownership/UPDATE/DELETE
expressions over admission JSON. A real isolated API server proves defaulted
CREATE, UID/RV-fenced JSON PATCH, generation/managedFields normalization, terminal
status observation and DELETE. The harness has no Kyverno, kubelet or device
runtime. It does not establish Kyverno compilation, exception-condition matching,
actual lookup permissions/webhook ordering, admission-enforced lifecycle or
termination on a real node. These exact proposals remain UNAPPLIED and held for
Tom's concrete policy review and separately authorized isolated runtime
qualification. No live policy apply, dry-run or test is authorized by this packet.
Shared NVIDIA control devices remain broader than GPU UUID intent; persistent
hardware-state restoration is separate from TTL. No limits/capabilities grow.

## Optional fixed NVIDIA utility profile — source preparation only

The immutable optional `runtimeProfile: nvidia-utility` derives only
`runtimeClassName: nvidia`, `NVIDIA_VISIBLE_DEVICES` equal to the approved exact
GPU UUID resource, and `NVIDIA_DRIVER_CAPABILITIES: utility`. Omission preserves
the generic host-device path. The profile requires an empty devices list;
existing explicitly approved read-only inputs keep their validation. No caller
runtime class, environment map, compute capability or implicit SYS_RAWIO is added.
The complete scope digest and schema immutability include the profile.

The UNAPPLIED CREATE policy must compare the exact profile-derived runtime and
environment and fail closed for unknown profiles or malformed GPU UUIDs. RuntimeClass
admission may add overhead and scheduling constraints; policy qualification must
pin the existing nvidia RuntimeClass with no overhead or scheduling. The profile
refuses any added overhead or node selector and tolerations beyond Kubernetes
default not-ready/unreachable NoExecute Exists 300-second entries. RuntimeClass
drift requires renewed review; no admission lookup permission is added. This source preparation does not qualify that
runtime, the candidate image, device cgroups or driver libraries. Activation still
requires concrete review of the exact policy and separately authorized runtime
qualification. Release #736 and live policies are outside this change.


Before live qualification or approval, review the exact digest-pinned executor
image and **all baked `NVIDIA_*` environment variables**, including inherited
image configuration. The Pod sets only `NVIDIA_VISIBLE_DEVICES` and
`NVIDIA_DRIVER_CAPABILITIES`; the NVIDIA runtime can also consume image-defined
variables such as `NVIDIA_MIG_CONFIG_DEVICES`, `NVIDIA_MIG_MONITOR_DEVICES` and
other runtime flags. The admission policy cannot inspect image ENV. This profile
therefore does not prove that image-inherited runtime settings are safe. Record
the complete image ENV review and its disposition as an explicit prerequisite to
live qualification and task approval; the current live-activation hold remains.
