# Task budget ledger and API protocol

Implementation appendix to [002 — owned task-budget execution](002-task-budget-execution.md)
and the accepted [task-budget requirement](../requirements/2026-10-09-task-budgets.md).
This source unit is an inactive integration seam. It does not enable a native host,
certify native child coverage, or establish deployed enforcement.

## Bounds and retained authority

The accepted policy remains 60 minutes without qualified evidenced progress or
three failed attempts against the same blocker. Heartbeats, tool counts, agent
assertions and activity are not progress. The first API pilot imposes additional,
stricter ceilings: 45 minutes overall, 60 minutes summed worker effort, and a
checkpoint at most 10 minutes away. These are finite implementation bounds within
the accepted requirement, not a replacement owner policy. Without independently
validated progress, the 10-minute checkpoint stops the campaign before its
45-minute overall ceiling. Provider token and cost accounting is unavailable.

One logical campaign is assigned externally to one host. The API cannot replace
that assignment, delete the ledger, reset history, extend its own deadline or
automatically rearm a stopped host. `TaskUID`, epoch, deadline, HostID and exact
live owning Pod UID fence every admission. Pod replacement does not reset budgets.

`internal/taskbudget.KubeStore` retains one ConfigMap named
`task-budget-<first 20 bytes of SHA256(TaskUID), hex>`, with label
`dev-env.haynesops.com/task-budget: retained-v1` and data key `ledger.json`.
There are no owner references or delete route. Live reads and resourceVersion
compare-and-swap retain worker, event, blocker, escalation and extension history.
Conflicts retry at most five times. A write the API server accepts is confirmed
from its own response: this writer's exact data at a new resourceVersion. A later
write by another replica supersedes it; it does not make it unknown. Only a write
whose result is unknown (a timeout or a lost response) is read back. Exactly this
writer's data at a new resourceVersion confirms it; anything else stays
unavailable, and the write is never blindly replayed. A call that changes nothing
durable except elapsed-time accounting writes nothing; the next write charges
that time, so frequent observation does not add write contention.

Each replica stamps `AccountedAt` from its own wall clock. A replica whose clock
trails the stored `AccountedAt` by at most five seconds uses the stored time and
charges nothing for that gap. A larger backward step is unavailable.

History is bounded at 512 retained events, 64 workers and 32 extensions;
exhaustion stops admission instead of dropping prior evidence. Heartbeat and
activity events are observations: a bound caller may send them, but they are
never retained, so they cannot fill the history. Each worker has at most one stop
receipt, and stop receipts sit outside the 512 bound (at most 64), so a full
history never refuses one.

The ledger persists a limit latch before a stop request. A request to stop is not
proof that execution stopped. A missing Pod or heartbeat is not proof either.
Every known worker needs an independently validated termination receipt.

## Server configuration and protected storage

The current program entrypoint supplies no budget service, assigned campaign or
production validator. `Server.TaskBudgets == nil` leaves the routes unavailable.
There is no default-on flag in this unit. Integration must supply:

```go
Server.TaskBudgets *taskbudget.Service
Server.AssignedTaskBudgets map[string]string // HostID -> assigned TaskUID

taskbudget.Service {
    Store     taskbudget.Store
    Validator taskbudget.Validator
    Now       func() time.Time
}
taskbudget.KubeStore {
    Client    client.Client
    Live      client.Reader
    Namespace string
}
```

Store the ConfigMaps in a dedicated budget namespace with no runtime agent write
authority. An operator Role needs ConfigMap `get`, `create` and `update` there;
this implementation needs neither list nor delete. Kubernetes RBAC cannot restrict
dynamic names by prefix. A shared namespace therefore requires a separate admission
guard checking the budget object prefix and server ownership, rather than a claimed
prefix-scoped Role. Deploy manifests and permissions belong in haynes-ops GitOps.

## Wire contract and routes

Types live in `internal/apiserver/apiv1/taskbudget.go`. A binding has JSON fields
`taskUID` (string), `epoch` (uint64), `deadline` (RFC3339 time), `hostID` (string),
and `podUID` (string). An API request cannot supply a later deadline or alternate
Pod identity. The server derives coordinator authority from existing authentication,
configured host policy, assigned campaign and its exact live owning Pod.

