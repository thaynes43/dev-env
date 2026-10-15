# Coordination archive item get — 2026-10-15

Story C7-R4-ARCHIVE-ITEM-GET-1015 is the source-only library slice for saga
distributed-dev-env, epic #175 / backlog 11, retention read fallback (R4). It began from accepted base
`a72f8dca73891f3eda1b00af2e855c55e7088186`. This record distinguishes source
and focused test evidence from CI, merge, installation and usable acceptance.

## Contract

`CoordArchive.GetItemSelected` selects a default or explicit UID's latest
verified revision under the existing shared lock. Its result retains the full
selected manifest identity and object bytes, but reports the UID's verified
first archive time in `Manifest.ArchivedAt`. It rejects any revision/hash pin
with `HistoryErrInvalid`. Missing or damaged UID-birth metadata, ambiguous
times, unsafe or absent identity, and a corrupt selected object refuse without
fallback or mutation.

The legacy contract is separate: `GetSelected` retains explicit-UID latest and
revision/hash-pinned recovery with the stored revision receipt, including when
the birth index is unavailable. `GetExact` remains that complete-identity
recovery entry point.

## Accepted-source counterexample

Before the implementation, the accepted test
`TestCoordArchiveLatestGetUsesUIDFirstArchivedAt` passed at source hash
`a715f577ab9a924d346686b460055e2f856af269e9d1242dee3f981fedf866cb`
for `coordarchive_get.go` and
`72b03644737438832c5eec921ddaa94e7fe9a3828827e937d82ef417bd756153`
for `coordarchive_get_first_at_test.go`. Its two revisions use
`2026-10-15T10:15:00Z` and `2026-10-15T11:15:00Z`: legacy explicit-UID
`GetSelected` correctly returns the latter stored revision receipt, whereas a
UID-first item requires the former time. That behavior is the intended legacy
contract, not an accepted-base failure.

## Focused evidence

The coordinator strengthened the retained-receipt assertions after worker
handoff: both legacy explicit-UID and pinned reads must preserve the full
manifest, latest object bytes and stored UTC revision time despite birth-index
damage. Each individual pin component is also refused before mounted-root
selection, with no result bytes or metadata. The private selector comment now
explicitly distinguishes legacy explicit-UID recovery from item mode.

The first advisory review found that the item ambiguity error inherited a
suggestion to supply revision/hash pins, which this new entry point rejects.
The item path now reports ambiguity without that unusable suggestion. The
legacy error guidance remains unchanged, and an exact pinned legacy recovery
still succeeds when item selection is ambiguous. The regression checks these
separate caller contracts without fixing the new message to exact prose.
The original finding and earlier captures remain preserved.

The final coordinator selection passed **11 methods / 33 test nodes** in one
direct JSON capture. The separate worker candidate capture also has 11 methods
and 33 test nodes, plus one package pass event; it is not added to this count.
The finite selection includes all new item methods and the eight named legacy
selector, first-time and exact-recovery methods.

```sh
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -p=1 -json ./internal/agentd -run '^(TestCoordArchiveItemGet.*|TestCoordArchiveLatestGetUsesUIDFirstArchivedAt|TestCoordArchiveSelectorsUseUIDFirstAndRevisionRecordedTimes|TestCoordArchiveSelectorsRefuseTimestampTiesButExactPinsWork|TestCoordArchiveSelectorsNeverFallbackFromCorruptLatest|TestCoordArchiveSelectorsValidateAndPreserveProjectAmbiguity|TestCoordArchiveSelectorsRefuseMissingOrDamagedUIDBirth|TestCoordArchiveExactGetRefusesMissingAndDamagedManifest|TestCoordArchiveExactGetRefusesUnsafeOrAmbiguousNames)$' -count=1 -timeout=60s
```

Use cached Go 1.27.2 and existing dependencies with downloads off. The direct
command used the cached executable’s full path; the command above is portable
with that toolchain on PATH. No broad agentd suite, held TTY test, envtest,
load loop or local image build ran. Four resource fixtures cover two revisions,
UID recreation, strict birth integrity, ambiguous or corrupt latest selection,
unsafe or missing identities, complete object/manifest equality and read-only
archive-tree snapshots. Timestamp assertions compare the instant and explicitly
require UTC where the contract requires it.

Direct raw JSON SHA-256: `e33836fe5303f7ad8f56cd3e040dae1cb7d211a8ece7f22d8e08cd56f4382964`.

| Tested source | SHA-256 |
| --- | --- |
| `internal/agentd/coordarchive_get.go` | `031c20f6bfa1757964aff1a5eff1f8df2012e3c1c997d4612294168aef1d2c8e` |
| `internal/agentd/coordarchive_item_get_test.go` | `09b8629e2aad0d58c6c23503ae90d3078f3b668995ed32e26a4e64296306ea73` |
| `.agents/sagas/distributed-dev-env/designs/024-coordination-record-retention.md` | `a89d5179c23736aa219c580944fd9504904fa7fb8cb4bf531572976572d4bdf6` |

The hashes attest the tested Go and contract bytes. This later verification
edit does not change those bytes. Raw commands, captures, exit statuses and
original failures remain supplemental PVC records under
`/home/dev/work/orders/dev-env-chapter7-archive-item-get-1015/`; they are not
public artifacts. The committed tests, source hashes, accepted-source example,
portable command and public PR checks provide reviewable source evidence.

The first focused run failed to build only because the new test passed a
`CoordArchiveStoredManifest` where the existing helper takes its embedded
`CoordArchiveManifest`. The raw JSON failure was retained; the test-only type
correction preceded the final green run. An intermediate green command omitted
the item-test suffixes from its regular expression; its raw JSON was retained
and the final candidate command uses `TestCoordArchiveItemGet.*`. Direct raw
JSON and stderr captures, exit statuses, and source hashes are supplemental
worker artifacts in the reserved order directory.

## State and remaining gates

At this source checkpoint, the candidate and its focused offline fixtures are
complete. Independent native review, ordinary exact-head required CI and
available advisory findings must be inspected before guarded source merge.
The public PR and acceptance receipt record later disposition; this candidate
record does not claim it in advance.

This source seam does not wire the CLI, shelf transport, TaskClaim HTTP route,
controller, provider, runtime or deployment. No installed or usable acceptance,
or completion of R4, is claimed. All historical failure, deviation, deadline
and stall records remain preserved. The original failed attempt and denied
rerun on #301, dependent R3 work, and every separate auth, access, security,
provider, archive activation or deletion, deployment, v1 and cutover hold remain
unchanged.
