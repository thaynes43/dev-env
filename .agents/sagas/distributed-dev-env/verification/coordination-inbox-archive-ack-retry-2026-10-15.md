# Inbox archived acknowledgement retry verification

Story #325 (`C7-R4-INBOX-ACK-RETRY-1015`) implements the retention read fallback
in backlog 11, R4, under DESIGN-024 section 6. Accepted base:
`862f7a4930f0d2d0274ffc5ac96750a8f311bac6`. This record attests source and
focused tests. Required exact-head CI, independent review, and source delivery
remain separate gates; installation and usable R4 acceptance remain unproved.

## Contract

An original recipient retries `POST /v1/inbox/{message}/ack` with one bounded
empty JSON object. A live message retains its ordinary acknowledgement create
and confirmation path without archive I/O. With archive reads enabled, a verified
archived message instead requires its verified archived acknowledgement, even
when a live acknowledgement remains temporarily. This branch returns HTTP 200,
`Confirmed`, the complete immutable stored acknowledgement, and its UID's first
UTC archive receipt. It attempts no write and never creates an acknowledgement.

The helper reuses the accepted canonical object, digest, complete recipient,
message, project, actor, and receipt verifier. It rechecks the original recipient
and current controlled executor after acknowledgement I/O. A missing archived
acknowledgement returns 404; other archive errors return 503. Coordinator writes
stay denied. The sender need not remain live. A deleting original recipient
Session remains eligible, but missing/replaced identities and changed or deleting
executors do not. The saved actor remains unchanged when a valid caller resumes.

The empty intent parser rejects unknown fields, multiple values, malformed,
oversized and non-object bodies, including `null`, before archive I/O. No accepted
GET helper, store, protocol, API shape, runtime setting, or workflow was changed.
Only the superseded POST-live-only assertions were removed from two existing
GET test methods; their publish, default-off and live acknowledgement assertions
remain. Those tests run together with the new POST tests.

## Counterexample and focused acceptance

The worker reported an accepted-base RED but omitted its durable raw capture.
Root retained that deviation and reconstructed the finite counterexample with
Go's supported overlay: only `inbox.go` is replaced by its exact accepted-base
content. The additive retry helper compiles but is unreachable from that handler.
The positive test first proves live acknowledgement creation returns 201, then
removes the live message and acknowledgement while supplying verified archive
envelopes. Accepted source returns 404 with zero message and acknowledgement
archive calls, instead of the required 200 confirmation. The strict intent case
also shows accepted source permits `null` through validation and returns 404
instead of 400. These are contract failures after valid fixture setup.

Direct reconstructed RED: `root-base-red-fixed.jsonl`, SHA-256
`9b8dbd9dde20569421821dc6e8fddf3255d09b7461d1ba6898f3e29cdf0c7e8f`. It is separate from the worker's unpersisted report.
The accepted `inbox.go` hash and overlay map are in `red-source-binding.json`
and `red-overlay.json` beside the raw capture. Reproduction uses the focused
command below with `-overlay <overlay.json>` and
`-run '^(TestInboxArchiveAckRetryConfirmedNoWrites|TestInboxArchiveAckRetryStrictEmpty)$'`.

After correcting the parser and strengthening assertions, root ran:

```sh
flock -w 60 /tmp/dev-env-go-tests.lock env TZ=UTC GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off nice -n 19 /home/dev/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64/bin/go test -json ./internal/apiserver -run '^(TestInboxArchiveGet.*|TestInboxArchiveAckGet.*|TestInboxArchiveAckRetry.*)$' -count=1
```

This one final selection passed: **23 methods, 153 test nodes**
(including 130 subtests), plus one separate package pass event.
Raw `root-scope-green.jsonl` SHA-256:
`8ca60ba84f492a02f10e5b414ed0104635a5a0e7cd2a776d119283f9d2f5277f`.
All supplemental captures reside at
`/home/dev/work/orders/dev-env-chapter7-inbox-ack-retry-1015/`;
committed tests, the contract, command and hashes provide portable source evidence.
No separate runs are summed into these counts.

Every archived success/refusal fixture installs an explicit Create-attempt
counter after setup and asserts zero attempts, even if a forbidden Create error
would otherwise be ignored. The positive oracle independently constructs and
wire-normalizes every acknowledgement field. POST-specific tests cover live
initial creation/retry, partial release, default-off, stored actor preservation,
original/deleting recipient, absent sender, coordinator and foreign callers,
replacement during either archive read, missing/deleting executors, strict body
validation, valid-envelope semantic mismatches, typed and plain failures,
canonical/digest corruption, missing reader/slots, busy slots and cancellation
before/after I/O, and removed project scope during acknowledgement I/O. Acquired slots are checked for release. Private message text
must not appear in refusal or acknowledgement projections.

## Preserved failures and gates

The worker's Create-interceptor callback initially had the wrong client type.
Its extra-intent fixture initially marshalled a buffer as an empty object; fixing
the fixture exposed and corrected acceptance of non-empty objects. Its initial
poison did not count attempted writes and its POST matrix was incomplete. Native
review recorded these gaps and missing durable RED evidence, plus `null` intent
acceptance. Root corrected these within the existing six-path claim. Root's first
expanded counterexample capture had one undefined fixture variable compilation
failure; that raw file remains preserved, followed by the corrected actual RED.
No setup failure is represented as the intended contract counterexample.

A native closure check also found one missing POST assertion for removal of
configured project scope during acknowledgement I/O. Root added that exact
404, zero-write and slot-release case and ran the final coherent selection
reported above; the earlier 23-method, 152-node run remains separate evidence.

Worker focused evidence (17 methods, 83 nodes) remains separate and does not attest
the later source. Root source bindings below attest the final focused selection:

```
c45e95e14af2612b83a341986c1cb331f83711a10ce0ade1beb2d2631a274897  internal/apiserver/inbox.go
ca657fd2007f66b395c86bca654605bcd4a46be888197bd6ea16ab4dee7e39fb  internal/apiserver/coordination_inbox_archive_ack_retry.go
26b23db6dae5e766c87fa6256cd81bfebf8e4f3b725f588f176c57fbdb632dd2  internal/apiserver/coordination_inbox_archive_ack_retry_test.go
128b80288eaac8df747764063daaee6049e1c5b08f8257c656cfd76b12c200f3  internal/apiserver/coordination_inbox_archive_get_test.go
b732b2e31c7fd8dfcc20f9c1f234d48e462aeadb22d4058f4717e4f424f3ca0e  internal/apiserver/coordination_inbox_archive_ack_get_test.go
```

Independent native closure, available advisory findings and exact-head required
CI must be inspected before guarded source merge. No installed, runtime,
provider, archive activation/deletion, deployment, v1 or cutover acceptance is
claimed. The #301 permission-denied rerun and dependent R3 hold remain unchanged.

The advisory on `4e8584c` identified a stale `authorizedInbox` comment that said
all writes stayed live-only. The comment now distinguishes normal live creation
from a read-only archived retry. Go behavior and tests are unchanged from the
source-bound final selection above; no additional local test run is claimed.

Required CI `38804891380` on `a1ab4fe` found a goimports formatting failure in
the new retry test imports. Root grouped standard, external and repository
imports to match the repository formatter configuration. Test bodies and
production behavior are unchanged; the failure is retained and ordinary new-head
CI is required. No manual rerun or new local test result is claimed.
