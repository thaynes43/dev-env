# ADR-012: Approve a bounded maintenance task, let its agents choose commands

Status: accepted for DESIGN/source preparation only, 2026-10-30. Live grants,
adapters, policies, release and activation remain held under the lead coordinator.
Scope: OP233 and OP219. Does not replace their canonical records or open source PRs.

## Dated owner ruling

Tom's 2026-10-30 16:42:08 direction permits agents to choose commands inside one
bounded approved task. OP304 automated approval review is backlog only. After the
assistant disclosed broad SYS_ADMIN authority inside the container and residual
selected-node/driver risk, Tom approved DESIGN/source preparation at16:52:10,
explicitly excluding live policies/grants. At16:53:33 he directed a pragmatic
existing grant/broker path: one task approval, command freedom, audit, expiry,
rollback and critical-access protection. No command-by-command gates or extra
approval layer. These are the trusted directions supplied to this worker; this
ADR creates no additional runtime authorization.

## Decision

Extend the existing immutable AccessGrant rather than create a task database or
new role/orchestration framework. A maintenance envelope pins the authenticated
requester/coordinator and explicitly allowed direct-child session/pod UIDs, node
UID, resource identity and operation intent, capabilities, selected devices and
read-only inputs, digest image, CPU/memory and TTL. Default TTL is30m, at most1h
per request. An extension is a new explicit request/approval, never in-place TTL
mutation. Control-plane/master-labelled or tainted nodes are refused even with
spec.nodeName; no taint bypass may put this executor on a control-plane node.

The existing TokenReview-authenticated human API identity supplies the exact
UID/digest decision. A protected set-once annotation hands it to broker.Decide.
This is independent of unverified agent coordinator:string attestation. The broker
human identity configuration must match the operator's existing human identity.
Asks stays held, and existing credential grant semantics do not change. The lease
starts when broker.Decide records approval, not when the API queues the decision.

A broker-owned, tokenless isolated executor holds the grant's capabilities.
Explicit participants use one gated proxy and the same remaining lease; each
command rechecks authoritative session/parent/pod/node identities. Sessions receive
no pod-creation or executor-exec RBAC. Commands remain agent chosen. Deny-all
network and actor/shape/exec admission are mandatory held policy prerequisites.
Release/expiry immediately refuses new API execution, requests stop, and retains
unknown cleanup until container termination plus live pinned node/lease evidence.
Persistent effects require separately authorized restore/readback, never inferred
from expiry. Disabling source flags does not disable cleanup.

## Consequences and evidence

SYS_ADMIN is broadly administrative. Operation/resource labels cannot confine
semantic intent in the kernel; shared driver control devices can affect other
resources on the selected node. Device cgroup/runtime usability remains unqualified.
There is no claim of hardware containment or guaranteed termination on an
unreachable node. The policy patch is UNAPPLIED, separately owned by the lead.

[Design and review packet](../verification/bounded-maintenance-2026-10-30/README.md)
records exact source/manifest bases, threat boundary, routes, lifecycle, rollback,
overlap and validation. PR597/565/563 are preserved and later integration belongs
to the lead. Source tests and review cannot be reported as installed or accepted.
