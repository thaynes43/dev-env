# Claude keeper refresh failure prevention (source only)

Scope: prevent stale refresh callbacks and recovered intents from reporting or
changing another attempt, and distinguish only locally proven unsent requests
from possibly spent refresh tokens. This is synthetic source verification; it
is not provider, credential, installation, deployment, or runtime acceptance.

A matching intent must be fenced and changed by a successful journal CAS before
NeedsLogin is reported. An ambiguous write is not evidence for an observation.
Only failure before calling the HTTP client can prove no request was sent; all
failures after that boundary are treated as possibly spent. Successful responses
continue to be saved despite cancellation. The lease budget must fit the request,
save, and safety margin within the existing ten-second freshness bound.

## Source and synthetic checks

Base `1e2c814cd634c43f9bb387b02cedc50f88b8775c`. The stale-callback
regression compiled against that base and failed because the newer intent was
changed and NeedsLogin was reported. It passed after the fix. Tests cover a
CAS conflict, failed fence/load/write/readback, ambiguous committed writes,
restart and generation floor, two workers, cancellation on each side of the
HTTP dispatch boundary, and negative redaction checks. A safe unsent rollback
uses a one-minute in-process backoff; a restart does not retain that delay.
All post-dispatch errors remain possibly spent. A successful response is saved
on a cancellation-detached context. The request deadline begins before the
second fence; 3 seconds request, 2 seconds save and 2 seconds margin remain
within the existing 10-second lease freshness limit.

Go 1.27.3 was downloaded from go.dev and checked against its published SHA-256
`65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40`.
Tests ran serially with `GOTOOLCHAIN=local GOENV=off GOMAXPROCS=2`, private
HOME caches and temporary directory, and `nice -n 19 go test -p 1`.
The Claude tests pass with `-race`; the broader keeper tests pass both normally
and with `-race` when five fixture-dependent tests are excluded. Full keeper
runs, with and without `-race`, fail: three require unavailable envtest
binaries, and two Codex test functions cannot open their Unix control socket
in this temporary-directory setup. These are not a full-suite pass. No live
provider, login, credential, cluster, or deployment was exercised.

Private handoff `keeper-refresh-prevention-source-20261103-1545` contains the
exact source files, patch, Git bundle, command record, and logs. Selected log
SHA-256 values: red `d6beb9f3d4687cc627e0b1d2e9fe6dca338ea210bfcfbebca91ba6575fb66ef9`,
full `f762b66d279c0d786ae652c0a72546da5d4e6b4c9b91cde651b7ba3354809398`,
full race `c2a0d884572b105185cef20b884531bc7dd0b296ac910d12bcfb2c35c221bca0`,
keeper with exclusions `ccd7bccfd201a7c6b80a953500b1672b85328a9e38a56048ecd94aec28cabcaf`,
keeper race with exclusions `a48f6523d5fdff9ace71103aa8f67598d8a75fd528b30880fa2fec4f07128d52`,
and Claude race `91edcd4b3ef9aad799a4ffb8de646f8a923639290852af12931475324f378d47`.
Independent review and CI are still required; this source change is not merged,
installed, deployed, or accepted.

### Observation ordering: source repair in progress

The Claude hub uses the record's access generation and terminal stage as a
logical observation version. Under the current writers, Ready can become a
NeedsLogin tombstone in the same generation; returning to Ready after a
committed tombstone requires a new generation. Intent is not reported. The
hub rejects an older logical version under its mutex. Journal UID distinguishes
recreated records: an incarnation change requires an uncached UID witness while
the hub mutex is held, and previously observed UIDs are retired. If the witness
fails, the old observation remains until a later tick can retry. The mutex is
not held across adoption saves or notification delivery. Kubernetes resource
versions remain opaque and are not ordered numerically. This orders reports
only within one hub, after that hub has received the newer report.

