# OP272: default-off existing-daemon thread and handoff

Owner: `dev-env-1030-015210`, branch `agent/dev-env-1030-015210`.
Actual driving model: GPT-6 Astra. Direct October 30 work order and OP272
journal2926 authorize disabled implementation, fixtures and normal publication.
No runtime exposure, live RPC, installation, pairing or actor grant follows.

## Source delivered

One new operation on the existing authenticated operator listener:
`POST /v1/sessions/{name}/codex-thread`. It is absent unless
`--enable-codex-rc-thread` (default false) is explicitly supplied. It additionally
requires `--codex-rc-target-file=/absolute/deployment-owned/packet.json` and the
existing accepted project catalog binding. A target file without enablement is
refused at startup. No old wire types or client behavior change.

The predicate is exactly Codex remote creation's human, client or recognized
workbench session predicate. Ordinary sessions and kindCoordinator remain
refused. This changes no authentication policy, role, RBAC, port or ingress.
The operator uses its existing PodExecutor, fixed container `agent`, no shell:

```
agentd ctl codex-thread preflight
agentd ctl codex-thread start
```

Both take one bounded typed dispatch on stdin. Preflight checks only configured
qualification and local filesystem/process facts; it neither launches a process
nor opens a native socket. Start rechecks those facts before consumption and
before the single turn. It uses PR690's existing peer-verified Unix WebSocket
primitive: initialize, connected registration identity, one thread/start with
exact accepted cwd/model and `environments: []`, then one turn/start with that
new thread ID, exact model/effort and one text handoff. No existing conversation
resume, environment selection, approval/sandbox override or generic RPC exists.

The configured root is never taken from HTTP input. The operator resolves the
accepted catalog snapshot; agentd requires the same saved snapshot and the
SHA256 of an existing Ready `project-bootstrap.json` for that catalog/repository.
It verifies the deployment-qualified root's device/inode, ownership, permissions,
HOME containment and no symlink components. It performs no Git preparation,
fetch or root repair. The existing registered process must match PID, boot ID,
start ticks, effective UID and remote mode. Installation/environment IDs must
also match the daemon's native status. Neither statusConnected, a pairing code
nor pod readiness supplies the independently qualified supported-client pairing
witness required by the packet.

