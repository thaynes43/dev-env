# DESIGN-019: retain the original private executor Pod

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
