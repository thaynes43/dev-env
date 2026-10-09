# CephFS workspace feasibility trial, 2026-10-09

**Status:** first attempt incomplete and cleaned up; a corrected bounded check
is prepared. Tom accepted ADR-002 and this trial through structured Q-21.
Normal workspace rollout remains gated on acceptance. The gates below were
committed before execution in `b18f0166f89bcc91c0eddadf7945746dfa12ddfb`.

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
seconds. The corrected helper has a 60-second peer-ready barrier before locks;
that consumes its original 145-second total budget. Combined startup/phases may
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
HTTP duration, below frozen tripwires. A later ten-second range query found an
11.35ms MDS mean at `17:12:27`, between those observations. It did not produce
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
Its finite fake-clock check includes final receipt handling with a 31-second
skew; ordinary command and startup timings have separate caps. One new bounded
run requires the reviewed fix, a fresh empty claim, fresh telemetry and explicit
driver launch authorization. The failed attempt remains part of this record.

Keeper CA projection and Q-22's delegated five-node trust are separately
delivered with minting disabled. Certificate/provider acceptance remains open.

- [Accepted ADR-002](../../.agents/sagas/distributed-dev-env/adrs/002-shared-project-workspaces.md)
- [Plan 11 acceptance](../../.agents/sagas/distributed-dev-env/backlog/11-project-workspaces.md#acceptance)
- [Workflow guide](../workflow-guide.md)
