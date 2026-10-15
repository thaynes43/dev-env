# Coordination TaskClaim archive list — source checkpoint

Date: 2026-10-15. Scope: C7-R4-CLAIM-LIST-1015 only.

This source slice adds the default-off `GET /v1/task-claims?archived=only`
metadata list. It reuses the accepted archive lister, shared shelf slots and
route-bound cursor envelope. Live listing and its owner Session join are unchanged.

Archived views are made from immutable metadata only. They set `archived` and
the UID's first `archivedAt`; `ownerState` and `stopState` remain `Unknown` and
the archive path does not read the owner Session. The archive list validates its
canonical text-stripped projection and manifest/claim identity without comparing
the projection hash with the stored full-object digest.

This is source and focused-test evidence only. It does not activate archive
reads, change retention, archive storage, credentials, runtime behavior,
deployment, or acceptance of R4.
