# Coordination client archive tolerance — Story #331

This source verification covers `C7-R4-COORDINATION-CLIENT-TOLERANCE-1015`,
backlog 11 R4 and DESIGN-024 section 6, on accepted base
`504b1c5b12b8195347053667a61f0a38d52d597e`. The four claimed paths contain
client tests and this record. No production behavior or command flags changed.
This closes an acceptance-evidence gap; it does not claim an accepted-source
production defect or an artificial RED counterexample.

The sole Terra worker picked up at 21:53:14.948Z, retained its original
22:08:14.948Z checkpoint / 22:18:14.948Z stop, and committed candidate
`328762d825b1e9c1a28b240e0ce86246f1b7d9e1` at 22:01:30Z. Root verified its
stopped state and took separate acceptance ownership at 22:02:32Z, with a
22:12:32Z checkpoint, 22:21:32Z finalize target and 22:22:32Z hard stop.
The story's total cap remains 22:37:15Z. Historical failures and clocks are kept.

## Contract exercised

`ListPublishedContexts` accepts archived explicit and managed metadata, retains
all immutable fields and a UTC `archivedAt`, preserves continuation and makes one
GET. Its foreign-project, invalid-provenance and over-limit responses still
refuse. The expected complete metadata is compared independently of the
production projection, with fixture timestamps normalized to UTC for the wire.

Raw `InboxClient.Do` preserves exact valid JSON bytes for list, message, direct
GET acknowledgement and POST acknowledgement-result responses. Malformed
response JSON is refused after exactly one request. An invalid route is refused
before any request. This transport intentionally validates bounded JSON rather
than API semantic provenance. The CLI exercises its supported list, read and
POST acknowledgement entrypoints: exact response bytes plus its existing
newline, empty stderr, exact route/body, and one request per command. No new
GET-ack command, archived-list flag, automatic retry or unsolicited write is added.

Fixtures use valid complete message/ack specs, names derived by
`inbox.MessageName(savedSender, key)` and
`inbox.AckName(messageUID, recipientUID)`, distinct sender and recipient actors,
immutable identities/provenance, exact text and UTC receipts. GET ack returns
`InboxReadAck`; POST returns the distinct `InboxAckResult` wrapper. Expected
response bytes are built from those saved values, never a production projection.
This is protocol/client proof through in-process TLS fixtures, not a live API,
archive, credential, provider or installed-image observation.

## Finite source-bound evidence

The final root capture supplies the direct JSON events and counts missing from
the worker's short reported output. Each run retains its own identity; counts
are never added across runs. Supplemental artifacts live under
`/home/dev/work/orders/dev-env-chapter7-client-tolerance-1015/`.

| Capture | Evidence and outcome | SHA-256 |
| --- | --- | --- |
| Worker launcher refusal | Reported stdout embedded in `result.json`, exit 1 before tests; original direct file / exact failed command not retained | `3d006020abbc50f289d4c569928ff5a8ab45348210869e1ce514ce08ce7ffc0b` of the reported string |
| Worker timezone fixture failure | Summary in `result.json`; direct failed raw / counts unavailable | Not available |
| Worker corrected selection | Reported short stdout embedded in `result.json`, exit 0, two package summaries; direct method/node events unavailable | `3ee2fe0de2af55e03083b49323dd6be99147892ac093a7d7d3c8edecf312d6fc` of the reported string |
| **Final root capture** | `root-direct.jsonl`, exit 0, **5 methods / 10 nodes**, 5 subtests; 2 separate package passes | `27ad1c42ed5d07234c211d21bcaef4255ce07059a53ca4a9ee126dd140a6aeff` |

The five selected methods comprise the existing archived context read and four
new methods. Route operations within a method do not count as extra test nodes.
`root-direct-source.json` binds the exact test and exercised production bytes
before execution. Stderr is empty; `root-direct.exit` is 0. Exact command:

```sh
flock -w 60 /tmp/dev-env-go-tests.lock env TZ=UTC GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off nice -n 19 /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go test -json ./internal/agentd ./cmd/agentd -run '^(TestContextArchive(ReadPublishedContextAcceptsArchivedMetadata|ListAcceptsArchivedMetadataAndKeepsCurrentRefusals)|TestInboxClient(PreservesArchivedListReadAndAckEnvelopes|RejectsMalformedArchivedEnvelope)|TestInboxArchiveCommandsPreserveArchivedResponses)$' -count=1 -timeout=90s
```

The shared flock, nice 19, `GOMAXPROCS=2`, `-p=1`, cached explicit Go and downloads
off constrain this one finite selection. No broad package, TTY, envtest, load,
provider or image-build loop ran. Final pre-test source hashes:

| Path | SHA-256 |
| --- | --- |
| `internal/agentd/contextpublication.go` | `3abfd0194f9235c07387747f555fdf516a7f2cafd42aa6916d5e389fb650149e` |
| `internal/agentd/inbox.go` | `fdb953b3553d2edc6751951fd35dd63684a273938d3c2094ac494676adcc79b4` |
| `cmd/agentd/inbox.go` | `9edf4c9394c3c058b637ab6d9e0711fbb15a2b1945e4c142c3c5a7bebbafa3c7` |
| `internal/agentd/contextpublication_archive_test.go` | `6cc850df6725d9c8f79f3867f1f2c4ee26f14a579ce79c7a5b244f26d552b3c5` |
| `internal/agentd/inbox_archive_test.go` | `5cc5bf76166ca3a1eecaf06443278b3e686c9f3e33fa838499c070b50ec97613` |
| `cmd/agentd/inbox_archive_test.go` | `c4549c4f68974bec46f98ebdf332fbfd396339deaf0d4a504291e2b89d8b0706` |

## Preserved corrections and limits

The initial reported launcher/checksum refusal occurred before tests. The exact
failed command and direct failed raw are absent, so its cause is not attributed
to a general incompatibility with `GOSUMDB=off`. Root's explicit cached-toolchain
capture above passes with that setting. The original worker report remains
unchanged rather than being promoted to independently verified raw evidence.

The worker's context fixture initially retained local `PublishedAt` while JSON
decoding produced UTC. The expectation now normalizes UTC without weakening the
full-field comparison. Root also interrupted before candidate delivery to
correct the GET-ack fixture's POST wrapper and require names bound to saved
identities plus a separate recipient actor. The same worker and original clock
were retained. Those intermediate defects and the missing direct failed capture
remain recorded; no production fix is claimed.

Native review, available advisory findings, exact-head required CI and guarded
source merge are tracked on the source PR and in the durable acceptance receipts.
This record grants no installed or usable R4 acceptance. Operator read-flag
wiring remains a separate source obligation; its runtime activation, release,
deployment, archive activation/deletion and all access/provider/v1/cutover gates
remain separate. #301's original failed run and denied rerun, and dependent R3
E3/E5 observations, are unchanged.
