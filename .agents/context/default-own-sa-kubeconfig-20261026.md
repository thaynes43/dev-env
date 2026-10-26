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

## 2026-10-26 UTC — correction custody receipt

ACK: custody moved from predecessor task
`dev-env-1026-035800` (whose source head was
`18114cee47974a21a0c3f98258702e757bda204d`) to this isolated source task
`dev-env-1026-041213`, after the predecessor's reported exit. The verified
remote PR branch matched that exact head before this pass; base remains
`7976b1ad1ff50aef2b2a82e639a369546821b610`. This task has no operator-supplied
native task identifier (`AGENTD_SESSION` is unset). Parent source receipt:
`01a171cd-191b-72ce-94d9-1451d117b036`; access-coordinator receipt:
`01a17599-3e22-7428-b478-c74104db803b`; existing Sol/root review thread:
`01a164b6-4fdd-73f2-b49b-738c23b5a709`.

Configured source writer tier was Terra / medium. Actual served model and
effort are Unknown: this task has no supported runtime receipt for them.

This corrective pass restores `TestGrantsLifecycle`'s closure over its mutable
clock; moves grant-directory cleanup before own-projection validation; removes
the stale kubeconfig when that projection is absent after cleanup; and adds a
single daemon-hook fresh-zero-grant test that resolves the generated config via
client-go against a synthetic TLS endpoint using synthetic valid certificate and
token files. It preserves existing namespace, selected-valid-grant, lock,
private atomic-write, provider-directory, and token-path behavior. The stale
last-removal comment is corrected.

Measured source state before commit: `git diff --check` passed. The one focused
serial command, bounded as `GOMAXPROCS=2 nice -n 19 go test -p 1
./internal/agentd -run
'TestGrants|TestCredentialsCoexistFenceAndExpire|TestDaemonRemovesExpiredCredentials|TestDaemonBootRefreshesDefaultKubeconfig'
-count=1`, did not start: `go` and `gofmt` are absent, and `nice` returned exit
127. No toolchain was installed; source tests and formatter verification are
therefore not passed. No runtime, deployment, credentials, grants, RBAC, or
network endpoint outside the synthetic test server was used.

Existing required CI run `40267080572` was observed before this corrective
commit with Lint `FAILURE` and Test still in progress. Its lint diagnostic was
not retrieved or inferred in this source pass; coordinator follow-through owns
the resulting exact-head CI and independent review. This task neither merges
nor deploys.

Finish state: correction commit
`406545470a574f3172399e71ec67746c7f6c1ba1` was pushed as a non-force
descendant to the same draft PR #555 branch
`agent/dev-env-1026-035800`. The PR remains draft and unmerged; its body was
updated with this custody and test evidence. The final required-CI and fresh
independent-review gates remain explicitly coordinator-owned.
