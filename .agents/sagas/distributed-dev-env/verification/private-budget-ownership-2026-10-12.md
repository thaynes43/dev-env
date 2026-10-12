# Original private-worker ownership proof — 2026-10-12 UTC

## Source contract and validation

The bounded #154 source unit adds `internal/controller/private_taskbudget.go`
and its tests. The existing reservation interface needs no change. It proves
retained original identity through fresh Session, HOME, directory and Pod reads,
then checks those objects again for drift. The proof binds the logical-task
ledger, active worker, original Session UID, private HOME UID, executor intent,
directory UID/spec digest and original Pod UID. It reports stop as Unknown.

The separate `PrivateManagedAdmissionInspector` has no production wiring and
always refuses reservation, including after a successful ownership proof.
Ownership freshness supplies neither a private child-admission capability nor
a one-use launch fence. A missing executor after recorded intent is uncertain;
it cannot authorize a replacement, mark effort stopped or reset task history.
Existing canary binding metadata supplies original-identity evidence only.
It does not make a budget-bound canary admissible.

Fourteen existing guard paths remain byte-identical: API session/budget/canary
handlers; reconciler, shared budget inspector, private history, canary and Pod
builders; directory and common-rule binding; budget binding, ledger and store;
and AgentSession types. No policy, schema, flag, launcher, stop adapter, directory
enrollment or native recovery route changes. Source tests alone do not establish
private dispatch or #160 recovery.

Focused checks passed for both providers, changed or uncertain identities,
read drift, default-off refusal, lost acknowledgement and reservation contention.
The retained contention fixture proves ledger counters and active-worker history
survive without another launch. Existing shared-budget, canary, private-Pod
retention and API zero-dispatch regressions passed; affected lint found zero
issues. Tests ran sequentially under nice 19, GOMAXPROCS 2 and Go parallelism 2,
using cached offline tools. The corrected initial fixture compile and lint
failures remain recorded. Validation packet `a34684b5` binds the final two files
(`c815a7d6` and `9732ec57`), logs and all fourteen unchanged guards.

Native independent review and required `CI - Success` are source shipping gates.
Claude review is optional during the outage; read any available findings without
retrying the failed reviewer. This unwired seam requires no runtime pin change
or session restart. A future wired private-admission slice needs its own policy,
source review, signed operator delivery and runtime acceptance. Preserve the
#170 fixture/ledger and uncertain lifecycle receipts.

## Runtime acceptance checkpoint

This checkpoint supersedes the earlier Directory TLS-only state; it does not
rewrite historical receipts. The legitimate public operator CA was bound from
GitOps and passed offline certificate checks. Two later one-shot Directory reads
completed successfully with empty complete pages: attempt 2 at 04:08:28 UTC and
attempt 3 at 04:43:26 UTC. Their resourceVersions are `824622567` and `824675951`.
Both five-minute windows expired before Create. Attempt 1's certificate failure
and both successful reads are retained, for three logical Directory invocations.

Attempt 3's saved native review `69166763` and closed binding `504f47e6` passed.
Final Create gate `cb9a657e` arrived at 04:48:14.658784 UTC; the Root ruling expired
at 04:48:21.820470 and the page at 04:48:26.820470. The Root pre-spawn freshness
assertion refused execution before any authorization write, campaign, helper,
CLI or POST. Root refusal `45a9f745` records the exact execution timestamp as
Unknown and separately records the later local artifact-absence checks.
Create/helper invocations remain zero; no permanent key was consumed.

All static first-Claude Create, original named-Session discovery and fixed-result
reader methods passed independent preparation review. Their actual runtime calls
remain unexecuted. A reviewed sequencing successor must validate the real fresh
page and conditionally perform the fixed Create within the unchanged 300-second
window. It must not fabricate a native saved-response verdict, reuse an expired
page, replay a Create, rearm a key, replace an identity or extend the deadline.
There is no automatic fourth read under the expired authorization.

The signed v2.23 agent publication is verified separately: root acceptance
`c65af0ce`, native saved review `69d18f20` and precision supplement `b8cdb3f1` bind
`ghcr.io/thaynes43/dev-env:2.23.0@sha256:15e462bb224e479240a25f73ef89ff69db6de21d2d0430726c255c14d6ec1794`.
It is published, not deployed; the fixed task image remains v2.20. The latest
accepted operator delivery remains haynes-ops #3825 in its saved preservation
window. Documentation #212 and haynes-ops #3827 merged without runtime changes.

Global private-project admission remains disabled in the accepted configuration.
Actual both-provider rule loading, private GitHub access, authorized live/stopped
history, claims/messages, whole-task private dispatch/recovery and the concurrent
owner pilot remain unaccepted. Q-24 still gates full native Codex isolation and
fleet UX; a peer-relayed owner statement is not an authenticated ruling here.
Local-model contracts remain in scope; inference awaits GPU readiness and
household reservations. Parent issues remain open. Preserve v1, credentials,
enrollment, existing sessions and all failed fixtures; use no memory stress.
