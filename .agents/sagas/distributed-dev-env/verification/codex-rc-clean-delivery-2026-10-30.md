# OP272 clean conversation delivery — 2026-10-30

Work order: `OP272-CLEAN-DELIVERY-20261030-1334`; canonical work queue OP272,
context OP267/3386 and predecessor findings OP272/3384. Source base
`df7c27c6832c9aa2078f2780612f36c1e87f4515`. Implementation session
`dev-env-1030-133529`; predecessor sessions are terminal. The tracker claim
was refused because OP272 is Needs approval; the hold was not overridden.
Routine source findings/plan comment 3392 was recorded successfully. The current
work order explicitly authorizes this bounded implementation independent of
runtime holds. No claim of installed or accepted behavior follows from source.

## Implemented delivery path

1. Operator flags `--codex-rc-agent-image` and `--codex-rc-agent-session` must
   be supplied together. The image is a released
   `ghcr.io/thaynes43/dev-env:2.X.Y@sha256:<digest>` reference; the session tuple
   is `<name>/<SessionUID UUID>`. Nil/default changes no Pod bytes. Only a newly
   constructed, Session-owned ordinary Codex remote Pod for that exact immutable
   identity receives the image. Tasks, local sessions, holds, budget workers,
   workspaces, canaries and private pilots are excluded. Legacy image mappings
   retain precedence on overlap, including the separately reviewed recovery
   mapping. The HOME init verifier continues to use the selected main image
   through its existing construction path. Neither templates nor existing Pods
   are patched; no restart is initiated or authorized by these flags.
2. The fixed host configuration key remains
   `/opt/dev-env/config/codex/rc-target.json`. A regular file is accepted, as
   before. A Kubernetes AtomicWriter projection is now also accepted, only as
   `rc-target.json -> ..data/rc-target.json` and
   `..data -> ..YYYY_MM_DD_HH_MM_SS.<digits>`. Each ancestor/generation directory
   and final leaf is opened relative to pinned descriptors with no-follow.
   Absolute links, traversal, arbitrary key links, linked generation directories,
   linked leaves and nonregular/oversized files refuse. A changed projection
   pointer during the read refuses; no packet is cached. Packet content still
   must equal the independently configured dispatch target. This exception
   applies only to the deployment-owned configuration key; HOME evidence and
   every existing process/root/pairing/replay predicate retain their guards.
3. `agent-run rc fresh --session NAME --request FILE -o json` submits the existing
   endpoint once, without redirects, retries or fallback. FILE is the bounded,
   strict `CodexRCRequest` document described in DESIGN-038: request ID, exact
   Session UID/Pod UID, project/repository, model/effort, task-only handoff and an
   absolute deadline at most 30 seconds ahead. No old thread, fork, caller path
   or generic RPC can be selected. The response must bind that request and
   target, both digests and both native ACKs. Exit 0 reports those ACKs with
   `phoneRegistration: unobserved`. It never prints the handoff. Failure after
   submission is not permission to resend. Existing `rc start` behavior and
   all existing caller guards remain unchanged.

No new credentials, RBAC, ports, ingress, actor classes, pairing, enrollment,
public exposure or lifecycle framework are part of this change. An ordinary
worker cannot call the fixed API; it still requires the configured human,
client or recognized live workbench.

## Exact live installation and qualification packet

The current order permits normal scoped software adoption/restart only after
all native work on the target is proven idle. It does not permit unrelated
holds or access changes. Activation is an existing authorized deployment-owner
step, never a reason to grant this implementation worker new access.

- Intended target: `haynes-ops-1023-041051`, Session UID
  `242846d8-feb0-4f52-8164-01622206b88a`. At the baseline read, its Pod UID was
  `4f350ee1-3ddf-45f0-bee0-6b1b3e11461c`; agentd 2.45 lacked `codex-thread`.
  A restart invalidates that Pod/process qualification: collect a new tuple
  afterward. Never reuse the old Pod UID/PID/start ticks in the launch packet.
- Before any restart, obtain a supported native read of every loaded thread,
  its idle state and stable loaded-set recheck. Also establish no running
  commands/other provider work; daemon connectivity, Pod Ready and the limited
  phone inventory are insufficient. The target's heartbeat reports busy for a
  live standing daemon, so it is neither a native idle proof nor a restart gate
  that may simply be changed to idle. Active Opus work is in a different Pod;
  do not contact or restart it. Do not message either old Codex thread.
- Review the exact signed agent image and GitOps change adding only the target
  selector above; keep the global templates, Asks, held705 and existing205125
  recovery mapping unchanged. Preserve HOME and provider history. Use the
  existing authorized scoped adoption/restart path only after idle proof;
  do not delete the Session, recreate enrollment or reset replay fences.
- The observed bootstrap is Partial; haynesnetwork operation is
  Failed/TimedOut(Uncertain). Inspect its retained operation/pending/receipt
  and repository state with the existing deployment-owner path. Use the
  supported `agentd project-refresh` preservation flow when its prerequisites
  hold. If it refuses, retain the uncertain evidence and stop that dependent
  operation. No forced Ready status, blanket cleanup, receipt deletion, anchor
  rebuild or root movement is authorized by this implementation. Such recovery
  needs its own exact qualified preservation packet if refresh cannot resume.
- After adoption and real Ready preparation, independently bind the accepted
  catalog snapshot, preparation receipt hash, root ownership/device/inode,
  daemon PID/boot/start/eUID/native-version, installation/environment identity,
  genuine supported-client pairing witness and both untouched replay fences.
  Put the identical fresh, at-most-15-minute target packet in the fixed host
  ConfigMap key and the operator's deployment-owned target file. Existing
  operator flag parsing reads the latter at startup; rotate the operator
  configuration through GitOps without restarting running agent sessions.
- Enable only the existing fixed endpoint with `--enable-codex-rc-thread` and
  `--codex-rc-target-file`, under its exact target and unchanged caller rule.
  From the existing human/client/recognized-workbench identity, submit one
  `rc fresh` request for `gpt-6.1-sol`, effort `medium`. Save the HTTP receipt,
  fresh native thread ID and turn ID. A supported client must actually observe
  that new thread on the paired host; record that witness separately. No
  inferred phone registration, fork, old history or second launch on uncertainty.

The initial handoff concerns HaynesTower array free space/growth/distribution,
physical data versus duplicate mounts/hardlinks, the actual download/media stack
and stale/orphan/failed downloads, plus stopped Trash recommendations and deleted
Trash returning (generation, exclusions, deletion/sync/reimport/errors/persistence).
Read-only first: no media deletion, array moves or configuration changes. Use
existing access and bundle missing scopes. Routine haynesnetwork fixes follow
its repository reviews/CI. Tom drives the conversation. Only task-specific prior
evidence may be referenced: `/home/dev/work/orders/haynesnetwork-storage-trash-20261030/REPORT.md`
and `/home/dev/work/haynesnetwork-trash-read-1030` on the target. The launcher
implementation does not start this research or import prior conversation history.

## Evidence and ending state

Targeted source tests and independent review are required before merge; required
CI and routine signed release follow. Record exact head, checks, release digests,
GitOps pin, installed version, qualification receipt and phone witness separately
in OP272. Source completion cannot close the runtime/phone acceptance gap.
