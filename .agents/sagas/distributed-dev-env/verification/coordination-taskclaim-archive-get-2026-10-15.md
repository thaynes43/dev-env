# Coordination TaskClaim archive GET — source checkpoint

Story `C7-R4-TASKCLAIM-ARCHIVE-GET-1015` / issue #329, accepted base `504b1c5b12b8195347053667a61f0a38d52d597e`. This is source-only evidence; it does not activate an archive, alter retention, access credentials, runtime, deployment, v1, or cutover.

## Contract implemented

`GET /v1/task-claims/{claim}?project=P[&uid=U]` keeps the live path first. No UID and matching-live-UID return the ordinary live projection with no archive I/O. A missing live record, or an explicit different valid UID, can read a default-off archive only through the optional `GetItem` interface. Legacy `Get` is never used for this path.

The archive path takes the existing shared slot, verifies the complete canonical object and digest before reusing the TaskClaim metadata validator, binds route name/optional UID/project, and rechecks the caller's current claim scope after archive I/O. It returns immutable metadata with `archived=true`, the item UID-first receipt, and unknown owner/stop state. It never calls `claimView` or joins the archived owner Session.

The test oracle independently constructs every `apiv1.TaskClaim` field from the saved object/spec, including identity, owner, saved actor, complete provenance, creation time, reported head/time, states and receipt. It normalizes both API wire values to UTC before one full `reflect.DeepEqual`; it does not reuse the production projection. The saved actor is distinct from the reading caller. The archived owner is removed, recreated at the same name with a different UID, and guarded by a poisoned/counted reader that proves zero owner-key reads.

## Captures and source binding

The accepted-base RED was run before production wiring with an exact `git show 504b1c5…:internal/apiserver/taskclaims.go` Go overlay. Its valid live control passed; after deleting the live claim, the verified archive object still returned 404. It exited 1 by design. Raw stdout/stderr, exit and hashes are private worker artifacts in `/home/dev/work/orders/dev-env-chapter7-taskclaim-get-1015/red-direct.*`; pre-wire hashes are `red-source-hashes.txt`.

The one final GREEN command is an offline serialized source test:

```sh
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go test -p=1 -json ./internal/apiserver -run '^(TestTaskClaim.*|TestTaskClaims.*)$' -count=1 -timeout=60s
```

Its raw JSON, stderr, exit, SHA-256, method count and package count are separately retained in `/home/dev/work/orders/dev-env-chapter7-taskclaim-get-1015/final-*`; they are not platform-only evidence. Pre-test code source hashes:

| File | SHA-256 |
| --- | --- |
| `internal/apiserver/taskclaims.go` | `1254a86849ffd2764e24690944d884a6db327526d52f3dd4c45d62f6cbe0468d` |
| `internal/apiserver/coordination_archive.go` | `4293d840250da03ea115c0916f90581c58a782d6d460cd425e593c8648314719` |
| `internal/apiserver/coordination_taskclaim_archive_get.go` | `7aa6b01d63917ba03bf8c2d6f2626bc801fb881ae0318eeff56dba453dd96652` |
| `internal/apiserver/coordination_taskclaim_archive_get_test.go` | `88910b420fa06341f93c5dd50ca04bbf785ce7acf20bda3b4ae6d9a6976d31a4` |

## Coverage and remaining gate

The focused fixtures cover the accepted-base counterexample, default-off, live-wins, explicit matching/different UID, strict query refusal before shelf, full-object corruption, typed not-found/plain error, nil/get-only reader, nil/busy slots, cancellation before/after I/O, caller scope/session/pod fences, coherent foreign live identity masking, owner absence/recreation/no-join, and an in-process typed shelf `get-item` transport assertion. Slots are checked for release on every acquired non-busy path.

This candidate does **not** claim the entire ORDER semantic envelope matrix: the remaining explicit source review gate is the exhaustive individual manifest/API/resource/spec/name/provenance/deletion/receipt mutation table with fresh valid envelope/digest reconstruction for every semantic case, plus the separate coordinator-positive fixture. No CI, review, merge, installation, R4, or usable acceptance is claimed. The interrupted pre-candidate build failure and all earlier RED/focused outputs remain preserved in the order directory.
