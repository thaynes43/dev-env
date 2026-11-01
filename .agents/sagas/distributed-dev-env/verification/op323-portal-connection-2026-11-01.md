# OP323 portal current-source connection, 2026-11-01

Author dev-env-1101-154146. Worktree `/home/dev/work/op323-portal-grok-1101-154146`, branch `agent/op323-portal-grok-1101-154146`. Dispatched base `367eef0f890d3c067b256d3a89ad5739419821d0`. Sol owns integration. This worker wrote source and tests only. It did not run Git, the test binary, or a publish, merge, or deploy.

Runtime activation is unchanged. No ConfigMap, Role, RoleBinding, flag, mounted report, or server route is installed. `server.go` and `pages.go` are untouched. The operator may still return 404 for the fixed slot. A synthetic fixture is not live acceptance.

## What this slice connects

`web/portal/session/current_source_transport.go` adds an optional consumer. `config/current-source.json` absent means no read. A present file copies one selection onto the existing operator pinned client and token path. It does not assign `SessionSource.allowance`.

`serveProduct` calls that connection only after Authentik `portal_is_active` and `portal_is_admin` have succeeded, and only for a Sessions inventory. The GET is `https://dev-env-operator.dev-env-system.svc.cluster.local:8443/v1/provider-source-reports/codex-current-source`, at most once. The body stays on the request. `NewCurrentSourceReader` projects it. A non-null `providerUpdatedAt` is the fixed Unknown panel on this path. `NewCurrentSourceReader` itself still accepts a non-null value that `ValidateCurrentSource` accepts.

Missing, stale, malformed, redirected, oversized, 403, 404, and timeout results use `Unknown · Current source unavailable` and keep the session list. A bound reader is not merged and is not a fallback. Claude and Cursor stay disconnected. Seven days stays a duration.

## Tests written, not executed here

The focused package is `./web/portal/session/`. Names:

- `TestCurrentSourceTransportProjectsUsageAndDetail`
- `TestCurrentSourceTransportReadsOnlyAfterAuthorization`
- `TestCurrentSourceTransportFailurePanelKeepsInventory`
- `TestCurrentSourceTransportRejectsProviderUpdatedAt`
- `TestCurrentSourceTransportDoesNotMergeBoundAllowance`
- `TestCurrentSourceTransportConcurrentRequestsDoNotMix`
- `TestCurrentSourceConfigIsImmutableAndOptional`
- `TestProductionCurrentSourceConfigFailsClosed`

Expected command, serial, two CPUs, one package, under the shared flock and cache named by the dispatch:

```
flock /tmp/usage-040820-test.lock nice -n 19 go test -count=1 -p 1 ./web/portal/session/ -run 'TestCurrentSourceTransport|TestCurrentSourceConfig|TestProductionCurrentSource'
```

`GOMAXPROCS=2` and `GOCACHE=/tmp/usage-040820-go-cache`. The toolchain pin is Go 1.27.3. This record does not claim that command's result.

## Still not done

- The central server supports conditional registration after PR821; production leaves its handler nil and the portal config absent.
- No production config file, grant, or flag is installed.
- A real retained report rendered from the live operator is not accepted.
- Hashes were not computed here because this worker did not run shell commands.

## Initial driver checks and remaining review finding

The current coordinator imported the frozen Grok task patch `1045c971fba090f9ef5dc8a344065af009f3744475748a240f87eb3104fef60e` and applied gofmt. Focused connected transport/config tests passed locally with `-race -p 1 -count=1` in 2.293 s on pinned Go 1.27.3, GOMAXPROCS=2, nice 19 and the shared lock/cache. This is the pre-correction source only. Native review found one P2: the selected transport parser allows the generic 86400 s maximum, while the selected-path plan uses an 1800 s ceiling. The source correction is recorded below. No runtime configuration or access is installed. This worker did not rerun that suite. Final qualification and review follow the frozen patch.

## Selected age correction, source only

Worktree `/home/dev/work/op323-portal-age-grok-1101-154146`, branch `agent/op323-portal-age-grok-1101-154146`. Dispatch exact base `02cf948dc9ccdbda7dee57f33c5375405407b5ce`. This worker did not run Git, so HEAD and a content hash were not recomputed.

`currentSourceMaxAgeSeconds` in `web/portal/session/current_source_transport.go` is 1800. The fixed-slot config accepts a positive `maxAgeSeconds` through 1800 and refuses 1801 and 86400. A shorter positive age stays allowed. The existing non-positive and other refusal cases stay. `internal/providerallowance` `CurrentSourceSelection` and its 24 hour validator ceiling are untouched.

The guide and DESIGN-030 now say this selected transport enforces 30 minutes and can choose a shorter positive age. `config/current-source.json` stays optional, unmounted, and default-off. No route, grant, flag, ConfigMap, credential, or server change is in this patch.

The 2.293 s result above is the pre-correction suite only. Tests for this correction were not run here. A synthetic fixture is not live acceptance. Sol qualifies the frozen artifact.

## Final driver qualification

The coordinator imported the frozen age correction and preserved the reviewed portal additions while integrating main through PR821/822. The changed configuration test passed under the race detector in 1.067 s. The combined current-source and question-audit production/transport suites then passed with `-race -p 1 -count=1` in 2.306 s on Go 1.27.3, GOMAXPROCS=2, nice 19, shared lock/cache. Earlier portal vet passed. Independent native review approved the corrected source and integration at `d58b4104933184311762c8a449a8c3baa7065216`; it verified both separately guarded portal GET cases, request isolation, authorization and the 1800-second ceiling. The subsequent PR822 merge adds independent Pages/MCP fixtures. Required hosted CI and the final documentation review are separate gates; no live ingestion, deployment or authenticated UI acceptance is established here.
