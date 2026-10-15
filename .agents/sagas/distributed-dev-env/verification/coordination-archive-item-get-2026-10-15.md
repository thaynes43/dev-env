# Coordination archive item get — 2026-10-15

Story C7-R4-ARCHIVE-ITEM-GET-1015 is the source-only library slice for saga
distributed-dev-env, epic #175 / backlog 11 R4. It began from accepted base
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

All Go commands used the shared offline lock and bounded environment:

```sh
flock -w 60 /tmp/dev-env-go-tests.lock env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off nice -n 19 /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go test -json ./internal/agentd -run '^(TestCoordArchiveItemGet.*|TestCoordArchiveLatestGetUsesUIDFirstArchivedAt|TestCoordArchiveSelectorsUseUIDFirstAndRevisionRecordedTimes|TestCoordArchiveSelectorsRefuseTimestampTiesButExactPinsWork|TestCoordArchiveSelectorsNeverFallbackFromCorruptLatest|TestCoordArchiveSelectorsValidateAndPreserveProjectAmbiguity|TestCoordArchiveSelectorsRefuseMissingOrDamagedUIDBirth|TestCoordArchiveExactGetRefusesMissingAndDamagedManifest|TestCoordArchiveExactGetRefusesUnsafeOrAmbiguousNames)$'
```

The final candidate run exited 0 and reported 34 pass events, including the new item
tests and every named legacy selector/exact-recovery regression. It uses four
resource fixtures, two revisions, a recreated older UID, UTC `Equal` checks,
full manifest/object checks, and before/after archive-tree snapshots. Candidate
source hashes are:

```text
937605f739af1430f3c27a6c1dc889286a5079471f8e1d39efbc1a2580e0fe4f  internal/agentd/coordarchive_get.go
591940c01fbfa5baf59ccd202fa72ad64243679044543f7a2f4f70f99cb6205b  internal/agentd/coordarchive_item_get_test.go
a89d5179c23736aa219c580944fd9504904fa7fb8cb4bf531572976572d4bdf6  .agents/sagas/distributed-dev-env/designs/024-coordination-record-retention.md
```

The first focused run failed to build only because the new test passed a
`CoordArchiveStoredManifest` where the existing helper takes its embedded
`CoordArchiveManifest`. The raw JSON failure was retained; the test-only type
correction preceded the final green run. An intermediate green command omitted
the item-test suffixes from its regular expression; its raw JSON was retained
and the final candidate command uses `TestCoordArchiveItemGet.*`. Direct raw
JSON and stderr captures, exit statuses, and source hashes are supplemental
worker artifacts in the reserved order directory.

## State and remaining gates

Source: candidate. Focused constrained tests: passed. Native acceptance, CI,
independent review, merge, release, installation and usable R4 acceptance:
PENDING. No CLI, shelf transport, TaskClaim HTTP route, controller, provider,
runtime or deployment wiring is included in this story.