| Method and route | Body and authority |
| --- | --- |
| `POST /v1/task-budgets` | `{taskUID,spec:{successCondition,overallSeconds,effortSeconds,checkpointSeconds}}`; only its configured live coordinator may create its assigned campaign. |
| `GET /v1/task-budgets/{uid}` | No body; bound caller reads exact binding and status. Observing persists any due latch and never grants admission. |
| `POST /v1/task-budgets/{uid}/admit` | `{binding}`; only the exact configured coordinator may admit its host worker. |
| `POST /v1/task-budgets/{uid}/observe` | `{binding}`; a bound caller observes the same retained authority. |
| `POST /v1/task-budgets/{uid}/events` | `{binding,id,kind,workerID?,attemptID?,blockerID?,evidence:{id,kind,reference}}`; only a bound executor, with independent validation for authoritative evidence. Caller-supplied worker starts are denied. |
| `POST /v1/task-budgets/{uid}/extend` | `{binding,decisionID,nextStep,overallSeconds,effortSeconds,checkpointSeconds,evidence}`; authenticated human plus independent durable owner-decision verification. Agent and trusted-client self extension is denied. |
| `POST /v1/task-budgets/{uid}/native` | `{binding,id,operation,threadID?,turnID?,text?}`; existing authenticated human only, one explicitly configured owned root. Supported operations are `thread/start`, `thread/resume`, `thread/read`, `turn/start`, `turn/interrupt`, `host/stop`. |
| `POST /v1/task-budgets/{uid}/native-consume` | Same exact request; original bound coordinator only consumes a previously reserved human mutation once. Response `{binding,id,requestSHA256,consumed:true}` is required before native dispatch. |
| `POST /v1/task-budgets/{uid}/continue` | `{binding,decisionID}`; existing authenticated human only, independently proven original guard stop and explicit policy opt-in, once within original budgets. |

Extension is refused while proof or an adapter is missing. It requires the exact
current task and epoch, a delivered escalation, all worker stops confirmed, and a
bounded next step. It preserves initial policy, all effort, events, failed attempts
and escalation history. An explicit new epoch cannot erase the previous blocker
counts. There is no native owner-decision adapter in this source unit.

Status includes `observed`, `admitted`, exact `binding`, `latched`, `reason`,
`effortMilliseconds`, `usageAccounting`, `lastProgress`, `nextCheckpoint`,
`escalation`, `stopConfirmed`, blocker counts and last evidence. HTTP success alone
does not admit work: the client checks positive exact binding confirmation and
`observed`; Admit additionally requires `admitted` and an unlatched ledger.

Optional `nativeStopRequested:true` is derived from the current epoch's retained
owner stop intent. It requests termination; it neither means `latched` nor
`stopConfirmed` and does not free a worker. Original budget accounting still
runs during stopping. A later independently validated stop event records the
existing `NativeRootStopped` latch; only then may explicit owner continuation
advance one epoch. Prior stop-request events remain in history.

The host gate client is:

```go
apiv1.HTTPTaskBudgetGate {
    BaseURL string                    // HTTPS only
    Client  *http.Client              // trusted CA, bounded timeout
    Token   func(context.Context) (string, error)
}
Admit(context.Context, apiv1.TaskBudgetBinding) error
Observe(context.Context, apiv1.TaskBudgetBinding) (latched bool, err error)
```

Use the projected token for the existing `dev-env-operator` audience. Redirects
are refused. Observe uncertainty returns latched plus an error. The owned native
executor's local Binding has the same five fields; the integration adapter must
map them without changing epoch, deadline or scope. Nil gates refuse launch.

## Managed child binding and accounting

Only direct managed children of the assigned host are supported by this unit;
bound nested dispatch refuses. New children reserve a stable worker identity
before Session creation. One dispatch UUID, or caller-scoped hash of its existing
idempotency key, spans all generated-name collisions. Successful retries therefore
charge one worker. A server-owned attempt nonce makes concurrent requests for
the same idempotency key conflict in CAS. A finished bound request returns its
original Session; a retained reservation without a Session refuses retry.
Unknown creation remains charged until independent proof; an
exhausted naming attempt also retains its conservative reservation. No rollback
infers non-launch from object absence or races an idempotent concurrent request.

