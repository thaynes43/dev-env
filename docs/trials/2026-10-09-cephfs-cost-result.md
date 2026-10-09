# Cross-pod Git cost result, 2026-10-09

**Incomplete: the peer status timed out again. Shared-storage acceptance remains
open.** This was one reviewed diagnostic execution under
[#136](https://github.com/thaynes43/dev-env/pull/136), not a normal rollout or a
fresh-remount acceptance test. It preserves the two earlier
[incomplete attempts](2026-10-09-cephfs-feasibility.md).

## What ran

The immutable helper came from source `e1c5010a07da10824435017a476033da2298a4da`
with SHA256 `939b589542d734d38683dbcc18e90bc7d33c9bb9f66016fd291e7fb32516545c`.
Both synthetic Jobs used the signed agent 2.9.1 image, a new disposable 1Gi RWX
claim and distinct worker nodes. Command/helper/Job caps remained 15/145/180
seconds; main/init CPU limits remained 250m/100m. No credentials, provider calls,
real remote, retry, remount or other PVC were used.

The frozen 20:21:14–20:51:14Z telemetry window passed the original coverage,
freshness and quiet-limit gates. A proposed historical requirement for *every*
availability scrape to equal one was removed before launch because it was not
part of the reviewed 95% coverage contract. Current required jobs still had to
be up; missing/stale monitoring still aborted. Both the rejected check and its
three historical missed scrapes remain in private evidence.

GitOps provisioning was
[haynes-ops #3671](https://github.com/thaynes43/haynes-ops/pull/3671), merged
`dba39485359b25b06e9ba40f9128546046d049ee`. Monitoring began at 20:53:40Z;
the only A/B launch occurred at 20:57:47Z after the exact-source, fresh-metrics,
new-claim, worker and protected-pod checks passed.

## Findings

| Observation | Consequence |
|---|---|
| Role B's exact peer-A status took 15.074s and hit its original command cap, with 118.178s still in the helper budget. | This is a per-command timeout; the global deadline was not exhausted. |
| Native Git 2.39.5 tracing entered `index/refresh` and did not exit before termination. | The wait is localized to refresh, but the blocked operation is still unknown. |
| That child used 2.29ms CPU; its own cgroup used 8.071ms, with zero throttled periods or duration. | CPU quota throttling does not explain this observed timeout. |
| A's own post-write status took 1.569s, while its traced refresh took 0.856ms and scanned no file contents. | The same run also had waiting before the traced index read; one slow phase cannot explain every delay. |
| Build/copy completion preceded both peer statuses. | Bulk fixture writes did not overlap the timed-out peer status. Concurrent metadata/status work remains a hypothesis. |
| Required peer index/stat samples were absent after timeout; A was stopped during its peer operation. | Required diagnostics and full A/B acceptance remain incomplete. |

Fixed owner index/stat samples were stable. Cached inode values were the expected
low 32 bits of native inodes, not evidence of index corruption. Different mounts
reported different device numbers; whether this build checks those numbers and
whether the peer cache actually mismatched are unproved.

Pinned [Git index refresh](https://github.com/git/git/blob/v2.39.5/read-cache.c)
can fall through metadata checks into
[content reads](https://github.com/git/git/blob/v2.39.5/object-file.c). That is a
conditional explanation, not the demonstrated cause. The next source diagnostic
will distinguish a metadata wait from a content read without changing Git flags,
configuration, deadlines or fixture size. It authorizes no automatic rerun.

An interruption-recording defect also lost A's in-flight measurement. Source-only
[#139](https://github.com/thaynes43/dev-env/pull/139), merged
`a222a69311940bc7efd3b8b1f58352e7ddc40cdc`, fixes that bounded receipt handling.
Its helper SHA256 is
`7f2306904565b0a7fdf617402c09baf7d5e8d6733e32f80b64b6346b7aa7f888`;
it was not the runtime helper in this diagnostic.

## Cleanup and observation limits

Both captured original Job UIDs were deleted normally. Genuine termination of
all admitted containers, original pod absence and healthy workers with fresh
node leases were verified before claim cleanup. GitOps
[#3675](https://github.com/thaynes43/haynes-ops/pull/3675), merged
`7ee2cb33172acbf366f2c71183bed1a827e49f50`, removed only the disposable inventory.
The claim, PV, child Kustomization and fixture Jobs/pods are absent. Seven
protected pod identities/images/restart counts were preserved. Activity ended.

Last cleanup completed at 21:02:16Z. Monitoring continued through 21:07:20Z,
303.896 seconds later. Its final receipt is complete with `allGatesPass=false`.
Required Ceph health/OSD-up series were missing at 21:03:50, 21:04:00 and 21:05:00Z,
then recovered. These are monitoring-availability breaches, not proof that a
Ceph daemon or household service failed. Household observation intervals were
60–70 seconds, rather than exactly the planned 60 seconds. Actual timestamps and
that deviation are retained; full household acceptance remains open.

Private bounded receipts live under
`/home/dev/work/state-snapshots/workspace-cephfs-diagnostic-20261009T205114Z/run/`.
Public records contain sanitized findings only. Closing
[#130](https://github.com/thaynes43/dev-env/issues/130) still requires a verified
explanation or remedy and the passing bounded Git/storage, retained-state/remount
and household acceptance. Project/rule, provider, ownership and phone gates in
[plan 11](../../.agents/sagas/distributed-dev-env/backlog/11-project-workspaces.md)
remain independent.
