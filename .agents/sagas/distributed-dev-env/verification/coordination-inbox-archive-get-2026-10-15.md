# Inbox message archive fallback: source verification, 2026-10-15

This record covers issue #315, the inbox message GET slice of coordination
retention R4 in DESIGN-024 section 6 and backlog 11. C7-R4 is its bounded
story prefix; Chapter 7 is the current Codex source coordinator. Source tests
and source delivery do not establish a released or installed operator, runtime
archive activation, full R4, or cutover acceptance.

## Behavior and boundaries

`GET /v1/inbox/{message}?project=P` checks the existing caller scope and live
store first. Only a genuine live-store `ErrNotFound`, with archive reads enabled,
can request the shelf. A valid live record wins, a foreign live record stays
hidden, and other live errors do not reach the shelf. Ordinary inbox lists,
publication, and acknowledgement GET/POST keep their existing behavior; the
acknowledgement calls explicitly disable this message fallback.

The new helper takes the existing shared exec slot and uses the accepted
`inboxmessages` default no-UID Get selector. The underlying accepted selector
supplies the UID's first archive time; this helper consumes that receipt without
changing explicit UID or pinned-revision recovery semantics. Full canonical
bytes, digest, size, resource/kind/API, namespace/name/UID/revision/project and
recipient UID are verified, along with deterministic message name, full inbox
spec, creation/deletion state and nonzero UTC receipt. The API checks full
original recipient identity or configured coordinator scope and repeats the
live caller fence after shelf I/O. It projects the immutable message fields and
exact text, adds archive metadata, and makes no sender Session join.

Only typed archive `not_found` maps to uniform 404. Missing wiring or slots,
slot exhaustion, cancellation, malformed data and other typed/transport errors
remain 503. Default-off behavior never requests the shelf. All separate
runtime/access/security/provider/archive/deployment/v1/cutover gates and the
#301 denied-rerun/R3 dependency hold remain unchanged.

## Historical worker checkpoint

One fresh GPT-5.6-Terra/high worker picked up the four-path claim at 19:08:51Z.
It produced candidate `8aac01e` and was stopped by the coordinator before
acceptance edits; supported TUI quiescence was observed at 19:16:48Z. Its result
receipt timestamp predates its final commit-hash correction, so it is not used
as the actual stop time. The worker's initial four-method/nine-node green run
was partial evidence. Its disabled-fallback mutation RED is retained as such;
it was not an exact accepted-base source overlay.

Native review of that candidate found no production defect, but required an
actual baseline counterexample, an independent attribution oracle, missing
security/failure/transport cases, and correction of an overstated mismatch
case. A self-consistent foreign-recipient object tests ACL refusal (404);
a manifest/object recipient mismatch separately tests verification refusal (503).

## Direct acceptance evidence

The coordinator retrieved exactly
`f362fad1af31b43e5182c1a3bf40917e83dae8f0:internal/apiserver/inbox.go`
into a temporary Go overlay while retaining the new test/helper. The original
live-read positive assertion passed; the missing-live/valid-archive request
then failed its expected 200 with **404 and zero shelf calls**. This is the
finite exact-base counterexample, separate from the worker mutation diagnostic.
The overlay handler SHA-256 is `531317219d4e71760e63b6b80ade4e11b26094e6e2707b41aa06006cdaaeae22`.

The corrected full focused selection passed **23 methods / 101 nodes**. It
covers current inbox behavior, real acknowledgement route non-fallback, default
off/live precedence, sender absence, deleting/replaced original recipient and
post-I/O Pod/Session fences, independent full attribution including reported
time, exact text, full-object semantic/manifest/digest/canonical mutations,
error mapping, pre/post cancellation, slot release, and synthetic typed shelf
Get through the API decoder. The final recipient namespace/name and invalid
name/project cases then passed a separate **2-method / 24-node** selection.
The full run predates those test-only additions; the selections are not summed
or relabelled as one final-head run. Exact-head ordinary CI remains required.

All full-object semantic fixtures recompute valid canonical digests before
exercising API checks. Digest corruption is tested separately. Expected
metadata is built field by field from the original spec and normalized through
the API wire type; it never calls the production projection to build its oracle.
The typed transport fixture is synthetic source evidence, not an actual complete
archive-to-HTTP or installed run.

Use cached Go 1.27.2, no downloads, and the shared constrained test lock:

```sh
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off TZ=UTC go test -p=1 -json ./internal/apiserver -run '^TestInbox' -count=1 -timeout=60s
```

The final targeted selector replaces `^TestInbox` with
`^(TestInboxArchiveGetFullObjectRefusals|TestInboxArchiveGetInvalidNameAndProjectNeverReachShelf)$`.
The exact-base diagnostic uses its recorded overlay and selects only
`TestInboxArchiveGetRedAcceptedBaseAndLiveWins`.

| Capture | SHA-256 |
| --- | --- |
| Exact-base RED | `71fa3b43ee4238a76bb2e6c52d160dd3af888247a317376f85bb91d10db17828` |
| Corrected full GREEN | `3894402137c4cde46a08f3d814b064a66bc1df0cbe0cf510f3a9b2f13ac2a9cc` |
| Final targeted GREEN | `48ea8ba4ada1c69004eb8170bfbef884a16895f3c22cbc71f20e65ca70544402` |

Raw captures, overlay, exact commands/environment and review/CI receipts are
supplemental PVC-local evidence under
`/home/dev/work/orders/dev-env-chapter7-inbox-get-1015/`; they are not publicly
fetchable repository artifacts. The committed tests, reproducible selections,
hashes and ordinary public PR checks/review are the portable source evidence.

Go hashes from the final targeted local run (before the import-group formatting correction described below):

| File | SHA-256 |
| --- | --- |
| `internal/apiserver/inbox.go` | `184c0e263bdd3c57a7b910734ef3b7d8dbd1bd6a2b7d875b2907e21fdeeaf2e5` |
| `internal/apiserver/coordination_inbox_archive_get.go` | `4e4a95c62a6e74d2a9ea5e427922efd8c4b00a919fdb819cade0f834e2d5852d` |
| `internal/apiserver/coordination_inbox_archive_get_test.go` | `22a0d405da3cfc33693892fe30d6a865cdc74c59b2a7a3ed8f1fdd8ee9ceb70b` |

## Counters and delivery

The worker's bare-formatter PATH error is retained; the cached toolchain was
used afterward, without downloads. The coordinator's expanded test run initially
used a two-slot assertion against a one-slot fixture. That failed capture is
retained; a dedicated assertion now verifies the configured single token is
returned without adding capacity. No response, identity or provenance assertion
was weakened. The corrected full run and final targeted run are distinct,
finite checks, with the targeted addition explicitly budgeted after native
review. Historical failures, deviations and deadline records remain unchanged.

The login-check notice generated during instruction loading was informational,
not a source dependency, quota-reset proof or permission for login/credentials,
GUI, unrelated Claude or runtime work. No such action was required or taken.
Source acceptance requires independent native closure, exact-head required CI,
available advisory finding disposition and guarded source merge within the
recorded coordinator budget. It does not complete the remaining R4 units.

The first hosted CI run at `9c76d81` failed goimports at test line 21: the
controller-runtime client import belonged with the other external Kubernetes
imports, rather than the local project group. The real correction only moves
that import and updates this record; no assertion or production logic changes.
One hosted lint failure remains recorded. Earlier local test/source hashes and
native closure stay attributed to their exact bytes; ordinary new-head CI and
scoped independent review cover the later formatting delta. No manual retry,
workflow change, empty commit or budget extension was used.
