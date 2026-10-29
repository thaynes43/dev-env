# DESIGN-038: Repeatable Remote Control launch

Date: 2026-10-29. **Proposed contract; documentation only.**
Work order RCLAUNCH267-DESIGN-20261029-01,
[OpenProject #267](https://work.haynesnetwork.com/work_packages/267),
[Epic #91](https://work.haynesnetwork.com/work_packages/91),
related [#94](https://work.haynesnetwork.com/work_packages/94).
Source inspected at `a60b031cb6f93f82a35c2cebb1065b1f733942b2`, freshly fetched
`origin/main`. This document implements, installs and activates nothing. The
lead reviews and merges the single ready PR; this work stops with checks started.

## Outcome and boundaries

An agent already authorized to launch Remote Control (RC) supplies a project,
provider, full model ID and handoff. One command returns a fresh conversation
on a suitable host, actual provider RC readiness and its link, or the precise
pairing action needed. Failure names the failed phase, elapsed time and remedy.
An authorized coordinator can do this through the supported API without Mac
control. An agent coordinating work and the API's restricted `kindCoordinator`
caller class are different things; the latter is not currently authorized for RC.

Reuse a healthy paired Codex host when the supported provider route permits it.
Keep its computer ID, enrollment, HOME, history and existing RC conversations.
A new conversation must have a new provider conversation/thread ID; resuming or
forking the previous conversation does not meet this request. Claude initially
uses a new remote AgentSession because today's supervisor owns one conversation
per session. It must not consume the standby or alter an active session.

Extend `agent-run`, the existing operator API and a thin skill. No roles system,
new orchestration service, autonomous repair, inferred permission, provider
fallback or Mac automation is part of this design. Existing caller checks,
profile ceilings and the keeper's sole refresh ownership remain controlling.
New access requires a separate explicit proposal and Tom's decision. The
questions below are recommendations for the lead, not rulings or delivered asks.

## Evidence and current implementation

The #267 history records the Mac parent's October 29 observation, rather than
measurements performed by this author. All times below are UTC on 2026-10-29.

| Observed event | Time | Interval from previous row | What it proves |
| --- | --- | --- | --- |
| Original request | 22:09:31 | — | Start of user-visible elapsed time |
| Claude fallback selected | 22:24:53 | 15m22s | Discovery/relay delay before Claude creation |
| Temporary reviewed CLI source ready | 22:33:34 | 8m41s | Coordination and tool-route preparation |
| CLI build ready | 22:34:34 | About 60s | Reported build interval; not a cluster launch phase |
| Session created | 22:35:03 | 29s | API creation; 25m32s after original request |
| Pod scheduled | 22:35:10 | 7s | Placement |
| Pod ready | 22:35:27 | 17s | 24s from create, cached image, reported zero restarts |
| Daemon conversation started | 22:36:27 | 60s | Agent start, 84s from create |
| Link delivered | 22:40:36 | 4m09s | Retrieval/output/relay completed; 5m33s from create |

Request to delivered link was 31m05s. Only 24s was create to PodReady. The
record reports no keeper-auth or image-pull outage. It does **not** establish
when provider auth, RC registration or link availability first occurred. Leave
those times unknown; do not attribute the entire final 4m09s to registration.
The launcher should resolve the dispatch route before pairing, use the installed
versioned CLI, and return a compact link-first result without compiling a client
while the user waits.

These source references were rechecked for this design:

| Area | Current behavior and limit |
| --- | --- |
| [CLI create](../../../../internal/agentrun/create.go) | `--interactive` selects `remote`; rejects `--project`, a prompt/handoff, task limits and `--local` in that combination. Default wait is 30s with 2s polls. It waits for session/pod settlement, not provider RC readiness. A follow error after create can still return success with a warning. |
| [API admission](../../../../internal/apiserver/validate.go) | `kindCoordinator` is accepted-project/task/dev/S-or-M only. Codex remote creation separately accepts human, trusted client or recognized workbench session, and rejects a project task snapshot. Session children retain their parent's profile; a workbench-created Codex remote host must omit profile and uses the template default. |
| [Controller](../../../../internal/controller/reconciler.go), [API view](../../../../internal/apiserver/view.go) | `Running` can mean PodReady while agentd is still booting. The view exposes condition words, not their transition times. It exposes conversation ID and last heartbeat but drops protocol `AgentState.StartedAt`; RC view drops `sessionId`. These gaps prevent accurate phase timing today. |
| [Claude launch](../../../../internal/agentd/launch.go), [daemon](../../../../internal/agentd/daemon.go), [RC collector](../../../../internal/agentd/remote.go) | First launch chooses a fresh UUID with `--session-id` and `--remote-control`; later boots resume the saved conversation. The RC collector verifies live PID/process-start identity against the CLI session record, then reports `registered` and a bridge link. Its decoded record has only bridge ID/process start: this is registration evidence, not current provider connection or account acceptance. |
| [Claude projection](../../../../internal/agentd/claude_auth.go), [projection type](../../../../internal/claudeauth/access.go) | Merge access-only fields, retain CLI-owned fields, fence generations and refuse refresh material. Missing/invalid/expired access holds start; a pod-owned login with projection mode is `login-conflict`. |
| [Pod login CLI](../../../../internal/agentrun/login.go), [agentd login](../../../../internal/agentd/remote.go), [ctl entry](../../../../cmd/agentd/main.go) | `login url/code/status` is the legacy pod-owned ceremony. Projection mode refuses url/code. A live agent and login start exclude each other. URL lookup is bounded at 60s; code uses stdin. |
| [Keeper renewal API](../../../../internal/apiserver/keeper_login.go), [CLI](../../../../internal/agentrun/keeperlogin.go), [helper](../../../../internal/claudelogin/helper.go), [worker](../../../../internal/keeper/claude_worker.go) | Existing workbench-only renewal through the API needs no Mac. Helper stages a new login in memory; keeper adopts and publishes access only. Refresh and adoption serialize under the keeper's ownership/fence. Ceremony lifetime is 15m; URL/code API waits are 45s/40s. RC launch must not invoke this ceremony. |
| [Standby](../../../../internal/standby/standby.go), [creation](../../../../internal/apiserver/standby.go) | One labelled ordinary Claude remote session, default-off runnable, minute cadence, persisted three-creations/30m breaker. Suspended/draining/booting/missing-pod cases count as present; ended-agent replacement has current-pod and 5m grace checks. No claim, project/model reassignment or fresh-conversation operation exists. |
| [Codex remote supervisor](../../../../internal/agentd/codex_remote.go), [pair CLI](../../../../internal/agentrun/pair.go) | Supervised phone daemon on a retained HOME, keeper access only. Start bound 30s; restart backoff 5s to 5m. Connected states are `unpaired` or `pair-code-issued`, neither authoritative phone-pairing proof. Pair invokes `agentd ctl codex-pair`, six attempts with 3s gaps and up to 30s per native call. |
| [Native wire contract](../../../../internal/apiserver/apiv1/native_control.go), [handler](../../../../internal/apiserver/native_control.go), [executor](../../../../internal/agentd/hostexecutor/native_control_linux.go), [executor contract](../../../../internal/agentd/hostexecutor/README.md) | `thread/start` exists only inside an immutable task-budget/host/pod/epoch/deadline binding. Native execution is authenticated-human only; coordinator consumption spends an already reserved one-use intent. This adapter pins Luna and deliberately disables remote enrollment. It is not a generic RC grant or the paired remote supervisor's launch route. |
| [Input delivery](../../../../internal/agentd/deliver.go), [registered transcript fence](../../../../internal/agentd/portal_conversation.go) | Claude delivery confirms buffer paste and Enter, not that the CLI recorded input or the provider accepted inference. Existing transcript readers fence the current/initial launch and Pod identities around bounded reads; they supply a reusable identity pattern, not an existing handoff receipt. |

[Plan 03](../backlog/03-remote-control.md) contains delivered and historical
source-only notes; [plan 04](../backlog/04-rolling-updates-codex.md) includes the
older coordinator-host route and later source notes. Neither historical status
text proves today's installation. [HANDOFF](../../../HANDOFF.md) records dated
phone/standby acceptance. This design does not repeat those runtime checks.
Follow [ADR-003](../adrs/003-session-coordination-private-repositories.md) and the
[launch-profile preflight](../../../../docs/launch-profile-preflight.md): select
an already suitable authorized host/profile, keep private worktrees, and do not
infer hardware or workbench capability from an installed executable.

### Codex provider evidence and its limits

The image [pins 0.160.1](../../../../images/agent/Dockerfile). Read-only local
`codex --version`, `codex --help`, `codex app-server --help` and
`codex remote-control --help` confirmed that version and these command surfaces:
`app-server proxy` connects stdio to the existing daemon control socket;
`generate-json-schema` is available; `agents` is a shared-daemon browser;
`remote-control` has start/stop/pair, with no fresh-thread command. No proxy,
RPC, browser, schema generation, daemon start or pairing was executed.

Official [App Server documentation](https://developers.openai.com/codex/app-server/)
describes initialization, fresh `thread/start`, separate `turn/start`, and
version-specific generated schemas. These support a possible adapter design;
they do not prove that an app-visible remote project, link or permission exists
on this installation. Use the pinned schema in a later authorized source story.

The #267 parent observation reports a healthy paired host but `list_projects`
returned 11 `projectKind: chatgpt` entries with `hostId: null`, without the Codex
remote project IDs required by `create_thread`. This pod has neither that
connector implementation nor those tool schemas available. Repository code
confirms that its accepted GitOps catalog supplies repository snapshots, not
this app's remote project inventory ([catalog](../../../../internal/apiserver/catalog.go)).
Thus the mismatch is a **reported integration defect**, not a locally reproduced
provider response. Do not invent the missing IDs or treat the ChatGPT list as a
Codex inventory. Story 3/3 needs a supported, versioned mapping and independent
source evidence from the actual connector/app-server integration before use.

## Separate phase states and timing

A launch is a finite operation. Its receipt has independent phase records;
phases can overlap or arrive out of order. Auth may be usable before agent
start, while inventory must pass before any conversation mutation. `Running`,
`busy`, `boot=ready` and a code-issued timestamp cannot stand in for these facts.

| Phase key | Evidence needed for `ok` | Timing source and current gap |
| --- | --- | --- |
| `api_accepted` | Durable, caller-bound launch receipt; session creation/reuse decision acknowledged | API acceptance time and immutable request digest; current creation time alone does not describe reuse |
| `pod_scheduled` | Current session-owned Pod UID, `PodScheduled=True` and node binding | Pod condition transition time plus API observation time; add transport to the RC receipt |
| `pod_ready` | Same Pod UID, `PodReady=True`, current generation | Pod transition time plus observation time; current session condition view omits timing |
| `agent_started` | Fresh current-boot launch/process evidence, conversation ID for Claude or live daemon identity for Codex | Agentd start time/boot ID plus observation; carry existing start evidence through the API |
| `provider_auth_ok` | Provider accepted the RC connection under the intended account; local access validation is separately `credential_ready` evidence | Registration/connection observation, never credential contents; expiry checks alone are insufficient. Inference entitlement/served model remain separate |
| `rc_ready` | Claude current-process bridge registration plus a supported current provider/client connection witness; Codex current-daemon remote connection plus supported client/host readiness evidence | Provider event time when supplied, otherwise first fresh observation; the current Claude collector lacks a connection witness and registration timestamp |
| `link_available` | Validated provider link for the new conversation, bound to the intended host/project/thread | First availability observation separately from CLI delivery time; Codex thread links are not currently supplied by this repo |
| `pairing_required` | Supported client says the selected host needs pairing; otherwise report pairing knowledge `unknown` | Time the action was detected. `required` is an actionable stop, never successful pairing; `pair-code-issued` does not satisfy it |
| `project_inventory_ok` | Unique authorized project/repository/root; for Codex, correct remote project kind, host ID and project ID mapping | Inventory observation time, source/version and mapping revision; no guessing from a name, cwd or a ChatGPT project ID |

Each record contains `state` (`pending`, `ok`, `required`, `blocked`, `error`,
`unknown`, `not_applicable`), `startedAt`, `firstObservedAt`, `sourceAt` when the
source supplies it, `finishedAt`, `durationMs`, `sinceRequestMs`, `deadlineAt`,
`evidenceSource`, `origin` (`created` or `reused`), `reason` and the relevant
session UID/Pod UID/boot/host/thread
binding. Missing times and durations are null, not zero. Measure local elapsed
intervals with a monotonic clock; preserve UTC source and observation times.
Label clock disagreement and do not subtract unsynchronized source clocks.

On host reuse, retain the original pod/agent event times as historical evidence,
set their `origin` to `reused`, and record the current revalidation time and cost.
Do not report new scheduling, a zero-cost cold launch or negative elapsed time.
A changed Pod UID/boot invalidates current readiness until revalidated; a
historical successful event remains history, not present health. Report stale
or unavailable evidence as unknown. Initial proposal: heartbeat/readiness
observations must be no older than 30s at a mutation or final readiness result;
older installed heartbeat cadences need a bounded supported observation path,
not an artificially recent timestamp.

Add `conversation_created` and `handoff_delivered` records alongside the nine
required phases. Host readiness does not create a Codex thread. Handoff delivery
separates `transport_submitted` from `cli_recorded` (or a supported provider input
acknowledgement). Neither means completed work, successful inference or proof of
the served model. Keep `requestedModel`, provider-reported configured model and
`servedModel` separate; absent served model is unknown.

### Bounds

Proposed defaults, to be tested rather than presented as observed service levels:
`--launch-timeout=5m` fixes the server operation's absolute deadline at acceptance;
allow an explicit shorter timeout or a maximum 10m. Client `--wait=5m` controls
only how long to follow that receipt and is capped by its remaining deadline;
`--wait=0` returns the accepted receipt immediately. Accepted server work may
continue only inside its original finite deadline after a client disconnect or
local polling cancellation. It never acquires a new allowance on retry/status.
API acceptance and status calls are
at most 10s, inventory/capability preflight 15s, scheduling 60s after acceptance,
PodReady 120s after acceptance, agent start 90s after PodReady, RC/auth acceptance
90s after agent start, link retrieval 10s after registration, and handoff delivery
15s. Credential-projection settling gets at most 30s inside the agent-start bound.
Every inner deadline is capped by the absolute deadline; these are not additive
allowances. Return a detected refusal immediately. Status polling is paced at
2s, at most 150 reads in the default window; no busy loop.

Pairing/auth owner actions end the wait with an action record; the launcher does
not wait for a person. Existing pairing's possible 195s attempt envelope and the
keeper's 15m ceremony are separate explicit tools, outside `rc start`. No launch
poll starts either one or rides the daemon's perpetual restart loop indefinitely.
A server launch deadline ends launch work and handoff attempts; it does not terminate,
restart, reap or archive a session already created. Preserve its handle for
status. A request may time out with known earlier phases and an unknown outcome.

## Claude path

Today's path is `agent-run --repo R --agent claude --interactive --model ID`:

1. CLI validates the model and sends a remote create. API authenticates the
   caller, checks the ordinary admission/profile/parent limits and creates an
   AgentSession. Controller creates its private HOME and Pod.
2. PodReady only says the container is ready. Agentd still renders config,
   prepares the repository/worktree and applies auth. Projection mode merges
   keeper access and account metadata, stripping static OAuth/refresh variables
   from the remote CLI environment. A bad projection holds launch; conflicting
   pod-owned refresh material stops it.
3. With usable credentials, first launch creates a UUID and starts Claude's TUI
   with `--session-id` and `--remote-control`. Existing launch records lead to
   `--resume`, which is correct for recovery, not a fresh RC launch.
4. Agentd reads the current CLI's process-bound session record. A valid bridge
   session ID yields `registered` and its link. API heartbeat/view exposes state
   and URL; `agent-run show` can retrieve them without scraping a terminal. The
   bridge ID can remain after a connection/auth failure. A newly read record or
   heartbeat must not be presented as a fresh provider health check.
5. A later handoff can use the authorized API message delivery path. Today
   `--interactive` cannot carry a prompt, and `msg` is a separate one-shot input
   operation ([delivery](../../../../internal/apiserver/podcmd.go)). Success is
   transport submission only: paste/Enter has no provider input acknowledgement.
   It preserves
   sender attribution and treats peer text as information, not Tom's identity.

Story 2/3 must join these existing steps and add the missing evidence seams:
resolve project to an authorized
repository/root, create a fresh remote session, wait for current-process RC
registration and a supported connection witness, deliver and observe the handoff
once in that conversation, and return the
link with timings. This is a new RC request type; do not silently reinterpret
the task-only `CreateSessionRequest.project`/`prompt` contract. Reuse accepted
catalog validation and the private clone/worktree/rules helpers, with a fresh
fetch/pin before new work. Multi-repository projects require explicit `--repo`
when ambiguous. Confirm the selected cwd and rules in the receipt.

**Missing Claude evidence:** establish a versioned, supported provider/client
connection/auth witness bound to this bridge, process and intended account.
The existing two-field local record does not provide it, and refreshing its
observation timestamp cannot manufacture it. Do not run `claude auth status`,
start a login or create a probe conversation for this purpose. If the pinned
provider exposes no such witness within existing access, return
`provider_readiness_unavailable` with registration/link evidence kept separately;
`provider_auth_ok` and `rc_ready` remain unknown/blocked. Qualifying this seam is
a story 2/3 prerequisite; stop/split that source story if it cannot be established.

For handoff acknowledgement, add a bounded observer of a native user-input record
in the exact new conversation, with one-use handoff ID and canonical text digest.
Reuse the before/after current/initial-launch and Pod fences from the registered
transcript reader, without broadening portal read authority or exporting raw
transcripts. A match means `cli_recorded`, not accepted inference. If no qualified
input witness arrives within 15s, retain `transport_submitted`/unconfirmed and the
known link; do not report ready or resend. The transcript/record shape must be
qualified against the pinned CLI before implementing this observer.

A healthy access projection is sufficient to attempt start, not to declare
provider auth accepted. Missing projection returns the specific keeper action.
The launcher must not run a pod login as a fallback. If renewal is needed, an
already authorized workbench operator uses the existing `keeper-login` route;
its status/url/code ceremony stays separate and private. Legacy pod-owned
`ctl login url|code|status` remains for explicitly selected legacy sessions only.
The skill never opens either ceremony automatically.

**Standby answer:** reuse it to open its existing conversation when that is what
the owner wants. It cannot satisfy this fresh-conversation contract today. It
has no atomic vacant claim or supported conversation switch, and may hold active
work/history even when idle. Story 2/3 leaves its label, model, project, breaker
and process untouched. A warm pool with safe fresh conversations would be
separate work, not a prerequisite for this launch tool.

## Codex path and blockers

Preflight the exact dispatch capability and inventory **before** requesting a
pairing code or allocating a new host. Resolve `P` to the right provider project
namespace; an accepted dev-env project and a ChatGPT project are not remote
Codex project IDs.

1. Obtain a bounded, caller-authorized inventory of suitable existing hosts.
   Require correct account/provider, stable host ID, retained enrollment,
   compatible pinned protocol, fresh daemon/RC connection evidence, suitable
   resolved profile, and an unambiguous project/root mapping. Host reuse creates
   another conversation on the same failure boundary; it is not an isolated
   worker pod. Explicit isolation needs a different authorized dispatch scope.
2. Prefer an explicitly named suitable host; otherwise select the unique suitable
   healthy host. Multiple equally suitable hosts return an actionable ambiguity
   instead of choosing an arbitrary machine. A busy daemon is not automatically
   unhealthy: existing threads can remain active if the supported protocol allows
   another independent thread. Do not interrupt them to make a host eligible.
3. Check supported client-side pairing/host-access evidence. `pair-code-issued`
   only records that a code was once issued. Unknown pairing knowledge is reported
   as unknown; do not automatically re-pair a healthy previously paired host.
   A confirmed pairing requirement returns the existing `agent-run pair SESSION`
   action for an authorized operator, with no code generated or retained. `pair`
   takes an AgentSession name derived from a verified host-to-session mapping,
   not the provider host ID. If a supported host has no managed-session mapping,
   return its supported owner action or `pairing_action_unavailable`; never
   substitute the host ID in that command.
4. Once inventory, authorization and host suitability pass, create one new thread
   through the supported authenticated provider adapter on that exact daemon.
   Set and verify the requested full model and selected project/cwd in the
   versioned protocol, then submit the handoff as one initial input. Return the
   provider thread ID and supported open link. Preserve all other threads and
   the host's installation ID. A host-wide model setting is insufficient:
   today's remote supervisor merely records `--model`; phone threads choose it.
5. No suitable existing host is not permission to create one. When the caller
   has the existing Codex remote-create right, a separately explicit
   `--create-host` may select today's bootstrap, with the profile rule intact.
   It still cannot create a conversation without inventory/dispatch support.
   Otherwise return an authorization/action result, without mutation.

**BLOCKED: remote project inventory.** The reported connector list contains only
ChatGPT projects. Needed evidence is a supported mapping of provider remote
project ID, project kind, stable host ID, authorized local root/repository and
catalog identity, with version/freshness and a way to disambiguate names. The
connector/app-server owner must fix and verify that mapping; adding a local
name-to-ID guess does not repair inventory. This is a prerequisite to story 3/3,
not a reason to create test threads or pair another computer.

**BLOCKED: supported RC thread adapter and link evidence.** Pinned CLI help
establishes the existence of an existing-daemon proxy, not that current
`agent-run`/API can use it for arbitrary RC callers. A future source adapter must
use the pinned generated schema and establish app visibility, project binding,
configured model, history, active-thread preservation and link retrieval.
Do not start a second app-server or use an exec/proxy path around admission.
The owned-budget native executor has different authority and deliberately
suppresses RC; borrowing it would neither solve inventory nor preserve its
security contract.

**BLOCKED for `kindCoordinator`: authorization.** The early coordinator ceiling
and the Codex remote guard both remain. A working inventory does not answer
Q-267-01 or authorize a generic native call. Workbench coordination can already
use its own existing launch rights without a Mac; the inventory/adapter gap
still prevents the complete Codex flow.

## CLI, API and result contract

Proposed commands (not installed commands):

```text
agent-run rc preflight --project P --agent claude|codex --model <full-id> [-o json]
agent-run rc start --project P --agent claude|codex --model <full-id> --handoff <file> [-o json]
agent-run rc status <launch-id> [-o json]
```

Optional selectors are `--repo`, `--host`, `--profile` only where already allowed,
`--effort`, `--wait`, `--launch-timeout`, `--idempotency-key`, and explicit
`--create-host` for Codex
(default false). No force/skip-auth/skip-inventory/auto-pair switches. Default
host policy is reuse-or-report for Codex, fresh session for Claude. A profile
selects an already authorized launch; it cannot retrofit a host or elevate a
session. No model/provider fallback. Use the installed CLI; missing capability
returns a version/feature error.

Proposed API: `POST /v1/rc/preflight`, `POST /v1/rc/launches`, and
`GET /v1/rc/launches/{id}`. Preflight is read-only, resolves the same caller as
start, and returns exact capability blockers without an auth/pairing action.
Start performs preflight again, accepts a bounded request, and returns a durable
receipt (`202`, replay of the same receipt `200`). Polling reads evidence; it
never creates a host/thread or delivers input. No arbitrary RPC transport is
exposed. Refusals use the existing API error envelope plus RC phase/reason data.

The request carries project/repository selection, provider, full model/effort,
optional eligible host, handoff bytes plus digest, bounded deadline and request
key. The local filename is not a server path. Bound handoff to 8KiB UTF-8,
nonempty, no terminal controls except newline/tab, checked before upload.
A caller-scoped key binds the full immutable request digest; a changed payload
with the same key is a conflict. Establish receipt/session identity before any
provider mutation. All access uses the original authenticated caller and target
scope; a skill, project name or requested profile conveys no authority.

Persist only the Claude launch receipt's IDs, digest, one-use attempt and phase
metadata/status with the new AgentSession using existing operator session
storage. Handoff bytes travel in the authenticated bounded request and stdin
delivery transport; they must not enter public metadata, Session spec/status,
environment variables, logs or telemetry. The source proposal keeps the pending
8KiB payload in bounded operator memory only for the finite launch interval,
with admission limits on concurrent pending launches. This resembles an existing
bounded API ceremony, not a new scheduler/service. On process loss, no replica
recovers/resubmits the payload: retain the receipt, let its original deadline
settle pending work, and report payload unavailable or an attempted send as
unconfirmed. Never recover bytes from a transcript to replay them. If durable
private payload recovery is required instead, stop/split and design its storage
and access explicitly; do not assume Secret/RBAC authority.

Story 2/3 must pin receipt schema, concurrency/retention limits and this ephemeral
transport, and prove existing storage/write rights suffice. Story 3/3 must pin
the host-scoped receipt storage for
multiple Codex launches and prove existing write authority is sufficient; extra
storage/RBAC rights require a separate proposal before that implementation.
Status authorizes the original caller/owner and existing permitted readers;
never expose another caller's handoff or new cross-host metadata by request ID.

Before any thread-create or handoff-send, record its one-use attempt durably.
A lost reply or crash after that point is `unconfirmed`; do not replay, assign a
new key, send the handoff to another conversation or allocate a replacement.
A repeated start returns the same receipt only. Confirm thread identity first;
confirm handoff acceptance separately. Fixed UID/boot/conversation checks must
close the observe-to-deliver race. An unconfirmed handoff may already be running.

Return one concise JSON object, at most 32KiB with bounded phase/reason text;
never truncate a result into invalid JSON. Include version, launch ID, outcome,
project/provider/model, session/host/thread IDs, validated link or null, action
or null, phase records, handoff status, total timings, error and remedy.
A synthetic partial result illustrates that pairing and conversation creation
are different (IDs below are placeholders, not observed runtime identities):

```json
{
  "version": "rc-launch.v1",
  "launchId": "launch-example",
  "outcome": "action_required",
  "project": "example",
  "agent": "codex",
  "requestedModel": "gpt-6.1-sol",
  "servedModel": null,
  "sessionName": "session-example",
  "hostId": "host-example",
  "hostReused": true,
  "threadId": null,
  "link": null,
  "action": {"kind": "pair_host", "command": "agent-run pair session-example"},
  "conversationCreated": false,
  "handoff": {"state": "not_sent"},
  "timing": {"totalMs": 840, "deadlineMs": 300000},
  "error": {"code": "pairing_required", "phase": "pairing_required"}
}
```

The real response also carries the nine phase records defined above; this
example omits that array for brevity. `outcome` is `accepted`, `ready`,
`action_required`, `blocked`, `failed`, `timed_out` or `unconfirmed`. A ready
result requires a new conversation, inventory, provider RC readiness, a link
and a qualified handoff input record/acknowledgement. Transport submission alone
does not qualify. Preserve any known link in a partial result,
even if delivery fails. Unsupported Codex links return `link_unavailable`, never
a guessed Claude-shaped URL or a fabricated app deep link. The text output puts
the link or required action first, then handle and elapsed time.

Reuse CLI exits 0 (ready, or a successful status/preflight read), 1 (definite
failure/blocker), 2 (local usage), 3 (authentication/authorization), 4 (missing
handle), 5 (accepted still pending, timeout or required owner action), and 6
(unconfirmed mutation). Exit 5 permits a status read, not mutation replay.
JSON must be emitted for errors too when `-o json` is selected. Logs omit
handoff text, credentials, pairing codes and login links. RC links go only to
authorized output/storage, not public issues or telemetry.

Proposed operator flags, all default **false**: `--enable-rc-launches` for the
Claude receipt/preflight/launch/status surface and
`--enable-codex-rc-launches` for the supported Codex inventory/thread adapter.
The latter requires the former and reviewed inventory readiness. Flags do not
broaden caller classes or enable host creation for a denied caller. Keep the
existing `--enable-standby-session`, auth flags and native-budget flags separate;
changing them is not part of this work. Mixed versions return a precise missing
capability instead of downgrading to pod-only success.

### Thin skill

A later `rc-launch` skill takes the same project/provider/model/handoff inputs,
checks the explicit work scope against launch-profile preflight, and invokes
`agent-run rc start -o json` once. It retains the launch ID, presents the link or
action, and uses bounded `rc status` only when needed. It does not inspect provider
HOMEs, read credentials, compile clients, call native RPCs, select a more powerful
profile, open login/pairing ceremonies, or retry uncertain work. Its handoff must
retain attribution, scope, constraints, budget and stop criteria; forwarding text
does not impersonate Tom or expand the recipient's authority. Install the same
thin instruction through each provider's supported skill loader. Source, image
inclusion and actual instruction loading are separate evidence.

## Concrete errors and remedies

All messages below are proposed fixed messages with safe handle/timing fields.
Every error returns the last phase receipt and ends within the named bound.
No remedy grants permission to perform an otherwise unauthorized action.

| Code / bound | Message | Remedy |
| --- | --- | --- |
| `invalid_input` / local, before network | `RC launch needs a project, full model ID and a nonempty handoff of at most 8192 bytes.` | Correct inputs; remove invalid terminal controls. |
| `capability_disabled` / 10s call | `This operator does not expose rc-launch.v1 for the selected provider.` | Use an installed reviewed version/activation through the lead; no client build or legacy fallback while waiting. |
| `unauthenticated` / 10s call | `The API did not accept this caller's credential.` | Use the existing managed client identity/status route; do not borrow another identity. |
| `launch_forbidden` / before mutation, 10s call | `This caller cannot start remote sessions for this provider.` | Retain refusal; an already authorized workbench/human may launch within their own scope. Restricted coordinator expansion waits for Q-267-01. |
| `profile_mismatch` / preflight 15s | `The selected host/profile cannot serve the declared work scope.` | Select an existing authorized suitable host; preserve parent/default-profile rules. |
| `project_unavailable` / preflight 15s | `Project P is absent, unauthorized or has no unique repository/root.` | Correct declared project or supply its authorized repository; have the catalog owner resolve ambiguity. |
| `codex_inventory_unavailable` / preflight 15s | `No Codex remote project ID is available for P on the selected host; ChatGPT projects cannot be used.` | Fix/verify the supported project-kind/host/project-ID mapping. Stop before pairing or creating a thread. |
| `host_ambiguous` / preflight 15s | `More than one suitable Codex host matches; select --host.` | Select one authorized stable host ID. |
| `host_unavailable` / preflight 15s | `No healthy suitable paired Codex host is available.` | Inspect the existing host through authorized status. Explicit new-host creation needs an existing create right; no automatic replacement. |
| `pairing_unknown` / preflight 15s | `Daemon connection is healthy, but phone pairing cannot be confirmed by this adapter.` | Obtain supported client-side access evidence; do not re-pair on this uncertainty. |
| `pairing_required` / immediate on supported evidence | `Host H needs phone pairing; no conversation or handoff was created.` | Authorized operator uses `agent-run pair SESSION` for the mapped managed session and the phone once; keep the code private. |
| `pairing_action_unavailable` / preflight 15s | `This host has no verified managed-session pairing action.` | Use the supported provider owner action; do not pass a provider host ID to `agent-run pair`. |
| `scheduling_timeout` / 60s after acceptance | `Session S was accepted but its pod was not scheduled within 60s: REASON.` | Preserve S; inspect scheduler/PVC capacity through existing reads, then status S. Do not create a duplicate. |
| `pod_ready_timeout` / 120s after acceptance | `Pod U was scheduled but is not Ready within 120s: REASON.` | Inspect the bound pod's startup/image/PVC reason; retain the existing session. |
| `agent_start_timeout` / 90s after PodReady | `Pod U is Ready but the provider process has not started within 90s: STEP.` | Inspect the fixed boot/repository/auth reason. Preserve WIP; repair only in a separately authorized scope. |
| `auth_projection_unavailable` / 30s settling | `No current keeper access projection was accepted; RC start is held.` | Authorized workbench operator checks existing keeper auth status; renewal, if needed, is a separate ceremony. |
| `login_conflict` / immediate | `This pod has a pod-owned Claude login while keeper projection mode is configured; launch stopped.` | Owner resolves the login-mode conflict separately. Do not overwrite or delete credentials. |
| `provider_auth_rejected` / RC/auth 90s maximum | `Provider rejected the RC connection; a local access file did not establish provider auth.` | Report the fixed provider reason; authorized keeper owner diagnoses. No session login, refresh or paid/provider/model fallback. |
| `rc_ready_timeout` / 90s after agent start | `Provider process started, but current RC registration/connection was not confirmed within 90s.` | Preserve the host/session; inspect supported connection status, not terminal output. |
| `provider_readiness_unavailable` / preflight 15s or immediate on detection | `Registration/link evidence exists, but this adapter cannot establish current provider RC readiness.` | Qualify a supported current connection/auth witness within existing access. Keep auth/RC state unknown; no fabricated freshness or implicit new access. |
| `link_unavailable` / 10s after registration | `RC is registered but no validated link for the new conversation is available.` | Complete supported provider link retrieval/mapping; report the known handle and readiness separately. |
| `host_changed` / immediate at revalidation | `The selected host boot or Pod UID changed; launch evidence is no longer current.` | Read/revalidate the same logical host; never transfer an uncertain mutation to a replacement. |
| `protocol_unsupported` / preflight 15s | `Installed provider/agentd lacks the required inventory or fresh-thread contract.` | Deliver compatible reviewed versions under a separate rollout; do not restart an active host for this launch. |
| `handoff_rejected` / 15s | `Conversation C has RC readiness/link evidence, but handoff input was refused; no delivery was confirmed.` | Keep C and its link. Inspect the specific addressability/UID/size refusal before a separately authorized correction. |
| `handoff_unconfirmed` / 15s | `Input transport was submitted, but the CLI did not confirm the handoff record in time; it may be running.` | Preserve the conversation/link and one-use attempt. Read the same receipt; do not resend. |
| `handoff_payload_unavailable` / at original deadline or proven process loss | `The pending handoff is no longer available to its original launch process; no send was confirmed.` | Preserve the receipt and session. Do not infer permission to resubmit or retrieve a payload from another conversation. |
| `request_conflict` / 10s call | `This launch key already names a different immutable request.` | Inspect the original receipt. Do not change a key to retry an attempted mutation. |
| `mutation_unconfirmed` / capped provider/delivery call | `The thread or handoff request may have succeeded; its one-use attempt is consumed. Do not replay.` | Read the same receipt/provider handle through authorized reads; preserve uncertainty for the lead. |
| `launch_deadline` / absolute 5m default, 10m maximum | `RC launch reached its deadline at PHASE after N ms; created resources were preserved.` | Inspect the same handle with bounded status; no automatic cleanup or new launch. |
| `status_unavailable` / 10s read | `Current RC evidence could not be read; last successful phase is historical.` | Report unknown/stale state and retain the handle. A later explicit status read is safe; a replacement launch is not implied. |

## Authorization and lead-held questions

Server enforcement, not the CLI or skill, decides each operation. Reuse does not
inherit the creator's authority. Preflight and status must scope host/project
metadata to the caller before revealing it; thread creation and handoff require
the caller's own target permission. Do not turn read visibility into launch
rights. Recheck live session identity and existing parent/child ceilings.

| Existing caller | Claude remote create | Codex remote host create | Other boundaries |
| --- | --- | --- | --- |
| Human / configured trusted client | Existing route, admission/profile limits apply | Existing route, admission/profile limits apply | Keeper renewal is specifically workbench-only, even for human callers |
| Ordinary session | Existing child create with inherited profile and existing limits | Refused | No direct pod exec, pairing or login through a new wrapper |
| Recognized workbench session | Existing route with normal child/profile limits | Allowed only with omitted profile; template default, no inherited workbench tier | Existing explicit exec/pair and keeper-login rights are separate from RC launch |
| Configured `kindCoordinator` | Refused: task-only ceiling | Refused by ceiling and Codex guard | Child-scoped task operations and one-use native intent consumption do not grant general RC/thread creation |

There is no generic fresh-thread RC grant in the current native-control route.
Keep its authenticated-human/task-budget restrictions, Luna pin, immutable
binding, one-use consumption and no-replay behavior unchanged. No kubectl exec,
local proxy, borrowed workbench token or alternate API bypass is an acceptable
answer to denial. Proposed flags remain off until reviewed delivery, and new
access is not implicit in adopting this document.

**Q-267-01 — restricted coordinator RC permission (pending, not delivered).**
Should the API's configured `kindCoordinator` be allowed a narrowly defined RC
launch operation for explicit authorized projects and selected suitable hosts,
including Codex reuse and, separately, new remote-host creation? Recommendation:
first ship story 2/3 for existing allowed callers; request a separate default-off,
audited policy proposal for this caller class. Specify identities, project/host
scope, creation limits, profile ceiling, metadata visibility, fresh-thread/input
rights and refusal cases before approval. Keep current denial until Tom decides.
An agent already coordinating from an authorized workbench needs no Mac today;
that does not authorize converting every coordinator into a workbench caller.

**Q-267-02 — Codex adapter authority if inventory repair is insufficient
(pending, not delivered).** Should an operator-mediated same-daemon adapter be
added if the supported app connector cannot expose remote projects and create
fresh threads? Recommendation: repair the supported inventory/connector path
first. Any operator adapter must be proposed separately with exact caller/host
rights, transport, protocol, pairing evidence and link behavior, while retaining
the current human-owned budget route. Permission to connect to a daemon socket
or exec into a host must not be inferred from an RC creation right.

No ADR records a settled new policy here: these are open questions. The lead
owns delivery of questions and creation of the following stories after review.

## Build stories under Epic #91

These are scope/acceptance proposals linked from canonical #267, not a second
work queue. Do not start either story under this docs work order. The lead
searches OpenProject before creating or claiming their canonical records.

### 2/3 — Claude RC launch in agent-run/API and the thin skill

**Outcome:** callers with existing Claude remote-launch rights supply a project,
full model and handoff, receive a new conversation's link or a bounded concrete
failure, and need no Mac. Include phase/timing transport, receipt/idempotency,
private handoff delivery, default-off API surface and the shared skill. Exclude
standby consumption, coordinator access expansion, Codex thread implementation,
renewal/pair automation and deployment. Dependency: reviewed DESIGN-038, accepted
project/root helpers and unchanged current caller policy. Qualify the supported
Claude connection/auth witness and native input-record shape before implementing
ready output; otherwise stop/split the blocked seam and retain partial outcomes.
No owner decision is needed to preserve current denials.

**Ownership and budget:** one fresh bounded session/worktree owns CLI, apiv1/API,
agentd/heartbeat/controller fields, skill packaging and focused tests in dev-env.
Propose one 75-minute source unit with one serial independent review, concurrency
at most two; token/cost quota unknown. Checkpoint/split at the bound instead of
rolling into installation or an auth incident. The lead must ratify scope and
budget in the new story before pickup.

Acceptance separates:

- **Source/tested:** finite fake-clock/API/provider fixtures distinguish every
  required phase, PodReady with no agent, local credential versus provider auth,
  stale/current-boot evidence, registration without live-connection evidence,
  inventory ambiguity, one fresh conversation, transport-only submission and a
  qualified bound handoff input record. Exercise two callers racing/replaying one key,
  changed payload conflict, crash/lost-reply consumption, revalidation races,
  cancellation/deadline and status without mutation. Verify denied callers and
  all existing profile/child restrictions. Assert standby/peer/history bytes are
  unchanged. Check valid bounded JSON, redaction, link-first text and errors;
  raw handoff bytes never persist in Session metadata/spec/status or environment.
  Pin finite memory/concurrency/receipt limits and process-loss behavior without
  new storage rights. Missing supported witnesses cannot pass using fake-only
  assumptions; record their qualification source and version.
  Tests use inert fakes: no real host/thread/auth/pairing. Run one focused suite
  at a time under `nice -n 19`, `GOMAXPROCS`/workers no greater than
  `DEV_ENV_CPU_LIMIT` (cap at two); no burners, stress or repeated load loops.
  Native independent review and required `CI - Success` are source gates.
- **Installed:** separately authorized signed CLI/operator/agent publications,
  compatible schema/status fields and provider skill packaging are pinned through
  GitOps; flags are explicitly selected and version preflight works. Installation
  does not mean loaded skill instructions or successful RC launch. Never restart
  active sessions to adopt a field or verify a skill.
- **Usable/accepted:** a separately authorized bounded launch from an allowed
  agent caller creates a genuinely fresh Claude conversation, receives the
  authorized project/rules/worktree and one CLI-recorded handoff, and returns
  its valid current RC link with all known phase timings. The owner can open and
  steer it without Mac control. Demonstrate concrete failure/action output with
  approved fixtures and unchanged standby/active peers. Record remaining unknown
  auth/phase/model evidence honestly. No new login or fault injection is implied.

Artifacts/handoff: one source PR, exact head, test/review evidence, flag/version
matrix, installation packet and pending usable check linked to the canonical
story. Stop at its authorized acceptance/budget boundary; delivery needs its own
scope if not included. A source pass must not close installed/usable criteria.

### 3/3 — Codex launch after remote inventory is fixed

**Outcome:** reuse an authorized healthy paired host, select its real remote
project/root, create a fresh model-bound thread and deliver the handoff once;
return the supported link or exact pairing action/error with timings. Preserve
host ID, retained state, existing history and all active threads. Include the
pinned versioned adapter, host selection, conservative pairing knowledge and
host-scoped receipt bounds. Exclude automatic re-pair, new credentials, daemon
restarts, budget-route changes and any unapproved coordinator/socket/exec grant.

**Dependencies, serial:** story 2/3's receipt/CLI/skill contract; supported remote
project inventory repair with actual connector/schema evidence; existing caller
permission for every selected operation. Q-267-01 is required only for a proposed
new coordinator class right, and Q-267-02 only for a proposed new adapter access
route. Both remain holds for those changes. Inventory is a hard prerequisite
regardless of caller. Lead records ready prerequisites before assigning work.

**Ownership and budget:** one new fresh bounded source session/worktree, one
75-minute proposal with at most one serial native reviewer (concurrency two,
quota unknown), subject to lead ratification. Stop at a missing dependency or
budget threshold. Do not turn inventory discovery into an open-ended provider
investigation or consume runtime actions to find the schema.

Acceptance separates:

- **Source/tested:** pinned schema fixtures establish project kind/ID/host/cwd
  mapping and prevent use of ChatGPT IDs. Exercise one/many/no eligible hosts,
  paired/required/unknown evidence, supported-version refusal, requested model
  configuration, fresh thread/input IDs, supported link mapping, active-thread
  preservation, caller/UID/boot races, durable uncertain attempts and no mutation
  from status. Prove explicit host creation retains today's caller/profile checks;
  default behavior never creates a host. Pin receipt storage/retention limits and
  demonstrate no extra RBAC is assumed. New access remains blocked pending its
  explicit decision. Run finite inert suites serially under the same CPU rule as
  story 2/3; native independent review and `CI - Success` gate source acceptance.
- **Installed:** separately authorized versions and fixed inventory integration
  are published/pinned; default-off Codex flag is selected explicitly only when
  compatible and permitted. Confirm version/capability reporting without host
  restart, re-enrollment or pairing. Do not infer all retained hosts upgraded.
- **Usable/accepted:** under a new explicit runtime work order, an authorized
  agent launches from outside the Mac into one designated healthy paired host.
  Verify a new thread on the same computer, correct project/model configuration,
  one handoff acceptance, actual RC readiness/link, owner open/steer, and unchanged
  earlier thread/history/host IDs. Pairing-required behavior is tested separately
  with authorized fixtures or an explicitly approved host; this design authorizes
  no pairing. Log timings/unknowns and preserve active peers. Inventory-only,
  daemon-connected and code-issued results cannot accept the entire story.

Artifacts/handoff: exact source/schema/inventory references, PR/head, finite test
and review evidence, caller matrix, preserved-host assertions, installation and
usable evidence as separate states. Stop on acceptance, dependency/approval hold
or budget limit. Do not start another story or runtime check automatically.
