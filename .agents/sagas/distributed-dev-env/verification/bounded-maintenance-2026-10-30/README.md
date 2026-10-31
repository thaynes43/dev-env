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

Qualification gap: direct CEL tests execute all four packet validation branches
over JSON-normalized AdmissionRequests. The pinned official CLI additionally
compiles and evaluates synthetic generic/profile CREATE and refusal cases offline. A real isolated API server proves defaulted
CREATE, UID/RV-fenced JSON PATCH, generation/managedFields normalization, terminal
status observation and DELETE. The harness has no Kyverno, kubelet or device
runtime. Offline compiler/engine evidence does not establish installed exception matching,
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


The offline policy regression uses the checksum-pinned official Kyverno1.19.2
CLI to compile and evaluate the exact executor CREATE packet against synthetic
resources and request normalization. It covers the homogeneous typed environment
literal and omitted empty AdmissionRequest subResource that the permissive CEL
harness missed. This is compiler/offline-engine evidence only: live lookups,
webhook ordering, CONNECT exec enforcement, Cilium, runtime/image/hardware and
termination qualification remain held. The CLI cannot qualify CONNECT because
its1.19.2 processor supplies an empty subresource; a zero-match run is not a pass.

Run `make check-maintenance-policy` for the pinned offline regression. It verifies
the official release archive checksum and CLI version/commit, uses synthetic
context-file resources with KUBECONFIG=/dev/null, and enables neither cluster nor
registry access. The script's `--archive` option reuses the same verified archive
without downloading it. Production denials must be explicit Fail results with no
engine errors; compilation or evaluation errors never count as a denial pass.

## Amendment 2026-10-31: approval by message to the requesting agent (ADR-013)

Tom, 2026-10-31, relayed by lead dev-env-1029-132039 in the OP233 work order:
"I or an agent can approve with a message to the agent requesting the grant."
[ADR-013](../../adrs/013-attested-maintenance-approval.md) records the ruling and
amends ADR-012's "independent of unverified agent coordinator:string attestation"
clause. This
section is the source design for ADR-013's path (a), the requester's
self-attestation. Path (b), authenticated delegate approval on the
maintenance-decision route, is a separate follow-up. This section authorizes no
policy, grant, flag change or pilot.

What changes:

- A maintenance grant may also be approved by an agent attestation whose binding
  the broker verifies (not verified human identity; DESIGN-001 D-88), through an
  attested GrantPolicy (`spec.attested`) with a `spec.maintenance` scope. The
  grant matches only when all of these hold:
  - its node, node UID, resource (the GPU UUID), runtime profile and
    digest-pinned image equal the policy's;
  - its capabilities are a subset of the policy's;
  - `cpuMillis` and `memoryMiB` are at or below `maxCPUMillis` and
    `maxMemoryMiB`;
  - every worker, device file and read-only mount it names is one the policy
    names exactly (workers by name, session UID and pod UID);
  - its TTL is at most the policy's `maxTTL`.

  The operation, restore and `extensionOf` text are audit intent and are not
  compared. The coordinator is the requester, which the attestation binds.
- The schema accepts a maintenance policy only with `spec.attested`, and caps
  its `maxTTL` at 1 hour. `Matches`, the standing-policy test, is false for
  every maintenance grant and policy, whatever the schema did.
- The human route is unchanged and runs first. `maintenanceDecision` handles a
  grant that carries a human decision annotation, valid or not, before any
  policy is read. A human decision wins only while the grant is Pending. After
  a policy approval, revocation is by release, by deleting or narrowing the
  policy, or by turning a flag off. Each ends the lease at the next broker pass.
- The route needs a top-level requesting session. Its live AgentSession has no
  parent session: `spec.parent` is a caller ServiceAccount (`namespace/name`) or
  empty, and the depth label is absent or 0. A child task session waits for the
  human route.
- The route never approves an extension (`extensionOf`). That needs the human
  route.
- The route approves one lease per node UID and resource at a time. It reads the
  grants from the API server, and waits while another maintenance grant on that
  target has not ended: Active, Pending with a human approval recorded, or with
  an executor that is running, stopping or of unknown state.
- Otherwise, and only with `--enable-maintenance-grants`,
  `--enable-attested-grants` and `--enable-attested-maintenance` all on, the
  broker evaluates attested policies with the existing `verifyAttestation`
  checks: the statement's grant UID, the requesting session UID, the session's
  current Running pod, and the scope digest. It also checks explicitly that the
  attestor is the envelope's coordinator pod. Then it runs
  `CheckMaintenanceIdentities`, as `Decide` does for the human route. It records
  `approvedBy policy/<name>` and the D-88 message "agent
  attestation, not verified human identity".
- `grantattest.ScopeDigest` now includes the whole maintenance envelope (sets
  sorted). Digests of kube, egress and credential grants are unchanged. A
  statement made for one envelope does not verify for another.
- `activeMaintenance` re-checks a policy approval on every pass, before the
  network policy and the executor exist and while they run. It checks:
  - the flags;
  - that the requester is still top-level;
  - the approving policy, read from the API server by name (recording its UID
    is an ADR-013 follow-up);
  - that policy's match;
  - the statement, in its execution form.

  A refusal ends the lease through the existing stop path. A read error is
  retried and creates nothing. The operator's exec route checks the lease, not
  the approver, so commands run until the broker's pass ends the lease. Before
  rolling back to a broker without this route, turn
  `--enable-attested-maintenance` off and wait until no policy-approved lease
  is Active: an older broker does not re-check one.

What does not change: the envelope, its bounds and immutability; the executor
and its network isolation; expiry, release and restore. An attested maintenance
approval is a claim by the requesting agent, not verified human identity.

Code defaults versus live state (2026-10-31). In code, all five flags default to
false:

- broker: `--enable-maintenance-grants`, `--enable-attested-grants` and the new
  `--enable-attested-maintenance`;
- operator: `--enable-maintenance-api` and `--enable-grant-attestation`.

Live, as the lead verified and this change's author re-read with `kubectl`, four
of them are already true:

- broker `--enable-attested-grants=true` and `--enable-maintenance-grants=true`;
- operator `--enable-grant-attestation=true` and `--enable-maintenance-api=true`.

So the "activation hold" earlier in this packet no longer describes those four
flags.

Parts of this packet that are installed:

- `dev-env-maintenance-annotations` with its binding;
- the broker maintenance RBAC (`dev-agents/dev-env-broker-maintenance`, its
  target and stop-evidence roles) and `dev-env-maintenance-admission-lookups`;
- the executor ServiceAccount;
- `PolicyException` `pss-baseline-dev-env-maintenance`;
- the maintenance exclusions on the session CCNPs.

Missing: the executor-shape admission, the `dev-env-maintenance-executor` and
`dev-env-maintenance-network` ValidatingPolicies. The lead verified that no
admission policy matches pod CREATE. This author's identity cannot list Kyverno
ValidatingPolicies.

What remains for this route:

1. Copy the regenerated GrantPolicy CRD to haynes-ops.
2. Cut a release and update the pin.
3. Set `--enable-attested-maintenance=true` through GitOps.
4. Add one attested maintenance GrantPolicy that Tom approves.

A proposal for that policy exists only in the source PR body. Nothing is staged.