A child whose one Pod create was never reserved is different: the ledger itself
shows `launchRequested` false and no Pod UID, and every managed Pod create is
reserved there first. Once its session is suspended or being deleted, has no
recorded Pod name, and an uncached read finds no Pod, the controller records a
`worker-stop` receipt with evidence kind `managed-never-launched`, and the worker
stops being charged. The ledger compare-and-swap orders that receipt against
`ReserveManagedLaunch`: whichever commits first wins, so a closed worker never
launches and a reserved launch is never closed this way. Suspending or deleting
an unlaunched budget child is therefore final: resume and continuation require an
active worker and are refused, as they are for a launched child after its stop
receipt. A reservation whose Session was never observed stays charged, as above.

The server writes these AgentSession annotations:

| Annotation suffix under `dev-env.haynesops.com/` | Value |
| --- | --- |
| `task-budget-uid` | Logical TaskUID |
| `task-budget-epoch` | Decimal epoch |
| `task-budget-deadline` | RFC3339Nano immutable deadline |
| `task-budget-host` | HostID |
| `task-budget-pod` | Owning coordinator PodUID |
| `task-budget-worker` | Stable child dispatch reservation ID |

API create, resume and continuation check the retained latch and owning binding.
The current controller does not independently stop a running child or recheck a
delayed Pod creation. Native model/tool attempts and all native child cardinality
are not instrumented by this ledger. Heartbeat timing and AgentSession Outcome
alone cannot substitute for the missing execution receipts. Native and controller
backstops must be integrated and proved before opting in.

## Independent evidence and notification state

```go
type Validator interface {
    Validate(context.Context, *taskbudget.Ledger, apiv1.TaskBudgetEvent) error
    ValidateOwnerDecision(context.Context, *taskbudget.Ledger,
        apiv1.TaskBudgetExtension, authenticatedOwner string) error
}
```

An adapter must verify independently observed resource or execution receipts
against task, epoch, exact owning Pod and worker identity. A non-nil callback that
trusts caller fields is insufficient. Failure receipts bind stable attempt IDs
and canonical blocker IDs across children, techniques and resumes; caller labels
cannot establish a fresh bucket. Qualified progress needs actual bound success
evidence. Worker stop needs checked supervisor and process-tree termination.

Both notification-delivered and notification-failed require independently verified
native delivery results. One pending escalation remains sticky; a failed delivery
does not create another question or clear the latch. A subsequently verified
delivery can recover that same escalation from Failed to Delivered, preserving
the failed-delivery event, so an agent cannot irrevocably prevent owner control.
No native notification adapter, owner adapter, universal failure classifier or
complete native aggregate-effort proof ships with this source unit. Unknown
authority must refuse admission and remain stopped.

## Independent native admission and managed stop backstop

The next source unit separates receipt validation from native readiness.
`NativeAdmissionInspector.InspectNativeAdmission(context.Context, *Ledger,
Binding) error` must inspect the exact retained task/epoch, frozen private native
home and finite campaign, immutable deadline, owned stop and readiness. Precise
native internal usage may remain unknown; elapsed host time plus reserved managed
child elapsed time is an estimate and still enforces time and attempt limits.
Admit calls it before charging a host worker; native Observe calls it again while
unlatched. A non-nil Validator is insufficient. Nil inspector or inspection error
refuses work. A restricted model-free fixture inspector is a separate source unit; this unit
keeps production native admission closed. Native model/tool failure observations
and real owner notification remain acceptance gaps. Unavailable precise internal
usage does not disable elapsed-time or independently observed attempt limits.

Existing `agentd ctl status` reads private state files in a newly exec'd process;
it cannot provide the original run-agent's in-memory `cmd.Wait` result. The private
invocation file also shares the agent's Unix identity. Neither is independent
budget evidence. The existing whole-container stop verifier instead reads the
exact retained Pod UID, every admitted container's termination, its healthy Node
UID and a fresh owned node Lease through the uncached APIReader. A missing Pod or
unknown node remains uncertain. Its stop-and-preserve flow retains the old executor
and writer until a separate bounded hold rescue succeeds.

