# OP922 mutation evidence

2026-11-18; worker branch `agent/dev-env-1118-201802`.

Each mutation below was applied alone, then its named acceptance test was run
under `GOMAXPROCS=2 nice -n 19 go test -p 2 -timeout 15m <package> -run <test> -count=1`.
KUBECONFIG and every KUBERNETES_* variable were removed from the test process.
All 22 mutations compiled and failed the named test. Every edited file was
restored byte for byte in a finally block before the next mutation.

| Mutation | Decision removed | Test that failed |
| --- | --- | --- |
| M01 | Skip a full host | `TestHostMemoryCapacity` |
| M02 | Reserve 1 GiB | `TestMemoryObservationBounds/absolute_reserve` |
| M03 | Reserve 20 percent | `TestMemoryObservationBounds/percentage_reserve` |
| M04 | Bound sample age to three minutes | `TestMemoryHeartbeatAndLegacyViews` |
| M05 | Permit a second slot only on full/fresh intent | `TestHostMemoryCapacity/full_leads_to_create` |
| M06 | Fresh only for human/trusted client | `TestFreshHostIntent` |
| M07 | Fixed two-host bound | `TestFreshHostIntent|TestHostMemoryCapacity/second_full_host` |
| M08 | Repeat selection at API create | `TestCapacityAPISelectAndCreate` |
| M09 | API bound survives selection being off | `TestCapacityBoundWithSelectionOff` |
| M10 | Count pending creates | `TestPendingCreateCountsAcrossProfiles` |
| M11 | Lost CAS does not admit another replica | `TestCapacityReservationAcrossReplicas` |
| M12 | Keep an uncertain create reservation | `TestCapacityUnknownCreateRetainsReservation` |
| M13 | Release only the same attempt | `TestCapacityReservationAcrossReplicas` |
| M14 | Omit false request keys for old clients | `TestFreshHostCLI/reuse_default` |
| M15 | No new legacy response keys | `TestMemoryHeartbeatAndLegacyViews` |
| M16 | Carry fresh intent through preflight | `TestFreshHostCLI/fresh_intent` |
| M17 | Bind memory budget to owned agent container | `TestCapacityBudgetBindingAndLeaseRecheck` |
| M18 | Reject unbounded memory samples | `TestSampleCodexHostMemory` |
| M19 | Fresh CLI is only new Codex interactive | `TestFreshHostCLI` |
| M20 | Fresh refuses an old/disabled selection route | `TestFreshHostCLI/fresh_refuses_older_operator` |
| M21 | Record creation reason on new session | `TestFreshHostAPI|TestCapacityAPISelectAndCreate/full` |
| M22 | Persist valid memory reports | `TestMemoryHeartbeatAndLegacyViews` |

## Exact edits and observed failures

### M01: Skip a full host

Package: `./internal/codexhostpool`. Exit: 1 with an assertion failure.

File: `internal/codexhostpool/select.go`.

Before:
```go
if MemoryFull(h, now) {
```
After:
```go
if false && MemoryFull(h, now) {
```

Observed:
```text
--- FAIL: TestHostMemoryCapacity (0.00s)
--- FAIL: TestHostMemoryCapacity/full_leads_to_create_with_reason (0.00s)
--- FAIL: TestHostMemoryCapacity/another_host_with_room_wins (0.00s)
--- FAIL: TestHostMemoryCapacity/session_cannot_create_beside_full_host (0.00s)
--- FAIL: TestHostMemoryCapacity/second_full_host_reaches_fixed_bound (0.00s)
--- FAIL: TestHostMemoryCapacity/fleet_cap_still_holds (0.00s)
```

### M02: Reserve 1 GiB

Package: `./internal/codexhostpool`. Exit: 1 with an assertion failure.

File: `internal/codexhostpool/select.go`.

Before:
```go
MemoryReserveBytes int64 = 1 << 30
```
After:
```go
MemoryReserveBytes int64 = 0
```

Observed:
```text
--- FAIL: TestMemoryObservationBounds (0.00s)
--- FAIL: TestMemoryObservationBounds/absolute_reserve (0.00s)
```

### M03: Reserve 20 percent

Package: `./internal/codexhostpool`. Exit: 1 with an assertion failure.

File: `internal/codexhostpool/select.go`.

Before:
```go
remaining < h.MemoryLimit/5
```
After:
```go
remaining < h.MemoryLimit/1000
```

Observed:
```text
--- FAIL: TestMemoryObservationBounds (0.00s)
--- FAIL: TestMemoryObservationBounds/percentage_reserve (0.00s)
```

### M04: Bound sample age to three minutes

Package: `./internal/apiserver`. Exit: 1 with an assertion failure.

File: `internal/codexhostpool/select.go`.

Before:
```go
MemoryMaxAge = 3 * time.Minute
```
After:
```go
MemoryMaxAge = 60 * time.Minute
```

