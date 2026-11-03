# Collector incarnation handoff: source qualification

This source-only maintenance proposal grants no runtime authority. Its tests use
synthetic objects and do not establish provider quota, historical witness
provenance, deployment, approval, or runtime acceptance.

DESIGN-049 assigns capture headers to the caller and makes sequence stable per
producer and incarnation. Neither that contract nor the aggregate contract
requires an incarnation to equal a Pod UID. The same logical stream is retained
only with identical selected trust, accounts and workspaces. Historical captures,
timestamps and sequence numbers are preserved.

A trusted named ConfigMap's validated idle journal is transitive terminal
cleanup evidence: Runner checks the host and expiry after remote execution,
validates the matching terminal receipt and its cleanup proof, then atomically
commits journal and report. This inference depends on independently qualifying
that journal's provenance, fingerprint, exact bytes, cycle, nonce, report and
known helper; an arbitrary supplied snapshot does not establish provenance.
The old runtime ledger is not reconstructed or waived. An independently
authenticated historical old-Pod eviction snapshot, bound into the plan, plus
fresh checks of the replacement session, Pod and HOME, fences the executor.
The source helper validates their shape and identity but cannot authenticate an
archived snapshot or a human approval. The human-directed parent coordinator
session must independently qualify those facts and the exact prepared hash
before any invocation; a hash is not approval.

The helper prepares offline with the real collector parser and permits only the
specified host and deadline changes. Its apply route checks the named slot and
fresh host, fsyncs a private full backup, replaces only the fingerprint with a
resource-version CAS, and checks exact readback. No deployment or live apply is
authorized by this source work. Synthetic tests are not runtime acceptance.

## Proposed one-time handoff design

This operation is deliberately specific to cycle 56 and the approved deadline
2026-11-04T15:33:23Z; it is not a general incarnation migration facility.
The wall-clock public-apply fixture is necessarily skipped outside that fixed
window. The clock-injected apply and real Runner fixtures remain runnable after
it, including the next-cycle and lost-ack paths.

The human-directed parent coordinator session supplies a private snapshot of
both exact configurations, selected trust, named slot UID and resource version, original journal and report, their
hashes, cycle and nonce, the known helper digest, and the authenticated archived
old Pod. Preparation parses both configurations and the journal, verifies the
old Pod's eviction identity and the unchanged logical-stream policy, and
hashes the whole proposed transition. That session, not the operator binary,
independently verifies the snapshot provenance and eviction and obtains exact
prepared-hash approval; none can be established by the caller's bytes alone.

Apply revalidates the proposed hash and reads the new session, current Pod and
HOME using the selected uncached client. It requires the selected generation,
Pod UID, image, service account, owner references, bound HOME and fresh
heartbeat. It then checks the named mutable ConfigMap and exact old bytes,
fsyncs a private full old configuration, journal and report backup, rechecks the
host and slot, and makes one resource-version-guarded fingerprint replacement.
Exact readback resolves a lost acknowledgement and permits an identical retry
only with the matching backup. A changed slot, journal, report, host, deadline,
trust or evidence refuses. Existing runners on either mismatched fingerprint
cannot reach provider execution. No other state keys or report bytes are changed.

