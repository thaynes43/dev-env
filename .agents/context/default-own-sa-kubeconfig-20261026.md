# Default own-SA kubeconfig regression — source pass

2026-10-26 UTC — ACK: narrow source-only fix in progress for canonical OpenProject
#113 / journal 1663. Owner: this isolated worktree
`agent/dev-env-1026-035800` at `7976b1ad1ff50aef2b2a82e639a369546821b610`.

Contract: agentd must keep a private, atomically written default kubeconfig when
the grants volume and existing projected own-ServiceAccount token and CA are
available, including fresh boot and zero/expired/removed grants. It must retain
a selected valid grant and never copy token material, invent an API endpoint, or
create a fallback when the own projection is absent. Source state only; no live
pod, credential, grant, RBAC, deployment, or cluster access is used.

Requested runtime: `gpt-5.6-terra` / `medium`. Actual served model, cost, and
operator runtime limits: Unknown from this source workspace. Source execution
started after the parent-reported pre-execution denial; no child/retry was
launched by this writer.

Progress: source change is limited to `internal/agentd/grants.go`,
`internal/agentd/daemon.go`, and focused synthetic tests in
`internal/agentd/grants_test.go` plus the shared credential fixture. `Refresh`
is invoked by the existing boot/periodic credential-maintenance hook; it retains
the default context at zero grants and validates only the existing projection
paths before writing. Local Go 1.27.2 toolchain is not present (`go` and
`gofmt` unavailable), so focused test execution and formatter verification are
currently blocked; no toolchain was installed.

Measured verification: `git diff --check` passed. The serial focused command
`GOMAXPROCS=2 nice -n 19 go test -p 1 ./internal/agentd -run
'TestGrants|TestCredentialsCoexistFenceAndExpire|TestDaemonRemovesExpiredCredentials'
-count=1` could not start and exited 127 because `go` is absent. No live or
cross-pod execution was attempted.
