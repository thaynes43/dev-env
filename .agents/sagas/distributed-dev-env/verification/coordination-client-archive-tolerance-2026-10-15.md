# Coordination client archive tolerance — Story #331

Source-only verification for `C7-R4-COORDINATION-CLIENT-TOLERANCE-1015` on
`504b1c5b12b8195347053667a61f0a38d52d597e`. Pickup was
2026-10-15T21:53:14.948Z; the unchanged checkpoint and hard stop are
2026-10-15T22:08:14.948Z and 2026-10-15T22:18:14.948Z.

## Contract exercised

- `ListPublishedContexts` accepts archived explicit and managed metadata,
  retains every immutable field and a UTC `archivedAt`, preserves the cursor,
  makes one GET, and still refuses a foreign project, invalid provenance and a
  list over its requested bound.
- Raw `InboxClient.Do` retains exact valid JSON bytes for the list, message,
  direct GET acknowledgement, and POST acknowledgement-result responses. Its
  existing bounds reject a malformed JSON envelope and an invalid route without
  an API request. It intentionally does not acquire API semantic provenance
  validation in this slice.
- The CLI exercises only its supported list, read and POST acknowledgement
  entrypoints. It retains the exact response bytes plus its newline, keeps
  stderr empty, and emits exactly one request per command.

The synthetic message and acknowledgement names come from
`inbox.MessageName(savedSender, key)` and
`inbox.AckName(messageUID, recipientUID)`. The source and recipient identities,
message/ack UIDs, and actors use UUID-shaped immutable values. The saved sender
and recipient acknowledgement actor are deliberately distinct. GET ack is the
direct `apiv1.InboxReadAck` response of `readInboxAck`; POST ack is the distinct
`apiv1.InboxAckResult` response. The tests marshal independent complete expected
views and reject any collapse of those two byte shapes.

## Source hashes before final run

| Path | SHA-256 |
| --- | --- |
| `internal/agentd/contextpublication.go` | `3abfd0194f9235c07387747f555fdf516a7f2cafd42aa6916d5e389fb650149e` |
| `internal/agentd/inbox.go` | `fdb953b3553d2edc6751951fd35dd63684a273938d3c2094ac494676adcc79b4` |
| `cmd/agentd/inbox.go` | `9edf4c9394c3c058b637ab6d9e0711fbb15a2b1945e4c142c3c5a7bebbafa3c7` |
| `internal/agentd/contextpublication_archive_test.go` | `6cc850df6725d9c8f79f3867f1f2c4ee26f14a579ce79c7a5b244f26d552b3c5` |
| `internal/agentd/inbox_archive_test.go` | `5cc5bf76166ca3a1eecaf06443278b3e686c9f3e33fa838499c070b50ec97613` |
| `cmd/agentd/inbox_archive_test.go` | `c4549c4f68974bec46f98ebdf332fbfd396339deaf0d4a504291e2b89d8b0706` |

## Finite runs and raw captures

Every run took the shared lock, ran at `nice -n 19`, used `GOMAXPROCS=2`,
`-p 1`, and selected only these five test functions in two packages. There are
zero cluster/runtime nodes; route-family counts are context GET 2, raw inbox GET
3 plus POST 1, and CLI GET 2 plus POST 1. Counts are route families, not a sum
across reruns.

1. Early run, exit 1, before tests started. Stdout/stderr combined exactly:

   ```text
   go: golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64: verifying module: checksum database disabled by GOSUMDB=off
   ```

   Its raw SHA-256 is
   `3d006020abbc50f289d4c569928ff5a8ab45348210869e1ce514ce08ce7ffc0b`.
   The cause was the incompatible `GOSUMDB=off` setting, not source behavior.

2. The next cached/offline run used `GOPROXY=off GOTOOLCHAIN=local` and reached
   the tests. It exited 1 only because the context fixture compared a local-time
   `PublishedAt` to its JSON-decoded UTC equivalent. Both explicit and managed
   subtests reported one request and a valid archived UTC receipt. The exact
   failure is retained in the task tool transcript and the durable worker result;
   the correction normalizes the fixture expectation to UTC without changing the
   client or acceptance boundary.

3. Final run, exit 0:

   ```text
   ok      github.com/thaynes43/dev-env/internal/agentd  0.046s
   ok      github.com/thaynes43/dev-env/cmd/agentd       0.045s
   ```

   Raw stdout SHA-256:
   `3ee2fe0de2af55e03083b49323dd6be99147892ac093a7d7d3c8edecf312d6fc`.
   Stderr was empty. Command:

   ```sh
   flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOPROXY=off GOTOOLCHAIN=local GOMAXPROCS=2 /home/dev/.local/go/bin/go test -p 1 ./internal/agentd ./cmd/agentd -run '^(TestContextArchive(ReadPublishedContextAcceptsArchivedMetadata|ListAcceptsArchivedMetadataAndKeepsCurrentRefusals)|TestInboxClient(PreservesArchivedListReadAndAckEnvelopes|RejectsMalformedArchivedEnvelope)|TestInboxArchiveCommandsPreserveArchivedResponses)$' -count=1
   ```

This is source/test evidence only. It does not enable archive reads, prove an
installed image or runtime behavior, replace API-level semantic validation in
the raw inbox transport, or clear the remaining R4/R5/R6 delivery gates.
