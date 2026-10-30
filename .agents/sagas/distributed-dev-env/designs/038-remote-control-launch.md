# DESIGN-038: Repeatable Remote Control launch

Date: 2026-10-29. **Proposed; stories 2a-2d and 3/3 pending in OpenProject #267 and #268.**
Work order RCLAUNCH267-DESIGN-20261029-01,
[OpenProject #267](https://work.haynesnetwork.com/work_packages/267),
[Epic #91](https://work.haynesnetwork.com/work_packages/91),
related [#94](https://work.haynesnetwork.com/work_packages/94).
Docs fix RCLAUNCH267-DESIGN-FIX-20261029-02 rechecked the source at
`e984c06d0faafe8cdee53b30a3090be3abbf3899`.
Original source inspection: `a60b031cb6f93f82a35c2cebb1065b1f733942b2`,
freshly fetched `origin/main` for the first draft. This document implements, installs and activates nothing. The
contract remains proposed; stories 2a-2d and 3/3 are pending in
OpenProject [#267](https://work.haynesnetwork.com/work_packages/267) and
[#268](https://work.haynesnetwork.com/work_packages/268).

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
| [CLI create](../../../../internal/agentrun/create.go) | `--interactive` selects `remote`; the CLI rejects `--project`, a prompt/handoff, task limits and `--local` in that combination. The project refusal is CLI-only for Claude remote. Default wait is 30s with 2s polls. It waits for session/pod settlement, not provider RC readiness. A follow error after create can still return success with a warning. |
| [API admission](../../../../internal/apiserver/validate.go) | `kindCoordinator` is accepted-project/task/dev/S-or-M only. Other allowed callers can create Claude remote sessions with accepted project snapshots, subject to the existing project feature gate and admission limits; no test currently pins this combination. Codex remote creation separately accepts human, trusted client or recognized workbench session, and rejects a project task snapshot. Session children retain their parent's profile; a workbench-created Codex remote host must omit profile and uses the template default. |
| [Controller](../../../../internal/controller/reconciler.go), [API view](../../../../internal/apiserver/view.go) | `Running` can mean PodReady while agentd is still booting. The view exposes condition words, not their transition times, plus conversation ID and last heartbeat. Protocol `AgentState.StartedAt` is dropped earlier by [heartbeat storage](../../../../internal/apiserver/heartbeat.go#L143-L151): `v1alpha1.AgentStatus` has no start-time field. The RC view drops `sessionId`. Accurate phase timing needs a CRD change as well as API transport. |
| [Claude launch](../../../../internal/agentd/launch.go), [daemon](../../../../internal/agentd/daemon.go), [RC collector](../../../../internal/agentd/remote.go) | First launch chooses a fresh UUID with `--session-id` and `--remote-control`; later boots resume the saved conversation. The RC collector verifies live PID/process-start identity against the CLI session record, then reports `registered` and a bridge link. Its decoded record has only bridge ID/process start: this is registration evidence, not current provider connection or account acceptance. |
| [Claude projection](../../../../internal/agentd/claude_auth.go), [projection type](../../../../internal/claudeauth/access.go) | Merge access-only fields, retain CLI-owned fields, fence generations and refuse refresh material. Missing/invalid/expired access holds start; a pod-owned login with projection mode is `login-conflict`. |
| [Pod login CLI](../../../../internal/agentrun/login.go), [agentd login](../../../../internal/agentd/remote.go), [ctl entry](../../../../cmd/agentd/main.go) | `login url/code/status` is the legacy pod-owned ceremony. Projection mode refuses url/code. A live agent and login start exclude each other. URL lookup is bounded at 60s; code uses stdin. |
| [Keeper renewal API](../../../../internal/apiserver/keeper_login.go), [CLI](../../../../internal/agentrun/keeperlogin.go), [helper](../../../../internal/claudelogin/helper.go), [worker](../../../../internal/keeper/claude_worker.go) | Existing workbench-only renewal through the API needs no Mac. Helper stages a new login in memory; keeper adopts and publishes access only. Refresh and adoption serialize under the keeper's ownership/fence. Ceremony lifetime is 15m; URL/code API waits are 45s/40s. RC launch must not invoke this ceremony. |
| [Portal launch](../../../../internal/apiserver/portal_launch.go#L28-L111) | Existing default-off constrained route creates Claude remote sessions through `createSessionMode`, the shared create path. This is precedent for composing launch tooling without a second create path; its derived caller is not a general RC permission. |
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
Codex inventory. The current Mac connector flow needs a supported, versioned
mapping before use.
That defect does not block source work on the existing-host app-server adapter
described in story 3/3 and #267 journal 2799.

## Separate phase states and timing

This is the full target evidence model. Story 2a records only observations
available through existing session views; its exact phase projection is below.
The durable receipt and source timing fields belong to later stories.

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

Proposed defaults, to be tested rather than presented as observed service levels.
Keep the 5m default to bound how long the caller waits. The sole observation took
5m33s from create to delivered link, without a publication/retrieval breakdown.
On that evidence a 5m timeout returning `timed_out` with a preserved handle is
expected if the link has not yet been observed; it does not mean the session
failed. An explicit longer wait (up to 10m) is available. Story 2a bounds client following and its one message
attempt only; it adds no server operation or server deadline. The server deadline
below belongs to the conditional receipt API in story 2c.

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

Story 2a joins the existing create/read/message routes and returns
`link_available`, with provider auth and RC readiness still unknown. It needs no
new create path: the [portal route](../../../../internal/apiserver/portal_launch.go#L28-L111)
already composes Claude remote creation through the shared handler.

**Ratified snapshot behavior:** the current CLI's `--project --interactive`
refusal is not an API prohibition for Claude. Outside `kindCoordinator`,
[validation](../../../../internal/apiserver/validate.go#L103-L118) resolves the
accepted catalog and [attaches the snapshot](../../../../internal/apiserver/validate.go#L296-L297).
Only [Codex remote](../../../../internal/apiserver/validate.go#L142-L143)
explicitly refuses it. [Repository preparation](../../../../internal/agentd/clone.go#L260)
applies a snapshot without a mode restriction, and the
[interactive launch](../../../../internal/agentd/launch.go#L218-L232) carries
project rules. No current test pins the complete Claude remote combination;
story 2a adds API and launch tests. Preserve the existing project feature gate,
caller, profile and parent limits. A snapshot narrows repository/root/rules
scope; it grants no permission. The prompt remains task-only; handoff uses the
separate one-shot message route. Use accepted catalog/private worktree helpers
and fresh fetch/pin before new work. Multiple repositories need explicit
`--repo` when ambiguous. Do not claim stronger cwd/rule evidence than the
current API supplies.

**Missing Claude evidence:** establish a versioned, supported provider/client
connection/auth witness bound to this bridge, process and intended account.
The existing two-field local record does not provide it, and refreshing its
observation timestamp cannot manufacture it. Do not run `claude auth status`,
start a login or create a probe conversation for this purpose. If the pinned
provider exposes no such witness within existing access, return
`provider_readiness_unavailable` with registration/link evidence kept separately;
`provider_auth_ok` and `rc_ready` remain unknown/blocked. Story 2a reports the
unknown evidence and can still return a validated link;
qualifying this seam is required before any later story may report `ready`.

For a later full-readiness result, add a bounded observer of a native
user-input record
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
work/history even when idle. Stories 2a-2d leave its label, model, project, breaker
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
name-to-ID guess does not repair inventory. This blocks the current interactive
Mac connector flow, not the bounded source adapter in story 3/3. It is not a
reason to create test threads or pair another computer.

**BLOCKED: supported RC thread adapter and link evidence.** Pinned CLI help
establishes the existence of an existing-daemon proxy, not that current
`agent-run`/API can use it for arbitrary RC callers. Story 3/3 may build a source
adapter over codex-cli 0.160.1's official
app-server proxy against an existing host, with fixtures and tests under the
existing human/workbench-only guards, without waiting on the Mac's project
inventory (#267 journal 2799). It must use the pinned generated schema and
establish app visibility, project binding,
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

Story 2a implements only `rc start` and `rc status <session>` over existing
routes, as specified below. `rc preflight`, launch IDs and the new receipt routes
are conditional story 2c proposals, not prerequisites for 2a.

Proposed full contract (not installed commands):

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

Conditional story 2c API, only if 2a proves too weak:
`POST /v1/rc/preflight`, `POST /v1/rc/launches`, and
`GET /v1/rc/launches/{id}`. Preflight is read-only, resolves the same caller as
start, and returns exact capability blockers without an auth/pairing action.
Start performs preflight again, accepts a bounded request, and returns a durable
receipt (`202`, replay of the same receipt `200`). Polling reads evidence; it
never creates a host/thread or delivers input. No arbitrary RPC transport is
exposed. Refusals use the existing API error envelope plus RC phase/reason data.

The request carries project/repository selection, provider, full model/effort,
optional eligible host, handoff bytes plus digest, bounded deadline and request
key. `rc start` always generates a key when omitted, sends it and prints it;
the caller can preserve identity after a lost create reply. This follows
[existing create key generation](../../../../internal/agentrun/create.go#L236-L238).
The [shared API create handler](../../../../internal/apiserver/sessions.go#L102-L110)
returns 409 for a changed request under the same key; map it to `request_conflict`.
A create key does not deduplicate handoff sends.
The local filename is not a server path. Bound handoff to 8KiB UTF-8,
nonempty, no terminal controls except newline/tab, checked before upload.
This is deliberately stricter than the [16KiB message limit](../../../../internal/apiserver/apiv1/types.go#L55):
it leaves room for sender attribution and the handoff wrapper within the existing
message transport, while bounding private payload memory for launch tooling.
For story 2c, a caller-scoped key binds the full immutable request digest
(including handoff digest); a changed payload with the same key is a conflict. Establish receipt/session identity before any
provider mutation. All access uses the original authenticated caller and target
scope; a skill, project name or requested profile conveys no authority.

In conditional story 2c, persist only the Claude launch receipt's IDs, digest,
one-use attempt and phase metadata/status with the new AgentSession using
existing operator session storage. Handoff bytes travel in the authenticated bounded request and stdin
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

Story 2c must pin receipt schema, concurrency/retention limits and this ephemeral
transport, and prove existing storage/write rights suffice. Story 3/3 must pin
the host-scoped receipt storage for
multiple Codex launches and prove existing write authority is sufficient; extra
storage/RBAC rights require a separate proposal before that implementation.
Status authorizes the original caller/owner and existing permitted readers;
never expose another caller's handoff or new cross-host metadata by request ID.

The full receipt API must record a one-use attempt durably before any
thread-create or handoff-send. Story 2a has no durable handoff receipt: it sends
once in the invocation, never from status or a reused create-key result, and
reports uncertain delivery without retrying. This limitation is evidence for
deciding whether story 2c is needed.
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
example omits that array for brevity. `outcome` is `accepted`, `link_available`, `ready`,
`action_required`, `blocked`, `failed`, `timed_out` or `unconfirmed`. A ready
result requires a new conversation, inventory, provider RC readiness, a link
and a qualified handoff input record/acknowledgement. A successful 2a launch reports
`link_available` rather than `ready`: registration/link and transport submission
do not establish provider auth, a live RC connection or CLI-recorded handoff.
Transport submission alone does not qualify. Preserve any known link in a
partial result, even if delivery fails. Unsupported Codex links return `link_unavailable`, never
a guessed Claude-shaped URL or a fabricated app deep link. The text output puts
the link or required action first, then handle and elapsed time.

Keep existing CLI exits 0 (`link_available`, future qualified `ready`, or a
successful status/preflight read), 1 (definite failure/blocker), 2 (local usage),
3 (authentication/authorization), 4 (missing handle), 5 (`ExitRetry`: nothing was
sent), and 6 (unconfirmed mutation). Add **`ExitPending = 7`** for accepted and
pending, timed out with a preserved handle, or required owner action. Exit 7
permits a status read, not mutation replay. Never reuse exit 5 for accepted work;
[the existing exit contract](../../../../internal/agentrun/agentrun.go#L48-L52)
allows a caller to retry because nothing was sent.
JSON must be emitted for errors too when `-o json` is selected. Logs omit
handoff text, credentials, pairing codes and login links. RC links go only to
authorized output/storage, not public issues or telemetry.

Story 2a adds no operator flag; its subcommand is inert until invoked.
Proposed operator flags for later receipt/adapter stories, all default **false**:
`--enable-rc-launches` for the Claude receipt/preflight/launch/status surface and
`--enable-codex-rc-launches` for the supported Codex inventory/thread adapter.
A Codex adapter using the story 2c receipt API requires `--enable-rc-launches`
as well. An existing-host source adapter under existing guards does not require
story 2c; its selected inventory/root mapping still needs reviewed evidence.
Flags do not broaden caller classes or enable host creation for a denied caller. Keep the
existing `--enable-standby-session`, auth flags and native-budget flags separate;
changing them is not part of this work. Mixed versions return a precise missing
capability instead of downgrading to pod-only success.

### Thin skill

Story 2d delivers a `rc-launch` skill through a haynes-ops session-config PR.
The skill takes the same project/provider/model/handoff inputs,
checks the explicit work scope against launch-profile preflight, and invokes
`agent-run rc start -o json` once. For 2a it retains the session handle and
idempotency key; a later 2c receipt also has a launch ID. It presents the link or
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
| `session_ended` / next session read | `The Session ended (Failed, Suspended, Archived or being reaped); a retained link is not usable.` | Stop with exit 1; report the last observed phase when one exists. No replacement launch, resume or cleanup is implied. |
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
first ship story 2a for existing allowed callers; request a separate default-off,
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

These are scope/acceptance proposals linked from canonical #267 and #268, not
a second work queue. This docs work order starts no implementation. Story 2a is
already assigned in [OpenProject #268](https://work.haynesnetwork.com/work_packages/268).
The lead searches OpenProject before creating or claiming 2b-2d and 3/3.

### 2a — Client-only Claude CLI over existing routes (OpenProject #268)

The following is the exact implementation specification from work order
RCLAUNCH268-2A-CLAUDE-CLI-20261029-01 in #268. It is the assigned 2a contract;
the broader receipt/readiness proposals above do not expand it.

```text
Spec (from the independent review of DESIGN-038; follow it exactly):
Add internal/agentrun/rc.go with:
  agent-run rc start --project P [--repo R] --agent claude --model <full id> [--effort e] --handoff FILE [--wait 5m] [--idempotency-key K] -o json
  agent-run rc status <session> -o json
- Scope: only the existing routes POST /v1/sessions (mode remote, project, repo, model, effort, and an idempotency key that is ALWAYS sent:
  generated if not given, and printed), GET /v1/sessions/{name}, and ONE POST /v1/sessions/{name}/messages. No operator, CRD, agentd-runtime or
  flag change; the subcommand is inert until invoked; --enable-rc-launches stays reserved.
- Code changes:
  - lift the CLI's --project refusal for remote mode for `rc start` only (create.go ~108-109);
  - add ExitPending = 7 in internal/agentrun/agentrun.go: accepted and pending, timed out with a handle, or owner action required. Exit 5 stays
    "nothing was sent";
  - register the command and its help;
  - put the rc-launch.v1 result type in internal/apiserver/apiv1/rclaunch.go (standard library only);
  - `--agent codex` returns outcome blocked / codex_adapter_not_implemented ("Codex remote launch is not implemented in 2a; no inventory was probed") with exit 1 and makes NO network call. The 3/3 error codex_inventory_unavailable is reserved for an actual failed inventory probe.
- Local checks: a full model id via apiv1.CheckClaudeModel; a handoff that is non-empty, valid UTF-8, at most 8192 bytes, with no control characters
  except newline and tab (the same rule as podcmd stripControl).
- Phase records, each with state, firstObservedAt (UTC), sinceRequestMs (monotonic), sourceAt: null, evidenceSource and reason:
  - api_accepted: 201 created, or 200 reused by key;
  - project_inventory_ok: the response echoes the accepted repo and base; a 422 on the project is project_unavailable;
  - pod_scheduled and pod_ready: from the view's conditions;
  - agent_started: agent running with a non-empty conversationId;
  - provider_auth_ok: always unknown, reason provider_readiness_unavailable;
  - rc_ready: unknown, plus rcRegistered true when the RC state is registered;
  - link_available: the URL matches apiv1.PortalRemoteControlURLPattern;
  - pairing_required: not_applicable;
  - handoff: transport_submitted on a 202; cli_recorded is always unknown.
  Top-level fields: pollIntervalMs 2000, polls, totalMs, deadlineMs.
- Bounds: poll every 2s; 60s to schedule; 120s to PodReady; 90s to agent start; 90s to RC registration; 10s for the link; ONE message attempt. All
  are capped by --wait.
- Errors: invalid_input, unauthenticated, launch_forbidden (403, including the coordinator refusal), project_unavailable, request_conflict (409
  for a reused key with a changed request), scheduling_timeout, pod_ready_timeout, agent_start_timeout, login_conflict and
  auth_projection_unavailable (from the agent message), rc_registration_timeout, link_unavailable, handoff_rejected (409 not addressable),
  handoff_unconfirmed (exit 6; never resent), launch_deadline (exit 7), session_ended (exit 1: the Session reached Failed, Suspended or Archived,
  or is being reaped), status_unavailable.
- `rc status` of an active Session that has no link yet reports outcome `accepted` with exit 7 (ExitPending); it makes one GET and never
  sends a message.
- Output: outcome link_available with exit 0, never "ready". The link comes first. Valid JSON of at most 32 KiB. The handoff text NEVER appears in
  stdout, stderr or logs.
- Tests:
  - internal/agentrun/rc_test.go, with the existing httptest TLS harness and a fake clock, covering: the happy path; PodReady with no agent;
    registered with no URL; a bad URL pattern; a 403 coordinator; a 422 project; a key replay (200); a changed request under the same key (409); a
    409 on the message; a lost message reply (assert exactly one POST); the deadline at each phase; --wait 0; JSON size and validity; redaction;
    Codex refused with zero requests.
  - API tests pinning that a Claude remote create with a project attaches the snapshot for human and session callers and is refused for the
    coordinator class.
  - agentd launch tests pinning that a remote launch carries a fresh --session-id, --remote-control, and the project rules arguments when a
    snapshot is present.
- Verify, one suite at a time: `nice -n 19 env GOMAXPROCS=2 go test -p 1 ./internal/agentrun/ -run RC`, then the apiserver and agentd tests you
  added (-run filters; NOT the full agentd package), then `make check-layout lint`. GOTMPDIR and GOCACHE under HOME.
NO live launch, RC session, login, pairing or message during development. No secrets, login URLs or codes. Stop when the PR is open and checks have
started. Record the PR and head on #268. Budget: about 2 hours.
```

**Current-view qualification:** the assigned specification above describes
scheduling through view conditions. Today the controller exposes `PodReady`,
not a separate `PodScheduled` condition. Story 2a uses only the existing node/
condition observations, labels the evidence source, and keeps `sourceAt` null;
it cannot claim a scheduler event timestamp. Story 2b adds source timing fields.

**Acceptance states:** source means the CLI/result type and API/agentd pin tests
pass focused checks, native independent review and required `CI - Success`.
Installed means a separately authorized released agent image carries the new
`agent-run`. Usable means one separately authorized launch returns a link that
the owner opens and steers from the phone. `link_available` is the honest 2a
outcome even then; its unknown provider readiness and input acknowledgement do
not become `ready` through installation. Preserve PR/head, checks and remaining
unknowns in #268. Stop at the work order's ready-PR/checks-started boundary.

### 2b — Server timing fields and CRD copy

**Outcome/scope:** carry protocol start evidence through heartbeat storage and
expose current-Pod condition transition times with clear source/observation time
and identity. Include `api/v1alpha1.AgentStatus`, heartbeat conversion, API view
and wire fields. Exclude receipt orchestration, input observers, launch/pairing,
auth or caller-policy changes. Dependency: reviewed DESIGN-038 and 2a's recorded
timing gaps. No measured event may be synthesized from a poll timestamp.

**Ownership/budget proposal:** one fresh session/worktree owns dev-env schema,
heartbeat/view and focused tests; a serial haynes-ops CRD copy PR follows with
its own worktree and bounded work order. Propose 75 minutes for source plus one
serial native review, maximum concurrency two; usage quota unknown. Ratify the
budget before pickup and checkpoint/split if it will not fit.

- **Source/tested:** fake heartbeat/view and real-schema fixtures prove start
  time survives storage, missing times remain null, and Pod/boot changes cannot
  reuse current readiness. Change `api/`, run `make generate`, and commit the
  generated deepcopy and `config/crd/` files. Native independent review and
  `CI - Success` gate source acceptance.
- **Installed:** publish separately authorized signed versions. Open and merge
  a **haynes-ops CRD copy PR before the operator pin** that needs the new fields;
  retain the `api/` change and `make generate` evidence in the installation
  packet. Verify schema compatibility and preserved timing fields without
  restarting active sessions. A source-only API field is not installed evidence.
- **Usable/accepted:** a separately authorized observation demonstrates the
  event/source/observation distinction on one actual launch and reports missing
  evidence as unknown. It does not establish provider RC readiness.

Artifacts: source PR/head, generated diff, focused checks/review, CRD-copy PR,
operator pin and separate installed/usable evidence linked in OpenProject.
Stop at acceptance, a dependency/approval hold or budget boundary.

### 2c — Conditional receipt API behind --enable-rc-launches

**Outcome/scope:** only if 2a's recorded limitations prove too weak, add the
bounded durable receipt/preflight/launch/status surface described above through
the shared create path. `--enable-rc-launches` defaults false. Include immutable
request identity, one-use mutation attempts, deadline/status and private bounded
handoff transport. Exclude new callers, storage rights, auth/pairing repair,
standby consumption and generic RPC. Dependencies: assessed 2a evidence, reviewed
2b timing contract, and a separately scoped story ratified by the lead. It is
not an automatic continuation after 2a.

**Ownership/budget proposal:** one fresh source session owns apiv1/API and its
focused fixtures; propose 75 minutes plus one serial native review, concurrency
two, quota unknown. Split rather than extending into storage or an auth incident.

- **Source/tested:** pin receipt schema, storage/concurrency/retention bounds and
  process-loss behavior using existing write rights. Fixtures cover equal-key
  replay, changed-request 409, concurrent callers, lost replies, consume-before-
  send, no resend after crash, status without mutation, cancellation/deadline,
  redaction and UID/boot races. Qualify real supported connection and input
  witnesses before any `ready` output; inert fake evidence cannot qualify them.
  Without those witnesses preserve `link_available`/unknown outcomes. Native
  independent review and `CI - Success` gate source acceptance.
- **Installed:** separately authorize signed publication and GitOps pins, schema
  copies before dependent operator pins, and explicit default-off flag selection.
  Do not restart active sessions to adopt fields.
- **Usable/accepted:** a separately authorized bounded launch proves the assessed
  2a limitation is addressed, preserves peers/history and returns truthful phase,
  handoff and link evidence. Full `ready` requires every defined witness.

Artifacts: assessment, canonical story, PR/head, tests/review, flag/version matrix,
installation packet and separate usable evidence. Stop on acceptance, a missing
qualified witness, approval/dependency hold or budget limit.

### 2d — Thin skill in haynes-ops session-config

**Outcome/scope:** one haynes-ops session-config PR installs the thin `rc-launch`
instructions through the supported provider loaders. Use the installed 2a CLI;
retain its handle/key, link-first `link_available` result, unknown evidence and
one-shot delivery semantics. Adopt 2c only if separately delivered. Exclude CLI
compilation, credentials/HOME reads, native RPCs, access expansion and automatic
login/pairing or uncertain retries. Dependency: reviewed 2a contract and an
installed compatible CLI; 2c is optional.

**Ownership/budget proposal:** one fresh haynes-ops session/worktree owns only
the supported session-config skill packaging. Propose 45 minutes plus one serial
native review, maximum concurrency two, quota unknown; ratify before pickup.

- **Source/tested:** validate skill paths, rendered instructions and finite fake
  CLI results for link, pending/action, refusal and unconfirmed handoff. Native
  independent review and required CI gate source acceptance.
- **Installed:** merge/pin the separately authorized session-config delivery;
  verify the provider loader receives it at a safe boundary. Config publication
  alone does not prove instruction loading. Preserve active sessions.
- **Usable/accepted:** in a separately authorized bounded session, the loaded
  skill invokes the CLI once, presents the truthful link/action and timings, and
  preserves the key without retrying an uncertain mutation.

Artifacts: haynes-ops PR/head, render/check/review evidence, loaded instruction
receipt and pending/actual usable acceptance linked from the canonical story.
Stop at the authorized acceptance, dependency/approval or budget boundary.

All four source stories use finite inert fixtures: no live host/thread/auth/
pairing or message during development. Run one focused suite at a time under
`nice -n 19`, workers/`GOMAXPROCS` no greater than `DEV_ENV_CPU_LIMIT` (cap at two),
with no burners, stress or repeated load loops. Source, installation and usable
acceptance are recorded separately; do not start the next story automatically.

### 3/3 — Codex existing-host source adapter and guarded launch

**Bounded source split:** [OpenProject #271](https://work.haynesnetwork.com/work_packages/271)
is **3/3a: discovery only**. Its pure `internal/codexrc` adapter pins schemas
generated offline from codex-cli 0.160.1 and maps caller-authorized catalog/root,
host identity and client pairing observations to one existing Codex project.
Native `project/list` supplies IDs/roots, not connector `projectKind`/`hostId`;
external host/catalog binding and pairing knowledge must remain explicit.
None/ambiguous/ChatGPT-only mappings yield `codex_inventory_unavailable`.
The CLI still refuses Codex locally with `codex_adapter_not_implemented` and
does not probe inventory. **3/3b retains thread creation and handoff**, launch
wiring, runtime revalidation and the remaining source acceptance below.
3/3a grants no live RPC, pairing, host restart or new caller rights and does
not establish installation, app visibility, links or usable acceptance.

**Outcome:** reuse an authorized healthy paired host, select its real remote
project/root, create a fresh model-bound thread and deliver the handoff once;
return the supported link or exact pairing action/error with timings. Preserve
host ID, retained state, existing history and all active threads. Include the
pinned versioned adapter, host selection, conservative pairing knowledge and
host-scoped receipt bounds. Exclude automatic re-pair, new credentials, daemon
restarts, budget-route changes and any unapproved coordinator/socket/exec grant.

**Dependencies, serial:** use the reviewed 2a CLI/result contract and the
existing human/workbench-only guards. Under #267 journal 2799, source work may
build an adapter over codex-cli 0.160.1's official app-server proxy against an
existing host, with pinned schema fixtures and tests, without waiting on the
Mac connector's project inventory. Bind the accepted catalog repository/root
to that host through the versioned native protocol; no guessed ChatGPT IDs.
The current interactive Mac route still requires its inventory repair. Source
fixtures do not establish app visibility, a supported link or installed access.
Use 2b/2c/2d only where their separately reviewed contracts are needed, not as a
mandatory bundled prerequisite.

Q-267-01 is required only for new coordinator rights; Q-267-02 remains a
recommendation and hold for any new operator-mediated access. Neither blocks
source work under existing guards or grants runtime socket/exec/native-call
rights. The lead records ready prerequisites before assigning work. Runtime
validation still needs a separate work order and existing permission for each
operation.

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
  stories 2a-2d; native independent review and `CI - Success` gate source acceptance.
- **Installed:** separately authorized versions and the selected adapter/inventory
  integration are published/pinned; default-off Codex flag is selected explicitly
  only when compatible and permitted. Confirm version/capability reporting without host
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
