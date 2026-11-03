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
