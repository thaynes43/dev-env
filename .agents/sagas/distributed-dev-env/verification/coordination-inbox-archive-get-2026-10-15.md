# Inbox archived GET fallback checkpoint — 2026-10-15

This is a bounded source checkpoint for C7-R4-INBOX-GET-1015 / issue #315. It
does not claim installed, released, runtime, shelf, or Chapter 7 acceptance.

## Scope and result

`GET /v1/inbox/{message}?project=P` keeps the live read first. Only the
inbox store's real `ErrNotFound` enters the default-off archive branch. The
branch takes one shared shelf slot, requests the existing default no-UID
`inboxmessages` selector, verifies the full canonical object and its manifest,
and projects exact immutable text and attribution with `archived: true` and the
stored UTC receipt. It checks project and full original recipient identity, then
repeats the live caller fence after shelf I/O. It does not read a sender Session.

Acknowledgement GET and POST remain explicitly live-only, as does publication.
Foreign live records and other live-store failures do not reach the shelf. A
typed shelf `not_found` becomes the uniform 404; unavailable slots, absent
archive wiring, malformed or corrupt data, and other shelf errors remain 503.
No flag default or runtime wiring changed.

The test fixture uses a full canonical InboxMessage and a digest/size manifest.
It covers live precedence, default-off with zero shelf calls, recipient and
coordinator scope, immutable metadata/text projection, a live error, a genuine
shelf miss, corrupted bytes, recipient-manifest mismatch, busy-slot refusal,
and acknowledgement/publication non-fallback. Existing inbox archived-list
tests continue to cover its metadata-only projection and archive-list bounds;
this slice adds no list behavior.

## Finite evidence

The RED used the accepted-base GET behavior as a temporary local overlay: the
new test and archive fixture remained, but the GET call passed `false` for the
fallback option. The direct raw JSON shows the deleted live message returning
404 with zero shelf calls. The final source restores the option and the focused
selection passes.

| Phase | Command | Result | Raw capture SHA-256 |
| --- | --- | --- | --- |
| RED | `nice -n 19 flock -w 60 /tmp/dev-env-go-tests.lock env PATH=/home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin:$PATH GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -json ./internal/apiserver -run '^TestInboxArchiveGetRedAcceptedBaseAndLiveWins$' -count=1` | expected exit 1: archive-good, live-missing case was 404 | `56b0708d15c381d5bf6f11a058b2d3b37d065b789c1c61228d2f48c370f50af8` |
| GREEN | `nice -n 19 flock -w 60 /tmp/dev-env-go-tests.lock env PATH=/home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin:$PATH GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -json ./internal/apiserver -run '^TestInboxArchiveGet' -count=1` | exit 0 | `eeb41f6c0f1bb0d27ab9d4e43fd6a4c3f46ea909fd1ed6b6e573659113fde8a2` |

Both stderr captures were empty (`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`). The direct raw captures and the exact command/environment record are retained in `/home/dev/work/orders/dev-env-chapter7-inbox-get-1015/`; those PVC-local captures supplement this committed source checkpoint.

Final source hashes:

| File | SHA-256 |
| --- | --- |
| `internal/apiserver/inbox.go` | `184c0e263bdd3c57a7b910734ef3b7d8dbd1bd6a2b7d875b2907e21fdeeaf2e5` |
| `internal/apiserver/coordination_inbox_archive_get.go` | `4e4a95c62a6e74d2a9ea5e427922efd8c4b00a919fdb819cade0f834e2d5852d` |
| `internal/apiserver/coordination_inbox_archive_get_test.go` | `72f978f0cefcbdb52fa2c6a86a785a7d20af0835843af582bb9de31597ea717a` |

The reserved start receipt records the verified base, clean worktree, live
`gpt-5.6-terra` / `high` TUI banner, exact four-path boundary, and time budget.
All existing holds and counters persist. The next safe step is Chapter 7's
independent source review and sole acceptance process; this worker must not
push, open a PR, merge, activate the flag, or continue into another claim.
