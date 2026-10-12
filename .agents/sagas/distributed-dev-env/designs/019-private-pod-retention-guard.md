# DESIGN-019: retain the original private executor Pod

Delivery update, 2026-10-11: source and signed v2.20.0 prerequisites are deployed
through haynes-ops #3823. Actual provider/pilot acceptance remains separate. The
[installation note](../verification/installation-2026-10-11.md) preserves the
observer refusal, Directory Unknown state and remaining activation/read gates.

The complete source audit found PRIVATE-POD-R1: an unbound private Session with
retained history and suspended/reaped intent can lose its original terminal or
never-started Pod. The existing private check protects volume removal. Unknown
private HOME/causal observations become conditions after resource operations and
do not veto this Pod removal path. Preserve original audit `5aff018d` and the
corrected pilot audit `92a3997e5c198038d1f8de3fadc0028e08fd156a39763bb4db26ed1b84797bb9`.
Complete exact-source trace audit is
`05765bfe3f5ce589d8f02f9de2b044361b9b6ee1e2a73cff29cdacfb9899a5a8`;
no runtime deletion, provider or credential probe was attempted.

## Concrete source contract

Protect an original Pod whenever the existing protected-private-history
predicate applies, including retained records after the feature flag is disabled.
Use the existing retention refusal in `podRemovalAllowed`. Add the same refusal
at the start of `removePod`, before `needsRescue`, rescue/exec or deletion can run.
Preserve the exact original Pod UID/spec/status, HOME, private history latches,
worktrees and uncertain lifecycle receipts for suspended/reaped intent, across
terminal, never-started, running and ambiguous/partial private authority.

This change does not delete, shorten a deadline, kill a provider, execute rescue,
create a replacement, rearm a receipt or authorize lifecycle takeover. It does
not infer StopConfirmed from termination, intent or absence. Native history and
recovery remain Unknown. Existing shared Workspace proof and unprotected legacy
paths retain their current behavior. No new API/schema, feature flag, access
grant, provider record, image build in this pod or cleanup policy is included.

The reachable audited path is reconcile suspended/reaped with Workspace=nil,
then `removePod`; terminal/never-started bypasses rescue and passes the old
`podRemovalAllowed`, then `deletePod` issues the UID/RV-conditioned deletion.
A prior retained condition does not prevent it. Protecting only the final delete
would still admit legacy rescue/exec for a running private Pod, hence both guards.

Scope note (2026-10-12, #91 history-07): this contract binds the operator. A
human's foreground cascade (`kubectl delete --cascade=foreground`) lets the
garbage collector delete the original Pod, which carries no finalizer; DESIGN-001
D-45 records that human-only exception, and the API's reap uses background
propagation. HOME keeps its finalizer either way.
`TestPrivateHistoryForegroundAndBackgroundDeleteKeepProtectiveFinalizers` pins
both policies with a real running executor Pod: background keeps the same Pod
UID with no rescue, and after a foreground cascade the operator creates no
replacement and keeps the executor record with StopState Unknown.

## Affected paths and acceptance

Affect `internal/controller/guard.go`, `internal/controller/reconciler.go` and at
most two existing focused controller test paths. This design, saga index and
HANDOFF are the only additional documentation paths. Reuse the existing private
predicate/error; do not change private admission/preparation or storage topology.

Preserve one failing pre-fix regression showing the exact original owned Pod is
removed through the suspended/reaped terminal or never-started path. After the
fix, prove the same UID remains with HOME/latches intact and no rescue/exec/delete.
Cover running and uncertain private ownership before rescue, rollback-aware
retention and shared/unprotected positive controls. Source refusal remains
separate from an independently accepted original-executor stop proof.

Use one bounded pre-fix case and one focused corrected set at nice19,
GOMAXPROCS2/go-p2 with cached tools and finite timeouts. Controller TestMain
requires its cached envtest startup for both runs; these are not a new schema
campaign or a retry of stopped Q-25. No wide/looped tests, stress, CPU burners,
local image builds, runtime writes, provider turns or credential/HOME reads.
Preserve all original fixtures/ledgers/reviews and logical counters. Stop at
three same-blocker failures or sixty minutes without evidenced progress.

Exact independent native review and required CI gate squash merge. Claude is
optional/down: read its failed/skipped receipt and available findings without
retry/auth repair. Fold the source fix into the pending directory/usage release,
then signed GitOps installation with a fresh finite preservation observer. Ship
this protection before fixed provider activation. Existing sessions never roll
for an operator upgrade. Actual retention/stop/concurrent owner pilot remains
separate; keep #174/#91 and all other partially delivered parents open.

The next stop-and-preserve source contract must establish an actual finite stop
request and exact original all-container/Node/Lease proof without losing causal
Pod/HOME evidence. Private task budget integration and exceptional native
recovery do not acquire authority from this guard-only change.

Source `cf0a708c` passed the bounded corrected set after preserving actual pre-fix
original UID deletion through full Reconcile. Exactly two required cached
TestMain startups; sixteen state cases plus partial/unknown/canary and shared/
legacy controls passed, as did affected lint after a PATH preparation correction.
Validation packet SHA256
`189bf28fa7733cb85e2aa71d5b38f2011e865252b24605545d1206d82e7d84e7`
retains the failed fixture and all earlier evidence. Final Cost200 integration,
exact-head native review/CI and signed deployment are still gates.

Integration source `3cd61b8d` on merged Cost200 `a0ba2ee4` retains the four source/
test hashes and exact combined Directory/Usage schemas. No third local TestMain
startup, lint or generation repeat. Packet
`92cb9dcf525d4aeeb39ee2a61ec48f8f3c1493ebabcfc557755b7218c860ee12`
preserves 565 main incoming files and 102 older artifacts. Final native review
and required CI remain gates; installed operator source predates this correction.

## A known non-attempt after the executor latch — 2026-10-12 (history-01, #174)

Review finding history-01: `ensure()` latches `executorAttempted` and its intent,
then re-validates the canary authority, then creates the Pod. A failed read in
that re-validation (a timeout or 429 on the catalog, templates or common-rule
read) returned before Create with the latch set, and admission then refused
forever. One transient read consumed the one canary attempt with no provider
execution. A lost confirmation of the latch write itself did the same.

Rule: the operator records a known non-attempt and may resume the same intent.
A new optional `status.privateHistory.executorNotCreated` is set, by a confirmed
status write, only when this reconcile returned before calling Create for the
recorded intent because an observation failed: the latch write's own
confirmation, or a read (other than NotFound) in the post-latch canary
re-validation. Admission accepts an attempted intent marked not created. It
re-arms the same intent (clears the mark by a confirmed write) and runs the same
final validation before its one Create. The schema refuses the mark once an
executor was observed or without an attempt.

Unchanged, deliberately:

- An authority change after the intent, a refusal on observed facts, stays
  latched (`TestProjectCanaryAuthorityLossAfterIntentNeverRearms`).
- Any Create error stays latched. A POST that failed may still have been
  stored: client-go repeats a POST after a 5xx with Retry-After, so even a
  final 403 does not prove that no Pod was created.
- Absence still never authorizes a replacement, and an uncertain attempt is
  still PreservationUnknown.
- Post-latch task-budget steps keep today's behavior. Private budget dispatch is
  #154's separate acceptance; canaries are never budget-bound.
- The API wire view is unchanged; the strict once clients refuse unknown fields.

The CRD change reaches haynes-ops by the usual copy before the operator pin. A
stale schema prunes the mark, its confirmation fails, and the attempt stays
latched, which is the previous behavior.
