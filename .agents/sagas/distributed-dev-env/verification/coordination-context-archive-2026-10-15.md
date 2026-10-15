# PublishedContext archive GET source verification — 2026-10-15 UTC

This bounded R4 source slice adds only `GET /v1/published-contexts/{name}`
fallback behavior. It is default off and has no operator flag wiring, runtime
activation, retention deletion, list, inbox, claim, release or deployment work.

With archive reads explicitly configured, the unchanged live store is read first.
Only a true live absence takes an existing replica `ShelfSlots` token (waiting no
more than two seconds) and uses the typed shelf `Get` transport. A masked foreign
live record remains a 404 without a shelf call; live errors remain 503. The
returned archive manifest and object are reverified, then its complete immutable
PublishedContext identity, namespace, name, project and spec are checked before
the existing projection is used. An archive result includes additive `archived`
and `archivedAt`; live replies omit both. Archived attribution is all Unknown and
does not read the source Session.

Focused source evidence, run once under the shared serialized Go-test lock with
the cached Go 1.27.2 toolchain, `nice -n 19`, `GOMAXPROCS=2` and `-p=1`:

```text
go test -p 1 -count=1 -v ./internal/apiserver ./internal/contextpublication ./internal/shelf ./internal/agentd -run '^TestContextArchive'
PASS internal/apiserver
PASS internal/contextpublication
PASS internal/shelf
PASS internal/agentd
```

The synthetic fixtures prove default-off no-exec absence, unchanged live shape,
verified archived text/hash projection, foreign live masking, corrupt refusal,
slot cancellation/release, and the existing strict Heartbeat client accepting the
additive archived metadata. This is source/test evidence only. R4 lists, other
record kinds, operator wiring, release, installation, archive activation and
retention/deletion acceptance remain separate and held.
