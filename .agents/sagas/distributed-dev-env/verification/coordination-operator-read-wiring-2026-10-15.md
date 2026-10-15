# C7-R4 operator archive-read wiring — 2026-10-15

## Worker start

```json
{
  "story": "C7-R4-OPERATOR-ARCHIVE-READ-WIRING-1015",
  "owner": "task-dev-env-1015-180602",
  "base": "47d7c9b709fee696e11b63117d7a34cdba89680d",
  "actualWorkerStart": "2026-10-15T22:07:37Z",
  "checkpointAt": "2026-10-15T22:22:37Z",
  "workerHardStopAt": "2026-10-15T22:32:37Z",
  "totalHardStopAt": "2026-10-15T22:51:53Z"
}
```

This is the existing alternate start record, not a late-created canonical
`worker-start.json`; that canonical filename was absent. The order-side
`pickup-verified.json` and `start-alternate-receipt.json` remain the pickup
receipts. No clock was reset.

## Source result

`cmd/dev-env-operator/main.go` adds one default-false
`--enable-coordination-archive-reads` option. The existing API literal receives
that option as `CoordinationArchiveReads`, and its typed
`CoordinationArchive` is `&shelf.CoordinationArchive{Shelf: rescueShelf}`. The
existing `ShelfSlots` remains the same `shelf.NewExecSlots(shelf.DefaultExecSlots)`
pointer, whose accepted capacity is two. No controller, factory, runtime
activation, retention wiring, ACL change, or create-path change was added.

The focused test covers default, explicit false, true, and invalid boolean
parsing. Its compile-time assertion keeps the existing
`shelf.CoordinationArchive` reader contract typed. Existing package tests cover
the independent record, history, delete, coordinator, and ACL gates without a
manager, cluster, or exec.

The workflow guide says this source option is default off and uninstalled; it
does not provide a deployment or activation instruction.

## Captures

The original `TestCoordinationArchiveReadFlagAcceptedSourceRED` run was an
absence control, not the desired-contract RED: it expected the unknown-flag
error and therefore passed (exit 0). Its direct raw files are
`/home/dev/work/orders/dev-env-chapter7-operator-archive-wiring-1015/absence-control.stdout`,
`absence-control.stderr`, and `absence-control.exit`; their SHA-256 values are
`ee4d795ebda2e6d8a1381c9d60fea34a0194a48c20b4922d3ad7eb196a2dd58f`,
`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`,
and `9a271f2a916b0b6ee6cecb2426f0b3206ef074578be55d9bc94f6f3fe3ab86aa`.

The genuine desired-contract RED used a `go test -overlay` mapping of the
byte-exact accepted-base `main.go` to a temporary test that first accepted
`parseFlags(nil)` then required the new flag to parse. It had no new option-field
reference. It failed as required, exit 1, specifically with `flag provided but
not defined: -enable-coordination-archive-reads`. Raw JSON stdout, stderr,
exit, overlay, accepted source, test source, and source/test hashes are retained
under `/home/dev/work/orders/dev-env-chapter7-operator-archive-wiring-1015/` as
`desired-contract-red.json`, `desired-contract-red.stderr`,
`desired-contract-red.exit`, `desired-contract-red-overlay.json`,
`accepted-main.go`, `desired-contract-red_test.go`, and
`desired-contract-red-source-test.sha256`. The accepted-main SHA-256 is
`033d545301d3ed41bfb5ddbdb0d791ad8cc82c666df05f6d42b1b7c22c95bc9c`; the
desired RED test SHA-256 is
`1e7ac677596d7f82dece36b5f525216eeef91117fc5d242571e53c96e26607ac`.

After the source correction, the sole final focused capture was:

```text
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 \
  env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
  timeout 90s /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go \
  test -json -count=1 ./cmd/dev-env-operator
```

It exited 0: 25 passing test events, one passing package event, and zero failing
events. These are separate event counts, not combined with the RED run. The raw
files are `final-focused.json`, `final-focused.stderr`, and
`final-focused.exit` in the same order directory, with SHA-256 values
`d4f994421544e70f8c37ecc0fe2e1ac7e751f86f753c84620bf1dd928ffbd080`,
`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`, and
`9a271f2a916b0b6ee6cecb2426f0b3206ef074578be55d9bc94f6f3fe3ab86aa`.

The final source hashes, also preserved in `final-source-test.sha256`, are:

```text
7d935c005fe755a302bd53d6d43f338b17b4abf7e367211d4c08cdcfd932fed6  cmd/dev-env-operator/main.go
3019e6c1b1659a28b130c1b2b9cbd946247add26f5e2725a3560476e66c3d231  cmd/dev-env-operator/coordination_archive_test.go
dad8716b661596364f5738eb5d15805936820c9bcb43ed61459f5e99a00d121a  docs/workflow-guide.md
```

No unexpected test failure occurred. The expected accepted-base desired-contract
RED is retained above. No full CI, remote CI polling, advisory review, or
independent native review ran: those remain delivery gates outside this
no-agent, no-push, no-PR stop boundary. This is source/test evidence only, not
runtime, installed, or archive-read acceptance.

## Stop

Actual result recorded at `2026-10-15T22:10:01Z`, before the original worker
stop at `2026-10-15T22:32:37Z`. Next safe step: an authorized independent review
of the literal wiring, then normal guarded CI/PR handling; do not activate the
flag or archive reads from this record.
