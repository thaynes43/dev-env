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
whole index. A malformed index or selected revision is represented as an item
with `error: corrupt` and no object; it never falls back to an older revision.

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