The managed controller budget backstop checks the ledger before delayed Pod
creation, periodically while running, and before cleanup. A latched or unavailable
budget requests the existing suspended stop-and-preserve transition; uncertainty
retains the resources. Stop accounting requires fresh whole-container proof plus
the exact independently recorded successful rescue. Nonzero whole-container exit
can conservatively count one stable attempt against a server-assigned task/epoch
blocker. Native model/tool failures inside a live container remain unsupported;
there is no synthetic progress or owner-extension validator.


The operator wiring is explicitly off by default. Enabling `--enable-task-budgets`
requires `--task-budget-namespace`, a dedicated namespace distinct from sessions
and templates, `--task-budget-worker-image` pinned to the reviewed digest, and
`--assigned-task-budgets` mapping only configured coordinator HostIDs to TaskUIDs.
The namespace Role permits only ConfigMap get/create/update for the operator;
agents, coordinators and clients receive no write/delete access. The controller
uses its existing uncached Pod, Node and node-Lease reads. Missing permission
retains the executor and writer rather than bypassing stop proof. No RBAC grants
or runtime activation are made by this source unit.

Managed admission checks exact server-assigned SessionUID, root binding, shared
workspace and live approved template image before reserving one Pod create. An
unknown create remains reserved and charged; absence cannot authorize replay.
The immutable Pod environment `AGENTD_TASK_BUDGET` carries version 1, TaskUID,
epoch, finite deadline, HostID, root PodUID and SessionUID. Agentd validates it
against the session and downward Pod identity before boot side effects, then arms
a non-resettable hard exit. The controller conservatively selects the earlier
of the overall deadline and first checkpoint, because no progress adapter is
available. Kubernetes ActiveDeadlineSeconds and a five-second termination grace
provide a separate finite backstop; late startup still checks the absolute
deadline before any native work. Budget-bound executors never restart, and budget
hold rescue Pods explicitly receive an empty timer and opt out of k8tz injection.
A stop intent, deadline exit or missing Pod is never a confirmed stop receipt.

## Campaign CLI entry point — 2026-10-18 source increment

`agent-run budget create TASK_UID --success "observable result"` sends one
POST through the existing authenticated API. Defaults are 45 minutes overall,
60 minutes summed effort and a 10-minute checkpoint; `--overall`, `--effort`
and `--checkpoint` accept positive whole-second durations within those ceilings.
The checkpoint cannot exceed the overall limit. The server must already assign
this campaign to the caller's exact live coordinator host/Pod.

`agent-run budget show TASK_UID [-o json]` reads retained identity, epoch,
deadlines, latch, stop confirmation and elapsed-effort accounting. Reading may
persist a due latch. Neither command admits or launches an executor, resets a
campaign, extends limits or clears a latch. Repeating create conflicts; it does
not silently retrieve or replace the original campaign. Use show to inspect it.

Both commands refuse redirects and malformed or mismatched receipts. Create
requires the initial epoch and exact requested deadline/checkpoint intervals.
An uncertain response exits 6 without replaying the mutation; inspect retained
state before considering any follow-up. Existing connection flags apply.

This supplies the missing CLI bootstrap/read step for an assigned owned host.
General private dispatch still requires reservation-before-create, independent
whole-executor stop proof and the private lifecycle adapter. No private-admission
flag, native readiness validator, pairing, credential or runtime activation is
added. CLI tests use a local TLS fixture and no provider turns. Source delivery
is separate from the held task release and GitOps installation.

## Private finite task adapter — bounded source contract

The private adapter must cover dispatch, pre-create reservation and stop accounting
together. It cannot reuse the post-create canary-directory witness as launch
permission. Ordinary private tasks retain the existing common-rule snapshot against
their accepted project snapshot; the CRD must permit that server-owned finite-task
binding before the API exposes this route. No canary/pilot restriction is relaxed.

