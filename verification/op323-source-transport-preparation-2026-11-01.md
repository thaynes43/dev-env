# OP323 source transport preparation, 2026-11-01

Author dev-env-1101-113208. Worktree `/home/dev/work/op323-source-transport-grok-1101-113208`, branch `agent/op323-source-transport-grok-1101-113208`. Sol113208 owns integration. OP321 retains `internal/apiserver/server.go`, `cmd/dev-env-operator/main.go`, and the portal product/production joins. This context did not edit those files, did not commit, and did not enable a route, flag, Role, or ConfigMap.

The sealed input `.task-inputs/sealed-op323-source-slot.md` was read and left untracked. This context did not recompute its SHA256. The assignment's expected digest is `a1bb185771e6ee0c29e0ee3660fdd7c31865b6ad9aec9c12df09e888f061ce11`.

This is source preparation. It is not connected delivery, not an accepted source, and not runtime activation. No shell, API, credential, or cluster call was made here, so compile, focused tests, and the envtest SSA subtest are not claimed green.

## What landed

New files, all uncommitted:

- `internal/apiserver/provider_source_report_store.go`
- `internal/apiserver/provider_source_report_store_test.go`
- `internal/apiserver/provider_source_reports.go`
- `internal/apiserver/provider_source_reports_test.go`
- `verification/op323-source-transport-preparation-2026-11-01.md`

The store and handler compile in package `apiserver` with injected clients, slot name, and `providerallowance.CurrentSourceSelection`. They call `DecodeCurrentSource` and `ValidateCurrentSource`. They do not define a second report, allowance store, collector, or entitlement mapping. `claimSnapshot` is not called.

Live packet identities are not constants. Tests use `synthetic-session-uid`, `synthetic-pod-uid`, `synthetic-incarnation-1`, and namespace `synthetic-op323-source`. The historical Pod UID is not a field. Each publisher call reads the current Pod from the injected getter.

`ProviderSourceReportStore` is a live GET plus resourceVersion `Update` of one precreated object. `objectUpdater` has `Update` only, so the store cannot create or delete. A missing object returns `ErrProviderSourceAbsent`. The object passed to `Update` is the object just read: labels, annotations, finalizers, owner references, and managedFields are not rebuilt. The field manager on that update is `dev-env-provider-source`. The report is one data key, `report.json`, holding the canonical JSON of the decoded `CurrentSourceReport`. A replay of those same bytes does not call `Update`. A stale resourceVersion returns `ErrProviderSourceConflict` and does not confirm a different body. A lost response is accepted only when a later live GET shows the same bytes at a newer resourceVersion.

Reads do not write. A failed decode, a canonical-byte mismatch, a selection mismatch, `ErrCurrentSourceExpired`, or a non-null `providerUpdatedAt` suppresses the value and leaves the stored bytes in place. The slot does not mint `providerUpdatedAt`. A canonical report that generic `ValidateCurrentSource` accepts, with `providerUpdatedAt` set, is refused by `Put` before the live read and before `Update`. GET, restart, and replay do not change retrieval.

`ProviderSourceReports.Handle` is not mounted. `RouteEnabled` is false unless `Enabled` is set and the publisher, portal, and selection identity arguments are present. `Selection.MaxAge` is not an input to `RouteEnabled`. While disabled, exact GET and PUT return 404 and do not read the slot, including when the stored report is still inside max age. While enabled, a stale report returns `{"availability":"Unknown"}` and does not delete the object. A fresh GET returns the stored report bytes unchanged. That document is the current-source report, not a measured allowance.

The publisher fence requires `kindSession`, the configured ServiceAccount, a Running non-deleting Pod, token Pod UID equal to that live Pod, token Pod name equal to the session name and `status.podName`, controller Session UID equal to the configured UID, `operatingMode` Running, and phase Running or Idle. Pending, Draining, Suspended, Archived, Failed, a missing phase, a missing operating mode, and `operatingMode` Suspended are refused. A replacement Pod for the same Session UID is allowed. An older Pod name or older Pod UID is refused. A session with `spec.workspace` set and no project-snapshot annotation is still allowed, which is the stand-in for not calling `claimSnapshot`. Portal GET uses `kindPortalReader` and the same current-Pod checks, including Pod phase Running. Portal PUT is refused.

`PortalProviderSourceGET` is true only for enabled GET of `ProviderSourceReportPath`. PUT, `/v1/sessions`, `/v1/inbox`, `/v1/published-contexts`, and any other slot name are false. `Server.serve` does not call it yet.

## Exact later connection patch

Apply this only in the serial integration that already owns `server.go`. Do not apply it in parallel with OP321. After the patch, leave the field nil. A nil field keeps the route off. `main.go` does not need an edit for that zero value, and this preparation adds no flag.

In `Server`, next to `Asks`:

```go
// ProviderSourceReports is one fixed current-source slot.
// Nil, or RouteEnabled false, leaves both methods unregistered.
// Selection.MaxAge does not register the route.
ProviderSourceReports *ProviderSourceReports
```

In `Handler`, immediately before the unmatched `/` handler:

