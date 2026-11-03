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
archived snapshot or a human approval. Root must independently qualify those
facts and the exact prepared hash before any invocation; a hash is not approval.

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

The operator supplies a private snapshot of both exact configurations, selected
trust, named slot UID and resource version, original journal and report, their
hashes, cycle and nonce, the known helper digest, and the authenticated archived
old Pod. Preparation parses both configurations and the journal, verifies the
old Pod's eviction identity and the unchanged logical-stream policy, and
hashes the whole proposed transition. Root independently verifies the snapshot
provenance, eviction and approval; none can be established by the caller's
bytes alone.

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

The source check `make check-layout` also requires a `CLAUDE.md` Layout entry
for this new command. The five-file source assignment excludes that file; the
check remains blocked pending explicit scope acknowledgement. A separate design
file is likewise outside that assignment; this record documents the proposed
behavior pending any broader authorized design update.

The approved host delta permits a different valid digest-pinned image. The fresh replacement Pod must match the newly selected image exactly.
