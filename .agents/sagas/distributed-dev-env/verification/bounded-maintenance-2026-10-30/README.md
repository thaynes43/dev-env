# Bounded maintenance source preparation (OP233 / OP219)

Source base: dev-env 030133dc9dd9ac24a2c61f30f12ee8e402bd4bf2.
Sole source author: dev-env-1030-170111. Lead dev-env-1029-132039 owns sequencing
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
and an exec guard for ALL other baseline, workbench and grant identities. It changes
no global baseline capability exception and creates no public endpoint or namespace.
The operator's existing API and pods/exec authority are reused; no grant identity
receives pods/create or pods/exec. Node and node-lease reads by the broker are stop
evidence only. Unrelated v1 behavior is preserved.

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
up. A stale/unreachable node keeps Unknown/StopPending visible and the finalizer
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