Observed:
```text
--- FAIL: TestMemoryHeartbeatAndLegacyViews (0.06s)
```

### M05: Permit a second slot only on full/fresh intent

Package: `./internal/codexhostpool`. Exit: 1 with an assertion failure.

File: `internal/codexhostpool/select.go`.

Before:
```go
room = fleetN < fleetCap && cellN < MaxHostsPerProfile
```
After:
```go
room = fleetN < fleetCap && cellN < perCell
```

Observed:
```text
--- FAIL: TestHostMemoryCapacity (0.00s)
--- FAIL: TestHostMemoryCapacity/full_leads_to_create_with_reason (0.00s)
```

### M06: Fresh only for human/trusted client

Package: `./internal/codexhostpool`. Exit: 1 with an assertion failure.

File: `internal/codexhostpool/select.go`.

Before:
```go
return c.Kind == KindHuman || c.Kind == KindClient
```
After:
```go
return true
```

Observed:
```text
--- FAIL: TestFreshHostIntent (0.00s)
--- FAIL: TestFreshHostIntent/ordinary_session (0.00s)
--- FAIL: TestFreshHostIntent/workbench_session (0.00s)
--- FAIL: TestFreshHostIntent/coordinator (0.00s)
```

### M07: Fixed two-host bound

Package: `./internal/codexhostpool`. Exit: 1 with an assertion failure.

File: `internal/codexhostpool/select.go`.

Before:
```go
MaxHostsPerProfile = 2
```
After:
```go
MaxHostsPerProfile = 3
```

Observed:
```text
--- FAIL: TestHostMemoryCapacity (0.00s)
--- FAIL: TestHostMemoryCapacity/second_full_host_reaches_fixed_bound (0.00s)
--- FAIL: TestFreshHostIntent (0.00s)
```

### M08: Repeat selection at API create

Package: `./internal/apiserver`. Exit: 1 with an assertion failure.

File: `internal/apiserver/codexhost_capacity.go`.

Before:
```go
if s.CodexHosts != nil {
```
After:
```go
if false && s.CodexHosts != nil {
```

Observed:
```text
--- FAIL: TestCapacityAPISelectAndCreate (0.07s)
--- FAIL: TestCapacityAPISelectAndCreate/room (0.05s)
--- FAIL: TestCapacityAPISelectAndCreate/full (0.01s)
```

### M09: API bound survives selection being off

Package: `./internal/apiserver`. Exit: 1 with an assertion failure.

File: `internal/apiserver/codexhost_capacity.go`.

Before:
```go
if codexhostpool.RetainedInProfile(filtered, profile) >= codexhostpool.MaxHostsPerProfile {
```
After:
```go
if false && codexhostpool.RetainedInProfile(filtered, profile) >= codexhostpool.MaxHostsPerProfile {
```

Observed:
```text
--- FAIL: TestCapacityBoundWithSelectionOff (0.05s)
```

### M10: Count pending creates

Package: `./internal/apiserver`. Exit: 1 with an assertion failure.

File: `internal/apiserver/codexhost.go`.

Before:
```go
Pending:          (sess.Status.Phase == "" || sess.Status.Phase == v1alpha1.PhasePending) && codexOperatingMode(sess) == codexhostpool.ModeRunning,
```
After:
```go
Pending: false,
```

Observed:
```text
--- FAIL: TestPendingCreateCountsAcrossProfiles (0.06s)
```

### M11: Lost CAS does not admit another replica

Package: `./internal/apiserver`. Exit: 1 with an assertion failure.

File: `internal/apiserver/codexhost_capacity.go`.

Before:
```go
if apierrors.IsConflict(err) {
				continue
			}
```
After:
```go
if apierrors.IsConflict(err) {
				return nil
			}
```

Observed:
```text
--- FAIL: TestCapacityReservationAcrossReplicas (0.04s)
```

### M12: Keep an uncertain create reservation

Package: `./internal/apiserver`. Exit: 1 with an assertion failure.

File: `internal/apiserver/codexhost_capacity.go`.

Before:
```go
if err == nil || apierrors.IsAlreadyExists(err) || apierrors.IsInvalid(err) || apierrors.IsForbidden(err) || apierrors.IsBadRequest(err) {
```
After:
```go
if true {
```

Observed:
```text
--- FAIL: TestCapacityUnknownCreateRetainsReservation (0.04s)
```

### M13: Release only the same attempt

Package: `./internal/apiserver`. Exit: 1 with an assertion failure.

File: `internal/apiserver/codexhost_capacity.go`.

Before:
```go
 || pending.Attempt != attempt
```
After:
```go

```

Observed:
```text
--- FAIL: TestCapacityReservationAcrossReplicas (0.04s)
```

### M14: Omit false request keys for old clients

