# Coordination archive incoming cleanup — 2026-10-15 UTC

This source-only R2 slice implements DESIGN-024 section 4, `put` step 7. Once a
put has durably published its immutable revision, repaired indices, and completed
its journal action, it examines `.incoming` while still holding the exclusive
shelf lock.

Only a private 32-lowercase-hex direct child created by the archive writer is
eligible. It must be an ordinary directory and contain at most the known flat
staging files: `object.json`, `manifest.json`, `index.json`, or `event.json`.
The traversal reads at most 128 incoming names and at most three child names at
a time. An entry is removed only when the injected archive clock is strictly
more than one hour after its modification time. Fresh entries, the exact
one-hour boundary, and future timestamps remain. Linked, nested, malformed, or
unknown old entries are preserved and make `put` return `no_archive`; no path
outside `.incoming` is followed or removed.

The cleanup directory is synced after the removal pass. A removal or sync
failure also returns `no_archive`, even though the preceding immutable revision
may already be visible and readable. A retry can repair that durable-cleanup
uncertainty without rewriting a published object, index, or journal event.

Focused synthetic tests cover successful old-only removal, exact/fresh/future
retention, verified get preservation, invalid-put debris preservation,
unknown/link refusal without outside effects, and injected cleanup-sync
failure. This is source/offline evidence only: no archive was mounted, queried,
activated, deleted, deployed, or released.
