# Coordination TaskClaim archive list — source checkpoint

Date: 2026-10-15. Scope: C7-R4-CLAIM-LIST-1015 only.

This source slice adds the default-off `GET /v1/task-claims?archived=only`
metadata list. It reuses the accepted archive lister, shared shelf slots and
route-bound cursor envelope. Live listing and its owner Session join are unchanged.
Both live and archived TaskClaim pages default/max at 100, as DESIGN-024 requires;
the coordinator's original 20-item claim confused the PublishedContext bound
with the TaskClaim bound and was corrected after advisory review. The design
itself is unchanged. The archive result envelope remains bounded at 4 MiB.

Archived views are made from immutable metadata only. They set `archived` and
the UID's first `archivedAt`; `ownerState` and `stopState` remain `Unknown` and
the archive path does not read the owner Session. The archive list validates its
canonical text-stripped projection and manifest/claim identity without comparing
the projection hash with the stored full-object digest.

Independent native review found no production defect in the initial candidate
but requested the missing route acceptance cases and typed transport proof.
The coordinator added bounded fixtures for caller fences, live/archive limits,
immutable attribution, decoded cursor progress, malformed metadata/envelopes,
corrupt-row continuation, response bounds and shared-slot cancellation/release.
The typed shelf-client round trip uses an in-process synthetic executor and
asserts the TaskClaim resource route with one List exec. This is a composed
source boundary fixture, not a real archive-to-HTTP or installed run. Direct test
output and source hashes are retained with the order; required exact-head CI
and native closure precede source acceptance.

This is source and focused-test evidence only. It does not activate archive
reads, change retention, archive storage, credentials, runtime behavior,
deployment, or acceptance of R4.