The [DESIGN-053 one-time replacement handoff](../designs/053-operator-owned-allowance-collection.md#one-time-replacement-handoff-2026-11-03)
records these preconditions and fences.

The approved host delta permits a different valid digest-pinned image. The fresh
replacement Pod must match the newly selected image exactly.

## Operator-native execution route (source, 2026-11-03)

The standalone CLI needs a caller that can Update the slot. The Root session's
caller cannot, and the borrowed-identity alternatives are not allowed. The
elected operator already holds `get, update` on exactly the slot ConfigMap and
nothing else in `dev-env-system` (haynes-ops `rbac/app/operator.yaml`: no
`create`), so it can run the same operation without a new permission.

**Flag.** `--allowance-handoff-file=<abs path>` is off by default and requires
the enabled collector. The file is private (no access for others, at most 512
KiB) and holds exactly `{"approvedHash": ..., "snapshot": {...}}`. The snapshot
is the CLI's snapshot. An unreadable or invalid file is refused and logged with
a fixed message; it never stops the operator, because the collector and the API
do not depend on it.

**Fences, in order.** `HandoffRunner` (leader-only) makes an attempt about
every minute until it is acknowledged, the new window ends or the operator
stops:

1. The snapshot's new configuration must parse to exactly the configuration
   this process is running (fingerprint, host, slot, deadline). The trust and
   publisher come from the operator's own aggregate selection, not the file.
2. `PrepareHandoff` must reproduce the approved hash. The hash binds both
   configurations, the evicted-Pod witness, the journal and report digests, the
   cycle, nonce and helper, and both fingerprints. It also binds a kubeconfig
   path and context; for the operator these are the fixed values
   `allowancecollector.OperatorKubeconfigPath` and `OperatorKubeContext`, so
   the approver reproduces the hash with `dev-env-allowance-handoff prepare
   --kubeconfig /var/run/secrets/kubernetes.io/serviceaccount --context
   in-cluster:dev-env-operator`. Prepare opens neither value.
3. The replacement session, Pod and HOME checks use an uncached client.
4. The slot must have the exact old resource version, journal and report.
5. One update writes the new journal and the backup together.

**Backup.** There is no durable directory in the operator, and `/tmp` there is
an emptyDir. The full pre-image (old configuration, journal and report) is
therefore written to the slot object itself, under
`handoff-backup-<prepared hash>.json`, in the same compare-and-swap update as
the journal change. It is durable in etcd exactly when the new journal is, and
it cannot be missing after the journal changed. No other key or report byte
changes. The route refuses when any earlier backup entry exists (the operation
is single-use) or when the slot would pass 900 KiB (a ConfigMap is capped at
1 MiB; journal, report and configuration can reach about 390 KiB). The entry is
never pruned by the collector. Export it with the usual read access before any
later cleanup.

**Lost acknowledgement and restart.** A failed update is judged by exact
readback of a changed resource version, journal, report and backup; the update
is never retried. After success, the collector's own cycles move the journal on,
so an operator restart acknowledges the handoff only when the exact backup entry
is present and the current journal still parses under the new configuration. It
then writes nothing.

**Zero provider I/O.** Until the handoff lands, a collector on either
fingerprint refuses before it reserves or dispatches. The tests count
`Remote.Run` calls across both collectors and a refused handoff.

**What this does not do.** It does not authenticate the evicted-Pod snapshot or
the approval; the human-directed coordinator still qualifies both before the
file is mounted. It grants no permission, deploys nothing and applies nothing.
Mounting the file, pinning an operator image that has this route and the new
collector configuration are GitOps changes that remain held for the owner's
approval. After the acknowledgement, remove the file and flag in a later change.

## Eviction observation witness (source, 2026-11-03)

The plan requires a witness that the old Pod incarnation is terminal. The first
form is an archived Pod API object. Once a Session replaces its Pod the object
is gone, and a summarized note is not an object. A second form is therefore
accepted: an `eviction-observation.v1` document recording what the cluster's
kube-state-metrics retained for the old Pod UID: namespace and name, last and first range-evaluation instants at which the series returned the Pod
`Running` and then `Failed`, reason `Evicted`, controller `AgentSession` named like the
host, and the three series it came from (`kube_pod_info`,
`kube_pod_status_phase`, `kube_pod_status_reason`, in that order).

Checked: exact keys and bounded size; every identity field equals the old
configuration's host (namespace, name, Pod UID, owner name); the Pod UID is the
old incarnation, never the live one; the phase and reason; `lastReturnedRunning` is not before the held journal's cycle start (the old Pod
was seen Running during the cycle the idle journal records) and is before
`firstReturnedFailed`, which is not after `observedAt`; `observedAt` is not
after the plan's time. Both instants are evaluation states of a range query over a bounded window.
They do not establish the physical eviction time, the scrape time or any lag
between them, and they are not a transition time. The bytes are
part of the prepared hash, so a different witness is a different plan.

Not carried: the owner UID, service account, image and resource version of the
old Pod. The first three stay bound by the fingerprinted old configuration and
the live Session, Pod and HOME checks; the resource version is not needed to
establish that the incarnation is terminal. This weakens nothing a Pod object
established beyond those fields, and it adds no provenance: like a Pod object,
the observation proves neither who produced it nor human approval. The
qualifying coordinator still has to establish provenance, for example by
running the stated queries against the cluster's Prometheus and recording the
returned samples. A hand-built document is not a witness.

Be plain about the strength. Every field except the two times is derivable from
the public old configuration, and the series list carries no sample values. The
shape check therefore stops mistakes, not forgery. The safety property rests on
the coordinator reproducing the queries and recording the returned samples, and
that record must exist outside the document before a live step.
