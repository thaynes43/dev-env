# Existing Codex daemon transport: proof and activation packet

2026-10-30 UTC. [OP272](https://work.haynesnetwork.com/work_packages/272),
proposal journal 2913; existing [OP267](https://work.haynesnetwork.com/work_packages/267)
and [OP271](https://work.haynesnetwork.com/work_packages/271), Epic 91.
Owner: `dev-env-1030-012055`, branch `agent/dev-env-1030-012055`.
Requested coordinator was Sol 6.1 medium; this driving session is GPT-6 Astra.
Freshly fetched source: `e8e8db86d5fdf55df2c565584df487e601a40aff`.

**Result: the supported local transport exists. The missing capability is an
operator operation authorized to create a fresh thread on an existing paired
daemon. No currently registered request can carry that operation.** This packet
specifies the smallest addition and its approval boundary. It does not implement
or expose it. This is not an observed provider, pairing or thread-create failure.

## Custody and preserved evidence

The direct continuation work order resumes the bounded investigation after the
stops in OP271 journals 2898/2899. OP272 journals 2867/2868 establish the former
worker's suspension and clean checkpoint. OP272 was claimed by this session
after posting the concrete proposal. No other author was displaced from source.

- [PR690](https://github.com/thaynes43/dev-env/pull/690),
  `118c2552aaf5307647d0b666786b9853453c42de`, remains an unmerged dependency for
  future integration. It supplies the package-private local `thread/start`
  primitive, **no command or API operation**. Its focused tests and native QA
  passed. Its full local agentd suite **failed**: missing envtest binaries,
  cross-device HistoryExport fixture and unconfirmed prior tmux teardown. No
  baseline equivalence is established. Hosted `CI - Success` subsequently passed
  on that head in [run 40983814246](https://github.com/thaynes43/dev-env/actions/runs/40983814246).
  Hosted success does not rewrite the local failure.
- [PR691](https://github.com/thaynes43/dev-env/pull/691),
  `6b1149a3366da4eda36bd46b745deb55ac5c2a18`, retains its author's custody.
  Its mapping package, fixtures, CLI documentation and DESIGN-038 are untouched.
  Native QA and full agentrun passed as recorded by its author; hosted
  `CI - Success` subsequently passed in
  [run 40983829094](https://github.com/thaynes43/dev-env/actions/runs/40983829094).
- The release cutoff remains a merge hold until the authorized release owner
  explicitly lifts it. A release commit or green CI alone does not lift it.
  Neither PR is merged, approved on Tom's behalf, copied or repackaged here.

This documentation branch depends on neither PR to build. Any later integration
must explicitly depend on PR690, and on PR691 if it uses discovery. Stacking on a
held branch does not authorize merging it or including its changes in a release.

## Official supported local route

Read-only CLI help reports `codex-cli 0.160.1` and:

```text
codex app-server proxy --sock <SOCKET_PATH>
Proxy stdio bytes to the running app-server control socket
```

The pinned official tag resolves to commit
`d27764b82f7118f674371e6d6e76271d9d606edb`, also used by the existing
[R-05 audit](../research/R-05-codex-native-routing.md). Inspected source:

| Official source at that commit | Established behavior |
| --- | --- |
| [cli/src/main.rs:680](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/cli/src/main.rs#L680), [1382](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/cli/src/main.rs#L1382) | `--sock` resolves relative arguments against cwd via `parse_socket_path` at line 733; omission resolves the control socket from Codex HOME. The narrow adapter must use its fixed absolute socket path. Proxy dispatch calls `codex_stdio_to_uds::run`, not daemon bootstrap. |
| [stdio-to-uds/src/lib.rs:12](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/stdio-to-uds/src/lib.rs#L12) | Connects one existing Unix stream, copies bytes in both directions and half-closes on stdin EOF. No listener, auth ceremony or JSON conversion. |
| [app-server-transport/src/transport/unix_socket.rs:125](https://github.com/openai/codex/blob/d27764b82f7118f674371e6d6e76271d9d606edb/codex-rs/app-server-transport/src/transport/unix_socket.rs#L125) | The existing control listener upgrades the Unix stream to WebSocket, then runs the WebSocket connection. |

All three fetched files matched their Git blob IDs in the pinned commit tree:
CLI `bdd7771b3aadfd69f46698c14844a6d65e1e65ba`, byte relay
`a103de3312a04d087f81e5e49c7b2c05e3f4d208`, Unix transport
`3f70bcced464e3ce0592c722d155efd06ea04ae1`. This is finite source verification,
not execution of the proxy or a daemon connection.

**Proxy stdio is not JSON-lines app-server stdio.** A proxy client must carry the
WebSocket HTTP upgrade and framing through those bytes. Piping `initialize` JSON
lines into proxy would not meet the pinned control-socket protocol. This
distinction is established from source, not a failed live experiment.

Agentd can therefore spawn the documented proxy against its own daemon without
starting a listener, but it does not need a proxy subprocess. Existing
[`openCodexHostRPC`](../../../../internal/agentd/codex_host_rpc.go) already dials
the same local socket using WebSocket framing and checks `SO_PEERCRED` against
the expected daemon PID and effective UID. A proxy subprocess would hide that
peer credential from the parent unless additional verification were added.
Reuse the direct existing implementation for the narrow operation; never expose
either transport as a generic RPC tunnel.

The current remote daemon supervisor
[`codex_remote.go`](../../../../internal/agentd/codex_remote.go) launches
`codex remote-control start --json` and retains Codex HOME/enrollment on its
volume. That is the existing daemon to address. No start/status/proxy/RPC was
executed for this investigation. Reading this source does not attest a live
daemon's readiness.

The [official app-server lifecycle](https://learn.chatgpt.com/docs/app-server)
separates initialize/initialized, fresh `thread/start` and `turn/start`.
PR690's pinned schema permits `cwd` and `model`; `projectId` is **optional**, not
absent. The Mac's reported ChatGPT/null-host inventory is not needed to encode
native cwd-based creation. It still cannot supply missing pairing, host/root
authorization, a link or evidence that the new thread appears on the phone.

## Existing operator transport and exact missing operation

| Existing path | Why it cannot perform the requested action |
| --- | --- |
| [`Server.Handler`](../../../../internal/apiserver/server.go) and `createSession` | Creates an AgentSession/host, not a thread on an existing daemon. Reusing its body for provider RPC would change its security contract. |
| [`sendMessage`](../../../../internal/apiserver/podcmd.go) -> `agentd ctl deliver` | Delivers input to an existing conversation. It cannot select a new conversation; peer text is not human authorization. |
| [`nativeControl`](../../../../internal/apiserver/native_control.go) | Human-owned, task-budget-bound Luna executor. Remote enrollment is disabled. It is a different process/ownership boundary. |
| [`agent-run pair`](../../../../internal/agentrun/pair.go) | Uses the external caller's own kubectl rights to run `ctl codex-pair`. It requests pairing, not creation, and session callers cannot borrow those rights. |
| [`ctl`](../../../../cmd/agentd/main.go) switch | No fresh RC-thread subcommand. Rescue, log, pair and decision commands must not be overloaded. There is no generic agentd request socket to reuse. |

The reusable operator-to-target transport is
[`PodExecutor.Run`](../../../../internal/apiserver/podcmd.go) implemented by
[`internal/podexec`](../../../../internal/podexec/podexec.go): the operator's
existing Kubernetes `pods/exec`, WebSocket with SPDY fallback, fixed container
`agent`, bounded stdin/stdout and no shell or TTY. The existing server already
uses it for fixed ctl commands. Its Kubernetes identity, not this worker's,
performs exec. No worker RBAC, token or grant should change. Deployment RBAC
stays in haynes-ops and must be verified against the installed operator before
activation; source precedent alone is not live permission evidence.

There is no safe existing **API operation** for the new behavior. Adding one is
new endpoint/operation exposure even with unchanged authentication. It is
excluded by the current work order. This is the precise approval boundary, not
an absent Codex protocol or a request to grant this worker exec.

## Smallest proposed operation, not implemented

Approval candidate: one default-off
`POST /v1/sessions/{name}/codex-thread` operation, enabled only for an explicitly
configured target AgentSession UID and accepted project/repository. It invokes
one fixed new `agentd ctl codex-thread-start` command. No methods, socket paths,
shell commands, config overrides, provider URLs, host creation or handoff text
are accepted. The first operation creates an empty fresh context on the same
host with existing history retained; initial input and link delivery are separate
unimplemented acceptance requirements.

1. Authenticate/classify with the existing TokenReview and caller resolver.
   Apply the exact predicate from
   [`validate.go`](../../../../internal/apiserver/validate.go): `kindHuman`,
   `kindClient`, or `isWorkbenchSession(c)`. Explicitly refuse ordinary sessions,
   `kindCoordinator` and portal callers before catalog reads, writes or exec.
   `isWorkbenchSession` relies on verified ServiceAccount/pod ownership, never a
   claimed role, model, header or request field. Q-267-01 remains unanswered.
   Reusing this predicate does not silently authorize a new operation: the
   target-specific enablement is part of the approval requested below.
2. Read the exact target uncached: allowlisted UID, Codex/remote, not deleting,
   current owned Running/Ready non-hold Pod. Bind session UID and Pod UID.
   Resolve project/repo from the configured
   [`CatalogBinding`](../../../../internal/apiserver/catalog.go), not a submitted
   snapshot. Restrict model with the existing full-ID validator.
3. Fixed ctl preflight returns only the current process boot ID, PID/start
   ticks, pinned protocol version, installation/environment IDs and the exact
   already-prepared root for that accepted repository. Verify root ownership,
   normalized absolute path, no symlink escape and exact preparation/catalog
   revision. Do not trust a request `cwd`, derive a path from a repository name
   alone, initialize/fetch/repair a worktree, or treat a directory's existence as
   accepted preparation. [`project-snapshot.json` and project wrappers](../../../../internal/agentd/projects.go)
   are existing preparation evidence to qualify, not a universally available
   root witness. If this host has no compatible accepted preparation, refuse.
4. Require separately qualified supported-client pairing/host-selection
   evidence for this exact installation/environment. Connected native status
   and `codex-pair-issued.json` do not prove pairing. No such evidence source is
   established in this pod; default is `pairing_unknown`, zero mutations. Do
   not add a caller-controlled `paired: true` escape hatch. Existing PR691
   mapping may be consumed only under its own explicit trust/freshness contract.
5. Reserve a bounded durable intent before exec; immediately before RPC,
   recheck target/process/catalog/pairing binding and consume the exact intent.
   Pass the fixed binding via bounded stdin, not shell interpolation. Use
   PR690's primitive with `environments: []`, exact accepted root/model and the
   expected PID; never `turn/start`, resume, fork or a second app-server.
6. Persist a bounded receipt. Never retry `thread/start` after reservation,
   transport loss, malformed acknowledgement or uncertain persistence. Keep
   active conversations, process, enrollment and history untouched.

### Request, response and error contract

Proposed external request, maximum 4 KiB, strict JSON with unknown/duplicate
members refused (values below are synthetic):

```json
{"requestId":"rc-intent-example-01","sessionUID":"synthetic-session-uid","podUID":"synthetic-pod-uid","project":"example","repo":"example","model":"gpt-6.1-sol","deadline":"2026-10-30T02:00:00Z"}
```

The deadline is an absolute, short finite bound, not a retry allowance. The
server resolves caller identity, accepted catalog and all native/root facts;
none comes from caller assertions. Missing/expired deadlines, aliases, relative
paths, custom RPC method/params and all cwd overrides are refused. Use an inner
5s RPC bound capped by the remaining operation deadline, maximum 30s overall.

Successful `201` returns request ID/digest, caller attribution, session/Pod and
native host/process binding, accepted catalog revision/repository, fresh thread
ID, exact cwd and **configured** model. `servedModel`, link, handoff and phone
visibility stay unknown/unperformed. Returning a thread ID is not usable RC
acceptance. An identical request may return the stored receipt with `200`,
never execute again. A changed digest with the same caller/request key is `409`.

| HTTP / existing envelope code | Narrow reason and mutation guarantee |
| --- | --- |
| 403 / forbidden | Caller class or configured target scope refused; no exec. |
| 400 or 422 / existing bad-request or invalid code | Malformed/unknown request field, model or deadline refused; no exec. |
| 409 / conflict | Session/Pod/process/catalog/root changed, intent conflict or consumed intent with no confirmed result; no replay. |
| 409 / conflict | `pairing_unknown` or `pairing_required`; no create; never pair automatically. |
| 503 / unavailable | Unsupported protocol, missing preparation/evidence, or bounded preflight unavailable; no create. |
| 503 / unavailable | `create_unconfirmed` after reservation; intent remains consumed, caller must not resend under a new key to repair uncertainty. |

Use fixed reason strings and correlation IDs. Never return provider stderr,
credentials, raw thread data or handshake logs. Preflight transport errors are
not evidence that creation failed. Successful creation followed by a lost
receipt is unknown, not safe to repeat.

### Durable identity, replay and audit scope

No existing task-budget ledger applies to this paired host. The smallest
candidate store is a bounded intent journal on the **target AgentSession's
metadata**, using existing operator update rights and uncached resourceVersion
CAS. This requires annotation/admission/RBAC qualification before activation,
not a claim that a journal already exists or needs a new CRD field.

Key by authenticated principal + request ID + target session UID. Keep two
digests: the canonical original client fields plus authenticated caller, and a
separate frozen dispatch digest adding resolved target/Pod/process/native
identities and catalog revision/repo/root. A retry first compares original input
against the saved client digest; identical input can read the old receipt even
after target state changes, without recomputing or executing the dispatch. An
unconfirmed old receipt stays unconfirmed. Fresh readiness is never inferred
from that historical receipt. Keep
`reserved -> consumed -> confirmed|unconfirmed`; a crash at either of the first
two states must never cause automatic redispatch. The operator reserves by CAS
before its single mutating exec. The target ctl then exclusively creates and
fsyncs a per-intent local journal entry (and its directory) immediately before
dispatch, under a private trusted state directory on the existing host volume.
An existing entry, unreadable journal, fsync error or changed binding refuses
dispatch. The local fence covers duplicate exec delivery, concurrent processes
and target restarts without adding an intent-consumption API or granting the
target writes to operator-owned metadata. Operator reservation survives lost
exec/receipt results. A bare callback is not durable authorization. Neither
layer is implemented by this packet.

Store only bounded digests, identities, fixed reasons, timing and thread receipt;
no prompts, tokens or provider responses. Never silently evict replay fences:
use a finite cap (proposed 16 intents / 16 KiB per target) and refuse when full.
Do not append forever to annotations or add another tracker/framework. Normal
retention must preserve fences for that target UID; re-created same-name hosts
cannot inherit old authorization. Structured audit events record authenticated
caller, request digest, target bindings, phase and outcome. They do not claim
Tom approved an action merely because a coordinator submitted it.

## Exact source work and validation scope

| Paths | Owned change and checks |
| --- | --- |
| New `internal/apiserver/codex_rc_thread.go` and tests; `server.go` | Default-off target-bound operation, unchanged caller predicate, accepted catalog, exact owned Pod checks, durable reserve/receipt and fixed exec. No route while disabled. Test every caller class, disabled configuration, UID swaps, stale catalog, malformed bodies and zero exec on refusal. |
| New `internal/apiserver/apiv1/codex_rc_thread.go` and tests | Strict narrow wire types, canonical digest, bounds and stable error reasons. No arbitrary method/params/cwd. |
| New `cmd/agentd/codex_rc_thread.go` and tests; ctl switch in `main.go` | Fixed preflight/start operations with bounded stdin, target UID fences and explicit activation configuration; never generic proxy. |
| New `internal/agentd/codex_rc_transport.go` and tests | Accepted preparation/process/host revalidation, durable local one-use fence, then PR690 primitive. Synthetic Unix WebSocket fixture only. Fault injection before/after consume/write/ack; no duplicate mutation after uncertain outcomes. |
| Existing `codex_rc_create.go` from PR690 | Reuse unchanged unless native review identifies a concrete defect. Preserve explicit dependency, no cherry-picked replacement to evade its hold. |
| Existing `historyexport_layout.go` if local journal is selected | Classify only the new bounded metadata files, without exporting secrets or altering native history. |

One author, one settled-head native reviewer, serial focused tests at CPU <=2.
No envtest installation or wide test rerun is needed for this documentation
packet. Later implementation needs focused Go tests, meaningful restart/CAS
fixtures, diff review, required hosted CI and independent native QA; a protocol
fixture is not a live provider acceptance test. Do not alter PR691's mapping or
discovery docs. Any required new files outside the listed scope need custody
recorded before edits.

## Approval packet and stop boundary

**Decision requested in OP272/this PR for headless coordination:** authorize
registering/enabling this single new operation for unchanged authenticated
human/client/recognized-workbench callers, restricted to one explicit target
UID/project/repository allowlist, after source review, required CI and supported
pairing/root and durable-intent evidence are qualified. Keep ordinary sessions
and `kindCoordinator` refused. Recommendation: preserve the exposure/activation
hold until those facts and exact target values are available. No actual target
UID, supported pairing witness or prepared root is asserted by this packet.
Source implementation/tests within the current default-off scope are already
authorized; this decision requests only the presently excluded operation
exposure and activation, not another source permission or a worker access grant.

Source-only helper code/tests within existing authority need no new grant.
They cannot make this usable from an operator client without exposing an
operation. Rather than add another disconnected primitive or falsely claim the
existing message/native endpoints provide it, this increment stops at the
reviewable operation decision. Endpoint exposure, runtime use, installation,
host/pod restarts and public listeners remain prohibited. No credentials,
auth files, pairing, Mac UI/database or live daemon RPC were accessed.

Source: investigation/contract only; adapter integration **not implemented**.
Built/released/installed: no. Usable/accepted RC: no. PR690/691 remain separate
source increments under their existing holds. Global Codex usage is unknown;
last supplied value 26%, ceiling 75%; no inference probe or extra worker wave.

Validation: 13 local document links resolved; the three official-source blob
checks and retained start-schema inspection passed; `git diff --check` passed.
Independent native reviewer `review_transport_packet` (`gpt-6.1-sol`, `xhigh`)
found the `--sock` relative-path wording error, which was corrected against
`parse_socket_path`. Final review: no remaining findings. Review covered source
and the proposed contract only; no runtime tests or journal-durability claim.
No Go tests were run for this documentation-only change. Required PR CI and any
available advisory findings remain delivery evidence to record separately.
