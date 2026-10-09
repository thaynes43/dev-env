# Bounded cross-pod Git status diagnosis, 2026-10-09

**Status: executed once; incomplete and cleaned up.** The
[cost result](2026-10-09-cephfs-cost-result.md) records the timeout, CPU/Trace2
evidence and monitoring limits. The protocol below is the predeclared record;
it does not authorize another run. This was the next
diagnostic step for [#130](https://github.com/thaynes43/dev-env/issues/130), within
Tom's accepted bounded CephFS trial and instruction to deliver an owner-testable
v2. It measures the cause of the previous slow status operation. A completed
diagnostic does not itself accept shared storage or a normal workspace rollout.

The [first two attempts](2026-10-09-cephfs-feasibility.md) remain incomplete.
Their synthetic bulk-write completion marker precedes the post-file peer status
operations; bulk file writes were therefore no longer running at those calls.
Concurrent peer status and shared metadata operations still need diagnosis.
The prior MDS latency excursions remain evidence even though they did not trip
the original one-minute consecutive-observation rule.

## Reviewed source and fixed bounds

Use merged [#132](https://github.com/thaynes43/dev-env/pull/132), source commit
`e1c5010a07da10824435017a476033da2298a4da`. The embedded helper SHA256 is
`939b589542d734d38683dbcc18e90bc7d33c9bb9f66016fd291e7fb32516545c`.
Decoded Job arguments must match that exact source. Use signed agent 2.9.1 at
digest `a975de7dbc40c6f33048a38a327a2db2b9698a7615f5b2b9897abbb1d04df0cf`.

Provision one fresh disposable 1Gi RWX `ceph-filesystem` claim through the
four-file GitOps inventory in
[#3671](https://github.com/thaynes43/haynes-ops/pull/3671). Two synthetic Jobs
run on distinct workers and mount only that claim below private temporary homes.
At most two fixture pods run. Main limits remain 250m CPU/256Mi; init limits
100m/128Mi; workload priority is reduced with `nice`. Job and Pod both disable
timezone injection. Require actual admission to preserve these bounds.

No service-account token, credential, provider login, model call, real remote,
push, package installation or other PVC is admitted. Total data stays within
2,048 entries and 16MiB. Ordinary commands retain their 15-second cap, the helper
145 seconds including its bounded startup barrier, and each Job 180 seconds.
Deliberate lock-hook and copy/archive caps are unchanged. `backoffLimit` is zero.
There is one execution, with no automatic retry, resource increase or test matrix.
No reconnect/remount Job belongs to this cost-only diagnostic.

## Measurements

Keep the exact `git status --porcelain=v1 --untracked-files=all` operation,
`GIT_OPTIONAL_LOCKS=0`, staged-WIP assertions and Git change detection unchanged.
Collect native Trace2 through a private bounded pipe/file: at most 64KiB and
48 sanitized phase/counter events per command. The command owner controls its
deadline and process termination; collector failure continues draining the pipe
so it cannot terminate Git early. No raw paths, arguments, environment, config,
host identifiers or Trace2 session identifiers enter public receipts.

Record child user/system CPU and the resolved fixture cgroup's CPU usage,
throttling duration/count and quota before/after the same command. Record Git
version/build metadata once per role. After successful status, sample index/stat
metadata for only two fixed synthetic files, with at most three task/phase
samples per role. Preserve input-write and copy/archive durations on failure.
Unavailable or truncated required evidence makes the diagnosis incomplete.
Do not infer a cause from elapsed time or throttle counts alone.

## Gates before provision and launch

The coordinator reviews the exact helper, rendered A/B Jobs and isolated claim
diff. Both source/provisioning PRs require current green checks and actual
advisory findings resolved. The keeper rollout must be complete and its activity
declaration ended before fixture activity starts. Record all seven protected
v1/controller/keeper/shelf pod identities, images and restart counts anew.

Freeze the latest 30-minute telemetry window immediately before execution.
Require at least 95% coverage for known active storage and six household HTTP
series, with all original safety/freshness gates satisfied. Record planned
keeper work if it falls inside that window. For every series use the **lesser**
of its original quiet-baseline limit and its newly computed formula limit;
recent activity cannot increase the allowed latency. Preserve the original
10ms MDS/50ms OSD mean limits and any stricter original HTTP limit. Standby MDS
`NaN` with no operations is recorded separately, never treated as zero latency.

Declare fixture activity before GitOps provisioning. Root approval of the exact
render and fresh gates is the launch checkpoint; an earlier preparation record
does not authorize an unchecked rerun. Verify the new claim UID/Bound state and
admitted placement, resources, mounts, source and image before accepting results.

## Observation and stop rules

Observe fresh Ceph health, active MDS/OSD means and slow-reply counters every
10 seconds through provisioning, execution and the five-minute post-window.
Observe household HTTP, critical pods, relevant alerts and bounded MQTT signals
every 60 seconds, and immediately after a storage warning. Stop on two
consecutive fresh ten-second storage-mean observations above the frozen limit.
Household HTTP retains its two-consecutive-fresh-observation rule.

Every original immediate stop rule remains: worsening Ceph health, an OSD down,
new MDS slow replies, a protected/critical pod restart or identity/readiness
change, a new relevant pending/firing alert, an observed MQTT disconnect, missing
required series, stale telemetry, or a fixture assertion/deadline/admission
failure. Ceph freshness is at most 30 seconds; household metrics at most
150 seconds. Stop on uncertain monitoring rather than extending the budget.

Storage means and HTTP checks still do not measure household device/MQTT tail
latency. This tighter sampling can find shorter storage excursions; it does not
close the full household-impact acceptance gate.

## Result and cleanup

Save bounded receipts, actual admitted specs, terminated container image IDs,
immutable source/image versions, frozen limits and telemetry privately. Publish
sanitized evidence showing which Git phases, CPU/stat observations and storage
signals occurred together; distinguish observation from causal inference.
Failure or unavailable evidence remains incomplete. Any remedy or subsequent
feasibility/remount run requires its own reviewed plan and fresh gates.

Remove fixture Jobs normally and verify their pod UIDs are absent. Then remove
only the disposable inventory/claim through reviewed GitOps cleanup; confirm
its PV is reclaimed, all protected identities/restarts are preserved and the
activity declaration ends. Do not force-delete uncertain writers. Neither
Accepted ADR changes nor v1 cutover occur in this diagnostic.
