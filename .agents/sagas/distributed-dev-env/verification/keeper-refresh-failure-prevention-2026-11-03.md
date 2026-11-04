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
on a cancellation-detached context. The POST's deadline begins after the
dispatch commit and the Lease fence that follows it: `claude_transport.go:156`
starts a fresh 3-second timeout on a context detached from shutdown, and
`claude_transport.go:158` sends on it. The earlier deadlines
(`claude_worker.go:263`, `claude_transport.go:144`) bound only the checks
before the commit. That fence after the commit (`claude_worker.go:370`) needs
3 seconds request, 2 seconds save and 2 seconds margin, within the existing
10-second lease freshness limit. (Corrected 2026-11-04: this paragraph first
said the request deadline begins before the second fence, which was true
before the dispatch commit was added; see correction round 2 below.)

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

A keeper that did not itself see the not-sent outcome (a restart or another
leader) had no proof in this round, so it marked NeedsLogin for an intent
whose rollback never committed. Round 1 said that closing this would need new
RBAC or a second store; the 2026-11-04 independent review refuted that, and
round 2 below closes it with a marker in the existing journal Secret. No
provider, login, credential, cluster or deployment was exercised.

### 2026-11-04 correction round 2 (source only)

The cycle below, the marker format and the residuals are also the plan's text:
[plan 03, *Keeper refresh-failure prevention*](../backlog/03-remote-control.md#keeper-refresh-failure-prevention-2026-11-04-source-only-pr-885),
under [DESIGN-001 D-11](../designs/001-dev-env-v2.md#62-claude-max-login-and-its-monthly-renewal).

The independent review of round 1 passed the source and found four gaps
(G1 to G4). Its read-only check of haynes-ops `d481703e` found that the
keeper Role already has get and patch on the named journal Secret, that Flux
creates that Secret once and never applies it again, and that no admission
policy limits the keeper's writes to it.

- Durable proof that an attempt was never sent (G3). The journal Secret gets
  a second data key, `unsent-attempt`. `state.json` is unchanged, so older
  keepers still decode it. Refresh writes the intent and the marker (this
  Secret's UID and the attempt UID) in one compare-and-swap. The transport
  runs every check that can prove the request unsent, then calls the
  dispatch commit: one more fenced compare-and-swap that removes the marker,
  on the Secret as re-read after the intent. Only then is the POST sent, on
  a context that shutdown does not cancel, within its own 3-second bound.
  Any keeper that finds an intent whose marker names that Secret's UID and
  the intent's attempt returns it to Ready by compare-and-swap, with no
  provider call. The rollback re-reads the journal and requires the marker
  again. An intent with no marker, a marker for another attempt or another
  Secret UID, or a corrupt marker is tombstoned as before. Older keepers
  never write the marker, so their intents are always tombstoned; their
  merge patches leave the key alone, and a stale marker names an old attempt
  that never equals a fresh UUID. Every save other than the intent removes
  the marker.
- The commit and a successor's rollback are compare-and-swaps on the same
  resourceVersion, so only one wins: when the successor wins, the owner's
  commit fails and nothing is sent. A lost acknowledgement on the commit is
  read back: the commit holds only if the same Secret (same UID) still holds
  this intent and no marker, and the request is then sent and the replacement
  saved on the Secret as read.
- Lease budget. The fence before the commit needs `claudeRequestBudget`
  (7 s), which covers the commit's 2 s save and the 2 s margin. A third fence
  after the commit needs 7 s again: the 3 s request, the 2 s replacement save
  and the 2 s margin. Each is under the fence's 10-second freshness bound. A
  test pins the constants and the order: fence, intent, fence, commit, fence,
  request, replacement. If the fence after the commit refuses, nothing is
  sent and the proving worker rolls back from its own proof.
- The in-memory proof's 5-minute bound holds under repeated rollback
  failure (G2): at the first tick at or after the bound the intent is
  tombstoned, even with its marker still there, and the NeedsLogin page
  follows. A worker does not take that attempt's marker as a proof again.
- The rollback's 6-second deadline and its Lease budget are pinned (G1): with
  less than 4 s of Lease left the rollback is refused and the proof kept.
  The journal UID check in the rollback is defence in depth and is now
  pinned too (G4): a recreated journal with copied data is not rolled back.

Residuals, recorded (RES-7 to RES-11 and the RES-6 widening come from the
round-3 independent review):

- RES-1. A crash, or any stop that is not a not-sent path, after the
  dispatch commit is stored and before the POST: the journal then says the
  attempt may have been sent, and a successor tombstones a token that may
  be unspent. A test pins this behaviour.
- RES-2. A lost acknowledgement on the commit whose read back also fails:
  nothing is sent, and the proving worker rolls back from its own proof; a
  restart before that rollback finds no marker and tombstones.
- RES-3. The Lease fence after the commit refuses: nothing is sent, and, as
  in RES-2, only the proving process can roll back.
- RES-4. The provider has no idempotency for the refresh, so nothing on the
  keeper side can close RES-1.
- RES-5. The 5-minute proof bound is per process: a keeper restarted within
  it takes the marker as a fresh proof and retries for up to 5 more minutes
  before the tombstone and the page.
- RES-6. Restoring an old copy of the journal Secret in place, with the same
  UID, can bring back an old token. Before this round only an old Ready copy
  could do that; now an old marked RefreshIntent copy can too, because a
  keeper rolls it back to the record it holds. A recreated Secret, with a
  new UID, is refused.
- RES-7. A process stall (garbage collection, CPU throttling, a stopped
  process) longer than the remaining fence budget, between the fence after
  the commit and the end of the POST: the POST can complete after the Lease
  is lost, because the provider takes no fencing token. Not new here. A
  successor finds no marker and tombstones, never reusing the token, and the
  stale owner's replacement compare-and-swap fails.
- RES-8. Every failure after the commit, including dial, DNS and TLS
  failures before any byte is written and 429 or 5xx answers, tombstones and
  pages although the token may be unspent. Unchanged here, and still the
  main remaining source of a false login-required.
- RES-9. Any principal that can patch the journal Secret (the keeper's
  ServiceAccount, cluster administrators) can write or copy a marker that
  names the current intent; a keeper would then roll back to the record in
  `state.json`, which may hold a spent token. The journal is trusted state;
  this generalises RES-6.
- RES-10. Any other write to the journal Secret between the intent's re-read
  and the commit fails the commit's compare-and-swap, and nothing is sent. If
  that write left this attempt's intent in place, the read back still finds
  the marker and the intent rolls back with the 1-minute retry delay:
  availability only. If it replaced the intent, nothing is rolled back and
  the next tick handles what the journal then holds.
- RES-11. On a downgrade to a keeper without this change, the older keeper
  tombstones marked intents, as before, and leaves stale markers behind. They
  name old attempts and are harmless.