The API checks the complete retained-common session document, including the full
UID bound and longest name-collision candidate, before reserving child effort.
A deterministic transport refusal and its idempotent retry create neither a child
nor a charge. After reservation it re-confirms source identity; uncertainty there
retains the reservation rather than guessing an unknown dispatch did not happen.

Before one Pod create, the controller confirms the original protected HOME and
private executor intent, exact active ledger worker/Session UID, unchanged retained
common inputs and approved current template image. It then reserves the budget
launch in CAS. Unknown Create remains charged and cannot rearm either latch.

A private finite task has both the campaign deadline and the earlier immutable local
checkpoint deadline. Agentd observes the existing authenticated budget gate before
boot and periodically; a latch or uncertain observation exits its container while
the hard timer remains independent. One failed observation (including the two-second
request timeout) intentionally ends this finite child; there is no retry/grace window
or automatic relaunch. An API rollout/outage therefore sacrifices that child's
availability while preserving its HOME and Pod. The timer alone cannot reveal an
earlier effort, repeated-failure or withdrawn-authority latch. This policy applies
only to the assigned finite campaign child, not retained native/TUI or unbudgeted
sessions. This is finite executor self-stop, not a new
private Pod patch, suspend-as-stop proof or reap. ADR-006's Pod patch/reap/hold rules
and HOME retention remain intact. Shared executors retain their existing behavior.

The controller accounts a private stop only from exact retained executor/HOME
identity plus all admitted containers terminated on an independently healthy Node
with a fresh owned Lease. It retains the original Pod and HOME; no shared rescue
receipt is substituted and no removal/replacement follows a budget stop. Missing
or replaced resources remain uncertain and charged. A stopped bound executor
cannot resume, receive new work or create a replacement. Native root readiness,
phone pairing, notification/progress authority and installed acceptance remain
separate gaps. No live flag, deployment, credential, access or archive action is
part of this source adapter.

## Owned Codex root: independent PID1 evidence (source phase2)

The ordinary owned-root profile is separate from the zero-task fixture and is
explicit-off unless an operator-owned policy is configured. It runs the pinned
agentd owned supervisor directly as PID1, nonroot, with immutable image/rootfs and
all capabilities dropped. Supervisor and helper become nondumpable before native work. The fixed
probe also becomes nondumpable and requires preexisting kernel/pipe isolation
through the exec-to-main interval described below. A probe authenticates the ORIGINAL kernel PID1
by Unix peer credentials, boot/start identity and pidfd liveness; a pathname,
nonce, shared UID, executable name or private receipt alone is not authority.

After the original one-use receipt lock, a bounded read-only Unix server exposes
supervisor MEMORY, bound to task/epoch/deadline/host/Pod and the independently
assigned HOME UID/frozen Codex baseline. Operator policy—not a worker request—
authorizes that baseline. The guard hashes bounded regular native-home inputs
before Admit, excluding only rotating access auth.json; aliases, special files,
unknown peers and changed files refuse. Policy/PVC/Pod/container/image are read
uncached and confirmed around the pinned protected probe through existing
operator PodExec. No new live credentials or RBAC are installed.

Prepared permits first admission without circular native readiness. Starting is
explicitly not child/continuation authority; Ready requires original native peer
and pinned RPC readiness. Genuine uncertain evidence latches/stops. The guarded
one-use budget bootstrap creates the assigned finite campaign once and freezes
its confirmed server binding; unknown mutation acknowledgements never replay or
rearm. The initial bootstrap never resets a prior runtime budget or attempt. The explicit within-budget continuation below advances its retained epoch without resetting counters.

A native stop event is accepted only after the independently probed ORIGINAL
supervisor reports original native Wait, helper Wait and ECHILD in memory. The
supervisor stays alive for that stop-event acknowledgement. By default it then
exits. With policy `allowContinuation: true`, it may remain in that proven-stopped
state until the ORIGINAL deadline while awaiting one explicit trusted-human
`POST /v1/task-budgets/{uid}/continue` (`agent-run budget continue --decision ID
TASK`). Coordinator/model callers are refused. The request includes the exact old
binding and decision ID. All original native/child stops must be confirmed, the
latch must be NativeRootStopped at epoch1, and the unchanged deadline, effort,
checkpoint, stall and failure limits must still permit work. The service records
the prior binding/owner/decision and advances epoch1 to2 exactly once; it retains
all old counters, workers, events, stop receipts, intent and limits. It grants no
extra quota and cannot resume an exhausted or unknown-stop campaign. The existing
Extend decision/provenance contract remains unchanged.

