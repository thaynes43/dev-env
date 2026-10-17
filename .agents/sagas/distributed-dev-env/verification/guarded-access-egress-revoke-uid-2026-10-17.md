# Broker egress revoke UID recheck, 2026-10-17

Source-only proof of one D-64 revocation check. This does not accept guarded
access, create a grant, change haynes-ops, or retire Headlamp or any v1 workflow.

## Case

DESIGN-001 D-64 says both egress deletion paths name the policy UID they
observed, then delete with that UID, and a replacement must pass a fresh
ownership check. The operator backstop already proves that in
`internal/grantexpiry/reconciler_test.go`
(`TestExpiryUsesUIDPreconditionAndRechecksReplacement`). The broker path in
`internal/broker/kube.go` `deleteOwn` had the same checks and no test.

`TestBrokerEgressRevokeUsesUIDAndRechecksReplacement` expires an active egress
grant while its policy is replaced during delete:

- The delete names the UID observed before the call. A UID conflict leaves the
  grant Active.
- A replacement whose source selector does not select the session is left in
  place. The next reconcile expires the grant and does not delete that policy.
- An owned replacement is deleted only on that next reconcile, and only under
  the replacement UID.

## Test

This Mac cannot compile `internal/broker` tests for darwin because `agentd`
uses Linux calls. The package was cross-compiled and run in a local Linux
container limited to two CPUs. Envtest did not run: this machine has no
`KUBEBUILDER_ASSETS`.

```sh
nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go test -c -o /tmp/broker-guarded-access.test -count=1 ./internal/broker/
docker run --rm --cpus=2 --platform linux/arm64 -e GOMAXPROCS=2 \
  -v /tmp/broker-guarded-access.test:/broker.test:ro alpine:3.20 \
  /broker.test -test.count=1 -test.timeout 180s \
  -test.run '^(TestCheck|TestTokenSeconds|TestCredentialBroker|TestCheckEgress|TestBrokerEgressLostCreateResponse|TestBrokerEgressRevokeUsesUIDAndRechecksReplacement|TestInstallation|TestReleaseCleans|TestExecInstaller|TestMatches|TestMatchFirst|TestBrokerRequester)'
```

Result: PASS. The new test's `foreign source selector` and `owned replacement`
subtests passed, as did `TestBrokerEgressLostCreateResponse` and the other
broker unit tests in that filter.

## What this leaves open

Workflow guide section 8 still requires a supported task to succeed through a
scoped policy, a bypass or unauthorized action to be refused, and expiry and
revocation to be verified on the live system before any required v1 workflow
retires. D-71 still retires Headlamp only after that parity. Standing
GrantPolicies, private Proxmox targets, and the real provider acceptance pass
remain owner gates.
