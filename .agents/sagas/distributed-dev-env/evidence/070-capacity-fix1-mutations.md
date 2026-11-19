# OP922 capacity fix round 1 mutation evidence

Date: 2026-11-18. Base: `47dacbe51d6ff8b79ec5a29fd442379c1b72b018`.
Coordinator review probes P1–P6 are reconstructed in
`internal/apiserver/codexhost_fixround_test.go`; X1–X3 are the reviewer mutants.

Each of these 40 mutations was applied alone. Its named test compiled and failed
an acceptance assertion (F25/F30 fail with the corresponding guarded panic).
The test ran under `GOMAXPROCS=2`, `nice -n 19`, `go test -p 2`, one package at a
time, with KUBECONFIG and every KUBERNETES_* variable omitted. Original bytes
were restored in a finally block and checked after every invocation. No mutation
was committed, no new test sleeps, and no live cluster was used.

| ID | Guard broken | Named failing test | Result |
| --- | --- | --- | --- |
| X1 | newSession refuses fresh intent on workbench default profile | `TestFreshHostWorkbenchDefaultGuard` | Killed; restored |
| X2 | malformed ledger fails closed and is never overwritten | `TestCapacityLedgerUnavailable/malformed` | Killed; restored |
| X3 | Kubernetes Forbidden and Invalid release their slot | `TestKubernetesCreateOutcomeClasses/(Forbidden\|Unprocessable_Entity)` | Killed; restored |
| F01 | missing ledger refuses counted paths | `TestCapacityLedgerUnavailable/missing` | Killed; restored |
| F02 | forbidden ledger refuses counted paths | `TestCapacityLedgerUnavailable/forbidden` | Killed; restored |
| F03 | ledger outage preserves use and report | `TestCapacityLedgerUnavailable/(missing\|forbidden\|malformed)/(use\|report)` | Killed; restored |
| F04 | ledger outage refuses selected resume | `TestCapacityLedgerUnavailable/(missing\|forbidden\|malformed)/resume` | Killed; restored |
| F05 | capacity refusal names create_unconfirmed instead of claiming two real hosts | `TestCreateUnconfirmedIsNamed` | Killed; restored |
| F06 | unknown-outcome log names attempt, session and requester | `TestCreateUnconfirmedIsNamed` | Killed; restored |
| F07 | phantom never qualifies for use or resume | `TestUnconfirmedRowsAreOnlySkipped` | Killed; restored |
| F08 | phantom never becomes report filler | `TestUnconfirmedRowsAreOnlySkipped` | Killed; restored |
| F09 | named phantom cannot be offered as use | `TestUnconfirmedRowsAreOnlySkipped` | Killed; restored |
| F10 | selection names a bound filled by an unknown | `TestCreateUnconfirmedIsNamed` | Killed; restored |
| F11 | create ignores client cancellation | `TestCodexCreateIgnoresClientCancellation` | Killed; restored |
| F12 | detached create owns a bounded timeout | `TestCodexCreateIgnoresClientCancellation` | Killed; restored |
| F13 | observed own attempt releases its reservation | `TestUnknownCreateReadsOwnAttemptOnce/own_attempt` | Killed; restored |
| F14 | different attempt retains its reservation | `TestUnknownCreateReadsOwnAttemptOnce/different_attempt` | Killed; restored |
| F15 | rescue-blocked volume holds fill the retained bound | `TestRescueBlockedHostsKeepRetainedBound` | Killed; restored |
| F16 | unresolved accounts retain their slot | `TestRetainedOrdinaryVolumeHolds/unresolved_account` | Killed; restored |
| F17 | working set subtracts inactive file cache | `TestCodexHostWorkingSetFixtures/subtract_page_cache` | Killed; restored |
| F18 | working set is floored at zero | `TestCodexHostWorkingSetFixtures/floor_at_zero` | Killed; restored |
| F19 | missing inactive_file is unknown | `TestCodexHostWorkingSetFixtures/missing_inactive` | Killed; restored |
| F20 | empty selection actions have a complete refusal sentence | `TestCapacitySelectionRefusalMessage` | Killed; restored |
| F21 | committed-create release retries before logging exhaustion | `TestCodexCapacityReleaseRetries/recovers` | Killed; restored |
| F22 | JSON null ledger is malformed and preserved | `TestMalformedCapacityLedgerPreserved/null` | Killed; restored |
| F23 | forbidden ledger update has the typed outage reason | `TestCapacityLedgerWriteUnavailable/forbidden` | Killed; restored |
| F24 | missing ledger at update has the typed outage reason | `TestCapacityLedgerWriteUnavailable/missing` | Killed; restored |
| F25 | absent reader returns typed unavailability | `TestCapacityLedgerUnavailableWithoutReader` | Killed; restored |
| F26 | oversized memory.stat is unknown | `TestCodexHostWorkingSetFixtures/oversized_stat` | Killed; restored |
| F27 | duplicate inactive_file is malformed | `TestCodexHostWorkingSetFixtures/duplicate_inactive` | Killed; restored |
| F28 | overflowing inactive_file is unknown | `TestCodexHostWorkingSetFixtures/overflow_inactive` | Killed; restored |
| F29 | unreadable memory.stat is unknown, never zero cache | `TestCodexHostWorkingSetFixtures/missing_stat` | Killed; restored |
| F30 | malformed memory.stat rows are unknown | `TestCodexHostWorkingSetFixtures/malformed_line_shape` | Killed; restored |
| F31 | select bound refusal uses existing error envelope to preserve attempt in released CLIs | `TestCreateUnconfirmedIsNamed` | Killed; restored |
| HTTP429 | Kubernetes 429 definitely refuses and releases | `TestKubernetesCreateOutcomeClasses/Too_Many_Requests` | Killed; restored |
| HTTP404 | Kubernetes 404 definitely refuses and releases | `TestKubernetesCreateOutcomeClasses/Not_Found` | Killed; restored |
| HTTP405 | Kubernetes 405 definitely refuses and releases | `TestKubernetesCreateOutcomeClasses/Method_Not_Allowed` | Killed; restored |
| HTTP406 | Kubernetes 406 definitely refuses and releases | `TestKubernetesCreateOutcomeClasses/Not_Acceptable` | Killed; restored |
| HTTP413 | Kubernetes 413 definitely refuses and releases | `TestKubernetesCreateOutcomeClasses/Request_Entity_Too_Large` | Killed; restored |
| HTTP415 | Kubernetes 415 definitely refuses and releases | `TestKubernetesCreateOutcomeClasses/Unsupported_Media_Type` | Killed; restored |

