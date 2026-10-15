# Coordination item-get transport verification

Story #327, `C7-R4-ITEM-GET-TRANSPORT-1015`, implements the next retention read
fallback source seam in backlog 11, R4, under DESIGN-024 section 6. Its accepted
base is `1b8cc27997932d4474dcca21fcb1bdd3ba076a67` (PR324). The worker began
at 20:48:25Z and stopped at 20:57:29Z. Root accepted correction ownership in a
separate finite phase at 20:58:14Z. Source acceptance, required CI, review and
merge remain separate gates. No installed or usable R4 acceptance is claimed.

## Contract and scope

`agentd ctl coord-archive get-item --resource R --name N [--uid U]` dispatches
to accepted `GetItemSelected`. It selects the latest verified object and returns
the UID's first UTC archive receipt. It accepts no revision/hash flags and reads
no stdin. Legacy explicit-UID `get`, including exact pinned recovery, keeps its revision
receipt and recovery behavior when the birth index is unavailable. Default
`get` with no UID retains its existing first-UID receipt semantics.

`CoordinationArchive.GetItem` rejects either pin before shelf access with typed
`invalid`, validates its selector, constructs the narrow command, and reuses the
existing bounded full-object response verifier. Full selector, manifest, digest,
canonical bytes and UTC receipt checks remain shared with legacy `Get`.
The seven-path claim changes only this transport, its tests and help synopsis,
the narrow design contract, and this verification record. There is no HTTP,
controller, provider, runtime, credential or deployment wiring.

## Counterexample

The corrected RED uses Go's supported overlay with **byte-exact** accepted
`cmd/agentd/coordarchive.go` obtained directly from `git show`. That source has
SHA-256 `164108213f7058958d4b2f17e02ba25b91deb124fc8b47a5e77817dd4994778e`.
The test first puts a valid object and proves legacy exact recovery works. It
then requests `get-item`, which accepted source refuses with usage exit 2 and no
object. The test exits 1 for that missing contract, after successful setup.

The direct raw is `root-exact-base-red.jsonl`, SHA-256
`00c406b69a0377561cca57de92d44887019d8b0b5483a36f3a5e6ddb594ef3ef`. The overlay and source bindings are saved as
`accepted-base-exact-overlay.json`, `accepted-base-exact.go` and
`red-exact-source.json`. Reproduction uses the command below with
`-overlay <absolute-overlay-path>` and
`-run '^TestCoordArchiveCLIGetItemTransport$'`, selecting `./cmd/agentd` only.
This is separate from the worker's original RED, whose manually reconstructed
overlay changed a usage newline escape and removed a comment. That original
capture, its differing hash, and the native finding remain preserved; it is not
represented as exact accepted-source evidence.

## Final coherent focused evidence

After genuine missing-assertion corrections, root ran:

```sh
flock -w 60 /tmp/dev-env-go-tests.lock env TZ=UTC GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off nice -n 19 /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go test -json ./cmd/agentd ./internal/shelf -run '^(TestCoordArchive.*|TestCoordinationArchive.*)$' -count=1
```

It exited 0: **22 methods, 35 test nodes** (including
13 subtests), with 2 separate package pass events.
Direct raw `root-lint.jsonl` SHA-256:
`9ecb75a2536ebc2ca69df2de96b7eccb2ea53235a33ea444ce2ff65700c56211`.

CLI assertions cover controlled old/new UID publication, late recapture and
latest revisions, full object/manifest identity with first receipts, unsafe and
missing selectors, rejected pins, missing/damaged birth indexes, corrupt or
tied latest revisions, and unchanged legacy explicit/pinned recovery. Positive
item reads compare archive path, content, permission and modification-time
snapshots before and after the read. Shelf assertions cover exact arguments,
no stdin, default/explicit UID, individual and combined pins before exec,
typed invalid and corrupt errors, cancellation, missing fields, resource/name
and manifest refusal, digest failure and non-UTC/zero receipts. The canonical
byte test first proves its noncanonical input verifies against the valid
canonical manifest and digest, then requires the specific byte-canonical refusal;
an earlier invalid-digest object cannot substitute for that assertion.

The final selection includes the unchanged legacy command/client tests. Worker
GREEN (21 methods / 28 nodes) and root's earlier assertion run remain separate;
no runs are summed into the final count. Supplemental artifacts are under
`/home/dev/work/orders/dev-env-chapter7-item-get-transport-1015/`. Committed
contract, tests, command and hashes provide portable source evidence.

Go source SHA-256 bindings for the final run:

```text
e2c49a06d0cf8523f2adf2b880bdd095ba0c97c5649e430bdb96a976d09d94aa  cmd/agentd/coordarchive.go
fc55e561381fcab5545d4558d0542cb810ea718be81b04e426a976d5620155cb  cmd/agentd/coordarchive_item_get_test.go
962fd0488ef54c91b2ad42e0f80bc5352e5681c39874390df9a67658bd5d1479  cmd/agentd/main.go
4d78854053a87f9a56b8356ab40cc61099d57d2986da6a7b5609e7d0a8ce5571  internal/shelf/coordarchive.go
c690eb61869ff3aedeea62bf843d1999f18b5bf8fc9653b37c18f9e028e857cf  internal/shelf/coordarchive_item_get_test.go
```

## Preserved failures and remaining gates

The worker initially used the wrong backlog filename, then recovered. Its first
shelf assertion indexed absent fake-executor stdin; the fixture correction now
requires no stdin entry. Initial platform-only successful output lacked durable
raw captures. Root interrupted before candidate handoff to deliver the existing
evidence obligations, preserving the original pickup, checkpoint and stop.
Native review found the reconstructed-overlay mismatch, missing transport
assertions, an inaccurate command example and missing raw hashes. Root retained
all originals, corrected those concrete gaps, and reran only the affected finite
counterexample or focused selection. No production behavior changed during root
assertion and documentation closure. No failure counter or clock was reset.

Independent native closure, exact-head required CI, available advisory findings
and guarded source merge remain acceptance gates. No installation, archive
activation/deletion, provider, access/auth, deployment, v1 or cutover action is
claimed. #301's permission-denied rerun and the dependent R3 hold remain unchanged.

The first required CI on `85c3835`, run `38806928227`, failed Lint with
three diagnostics: QF1003 on command dispatch, gofmt simplification of a test
literal, and a direct test error assertion flagged by errorlint. Root changed
the same validated dispatch alternatives to a tagged switch, applied `gofmt -s`,
and used `errors.As`. The finite final selection above passed on those exact Go
bytes. The pre-lint `root-closure.jsonl` run also passed 22 methods / 35 nodes; its
SHA-256 is `37a3e99fd95b60593349617fc43296a96916a2ea87985143003a71b46d5e8ed5`.
The final post-lint run is `root-lint.jsonl`, reported in the focused evidence
section with SHA-256 `9ecb75a2536ebc2ca69df2de96b7eccb2ea53235a33ea444ce2ff65700c56211`.
Matching counts do not combine these distinct runs. The hosted
failure is retained; the correction uses ordinary new-head CI, with no manual
rerun, workflow change, weakened assertion or budget reset.

The advisory on `17ca0d7`, run `38807679719`, requested clearer names for
the two 22-method/35-node runs. This record now binds each run to its own raw
file and SHA-256. The correction changes documentation only and adds no test
result or source behavior. The original advisory finding remains preserved.