Continuation requires policy-bound nonsecret HOME inputs (`homeInputsName`,
`homeInputsUID`, `homeInputsSHA`) with exactly input.json and requirements.toml.
The Pod mounts its RWOP PVC at /workspace and creates UID1000-private HOME
/workspace/home with CODEX_HOME beneath it. Before baseline/intent, the existing
common-rules composer prepares Codex AGENTS.md and config; the admin floor mounts
read-only as /etc/codex/requirements.toml. Conflicts/aliases are refused. Credentials
and history are never preparation inputs. Native instruction loading is separate
acceptance from these bytes.

The ORIGINAL guard read-only checks frozen startup files before proving
Continuable (dedicated authenticated CRI status44). Drift still permits Stopped43
but refuses continuation before mutation. Inputs are checked again before the
next launch. Original policy Baseline remains unchanged; StartupBaseline measures
the actual retained HOME for each native-process epoch. The old evidence listener
closes before the next run; epoch2 uses a separate one-use receipt. Safe failed
GETs may be retried at one-second pace while the guard stays stopped; no unknown
mutation is retried and only an explicit valid next binding permits launch.
This supports native-process continuation only with the original living guard,
Pod, HOME and budget. Container/Pod-loss recovery, automatic exhausted campaign
recovery and full native conversation recovery parity are outside this source.
Missing/unknown proof stays charged. No root receipt, vanished PID/group/socket,
heartbeat or host-supplied ready boolean substitutes for this proof. Private
child admission still requires a fresh Ready root; its original HOME/Pod stop
proofs are unchanged. Source tests use inert native transport/processes and TLS/
isolated Kubernetes fixtures. Live route/policy/pins/enrollment/pairing, real native
recovery and authenticated phone acceptance remain separate held actions.

The root source profile additionally fails closed unless the host kernel enforces
Yama ptrace_scope 1, 2, or 3. PID1 checks before any native launch, and the fixed
probe checks again. This closes the fresh exec-to-main dumpability interval:
worker processes have no CAP_SYS_PTRACE and are not ancestors of the exec probe.
No sysctl is changed. Deployment must verify trusted exec-client ownership;
worker-created/helper images, sidecars, hostPID and privilege additions refuse.
The assigned HOME must be a Bound ReadWriteOncePod claim with exact UID; RWO alone
is insufficient. The baseline uses no-symlink directory-FD traversal and stable
identity/content checks excluding access time, including alias checks for the
otherwise excluded rotating auth document. Kernel support and effective storage/
network isolation remain installation acceptance, not inferred from these tests.
Bootstrap conflicts always refuse: no GET-based never-admitted inference or rearm.
An independently validated original-root stop atomically latches the campaign,
while each child stays charged until its own independent stop evidence arrives.

### CRI correction, 2026-10-18

Yama does not restrict proc FD reads in the exec-to-main interval. The former
UID0-owned0600 FIFO requirement refused ordinary UID1000 CRI output pipes.
The corrected pinned probe discards output authority rather than allowlisting
UID1000: it validates original PID1/native evidence plus operator binding/HOME/
baseline and every phase predicate locally. It returns only dedicated nonzero
process statuses40..44 through CRI's independent status channel. Operator stdout
and stderr are discarded. The actual pinned client-go decoder treats absent or
empty status as nil and truncated/malformed status as a generic error; both
refuse, as do generic exit0, signals, transport errors and cancellation. Only an
explicit typed process-exit result with a dedicated status can authorize a phase.
A same-UID preopened output FD can forge bytes or delay EOF, never select a normal
exit status. Yama/nondumpability, immutable command/image, original kernel peer/
pidfd/native RPC and trusted exec ownership remain intact. No root wrapper,
capability, sysctl, execute-only image, key or new credential is introduced.
Model-free wire/proc-FD fixtures establish the corrected seam; real installed
CRI/native acceptance remains distinct. Earlier source-unit no-deploy language
does not create a new permanent owner hold.