## Exact source edits

### X1: internal/apiserver/validate.go

Replace:
```go
if c.kind != kindHuman && c.kind != kindClient {
			return nil, forbidden("freshHost
```
with:
```go
if false && c.kind != kindHuman && c.kind != kindClient {
			return nil, forbidden("freshHost
```

### X2: internal/apiserver/codexhost_capacity.go

Replace:
```go
return nil, nil, capacityLedgerUnavailable(key, "malformed")
```
with:
```go
return &cm, nil, nil
```

### X3: internal/apiserver/codexhost_capacity.go

Replace:
```go
 || apierrors.IsInvalid(err) || apierrors.IsForbidden(err)
```
with:
```go
// removed
```

### F01: internal/apiserver/codexhost_capacity.go

Replace:
```go
return nil, nil, capacityLedgerUnavailable(key, "missing")
```
with:
```go
return &cm, nil, nil
```

### F02: internal/apiserver/codexhost_capacity.go

Replace:
```go
return nil, nil, capacityLedgerUnavailable(key, "forbidden")
```
with:
```go
return &cm, nil, nil
```

### F03: internal/apiserver/codexhost.go

Replace:
```go
if ledgerErr != nil && (res.Action == codexhostpool.ActionCreate || res.Action == codexhostpool.ActionResume) {
```
with:
```go
if ledgerErr != nil {
```

### F04: internal/apiserver/codexhost.go

