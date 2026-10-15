# Coordination inbox archived acknowledgement GET — 2026-10-15

Story `C7-R4-INBOX-ACK-GET-1015` / #319, under saga
`distributed-dev-env`, epic #175 backlog 11 R4. The accepted base was
`51f8e3912ecef646980e429557a2eb7ce875227d`.

This source-only candidate adds a default-off, typed archive fallback for
`GET /v1/inbox/{message}/ack` after a genuine live `ReadAck` NotFound. It
keeps acknowledgement creation live-only. The returned archived acknowledgement
is the canonical stored object, including its immutable actor; the API wire view
adds `archived` and `archivedAt` only for that verified archived result.

## Finite evidence

All Go checks used the cached full-path toolchain and shared lock:

```text
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go test ./internal/apiserver -run '<selection>' -count=1
```

- RED count: 2. The first (`red-go-test.out`, SHA-256
  `6bdee5cfa2a14fcf24eff8360c37251b971d58cd5b68879146d96fabee522223`)
  stopped in a new fixture because its `inboxreadacks` manifest omitted
  `messageUID`; it is preserved as fixture failure1, not an accepted-base
  behavior counterexample.
  The corrected accepted-base RED (`red2-go-test.out`, SHA-256
  `0b6b20383746794bd3c6a9d4d654f9059b5e79f34b4118b5ef77648db706f509`)
  ran `^TestInboxArchiveAckGetRedAcceptedBase$` and returned `404`, with zero
  archive calls, for an absent live ack and a valid archived receipt.
- GREEN count: 3 targeted selections. The initial production-wire check
  (`green-go-test.out`, SHA-256
  `e3f07ac0d7a06091bf2ece10941014caa16f1e6e6da86c786bac4cc9dbcfe7b9`)
  passed `^TestInboxArchiveAckGetRedAcceptedBase$`. The final focused selection
  (`green2-go-test.out`, SHA-256
  `fd636e783fae9736b9cedaa337a7ffa5b07e8b995baa8736e77d8f1dc2cae9c7`)
  passed `^TestInboxArchiveAckGet`. A final import-only test-source correction
  was checked with the same selection (`green3-go-test.out`, SHA-256
  `c3529b493b168971d63f7fb59e7fb6918515a4b4e01e76a2a5a6af72c503280c`).
- Exact source hashes are retained out of tree in `red2-source.sha256`
  (file SHA-256 `6fbea0eff8e14879eed58a8ee6921fa6f3b84ae70620b6fc1f001e5df54fde90`)
  and `green2-source.sha256` (file SHA-256
  `ff8981f05c5d7384fa1f8d5b78879d437688769b32b1e7fdf55ebec4c8aeabdd`).
  The final source hash record is `green3-source.sha256` (file SHA-256
  `360bdca1b60ca60e02b74f468fca455fcdd0983bcc321172f8901ce82694cec4`).

## Original acceptance closure

Production stayed frozen at `ed7524ed926d151f2dcb3167deaeb140280a9d64`.
The supplemental focused GREEN used this exact command and direct JSON output:

```text
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go test -json ./internal/apiserver -run '^TestInboxArchiveAckGet' -count=1
```

It passed with exit status 0. The raw JSON is `closure-green-go-test.json`
(SHA-256 `2e55e96d3e4f4653b84838425b9180ba9c673ff6aecab0abf1b773a926fa6a1f`);
its exact four-source hash record is `closure-green-source.sha256` (SHA-256
`af5444f299703555e94e4986e2d2b64f21e7c4b6f599bdca9676a86cefdeca5f`).

The closure cases exercise resource routing for archived message/live ack and
archived message/archived ack, stored immutable actor/recipient/message API
projection and archive receipt, default-off and live precedence, typed not-found
versus typed failure, malformed manifest bindings (namespace, name, UID,
message UID and project), digest/noncanonical/receipt refusal, nil reader/slots,
busy slots, cancellation before and after archive I/O, and released-slot checks.

The focused tests cover the accepted-base live miss, verified archive return,
default-off behavior, live ack precedence without a shelf call, and POST staying
live-only. The implementation validates the full canonical raw ack and digest,
typed resource identity, manifest message/recipient/project bindings,
deterministic acknowledgement name, stored object binding, non-deletion and
creation timestamp before applying the post-I/O caller fence.

Native independent review and CI are pending. This is a source candidate only:
it is not installed, usable acceptance, archive activation, or full R4.
