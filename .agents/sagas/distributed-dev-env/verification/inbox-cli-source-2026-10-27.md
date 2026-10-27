# Inbox CLI source verification, 2026-10-27

OpenProject #215. This is source-only evidence; it does not prove the command
is installed, the route enabled, or live provider delivery. The existing API
remains responsible for caller identity, project scope and immutable intent.

Synthetic TLS API tests cover explicit send intent, same-key retry, conflict,
503 Unknown without automatic replay, invalid intent before a request, bounded
list metadata, read without acknowledgement, explicit ack, redirect refusal,
token rotation, TLS rejection and suppression of API error bodies. The focused
`TestInbox` tests passed; the affected `internal/agentrun` package suite passed
on the final source. The package suite also ran before the final error-body
suppression change; it was repeated because that change affected error handling.

Go 1.27.2 at `/home/dev/work/orders/dev-env-sol-coordinator-20261022/toolchain/go1.27.2-linux-amd64/go/bin/go`; `GOTOOLCHAIN=local`, `CGO_ENABLED=0`, `GOMAXPROCS=2`, `-p 1`, `nice -n 19`, serialized with `/home/dev/work/orders/dev-env-sol-coordinator-20261022/GO-TEST.lock`. `GOCACHE`, `GOMODCACHE`, `GOTMPDIR` and `TMPDIR` were private under `/home/dev/work/orders/durable-reply-cli-20261027/runtime`. Commands were `go test -p 1 ./internal/agentrun -run '^TestInbox' -count=1` and `go test -p 1 ./internal/agentrun -count=1` with that environment and lock. `make check-layout`, gofmt and `git diff --check` also passed. No live inbox request, installation or deployment was performed.

Independent review and hosted CI remain pending; no merge is authorized by
this source work order. Acceptance of #215 remains with its authorized owner.

Advisory review correction: trusted HTTP status now selects actionable, bounded
input or limit guidance without printing typed or untyped response bodies. The
synthetic cases cover 400 and 429 with credential-bearing typed and malformed
bodies, verify no replay, and retain the 503 Unknown behavior. The focused and
package commands above passed again after this source change under the same
serial Go environment. This supersedes the earlier source test result for the
updated head; independent review and hosted CI on that head remain pending.

A second advisory correction distinguishes failure before any confirmed HTTP
response from HTTP refusal. Static guidance follows the existing client exit
classification for authentication/TLS, connection or token acquisition, and
unconfirmed reads, without printing transport error details. An untrusted TLS
wire test verifies no inbox request and CA guidance; a classification table
checks all these paths and credential suppression. Mutation Unknown guidance is
unchanged. Focused inbox tests and the affected package suite passed again under
the recorded serial environment. Independent review and hosted CI remain
pending on this updated head.
