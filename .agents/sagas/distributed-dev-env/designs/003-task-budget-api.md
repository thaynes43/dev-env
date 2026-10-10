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
Conflicts retry at most five times. Unknown writes require exact readback; they
are never blindly replayed. History is bounded at 512 events, 64 workers and
32 extensions; exhaustion stops admission instead of dropping prior evidence.

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
