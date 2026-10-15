# Coordination item-get transport — source checkpoint, 2026-10-15

Story `C7-R4-ITEM-GET-TRANSPORT-1015` began at
`2026-10-15T20:48:25Z` from accepted base
`1b8cc27997932d4474dcca21fcb1bdd3ba076a67`. It is source-only: no HTTP route,
controller, provider, runtime, deployment, access, or authentication work was
performed. The requested configuration was `supported TUI Terra high`; the
serving model remains unproved.

The contract is documented in DESIGN-024 section 6. `agentd ctl coord-archive
get-item --resource R --name N [--uid U]` uses `GetItemSelected`, accepts no
revision/hash flags, consumes no stdin, and retains the ordinary settings/mount,
canonical JSON, and error rules. `CoordinationArchive.GetItem` rejects pins
before exec and uses the same response verification as legacy `Get`.

## Direct accepted-base RED

The exact accepted `cmd/agentd/coordarchive.go` was reconstructed without a
production toggle and substituted only by Go's supported overlay. Its SHA-256
is `219ab25a3cd2d70614187ec127f468ac45da1bf0c11b43d871a2ddb9d3fa1789`.
The direct artifacts are in the assigned order directory:

- `base-red.stdout`, `base-red.stderr`, `base-red.exit`,
  `base-red.source.sha256`, and `accepted-base-overlay.json`.

The constrained command was:

```text
flock -w 60 /tmp/dev-env-go-tests.lock env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off nice -n 19 <cached-go> go test -overlay accepted-base-overlay.json -json ./cmd/agentd -run '^TestCoordArchiveCLIGetItemTransport$' -count=1
```

It exited `1`: the fixture put a valid object and legacy exact `get` succeeded,
then `get-item` returned exit `2` and the accepted usage string without that
subcommand. Stderr was empty; the JSON event stream is retained verbatim in
`base-red.stdout`.

## GREEN evidence

The direct final artifacts are `final-green.stdout`, `final-green.stderr`,
`final-green.exit`, and `final-green.source.sha256` in the order directory.
The same shared flock, `nice -n 19`, `GOMAXPROCS=2`, `GOFLAGS=-p=1`, offline
module settings, and cached Go binary ran:

```text
go test -json ./cmd/agentd ./internal/shelf -run '^(TestCoordArchive.*|TestCoordinationArchive.*)$' -count=1
```

It exited `0`, with empty stderr. From the direct JSON stream: 21 top-level test
methods, 28 test nodes (including seven subtests), and four package events
(two starts and two passes); these counts are deliberately not combined with
the separate RED or focused correction runs.

The new CLI coverage uses controlled times for an old UID, later new UID,
old-UID recapture, and newer revision. It proves default and explicit item
selection return the latest verified object with the UID's first receipt; legacy
explicit/pinned recovery retains revision receipts. It also refuses pin flags,
unsafe/missing identity, missing/damaged first-publication entries, corrupt
latest objects, and tied latest revisions without an object or unusable pin
advice. The shelf coverage proves exact command construction with no stdin,
pin rejection before exec (including a nil shelf), missing/mismatched/digest or
noncanonical responses, zero/non-UTC receipts, cancellation, typed refusal,
and unknown transport error propagation. Existing legacy get tests remained in
the final selection.

Go-source SHA-256 values used for GREEN:

```text
1edfed0d1713a9af3c403ace0ad8e187b6814f46aa20ad2241394028f0d2bea0  cmd/agentd/coordarchive.go
b99542a0834d402cd2406f3a754f8a932a76296b4408f8cb6f175a789cbdc897  cmd/agentd/coordarchive_item_get_test.go
962fd0488ef54c91b2ad42e0f80bc5352e5681c39874390df9a67658bd5d1479  cmd/agentd/main.go
4d78854053a87f9a56b8356ab40cc61099d57d2986da6a7b5609e7d0a8ce5571  internal/shelf/coordarchive.go
4f01d4222fafa9124eb78d7fa89031ab500abcd18dd5f15b68615c189e4e0436  internal/shelf/coordarchive_item_get_test.go
```

## Deviations and remaining gates

The first shelf-focused run failed because the fake executor records nil stdin
as no entry, while the new assertion indexed `stdin[0]`. That test-only error
was corrected to require no stdin entry; no production behavior changed. Before
the root interruption, a successful platform-only focused run existed without
the required direct raw artifact files; this was a missing-capture deviation,
not acceptance evidence. The overlay RED and direct GREEN above replace neither
history nor counters and retain the interruption record.

This is not source acceptance, review, CI, installation, runtime, or usable R4
acceptance. The next safe steps are root review, ordinary source CI and advisory
review if available, then any separately authorized delivery work. No push, PR,
merge, or CI polling occurred here.