Replace:
```go
if ledgerErr != nil && (res.Action == codexhostpool.ActionCreate || res.Action == codexhostpool.ActionResume) {
```
with:
```go
if false && ledgerErr != nil && (res.Action == codexhostpool.ActionCreate || res.Action == codexhostpool.ActionResume) {
```

### F05: internal/apiserver/codexhost_capacity.go

Replace:
```go
return conflictReason(codexhostpool.ReasonCreateUnconfirmed, "Unconfirmed Codex host create occupies capacity: %s; its reservation is retained.", strings.Join(parts, "; "))
```
with:
```go
return conflictReason(codexhostpool.ReasonAtCapacity, "The profile already has two retained ordinary Codex hosts. %s", strings.Join(parts, "; "))
```

### F06: internal/apiserver/codexhost_capacity.go

Replace:
```go
"attempt", pending.Attempt, "session", pending.Name, "requester", c.String()
```
with:
```go
"attempt", "", "session", "", "requester", ""
```

### F07: internal/codexhostpool/select.go

Replace:
```go
if h.ReservationAttempt != "" {
		return ReasonCreateUnconfirmed, false, false
```
with:
```go
if false && h.ReservationAttempt != "" {
		return ReasonCreateUnconfirmed, false, false
```

### F08: internal/codexhostpool/select.go

Replace:
```go
 && reason != ReasonCreateUnconfirmed
```
with:
```go
// removed
```

### F09: internal/codexhostpool/select.go

Replace:
```go
if h.ReservationAttempt != "" {
				return Result{Reason: ReasonCreateUnconfirmed
```
with:
```go
if false && h.ReservationAttempt != "" {
				return Result{Reason: ReasonCreateUnconfirmed
```

### F10: internal/codexhostpool/select.go

Replace:
```go
return ReasonCreateUnconfirmed
		}
	}
	return ReasonAtCapacity
```
with:
```go
return ReasonAtCapacity
		}
	}
	return ReasonAtCapacity
```

### F11: internal/apiserver/codexhost_capacity.go

Replace:
```go
context.WithTimeout(context.WithoutCancel(ctx), codexHostCreateTimeout)
```
with:
```go
context.WithTimeout(ctx, codexHostCreateTimeout)
```

### F12: internal/apiserver/codexhost_capacity.go

Replace:
```go
context.WithTimeout(context.WithoutCancel(ctx), codexHostCreateTimeout)
```
with:
```go
context.WithCancel(context.WithoutCancel(ctx))
```

### F13: internal/apiserver/codexhost_capacity.go

Replace:
```go
if readErr := s.Live.Get(readCtx, types.NamespacedName{Namespace: sess.Namespace, Name: pending.Name}, &made); readErr == nil && made.Annotations[codexCapacityAttempt] == pending.Attempt {
```
with:
```go
if readErr := s.Live.Get(readCtx, types.NamespacedName{Namespace: sess.Namespace, Name: pending.Name}, &made); false && readErr == nil && made.Annotations[codexCapacityAttempt] == pending.Attempt {
```

### F14: internal/apiserver/codexhost_capacity.go

Replace:
```go
readErr == nil && made.Annotations[codexCapacityAttempt] == pending.Attempt
```
with:
```go
readErr == nil
```

### F15: internal/codexhostpool/select.go

Replace:
```go
!h.Archived && !h.Reserved && !h.WorkbenchAccount && !h.HandsOff && !h.ProtectedAnnotation && profileMatches
```
with:
```go
!h.Archived && !poolExcluded(h) && !rescueBlocked(h) && profileMatches
```

### F16: internal/codexhostpool/select.go

Replace:
```go
!h.Archived && !h.Reserved && !h.WorkbenchAccount && !h.HandsOff && !h.ProtectedAnnotation && profileMatches
```
with:
```go
!h.Archived && h.AccountResolved && !h.Reserved && !h.WorkbenchAccount && !h.HandsOff && !h.ProtectedAnnotation && profileMatches
```

### F17: internal/agentd/codex_host_memory.go

