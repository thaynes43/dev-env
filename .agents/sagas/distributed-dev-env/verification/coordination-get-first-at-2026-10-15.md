# Coordination latest GET first-time receipt — 2026-10-15

Source-only C7-R4 diagnostic on accepted base `b8c81aa9b25c19b638bd66d5bd88dbdcfab43bea`.

The finite two-revision TaskClaim fixture publishes revision `42` at 10:15 UTC
and revision `43` at 11:15 UTC for one UID. Against the accepted-base production
file, unpinned `GetSelected` returned the later revision's stored 11:15 receipt;
the expected UID-first outgoing receipt was 10:15. The expected diagnostic failure
is preserved in the order record `accepted-base-regression.jsonl`.

`GetSelected` now changes only the outgoing `archivedAt` after it has fully read
and verified an originally no-UID/default selected latest revision. It uses the
already birth-verified selected UID's first time. Stored manifests, selected
identity/hash/object bytes, revision ordering, explicit-UID latest behavior and all
exact resource-version/hash receipts remain unchanged. The new fixture compares the
complete on-disk tree before and after reads and proves the default GET reports the
first receipt while explicit UID latest and exact pins retain revision receipts.

The explicit-UID first-time API contract remains an unresolved seam. In particular,
explicit UID latest continues its existing recovery behavior for missing or damaged
birth-index metadata; this slice neither exposes nor redefines it.

Focused tests used the required shared lock, `nice -n 19`, `GOMAXPROCS=2`,
`GOFLAGS=-p=1`, cached Go 1.27.2, offline module settings, and a 60-second timeout.
The final run passed the new fixture plus exact, selector, tie, corrupt-latest and
missing/damaged-birth tests. Its direct stdout/stderr capture and exit status are
retained beside the work order in `final-focused-tests.jsonl`.

This is source/tested evidence only. It does not install or activate archive reads,
change retained storage, or claim complete R4 acceptance.
