# Coordination archived context list source checkpoint — 2026-10-15 UTC

Story C7-R4-CONTEXT-LIST-1015 adds the source-only `archived=only` list
selection for published contexts. The default list remains live-only and does
not use the shelf. The default-off `CoordinationArchiveReads` gate returns the
same masked not-found result for the explicit archive selection.

The API performs the existing caller project/payload scope and live ownership
checks before taking one existing shelf slot. It asks the typed shelf List
contract for at most 20 `publishedcontexts` for the bound project and no
recipient. Its opaque cursor binds this route, project, empty recipient and
limit before any shelf use.

The list transport is deliberately metadata-only. The shelf has already
verified a full canonical stored object and removes `spec.text`; the API checks
the canonical projection has no `spec.text`, validates immutable identity,
schema, provenance, attribution, digest syntax and byte count, and checks the
manifest and item UID. It does not recompute a full-object digest from the
projection. Corrupt rows with only a valid UID are omitted while their shelf
cursor advances. Other malformed rows, invalid continuation, unavailable shelf
or exhausted/cancelled slot refuse without metadata disclosure.

The shelf list receipt now emits the verified UID-first archive time even when
its metadata payload comes from a newer selected revision. This changes only
the outgoing List manifest receipt; GET, storage, selectors and protocol stay
unchanged.

Focused source evidence used cached Go 1.27.2 with `nice -n 19`,
`GOMAXPROCS=2`, `GOFLAGS=-p=1`, local toolchain/downloads disabled, and the
shared `/tmp/dev-env-go-tests.lock`:

```
go test ./internal/agentd ./internal/contextpublication ./internal/apiserver \
  -run '^(TestCoordArchiveListReturnsUIDFirstArchiveTimeForLatestRevision|TestValidArchiveMetadata|TestContextArchiveList)' -count=1
```

The finite set passed. It covers metadata/full validation separation, text-free
projected List fixture, malformed metadata refusal, corrupt omission, cursor
binding, default/live separation, typed shelf-list projection handling, and a
two-revision UID-first receipt. This is source and focused-test evidence only;
it is not installed, runtime, consumer, release, archive activation or cutover
acceptance.

Pre-acceptance follow-up added inner shelf-cursor validation before slot use,
nonadvancing cursor refusal, caller fences, shared-slot unavailable/busy/cancel
paths, malformed row shapes and the complete 4 MiB encoded-result bound.

The composed fixture evidence has two deliberately separate bounded parts:

1. In `internal/agentd`, the existing temporary archive rig runs the real
   archive `List` behind the typed shelf List client. It proves full selected
   bytes were verified before text stripping, latest-revision metadata retains
   the UID-first archive time, and damaged selected bytes become an objectless
   corrupt item.
2. In `internal/apiserver`, the typed shelf transport projection reaches the
   HTTP handler and its caller, cursor, shared-slot and metadata checks.

This is **COMPOSED** source evidence, not one archive-to-HTTP end-to-end test,
and not envtest, installed, runtime, consumer, release, archive activation or
cutover acceptance.

The independent source review requested three corrections: decoded cursor
progress, import grouping, and focused acceptance coverage. Continuations now
compare decoded index names and must move strictly older, including when an
alternate encoding names the same position. Added fixtures cover selector
binding, malformed projections, valid coordinator/default and maximum limits,
foreign-project refusal, and immutable managed attribution with a source-read
guard. The existing full-content `ValidSpec` implementation remains unchanged;
the metadata-only helper reuses the origin and provenance validators but repeats
its metadata predicates to keep this slice from changing accepted live validation.
A future shared extraction must retain that separation.

The coordinator directly captured one constrained selection after these
corrections on October 15 at 16:32 UTC: 11 methods, 39 passing test nodes, zero
skips. Package times were agentd 0.103s, contextpublication 0.012s and apiserver
2.969s. These totals describe that one selection. The real archive-to-typed-client
and typed-client-to-API fixtures remain separate composed evidence. Earlier
compile and duplicate-query fixture failures are preserved in the private
work-order record; they are not runtime failures or evidence of installed parity.