Replace:
```go
max(int64(0), values[0]-inactive)
```
with:
```go
max(int64(0), values[0]-min(inactive, int64(0)))
```

### F18: internal/agentd/codex_host_memory.go

Replace:
```go
max(int64(0), values[0]-inactive)
```
with:
```go
values[0]-inactive
```

### F19: internal/agentd/codex_host_memory.go

Replace:
```go
if !found {
```
with:
```go
if false && !found {
```

### F20: internal/apiserver/codexhost_capacity.go

Replace:
```go
if res.Action == "" {
```
with:
```go
if false && res.Action == "" {
```

### F21: internal/apiserver/codexhost_capacity.go

Replace:
```go
for range codexHostLeaseTries {
		err = s.releaseCodexCapacity
```
with:
```go
for range 1 {
		err = s.releaseCodexCapacity
```

### F22: internal/apiserver/codexhost_capacity.go

Replace:
```go
 || pending == nil
```
with:
```go
// removed
```

### F23: internal/apiserver/codexhost_capacity.go

Replace:
```go
if apierrors.IsForbidden(err) {
				return capacityLedgerUnavailable
```
with:
```go
if false && apierrors.IsForbidden(err) {
				return capacityLedgerUnavailable
```

### F24: internal/apiserver/codexhost_capacity.go

Replace:
```go
if apierrors.IsNotFound(err) {
				return capacityLedgerUnavailable
```
with:
```go
if false && apierrors.IsNotFound(err) {
				return capacityLedgerUnavailable
```

### F25: internal/apiserver/codexhost_capacity.go

Replace:
```go
if s.Live == nil {
```
with:
```go
if false && s.Live == nil {
```

### F26: internal/agentd/codex_host_memory.go

Replace:
```go
len(stat) > 64<<10
```
with:
```go
len(stat) < 0
```

### F27: internal/agentd/codex_host_memory.go

Replace:
```go
if found {
```
with:
```go
if false && found {
```

### F28: internal/agentd/codex_host_memory.go

Replace:
```go
n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
```
with:
```go
n, err := strconv.ParseInt(fields[1], 10, 64)
		if false && err != nil {
```

### F29: internal/agentd/codex_host_memory.go

Replace:
```go
if err != nil || len(stat) > 64<<10 {
		return nil
```
with:
```go
if err != nil || len(stat) > 64<<10 {
		stat = []byte("inactive_file 0\n")
```

### F30: internal/agentd/codex_host_memory.go

Replace:
```go
len(fields) != 2 ||
```
with:
```go
// removed
```

### F31: internal/apiserver/codexhost.go

Replace:
```go
if res.Reason == codexhostpool.ReasonCreateUnconfirmed {
```
with:
```go
if false && res.Reason == codexhostpool.ReasonCreateUnconfirmed {
```

### HTTP429: internal/apiserver/codexhost_capacity.go

Replace:
```go
case 429, 404, 405, 406, 413, 415:
```
with:
```go
case 404, 405, 406, 413, 415:
```

### HTTP404: internal/apiserver/codexhost_capacity.go

Replace:
```go
case 429, 404, 405, 406, 413, 415:
```
with:
```go
case 429, 405, 406, 413, 415:
```

### HTTP405: internal/apiserver/codexhost_capacity.go

Replace:
```go
case 429, 404, 405, 406, 413, 415:
```
with:
```go
case 429, 404, 406, 413, 415:
```

### HTTP406: internal/apiserver/codexhost_capacity.go

Replace:
```go
case 429, 404, 405, 406, 413, 415:
```
with:
```go
case 429, 404, 405, 413, 415:
```

### HTTP413: internal/apiserver/codexhost_capacity.go

Replace:
```go
case 429, 404, 405, 406, 413, 415:
```
with:
```go
case 429, 404, 405, 406, 415:
```

### HTTP415: internal/apiserver/codexhost_capacity.go

Replace:
```go
case 429, 404, 405, 406, 413, 415:
```
with:
```go
case 429, 404, 405, 406, 413:
```