Dependencies remain separate: [PR690](https://github.com/thaynes43/dev-env/pull/690)
head `118c2552` provides the daemon primitive and pinned thread schemas. This
source PR stacks on that branch so it does not repackage its changes. Discovery
[PR691](https://github.com/thaynes43/dev-env/pull/691), head `6b1149a`, remains the
separate inventory/discovery lane; its files are untouched and its project IDs
are not made a requirement for native cwd creation. Contract-only
[PR695](https://github.com/thaynes43/dev-env/pull/695), head `7bc0397`, remains the
transport reference; this implementation replaces its proposed source gap and
adds the initial handoff required by the current work order. No dependency merge
or release action is performed by this author.

## One-use behavior and receipt

The task-budget ledger belongs to a different foreground Luna executor and is
not reused as authority over this RC daemon. The minimal new durable state is
one bounded Session annotation `dev-env.haynesops.com/codex-rc-once`, reserved
using resourceVersion CAS before the sole mutating exec. It stores only request
and dispatch digests, configured model/effort, target, native IDs and timings.
It never stores handoff text. Digests freeze authenticated principal and every
request field, then the entire resolved dispatch. A second concurrent reservation
loses CAS; there is no retry loop.

Agentd reuses the existing `writeFileOnce` helper: fsync the temporary file,
hard-link exclusively to `~/.agentd/codex-rc-once.json`, then fsync the directory,
immediately before native dispatch. The local fence prevents duplicate exec
from mutating twice. It stores only the dispatch digest. Both fences are retained,
never reset or evicted by this operation. This deliberately permits **one launch
per target Session UID**, rather than adding an expandable journal or retention
policy. Another launch on that UID requires a separately designed/authorized
extension; deleting these fences is not a supported retry procedure.

Identical authenticated requests can read a confirmed saved receipt (200) even
after target readiness changes. Different input/principal/request keys conflict.
Reservation, lost exec, lost thread ACK, lost turn ACK or failed receipt persistence
remain consumed/unconfirmed. A native refusal after reservation also stays
consumed. A crash before confirmed persistence cannot redispatch. No automatic
retry or repair under a new key is provided. Receipt `confirmed` means both native
ACKs only; served model, inference completion, phone visibility and usable RC
acceptance are unproven. Errors contain fixed reasons and bounded correlation,
never provider stderr, credentials or raw native data.

## Concrete future activation packet — held, not installed

The same exact JSON target packet must be independently qualified and supplied
to the operator file and the existing host's fixed
`$AGENTD_CONFIG_DIR/codex/rc-target.json`. This is deployment-owned configuration;
there is no HTTP pairing assertion or endpoint that creates this witness.
The DTO is `apiv1.CodexRCTarget` in `internal/apiserver/apiv1/codex_rc.go`:

```json
{
  "sessionName": "QUALIFIED_EXISTING_NAME",
  "sessionUID": "QUALIFIED_EXISTING_UID",
  "podUID": "QUALIFIED_EXISTING_POD_UID",
  "project": "ACCEPTED_PROJECT",
  "repository": "ACCEPTED_REPOSITORY",
  "snapshotSHA256": "SHA256_OF_CANONICAL_ACCEPTED_SNAPSHOT",
  "root": "/home/dev/work/QUALIFIED_PREPARED_ROOT",
  "rootDevice": 0,
  "rootInode": 0,
  "preparationSHA256": "SHA256_OF_EXISTING_READY_PROJECT_BOOTSTRAP_BYTES",
  "pid": 0,
  "bootID": "EXISTING_BOOT_ID",
  "startTicks": 0,
  "installationID": "EXISTING_NATIVE_INSTALLATION_ID",
  "environmentID": "EXISTING_NATIVE_ENVIRONMENT_ID",
  "pairingSource": "supported-client",
  "pairingSHA256": "SHA256_OF_VERIFIED_SUPPORTED_CLIENT_PAIRING_EVIDENCE",
  "verifiedAt": "2026-10-30T00:00:00Z",
  "expiresAt": "2026-10-30T00:15:00Z"
}
```

These are deliberately invalid placeholders, not fabricated live witnesses. The
packet has an at-most-15-minute validity window, checked afresh at dispatch and
before the turn. Zero/missing/unknown/mismatched evidence refuses. The current
work order supplies no qualified live root/process/pairing packet. No private
UI, credentials or provider history was read to manufacture one.

The future reviewed request is at most16KiB on the wire, at most4096 bytes of
handoff, with a deadline after now and at most30 seconds ahead:

```json
{"requestID":"ONE_REVIEWED_ID","sessionUID":"QUALIFIED_EXISTING_UID","podUID":"QUALIFIED_EXISTING_POD_UID","project":"ACCEPTED_PROJECT","repository":"ACCEPTED_REPOSITORY","model":"gpt-6.1-sol","effort":"medium","handoff":"EXACT_REVIEWED_HANDOFF","deadline":"FRESH_RFC3339_DEADLINE"}
```

Activation would expose only this fixed operation for the unchanged authorized
caller classes and exact qualified existing Session/Pod/catalog/root/native
identity, then consume the one request. Required runtime rights are the existing
operator's `get/update agentsessions`, `get pods/configmaps` and `create pods/exec`
for container agent. Source adds **no new privilege**; installed RBAC/admission
and annotation persistence must qualify through existing authorized reads before
activation. A denial must be reported, never worked around. No worker receives
operator credentials. The source and agentd binaries must be installed under a
separately approved preservation procedure; this author does not install or
restart active RC hosts. Phone acceptance remains a separate observed check.

## Validation record

Pinned `codex-cli 0.160.1` generated the unchanged complete TurnStartParams and
TurnStartResponse schemas offline with `codex app-server generate-json-schema
--experimental`. No daemon was started. SHA256 respectively:
`07771223642e1b61bd9aac0069fc0f98143a1c047724ca02c7ceb13653442738`,
`12c5151421bb061297c9790ff795a44aa4b9bdb91fbb68012dcf0a08cf4198b1`.
The [pinned official TurnStartParams](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/app-server-protocol/schema/typescript/v2/TurnStartParams.ts)
specifies threadId/input and optional model/effort. The retained thread schemas
and PR695's transport proof remain separate evidence.

Focused fixtures cover default-off404; human/client/workbench acceptance;
worker/Coordinator refusal; malformed/bounded JSON and model/effort; target
Session/Pod/catalog/repository/root/preparation/process/pairing drift; retained
replay fences across new invocations and new request keys; lost ACKs/fsync
uncertainty; existing host UID preservation. A synthetic Unix WebSocket peer
asserts exactly thread/start then turn/start, no other mutating methods or
new daemon. Tests never use native/provider runtime or pairing credentials.
Final exact-head review, package regression and hosted CI receipts are recorded
in the source PR and OP272. PR690's earlier full local agentd failures remain
historical facts and are not relabelled as a full pass here.
