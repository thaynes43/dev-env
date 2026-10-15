# Operator coordination archive-read wiring — Story #333

`C7-R4-OPERATOR-ARCHIVE-READ-WIRING-1015` closes the operator source seam in
backlog 11 R4 and DESIGN-024 sections 5–6, on accepted base
`47d7c9b709fee696e11b63117d7a34cdba89680d`. It changes only the option,
flag registration and API construction in `cmd/dev-env-operator/main.go`, one
flag test, a workflow-guide paragraph and this record.

## Source behavior

The new `--enable-coordination-archive-reads` option defaults to false. The actual
API literal receives that option as `CoordinationArchiveReads`, and its typed
reader is `&shelf.CoordinationArchive{Shelf: rescueShelf}`. It uses the existing
shelf and unchanged `ShelfSlots` pointer from
`shelf.NewExecSlots(shelf.DefaultExecSlots)`, whose capacity is two. History and
HOME relay wiring remain unchanged. No controller, factory, pool, retention
runnable, ACL change, create-path change or deployment setting was added.

The new test checks default, explicit false, true and invalid boolean parsing.
Its compile-time assertion checks the legacy typed reader contract; independent
source review also checks the optional item-reader/list methods and the actual
API literal. Existing operator tests exercise separate record/history/delete,
coordinator and scope gates. This is source/configuration proof without starting
a manager, cluster client or shelf exec. It does not activate the flag or prove
installed archive reads.

## Bounded ownership and deviations

The implementation agent was configured as GPT-5.6-Terra/high; this does not prove
a serving model. Its actual pickup was 22:07:37Z, checkpoint 22:22:37Z and stop
22:32:37Z, with a total story cap of 22:51:53Z. A checkpoint records progress, a
stop ends the relevant phase, and the cap bounds the whole story. The start and
reported result at 22:10:01Z were saved in this verification document. Separate
canonical `worker-start.json` and `result.json` files were absent. The original
candidate retains those alternate records; the coordinator saved their export
and supported stopped-pane evidence without fabricating earlier files or
resetting the clock.

The worker's initial passing test established flag absence; its RED name was
misleading. The coordinator required a separate desired-contract counterexample
and retained the absence control. The worker also ran the entire finite operator
command package instead of the reserved new/history-test filter. That selection
deviation remains recorded. It was constrained and serial, not a repository
suite or a repeated loop; no cosmetic rerun was used to relabel it.

The Chapter7 coordinating agent took separate acceptance ownership at 22:11:21Z,
with checkpoint 22:21:21Z, finalize target 22:30:21Z and stop 22:31:21Z. Worker and
coordinator clocks and all historical counters remain separate.

## Finite direct captures

All files below are in
`/home/dev/work/orders/dev-env-chapter7-operator-archive-wiring-1015/`.
Each run keeps its own identity; counts are not added across runs.

| Capture | Result | Raw SHA-256 |
| --- | --- | --- |
| `absence-control.stdout` | Pass, expected unknown-flag absence; not desired-contract RED | `ee4d795ebda2e6d8a1381c9d60fea34a0194a48c20b4922d3ad7eb196a2dd58f` |
| `desired-contract-red.json` | Expected failure, exit 1, unknown flag after successful default parse | `5ee9dd899c18ce78c565ab760744ac548f5e16c3176ac6933eabe360126335a0` |
| **`final-focused.json`** | **Pass, 21 methods / 25 nodes**, 4 subtests; 1 separate package pass, zero failures | `d4f994421544e70f8c37ecc0fe2e1ac7e751f86f753c84620bf1dd928ffbd080` |

The genuine RED used the saved overlay to replace `main.go` with byte-exact
accepted-base bytes and replace only the new test file with the desired contract:
`parseFlags(nil)` succeeds, then the new flag must parse. It references no new
option field. The failure is specifically `flag provided but not defined:
-enable-coordination-archive-reads`, rather than a compile or fixture failure.
The coordinator independently matched `accepted-main.go` to `git show` bytes.
Its SHA-256 is
`033d545301d3ed41bfb5ddbdb0d791ad8cc82c666df05f6d42b1b7c22c95bc9c`;
`desired-contract-red_test.go` is
`1e7ac677596d7f82dece36b5f525216eeef91117fc5d242571e53c96e26607ac`.
The overlay, test/source bindings, direct stderr and exit files remain retained.

Final command (entire finite command package, as disclosed above):

```sh
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off timeout 90s /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go test -json -count=1 ./cmd/dev-env-operator
```

`final-focused.stderr` is empty and `final-focused.exit` is 0. Pre-test Go source
bindings, also retained in `final-source-test.sha256`, are:

```text
7d935c005fe755a302bd53d6d43f338b17b4abf7e367211d4c08cdcfd932fed6  cmd/dev-env-operator/main.go
3019e6c1b1659a28b130c1b2b9cbd946247add26f5e2725a3560476e66c3d231  cmd/dev-env-operator/coordination_archive_test.go
```

No unexpected test failure occurred. Required CI and independent native review
are source acceptance gates. The automated advisory is optional; any findings it
emits are assessed and resolved or answered. Exact-head delivery is tracked on
the source PR and durable receipts. No image installation, runtime read,
flag/archive activation or deletion, provider/access/auth action, deployment,
v1 change or cutover is claimed. #301's original failed run and denied rerun,
and dependent R3 observations including E3/E5, remain held.
