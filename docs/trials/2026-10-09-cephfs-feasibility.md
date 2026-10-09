# CephFS workspace feasibility trial, 2026-10-09

**Status:** two incomplete attempts; cleanup complete. Shared Git/storage
feasibility and normal-rollout acceptance remain open. Tom accepted ADR-002
and this trial through structured Q-21.
Normal workspace rollout remains gated on acceptance. The
[predeclared gates](https://github.com/thaynes43/dev-env/blob/a83abbf3b4194b938ab9f78b57e6c2008b5ff218/docs/trials/2026-10-09-cephfs-feasibility.md)
were published before execution in PR #128's
[initial review run](https://github.com/thaynes43/dev-env/actions/runs/37962890145),
created at `16:58:03 UTC`. The linked copy is retained in this PR's history.

## Scope and fixture

Use a new disposable 1Gi RWX claim in `dev-agents`, named
`dev-env-workspace-cephfs-trial`. It contains synthetic Git data only. The
existing shelf, v1 home and real repositories are excluded. Provision and
remove the claim through a narrowly scoped GitOps app; no direct PVC writes.
The permanent workspace will separately need Retain/prune protection.

Two Jobs on distinct worker nodes mount the same real `/home/dev/repos`,
`/home/dev/codex` and `/home/dev/work` paths below otherwise private temporary
homes. A third Job mounts the data after the first two finish and their pods
are removed. At most two fixture pods run at once. Each main container is
limited to 250m CPU/256Mi; each explicit init to 100m/128Mi. Job and Pod
annotations opt out of timezone injection. Verify admitted resources before
accepting results; every container/init needs a CPU limit.

Use signed agent 2.9.1, no service-account token or credentials, no real remotes,
pushes, provider auth or model calls. Reviewed source makes no network calls;
namespace policy is not a separate no-network fence. No load loops, burners or
wide tests. Peer coordination sleeps between checks and has finite deadlines.

The fixture checks actual cross-node `flock`/`mkdir` exclusion and release, an
actual held Git ref-lock collision, serialized worktree creation, and separate
staged WIP/index/HEAD preservation. A capped copy/fsync/rename/archive workload
uses 256 files of at most 8KiB. Total fixture caps are 2,048 entries and 16MiB.
Ordinary subprocess calls have a 15-second cap; the deliberate Git transaction
hook has a 30-second deadline and its measured process a 35-second cap.
Initial Jobs have a 180-second Kubernetes deadline; the remount Job has 60
seconds. The corrected helper has a separate 60-second peer-ready wait cap before
locks. Time spent waiting counts against the original 145-second total helper
deadline; the clock is never reset. Combined startup/phases may
exhaust the global budget even within individual phase caps. There are no
automatic retries or enlarged resource budgets.

This is not a package installation benchmark or a real build. Graceful removal
of completed pods proves a fresh mount, not recovery from node/storage failure.

## Predeclared gates

Freeze a 30-minute telemetry baseline immediately before launch. Existing
unrelated alerts are recorded, not attributed to the fixture. Require at least
95% baseline coverage for the selected active storage/service series. Ceph
counters scrape about every 10 seconds; Gatus checks about every 60 seconds.
Standby MDS `NaN` means no operations, not zero latency.

Sample fresh telemetry every 60 seconds during the trial and for five minutes
after it. Stop fixture work promptly on a tripwire. These are conservative
trial tripwires, not household SLOs:

| Signal | Abort condition |
|---|---|
| Ceph health, OSD availability, MDS slow replies | Health worsens, an OSD goes down, or slow-reply counters increase |
| Critical household pods and related alerts | New restart/not-ready state or a new related pending/firing alert |
| MQTT observation | New error signal in the available bounded observation; absence does not establish device latency |
| Telemetry freshness | Ceph data older than 30s, Gatus older than 150s, or required coverage missing |
| Active MDS reply mean | Two consecutive fresh observations above `max(2 × frozen baseline maximum, 10ms)` |
| OSD latency mean | Two consecutive fresh observations above `max(2 × frozen baseline maximum, 50ms)` |
| Selected household HTTP checks | Two consecutive fresh observations above `max(2 × frozen baseline maximum, baseline maximum + 500ms)` |

For the corrected attempt, the fresh baseline includes the preceding failed
fixture and CA delivery. Preserve the stricter per-series limit: use the lesser
of the original quiet-baseline limit and the new formula's limit. Recent trial
activity must not raise its own stop threshold. This retains the original 10ms
MDS/50ms OSD limits and any stricter original HTTP limits. Record both baselines
and effective limits before launch.

Available probes cover storage metrics and six HTTP checks: Home Assistant,
Zigbee, Z-Wave, authentication and the internal/external Traefik dashboards.
SSO responses may satisfy an HTTP check without testing the underlying app.
They do not cover MQTT/device response
latency end to end, and means/last-check durations do not prove tail latency.
Sixty-second sampling can miss shorter disturbances. A pass must retain these
limitations and leave full household-impact acceptance open.

Before declaring fixture mechanics successful, verify the two peers actually
ran on distinct workers, mounted only the new claim, used the exact reviewed
image/source, exited successfully, and retained the expected worktree state.
Then prove the remount only after old pods terminate; compare WIP, index trees,
HEADs, common Git paths and archive hashes. Lock tests are filesystem evidence,
not a platform ownership fence or permission for lease-expiry takeover.

## Cleanup and evidence

Save the immutable source/image revisions, admitted pod specs and result
receipts before deleting Jobs. Remove all trial Jobs/pods first, then remove
the dedicated GitOps app/PVC through a reviewed cleanup PR and reconcile it.
Confirm the disposable PV is reclaimed, all activity declarations ended, and
protected v1/controller/shelf identities and restart counts are preserved.
The trial never edits Accepted ADR-002 or advances a composite parity row.

Keep raw telemetry and cluster snapshots private. Publish a concise result with
the actual limits, observations, tripwire outcome and remaining gates. A failed
fixture or missing observability is recorded as such; neither is a rollout pass.

## First attempt: incomplete, cleanup complete

Reviewed helper [#127](https://github.com/thaynes43/dev-env/pull/127), `853dae77`,
ran after disposable claim [#3646](https://github.com/thaynes43/haynes-ops/pull/3646),
`47729a52`, became Bound. Admission verified the exact signed image/source,
distinct worker placement, stated CPU limits, and absence of credentials/tokens
or other PVCs. The synthetic seed became visible; later checks did not complete.

A ran `17:07:08–17:07:39 UTC`; B started at `17:07:39` and failed at `17:07:40`.
The 31-second startup skew exceeded A's 30-second peer wait during the first
held-lock phase. A timed out and B reported an unexpected command exit. Its
generic failure receipt omitted the exact command/status, so that detail cannot
be reconstructed as proof of a particular lock behavior. No remount was run.
Storage feasibility was unproved; this is not a backend rejection.

The frozen baseline covered `16:28:21–16:58:21 UTC`: two MDS and ten OSD series
each had 180 raw samples; six HTTP probes each had 30 samples/actual checks.
All 39 monitored critical pods were Ready with stable UIDs/restart totals.
Fourteen one-minute observations stayed clear through the five-minute post-window
and cleanup; their maxima were 2.60ms MDS/7.09ms OSD five-minute means and 543ms
HTTP duration, below frozen tripwires. A later ten-second range query found
three MDS means above 10ms at `17:12:07`, `17:12:17` and `17:12:27` (10.82,
10.71 and 11.35ms). The adjacent grid points were below the limit; exact crossing
duration was not measured. These fell between minute observations and did not produce
two consecutive above-limit one-minute samples; this concretely demonstrates
the sampling limitation and leaves household-impact acceptance open.
Existing unrelated alerts were recorded.
The separate public-CA delivery overlapped recovery, not the failed A/B workload;
this limits attribution. No device latency or tail-SLO acceptance is claimed.

Jobs/pods were removed gracefully. Cleanup
[#3650](https://github.com/thaynes43/haynes-ops/pull/3650), `64de075b`, pruned only
the disposable claim/PV/app; all were absent at `17:14:31 UTC`. Protected
v1/controller/shelf identities, images, restarts and grant/session counts matched
baseline. Monitoring ended clear at `17:14:39`; the activity declaration ended.

The follow-up fixes only the demonstrated harness startup coordination and
missing diagnostics: [#129](https://github.com/thaynes43/dev-env/pull/129).
It merged as `747c1800`; its finite fake-clock check includes final receipt
handling with a 31-second skew. Ordinary command and startup timings have
separate caps. The failed attempt remains part of this record.

## Corrected attempt: incomplete, cleanup complete

The driver authorized one new bounded check after review of the corrected
source, fresh baseline and stricter limits. Provisioning
[#3652](https://github.com/thaynes43/haynes-ops/pull/3652), `b3996aed`, created a
fresh empty claim; no old markers were reused. The reviewed image, mounts,
worker placement, no-credential/token rule and CPU/deadline/data caps stayed
unchanged. Startup synchronization passed: A waited 0.007s and B 4.621s.

Partial traces reached real flock/mkdir exclusion/release, an actual Git
ref-lock collision/release, serialized worktree adds, staged WIP and the bounded
copy/fsync/archive. B then exceeded the original 15s cap on a real read-only
`git status --porcelain=v1 --untracked-files=all` against A's task worktree.
B failed at `17:28:21 UTC` after 29.15s; A's peer-completion wait failed at
`17:28:53` after 57.07s. Both had ample global budget left. Full final receipts
and remount acceptance did not complete; no reconnect or third run occurred.

A's peer Git status took 11.740s. Its own status also slowed from 0.024s to
5.463s; B's local status took 0.204s. The cause is unproved. Sparse CPU samples
(two for B, four for A) show some throttled periods but lack throttled-duration
data; they cannot attribute the timeout to the 250m limit. Git/index-cache and
filesystem behavior remain hypotheses, not a demonstrated backend defect.

The second baseline covered `16:49:57–17:19:57 UTC`, including preceding trial
and CA work; it had complete required coverage. The effective limits used the
lesser of each original quiet-baseline limit and new formula limit, retaining
10ms MDS/50ms OSD caps. A real MDS five-minute mean warning at `17:33:08` reached
32.453ms, then fell to 0.655ms at the next minute. Only one ten-second grid point
was above the cap; exact crossing duration was not measured. The two-consecutive
stop rule did not fire. All other hard health, Ready/UID/restart, MQTT and related
alert gates stayed clear. This is observed recovery, not proof of no latency
impact or full household/device acceptance.

Both Jobs terminated before graceful removal. Cleanup
[#3655](https://github.com/thaynes43/haynes-ops/pull/3655), `595cf392`, pruned only
the corrected trial inventory. Jobs/pods, claim/PV and Flux app were absent at
`17:35:16 UTC`. The five-minute recovery observation completed clear at
`17:36:08`, with the earlier warning retained. Protected identities, images,
readiness, restarts and session/grant counts matched baseline. The second
activity declaration ended at `17:39:30`.

[Issue #130](https://github.com/thaynes43/dev-env/issues/130) preserves the
diagnosis and a finite next proposal before any further cluster run. Do not
relax budgets, change Git detection semantics or switch storage to obtain a
pass. Provider/rules, two-host auth, task fencing, real workloads, household
tails and node/storage-failure recovery still need their own acceptance.

Source-only [#131](https://github.com/thaynes43/dev-env/pull/131), merged
`191b8c9e`, fixes the missing timed-out-command measurement in failure receipts.
Finite simulated checks cover the original exception, elapsed/effective cap and
bounded partial output. No third cluster run or deployment occurred; the slow
Git operation and workspace acceptance remain unresolved.

Keeper CA projection and Q-22's delegated five-node trust are separately
delivered with minting disabled. Certificate/provider acceptance remains open.

- [Accepted ADR-002](../../.agents/sagas/distributed-dev-env/adrs/002-shared-project-workspaces.md)
- [Plan 11 acceptance](../../.agents/sagas/distributed-dev-env/backlog/11-project-workspaces.md#acceptance)
- [Workflow guide](../workflow-guide.md)