Bootstrap preflights HOME UID/baseline before spending the intent or creating a
ledger, then the original guard measures again before admission. The returned
server deadline is never changed; a fixed five-second client-side sanity allowance
handles bounded operator/node clock differences. Larger/unknown deadlines refuse.
The evidence server permits at most four concurrent bounded connections, so one
silent local peer cannot hold the only observer slot. Saturation refuses rather
than creating an unbounded queue. This is not availability against a hostile
same-UID worker: socket unlink or saturation can still stop the campaign. Unknown
proof latches and keeps accounting/resources.

**Diagnostic correction, 2026-10-19:** accepted proof remains exactly statuses
40..44. The immutable probe additionally returns rejected diagnostic-only codes
50..64 for its existing refusal stages; these codes never authorize a phase or
relax any predicate. Unknown errors still exit1. The operator and inspector log
only fixed stage/category fields, error type, context completion and explicit
process-exit metadata. Remote error text, stdout/stderr, challenges, policy
contents and credentials remain excluded from logs and the external CRI phase
status result. The fresh challenge is still verified inside the probe. Original failed
attempts retain their bindings, counters and evidence; diagnostics do not rearm
actors or reset budgets.

**Admission deadline correction, 2026-10-19:** the guard's HTTP response-header
limit now matches its existing two-second client/admission budget. The previous
one-second header cutoff could terminate the original guard while the operator
was still running the allowed one-second proof plus policy/identity reads.
An inert TLS regression reproduces premature refusal of a valid response inside
the original two-second budget; caller cancellation and deadline expiry still
refuse without retry. Dial/TLS limits, the one-second CRI proof, 800ms local probe,
all accepted statuses and original campaign/stop authority are unchanged.
Diagnostics log caller and probe context states separately. When both have ended,
they do not infer which caused the refusal. A missing exit status is never proof.
The failed 003 ordering is consistent with this mismatch, but live correction remains unearned
until a fresh bounded acceptance succeeds; no old actor may be rearmed.

### Owned-root native owner control

The existing owned-root policy selects the native control adapter; it is absent
with current default/fixture configuration. Human control uses an exact original
binding and explicit ID (at most128 bytes), with only thread/turn IDs and at most
8192 bytes of turn text as applicable. No arbitrary RPC, path, provider/model,
approval, sandbox or credential override is exposed. Start/resume/turn requests
pin `gpt-6-luna`; `thread/read` uses native RPC with turns, not private native
JSONL/SQLite/auth stores. The one-shot CLI accepts JSON on stdin:
`agent-run budget native TASK --once < request.json`.

Before any mutation the human route commits an owner intent in the existing
ledger. The fixed immutable exec argv binds a canonical SHA256; stdin must match
it. After original guard/native peer checks and passive protocol initialization,
the helper independently consumes the exact binding/ID/operation/hash through
the original coordinator's HTTPS authority. Root/model callers cannot reserve
human intents. Unreserved, changed, consumed or ambiguously acknowledged payloads
run no native RPC; a racing copy of an approved payload can consume only that
same one-use authority. Neither client nor helper retries lost mutation ACKs.

Audit records are server-appended legacy-valid inert `activity` events with
private intent/consume evidence kinds and bounded hash-derived event IDs. Public
activity requests only observe and cannot append forged records. Existing event
limits remain, with capacity checked before both intent and consume. Old
serializers retain these fields; old operators do not understand the new stop
flag, so this is not a mixed-version stop-enforcement acceptance claim.

`host/stop` commits intent only. Admission, worker start, managed launch and
owner mutations refuse while it is present. The original guard observes it and
stops its owned native tree. `ObserveNative` still accounts/evaluates original
limits but does not turn expected Stopping readiness loss into a different
irreversible failure latch. Independent original Wait/ECHILD/helper and CRI stop
validation remains mandatory before the existing once-continuation. Original
receipts/counters, HOME and deadlines remain; exhaustion, unknown stop and Pod
loss do not recover automatically. `turn/interrupt` stops a native turn and is
not whole-tree termination.

