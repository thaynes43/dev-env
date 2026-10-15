# Coordination archive paged list source checkpoint — 2026-10-15 UTC

R2-LIST-1015 adds the bounded, read-only `agentd ctl coord-archive list`
source slice. It selects either the immutable all-record index or an inbox
message recipient index, walks entries newest-first by recorded UTC index time,
and resolves each UID's latest recorded revision. The full selected object is
verified before a projection removes `spec.text`.

Pages contain at most 100 items and are capped at 4 MiB. Their opaque cursor
binds the resource, project, recipient selector and last index name; a
cross-index or malformed cursor is refused. Traversal reads directory names in
128-entry batches without relying on physical directory order or accumulating a
whole index. A valid index filename with a malformed entry body or selected revision is
represented as an item with `error: corrupt` and no object; it never falls back
to an older revision. Invalid month/index names have no trustworthy identity
and refuse the whole page as `corrupt`, without objects.

The local synthetic fixtures cover traversal across two months, a verified
latest projection with text removed, cursor binding, corruption of the latest
revision without fallback, and command dispatch. The focused command was:

```
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 \
  env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
  /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go \
  test -p=1 ./internal/agentd ./internal/agentd/protocol ./cmd/agentd \
  -run 'TestCoordArchive(List|CLIList)' -count=1
```

It passed on 2026-10-15 UTC. This is source/offline evidence only: it does not
install, mount, activate, query, or delete a coordination archive, and it does
not establish R2 or consumer acceptance.

The final focused correction retains at most `limit + 1` month names as well as
at most `limit + 1` selected index entries. It fetches another parent-name batch
only after exhausting the prior batch, including empty or sparse months; each
actual month directory is scanned once per page. The cursor month is included
on the first batch, and later batches use an exclusive month bound. No complete
history of month names is accumulated.

Seven backend `TestCoordArchiveList` methods passed without skips in 1.826s on
2026-10-15 at 13:01 UTC, using the cached Go1.27.2 toolchain, shared test lock,
GOMAXPROCS=2, `-p 1`, nice19 and downloads off. The additional scenario crosses
131 empty newer months before retrieving three older records in order on
one-item pages, then proves cursor exhaustion without loss or duplication.
Other focused fixtures cover 130 entries over the 128-name read batch and
100-item page boundary, near-4MiB wire output/continuation including the final
newline, recipient/namespace index forgery without object disclosure, and
corrupt selected revisions without fallback. Focused protocol and CLI checks
from the corrective unit also passed, including malformed timestamp cursors and
bare-list invalid refusal. The initial failed CLI unsupported-list fixture and
formatter/tool-path attempts remain preserved in the work order evidence.

Ordering remains recorded UTC archive time. Physical arrival chronology after
clock rollback is unestablished; equal greatest revision times refuse instead
of guessing. These are synthetic source fixtures, not installed production
execution, runtime archive acceptance, or MVP completion.

The final advisory correction encodes each item once and counts the encoded
items, separators, empty envelope, optional cursor and newline before adding
it. Terminal pages omit `next`; a size-cut page keeps the last emitted cursor.
DESIGN-024 now states the distinction between a known UID with corrupt content
and an invalid directory filename whose identity is unknown. Eight focused
backend list methods passed without skips in 1.664s at 13:07 UTC on2026-10-15,
under the same lock/CPU/cached-toolchain limits, including terminal cursor
checks and whole-page unknown-name refusal. No broader suite or live command
was repeated for this correction.