```go
if s.ProviderSourceReports.RouteEnabled() {
    mux.Handle(ProviderSourceReportPath, s.serve(route{methods: map[string]handler{
        http.MethodGet: s.ProviderSourceReports.Handle,
        http.MethodPut: s.ProviderSourceReports.Handle,
    }}))
}
```

In `serve`, in the `kindPortalReader` switch, immediately before `default`:

```go
case PortalProviderSourceGET(s.ProviderSourceReports.RouteEnabled(), r.Method, r.URL.Path):
```

That case is GET of `/v1/provider-source-reports/codex-current-source` only. Portal PUT stays on the existing default refusal. No other portal path is added.

A later activation, still default off, is the first time `main.go` may set a non-nil value. It must pass an uncached live getter, an update-only writer, the configured namespace and object name, `DataKey: ProviderSourceDataKey`, and a `CurrentSourceSelection` whose publisher, incarnation, and generation come from configuration. `MaxAge` of 1800 seconds is that selection's freshness limit, not the enable bit. `Enabled` must stay false until the precreated object and the get/update Role exist. This patch does not create them.

The later portal join is not this patch. `web/portal/session/production.go` stays disconnected. After `Authorize` has already accepted the request-private session, and only for that admin audience (`portal_is_active` and `portal_is_admin`, no subject pin), a future change may GET this slot and call `SetAllowanceReader(NewCurrentSourceReader(raw, selection))`. Unknown and expired responses must not be passed to that constructor. The reader remains source-reported. It must not be merged with a measured allowance.

## Tests written, not run here

`TestProviderSourceStoreDecisions` uses an in-memory object. It covers metadata retention, replay, restart read, same-retrieval and older-retrieval refusal, generation mismatch and reset, reset time, digest mismatch, 16KiB, no repair of undecodable bytes, immutable and deleting objects, one conflict that does not mix, lost-response confirmation, expiry without deletion, generation replacement of the one slot, and a missing object that is not created.

Two subtests record the native P2 repair. `non-null providerUpdatedAt is refused before a write` builds a canonical report whose `providerUpdatedAt` equals `retrievedAt` (`2026-01-08T12:00:00Z`, so the timestamp is not after retrieval). `ValidateCurrentSource` accepts it. `Put` returns `ErrProviderSourceRejected` and does not call the writer. Stored bytes, resourceVersion, and update count stay on the seeded null report. The same body through `ProviderSourceReports.Handle` PUT is HTTP 422 and still does not write. `stored non-null providerUpdatedAt is suppressed` plants that canonical report directly in the ConfigMap. `Read` returns found, suppressed, not fresh, and no raw bytes. It does not update. The stored `retrievedAt` and the non-null `providerUpdatedAt` stay. GET through the same handler returns `{"availability":"Unknown"}` and does not echo the report. The generic `CurrentSourceReport` schema is unchanged.

`TestProviderSourceEnvtest` is the only SSA, real resourceVersion, and API-ownership evidence. It is not a live Flux reconcile and it was not executed here. Fake client is not used for that subtest. Subtests: metadata-only apply, operator update, second metadata-only apply, replica conflict, absent and deleted objects, expiry, generation replacement, handler GET/replay/disable, and a fixture prune of one ConfigMap, Role, and RoleBinding. A refused delete in that fixture returns an error and leaves the object; it is not reported as erased. `pruneSourceNames` exists only in the test.

`TestProviderSourceCallerFence`, `TestProviderSourcePortalAndRoute`, `TestProviderSourceMaxAgeDoesNotRegisterRoute`, and `TestPortalProviderSourceGETDoesNotWiden` cover the live-current-executor predicates, portal PUT and foreign paths, disabled fresh bytes, and max age not registering the route.

Focused command for the driver, serial, two CPUs, one package:

```
nice -n 19 go test -count=1 -p 1 ./internal/apiserver/ -run 'TestProviderSource|TestPortalProviderSource'
```

`GOMAXPROCS=2`. This context did not run it. SSA survival is incomplete until `TestProviderSourceEnvtest/ssa` has been run against envtest and has passed. A failure there is unfinished integration evidence, not a claim that the bytes survive a real Flux manager.

## Native finding and dormant integration

Independent native exact-head review found P2 at the parent-verified base `d84b5a530149ad410f8dcf5db27da2887c27261e`: `Put` relied on generic `ValidateCurrentSource`, which accepts a non-null `providerUpdatedAt`, while the sealed selected-source contract requires null. The repair is only in `ProviderSourceReportStore` and the two subtests above. `internal/providerallowance` is not edited. `server.go`, `main.go`, and the portal product and production joins are not edited.

This context did not run the focused test, did not format, and did not commit. A later exit 0 is not acceptance. The integration limit stays dormant: the route is not mounted, `Enabled` is not set by `main.go`, and no ConfigMap, Role, or RoleBinding is installed. `TestProviderSourceEnvtest/ssa` was not executed here. SSA survival against a real Flux manager remains unproven. Runtime stays disabled. The sealed packet was not staged or edited.

## Still not done

- The `server.go` patch above is not applied.
- `main.go`, portal product, and portal production are unchanged.
- No ConfigMap, Role, RoleBinding, route, or flag is installed.
- No commit, push, or PR.
