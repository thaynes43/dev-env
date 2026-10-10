# Plan 12: owned native host executor source seam

Technical source contract for D-84, #154 and #160. The root-authored
[task budget execution design](../designs/002-task-budget-execution.md) records the
conservative campaign/host scope and remaining owner acceptance. The isolated
implementation is [hostexecutor](../../../../internal/agentd/hostexecutor/README.md).

Production GitOps selection of `owned-codex-host` stays disabled; its inherited-socket helper is
not a public admission route. `Run` accepts one immutable task/epoch/deadline/host/
pod binding through injected `Gate.Admit` and `Gate.Observe`. Lock and one-use
receipt refusal precede off-pod admission. Attempting/Confirmed/Stopping/Stopped
receipts are private durable checkpoints. Confirmed proves owned process launch; a separate NativeReady/version receipt
records authenticated socket initialization when the CLI readiness path succeeds; interrupted and completed receipts never automatically rearm.

The helper owns foreground native `cmd.Wait`, uses a dedicated Linux subreaper,
freezes all threads, validates immutable ancestry/identity, and signals pidfds.
Only native Wait, adopted-descendant ECHILD and successful helper Wait permit
Stopped. Missing permissions, elapsed budgets or uncertain proof retain Stopping.
Finite inert setsid/double-fork and active supervisor SIGTERM tests prove this
candidate boundary while preserving WIP and fixture private-home artifacts.

The CLI now wires the real HTTPS budget Gate and exact five-field binding,
fixed credential projections, access-only adoption, and mandatory pinned-version
Unix socket readiness. The production independent inspector, complete native
effort accounting, retained-home recovery provenance, actual native lifecycle,
normal recovery and native owner notification remain separate gates. This source-only unit performs no provider startup, model turn, pairing,
credential ownership change, cluster deployment or v1 cutover.