CRI response/history bytes are untrusted content, including against preopened
same-UID FD forgery, and never become budget, stop, instruction-loading or
served-model authority. Output errors remain sticky even if client-go drains
past a valid JSON prefix. A native turn ACK means accepted, not completed;
supported `thread/read` observes completion/history. Actual pinned0.160.1 with
one scripted loopback Responses exchange establishes turn survival after client
WebSocket disconnect, later completed-history read and same-thread resume. It
uses no real model/provider/auth or shared owner daemon. Live owned-root/phone/
active-upgrade acceptance needs matching published operator and task images;
installed2.30 predates this helper. New root wiring is separately authorized,
not implied by source publication. Existing effective ConfigMap GET rights suffice.

The guarded helper intentionally launches local `app-server --managed-daemon
--listen unix://...` **without `--remote-control`**. In pinned0.160.1 that flag
selects enabled-ephemeral remote control, whose startup can automatically enroll
a new backend computer using access-only ChatGPT auth; pairing-code creation is
a separate operation. Fresh root approval covers local owner RPC, not new remote
enrollment. No native enrollment is copied, created or reset by this workflow;
phone/native remote enrollment requires its own explicit supported authorization.


### Complete owned evidence request frame — 2026-10-19 (Story110)

Owned004 refused admission with authenticated CRI exit61 (`response`) in166ms,
all caller/probe cancellation/deadline flags false, before native inference.
A real guard-to-separate-probe process regression reproduces the reset. The
guard decoded256 JSON bytes while one encoder newline remained in the socket;
it sent711 response bytes, then closed with unread request data. Linux reset
the stream, and the probe correctly refused the read error. Earlier lifecycle
tests invoked the probe inside the guard process and missed this boundary.

The fixed single-request protocol half-closes the probe's write side after its
JSON request. The guard consumes exactly one complete request through EOF,
within the original4096-byte/500ms server bounds, before replying. Extra JSON,
trailing junk and excess bytes refuse. The probe still reads through response
EOF and refuses every read error, including a complete-looking prefix followed
by reset. No error is dropped, no timeout raised, and accepted evidence remains
only authenticated CRI exits40–44 after all original identity/binding/nonce,
PID1/peer/pidfd, native, immutable command, budget and stop checks. Paired guard
and probe come from the same pinned task binary; this does not claim mixed
framing-version support.

Fixed refusal-only codes65–68 distinguish response timeout/reset/read/oversize.
The original guard reports fixed request/guard/stability/encode/capacity stages
through one bounded nonblocking diagnostic queue per epoch. Reporting is best
effort; a blocked sink cannot hold sockets or server shutdown, and epoch2 keeps
the reporter. No raw error, peer contents, challenge, policy or credential is
logged or promoted to authority. The final queued close reason is drained by
the independent diagnostic consumer if shutdown wins; proof never waits on it.

The actual lifecycle test now launches a separate protected probe executable
and requires an explicit40–44 process exit before checking diagnostic output.
It uses production-shaped IDs and nanosecond UTC deadlines; hosted CI runs the
same sequence as nonroot PID1, while local non-PID1 checks explicitly make no
PID1 acceptance claim. Real socket tests cover segmented encoder newline,
trailing JSON/junk, request/response byte limits, EOF/truncation, partial/reset,
finite read timeout and blocked reporting. An additional retained local-only
receipt prepared004's actual frozen public input bytes with the production HOME
preparation function and matched its original baseline/UID before explicit40.
No model, provider, credential or cluster actor was used in these reproductions.

Original004 Pod/HOME/ledger/deadline and counters remain retained; its terminal
Pod and caller STOP_UNCONFIRMED are not native-stop proof. Same original sole
owner, Saga85/Epic107/Story110 and all prior elapsed time/holds remain. Fresh005
is separately authorized only after matching signed publication/install,
required CI/review and one original finite caller window. Runtime fix, hidden
instruction response, stop/continue/history, phone and active-update acceptance
remain unearned until observed; no rearm, broader admission or authority change.