Package: `./internal/agentrun`. Exit: 1 with an assertion failure.

File: `internal/apiserver/apiv1/types.go`.

Before:
```go
FreshHost bool `json:"freshHost,omitempty"`
```
After:
```go
FreshHost bool `json:"freshHost"`
```

File: `internal/apiserver/apiv1/codexhost.go`.

Before:
```go
FreshHost bool   `json:"freshHost,omitempty"`
```
After:
```go
FreshHost bool   `json:"freshHost"`
```

Observed:
```text
--- FAIL: TestFreshHostCLI (0.01s)
--- FAIL: TestFreshHostCLI/reuse_default (0.01s)
```

### M15: No new legacy response keys

Package: `./internal/apiserver`. Exit: 1 with an assertion failure.

File: `internal/apiserver/apiv1/types.go`.

Before:
```go
type Session struct {
```
After:
```go
type Session struct {
 FreshHost bool `json:"freshHost,omitempty"`
```

File: `internal/apiserver/view.go`.

Before:
```go
v := apiv1.Session{
```
After:
```go
v := apiv1.Session{FreshHost: true,
```

Observed:
```text
--- FAIL: TestMemoryHeartbeatAndLegacyViews (0.05s)
```

### M16: Carry fresh intent through preflight

Package: `./internal/agentrun`. Exit: 1 with an assertion failure.

File: `internal/agentrun/codexhost.go`.

Before:
```go
FreshHost: fresh}
```
After:
```go
FreshHost: false}
```

Observed:
```text
--- FAIL: TestFreshHostCLI (0.01s)
--- FAIL: TestFreshHostCLI/fresh_intent (0.01s)
```

### M17: Bind memory budget to owned agent container

Package: `./internal/apiserver`. Exit: 1 with an assertion failure.

File: `internal/apiserver/codexhost.go`.

Before:
```go
container.Name == "agent" && container.Resources.Limits.Memory().Value() == m.LimitBytes
```
After:
```go
container.Name == "agent"
```

Observed:
```text
--- FAIL: TestCapacityBudgetBindingAndLeaseRecheck (0.05s)
```

### M18: Reject unbounded memory samples

Package: `./internal/agentd`. Exit: 1 with an assertion failure.

File: `internal/agentd/codex_host_memory.go`.

Before:
```go
if !m.Valid() {
```
After:
```go
if false && !m.Valid() {
```

Observed:
```text
--- FAIL: TestSampleCodexHostMemory (0.00s)
--- FAIL: TestSampleCodexHostMemory/zero_limit (0.00s)
```

### M19: Fresh CLI is only new Codex interactive

Package: `./internal/agentrun`. Exit: 1 with an assertion failure.

File: `internal/agentrun/create.go`.

Before:
```go
if freshHost && (agent != protocol.AgentCodex || !interactive || restoring || local) {
```
After:
```go
if false && freshHost && (agent != protocol.AgentCodex || !interactive || restoring || local) {
```

Observed:
```text
--- FAIL: TestFreshHostCLI (0.02s)
```

### M20: Fresh refuses an old/disabled selection route

Package: `./internal/agentrun`. Exit: 1 with an assertion failure.

File: `internal/agentrun/codexhost.go`.

Before:
```go
if fresh {
			return false, fail
```
After:
```go
if false && fresh {
			return false, fail
```

Observed:
```text
--- FAIL: TestFreshHostCLI (0.01s)
--- FAIL: TestFreshHostCLI/fresh_refuses_older_operator (0.01s)
```

### M21: Record creation reason on new session

Package: `./internal/apiserver`. Exit: 1 with an assertion failure.

File: `internal/apiserver/codexhost_capacity.go`.

Before:
```go
sess.Annotations[codexCapacityReason] = res.Reason
```
After:
```go
delete(sess.Annotations, codexCapacityReason)
```

Observed:
```text
--- FAIL: TestCapacityAPISelectAndCreate (0.05s)
--- FAIL: TestCapacityAPISelectAndCreate/full (0.05s)
--- FAIL: TestFreshHostAPI (0.03s)
--- FAIL: TestFreshHostAPI/tok-human (0.01s)
--- FAIL: TestFreshHostAPI/tok-client (0.01s)
```

### M22: Persist valid memory reports

Package: `./internal/apiserver`. Exit: 1 with an assertion failure.

File: `internal/apiserver/heartbeat.go`.

Before:
```go
if codexRemoteSession(&sess) && st.CodexHostMemory != nil
```
After:
```go
if false && codexRemoteSession(&sess) && st.CodexHostMemory != nil
```

Observed:
```text
--- FAIL: TestMemoryHeartbeatAndLegacyViews (0.05s)
```

The committed source contains none of these mutations. Source tests are not
evidence of installation, real provider thread capacity or owner acceptance.
No live host or cluster object was changed by the worker.
