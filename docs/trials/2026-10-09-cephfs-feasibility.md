# CephFS workspace feasibility trial, 2026-10-09

**Status:** prepared; not launched. Tom accepted ADR-002 and this bounded trial
through structured Q-21. Normal workspace rollout remains gated on acceptance.
This record defines the tripwires before execution and will hold the outcome.

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
Initial Jobs have a 180-second Kubernetes
deadline; the remount Job has 60 seconds. There are no automatic retries.

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

The current result is pending execution. The keeper CA projection is separately
delivered with minting disabled; Q-22 separately delegates node trust to Codex.

- [Accepted ADR-002](../../.agents/sagas/distributed-dev-env/adrs/002-shared-project-workspaces.md)
- [Plan 11 acceptance](../../.agents/sagas/distributed-dev-env/backlog/11-project-workspaces.md#acceptance)
- [Workflow guide](../workflow-guide.md)
