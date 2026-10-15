# TaskClaim archived GET verification

Story #329, `C7-R4-TASKCLAIM-ARCHIVE-GET-1015`, implements the single-read
retention fallback in backlog 11, R4, under DESIGN-024 section 6. Accepted base:
`504b1c5b12b8195347053667a61f0a38d52d597e`. Worker pickup was 21:24:34Z and
candidate stop was 21:37:09Z. Root took separate bounded acceptance ownership at
21:38:24Z. Source, test, review, CI and merge are separate acceptance states;
installation and usable R4 acceptance remain unproved.

## Contract and scope

`GET /v1/task-claims/{claim}?project=P[&uid=U]` keeps ordinary live reads first.
No UID or a matching live UID returns the existing live projection without shelf
I/O. A missing live record, or an explicit different valid UID, can select an
archived claim when archive reads are enabled. Default-off returns 404. A foreign
live project remains hidden, and a live-store error never falls back. Invalid,
empty or duplicate UID, malformed query, duplicate project and unsupported query
fields refuse before archive I/O. An omitted or empty project keeps the existing
403 scope response; a valid but unauthorized project remains 403.

The optional `CoordinationArchiveItemReader` keeps existing Get-only callers
compatible. Archived reads require its `GetItem` method, not legacy revision
`Get`. The helper takes a shared slot, verifies the complete object, canonical
bytes and digest, then reuses the accepted TaskClaim metadata validator. It binds
route name, optional UID and immutable project, checks the first UTC receipt and
rechecks the caller/executor/project grant after I/O. Typed not-found is 404;
missing capabilities, corrupt/ambiguous results and other archive failures are
503. Every acquired slot is released, and no TaskClaim write is attempted.

Archived projection uses the immutable metadata helper without `claimView` or
an owner Session lookup. Owner and stop states stay `Unknown`. Independent tests
construct every API field from saved data: identity, owner, actor, complete
provenance, creation time, reported head/time and receipt. UTC normalization
preserves each timestamp instant and every field before full comparison. The
saved actor differs from the reading caller. Absent and recreated owners cannot
be joined or substituted; an explicit owner-key read counter proves this.

Only the five reserved paths changed: the GET handler, optional reader interface,
new helper, new test file and this record. Creates, lists, stores, other archive
helpers, protocol/API shapes, flags and runtime settings are unchanged.

## Finite counterexamples and final evidence

The accepted-source RED ran before production wiring using the exact accepted
`taskclaims.go` bytes through Go's overlay. A valid live GET passed first, then
the missing live claim returned 404 with zero archive calls despite a verified
archive object. The accepted handler SHA-256 is
`635f8d67afe7b254f4e6fe6bb16c37d48583ad4e2b70657fa6d76c245f3f6609`;
root independently checked it against `git show` and preserved the accepted
bytes. Original pre-wire test/source hashes are in `red-source-hashes.txt`.
They describe that historical capture, not the later expanded test file.

Native review also found an actual compatibility regression: omitted `project`
returned 400 instead of the prior 403 scope response. The finite
`TestTaskClaimArchiveGetMissingProjectKeepsACLStatus` counterexample captured
that 400, and the handler now lets the missing project reach the existing scope
check while still rejecting duplicate project values.

Each run has its own raw identity; none are combined:

| Role | Raw file | Outcome and counts | SHA-256 |
| --- | --- | --- | --- |
| Accepted handler counterexample | `red-direct.json` | Expected failure after valid live control | `fca016f3ad1215c0596c94aa0bc312953ddd1dbc2af5b90ff99681fd900c5706` |
| Worker candidate | `final-direct.json` | Pass, 21 methods / 79 nodes | `4bb6d4fa44d6289e1cf8d7d2a10bb9f7dff8ee625f21609212c3006262e04b3a` |
| Compatibility counterexample | `compat-red.jsonl` | Expected failure: missing-project 400 | `ddc19c0f8155863d26bfbc80c11d7bfe7174826be84a07c544b382c2f2575df8` |
| **Final root selection** | `root-corrected.jsonl` | **Pass, 25 methods / 110 nodes** | `1565209336c8e253444c9dac634d6790793fa85f4be9f57785965c0b5ff41c13` |

The final selection includes 85 subtests and 1 separate package pass event.
It ran after the genuine compatibility, coverage and fixture corrections:

```sh
flock -w 60 /tmp/dev-env-go-tests.lock env TZ=UTC GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off nice -n 19 /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go test -json ./internal/apiserver -run '^TestTaskClaim' -count=1 -timeout=90s
```

Supplemental artifacts are under
`/home/dev/work/orders/dev-env-chapter7-taskclaim-get-1015/`. Committed tests,
contract, commands and hashes provide portable evidence. Final pre-test Go source
bindings are:

```text
0612ea57d2da635a90c6b34010b0675d013ab7e8b079ebc491fabeb790c4c0de  internal/apiserver/taskclaims.go
4293d840250da03ea115c0916f90581c58a782d6d460cd425e593c8648314719  internal/apiserver/coordination_archive.go
7aa6b01d63917ba03bf8c2d6f2626bc801fb881ae0318eeff56dba453dd96652  internal/apiserver/coordination_taskclaim_archive_get.go
a19075b3f0407fa2e17f4f99211f90e25f21c4a914b5e3354f0cfd05a24e4917  internal/apiserver/coordination_taskclaim_archive_get_test.go
```

The final matrix includes live/default-off/matching and old UID selection,
explicit item queries, missing/invalid query refusal, valid foreign live/archive
records, plain live-store error without fallback, and first-item versus legacy
revision receipt contrast. It covers full manifest, digest, byte-canonical,
namespace, identity/name, owner, actor, provenance, creation/deletion and receipt
refusals. Semantic mutations retain valid complete envelopes and fresh digests;
the noncanonical fixture first passes canonical-digest verification. Coordinator
scope, caller replacement/deletion, executor replacement/deletion, removed grants,
Get-only/nil readers, nil/busy slots and cancellation are checked. Counters prove
zero legacy Get calls, archived-owner joins and attempted TaskClaim writes in the
relevant archived cases. An in-process fake shelf verifies the typed `get-item`
command; this is source proof, not a live shelf or archive observation.

## Preserved failures, deviations and remaining gates

The worker first attempted bare `gofmt` outside the cached toolchain PATH. Two
intermediate focused failures required client/import and foreign-object fixture
corrections. Root interrupted before candidate delivery to require the already
specified independent oracle and coherent foreign-record proof, preserving the
original clock. A timezone representation mismatch was corrected with explicit
UTC normalization while retaining full-field comparison. The worker reported
its semantic matrix and coordinator-positive coverage incomplete; root completed
those gates in the existing five-path acceptance claim.

The original worker failure summaries and platform outputs remain attributed as
such. Direct intermediate raw files are absent; only the named direct RED and
final GREEN are claimed as durable worker captures. Root's first expanded run,
`root-final.jsonl`, retained two fixture failures: a supposedly invalid owner UID
was legal under the accepted opaque UID pattern, and the live-read error poison
was installed on `Client` instead of the store's actual `Live` reader. The invalid
owner fixture is now empty and the poison targets `Live`. No production UID rule,
ownership assertion or error boundary was weakened. Those failed captures and
the separate genuine compatibility counterexample remain saved.

Independent native closure, exact-head required CI, available advisory findings
and guarded source merge remain gates. No source acceptance, installed parity,
archive activation/deletion, provider runtime, access/auth, deployment, v1 or
cutover result is claimed here. #301's denied rerun and the dependent R3 hold
remain unchanged. All historical failure, budget and overrun records are retained.