The synthetic same-hub readback and tick interleavings pass with this source
change, including a delayed Ready after a same-generation tombstone and the
actual staged-adoption method with synthetic material. A notifier that sees
NeedsLogin now takes an uncached journal snapshot before deciding: a separate
synthetic two-hub test passes when the new Ready committed before that read.
Unavailable and in-flight records become Unknown for that decision; a current
committed tombstone still produces a synthetic notice.

The notifier checks the usual leader budget, reads the authoritative journal,
and uses that read as the decision's linearization point. It retains the opaque
resourceVersion and local read-start/read-finish bounds in the decision, without
logging them or using them as metric labels. It treats a failed fence, failed
read, or in-flight record as Unknown, and checks cancellation before acting.
The notifier passes the context through delivery; cancellation is best effort.

A synthetic regression pauses after the authoritative read captured a valid
tombstone, commits a new Ready, and resumes delivery. With a live context the
earlier decision may still send; with a canceled context it does not start the
send. Root's 2026-11-03 read-linearized decision accepts that overlap: the
external sender does not atomically order delivery with journal adoption. No
universal cross-process delivery ordering, complete false-notification
prevention, deployment, or journal overwrite is established. Current-head
independent review remains open.

### 2026-11-04 correction round 1 (source only)

An independent QA review of 5d6640c passed the source and found test gaps
by mutation. This round adds tests for them and fixes one false-login path.

- Shutdown before dispatch (fix). Three exits that prove nothing was sent
  (intent save with a lost acknowledgement, a failed intent re-read, and a
  failed pre-dispatch fence) returned the intent to Ready on the worker's
  context. During shutdown that context is cancelled, the Lease fence and
  the journal save refuse it, and the intent stayed; the successor then
  marked NeedsLogin although the refresh token was unspent. These rollbacks
  now run on a context detached from cancellation, like the existing
  not-sent rollback, and the fence still refuses a writer that lost
  leadership. A synthetic test
  cancels at each exit with the real Lease fence: before the fix the journal
  held RefreshIntent; after it, Ready, and the successor does not mark
  NeedsLogin.
- After dispatch nothing changes. A worker table test drives every
  post-dispatch class (dispatch, status, read, parse, a wrapped status and an
  unclassified error): each tombstones, and neither the worker nor a
  restarted one sends the token again past the unsent-retry backoff and the
  due time. A second table cancels the context inside the transport before
  such a failure: the tombstone is written at once.
- The notifier construction moved unchanged from Run into `loginNotifier`.
  A test builds it there and checks that a stale hub NeedsLogin with a Ready
  journal sends nothing, a tombstone sends one notice, a named journal is
  the one read, and another Lease holder sends nothing.
- The fixture journal now has a synthetic UID, so ordinary observation tests
  reach the hub's incarnation checks, and a test pins that an adoption
  reports under the journal's UID.

- A failed rollback write after a proven-unsent outcome (fix). The worker
  now keeps, in memory under its mutex, the journal UID and attempt UID it
  proved unsent, with a 5-minute bound. While the journal still holds that
  exact intent, each tick retries the fenced, CAS-guarded rollback and sends
  nothing; the 1-minute unsent backoff then applies. A lost acknowledgement
  is read back as Ready on the next tick. Any other journal state, another
  attempt or journal UID, a restart, a new leader, or the bound expiring
  drops the proof, and the intent is tombstoned as before. Every rollback
  (shutdown exits, not-sent and retry) runs detached from cancellation
  within a 6-second deadline, and the Lease fence refuses it past the lease
  budget. Tests cover the retry, the lost acknowledgement and each bound.

One case stays conservative by design: a keeper that did not itself see the
not-sent outcome (a restart or another leader) has no proof, so it marks
NeedsLogin for an intent whose rollback never committed. Rolling that intent
back would need durable proof written after the not-sent outcome, which is
the write that failed. No provider, login, credential, cluster or deployment
was exercised.
