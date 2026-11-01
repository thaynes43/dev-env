# Current driver qualification — 2026-11-01 13:14 UTC

The GrantDecision and ParseGrantDelegates suite passed in 1.407 s (14.57 s elapsed). Actual targeted tests ran serially under the pinned toolchain and shared lock; source hashes stayed stable. This is source regression evidence only. Independent final-head review and required hosted CI remain pending. No runtime permission or feature was enabled. All sections below are the original worker record from before that test run; their statements about not running tests describe that earlier context only.

---

# OP323 fixture uniqueness, 2026-11-01

Source-only repair of a synthetic AccessGrant name collision. No deploy, release, runtime activation, flag change, or Actions rerun. Served model: Unknown.

Pinned base `964a134ddfbc9045c88526ba6dc1550ec8f9936b` (PR 819). Worktree `/home/dev/work/op323-fixture-grok-1101-123940`, branch `agent/op323-fixture-grok-1101-123940`. This writer did not re-read git refs. PR 820 stays separate. A successor updates PR 819 from this branch. The sealed packet was not read and not copied.

## What failed

Required CI failed in `TestGrantDecisionReachIsTheDelegatesTree`. Evidence `OP323-113208-CI-FAILED.log`: `grant_decision_test.go:403` returned `grant-1006-172226-0b95 already exists`. That call is `fixture.putGrant`. On the pinned source, `putGrant` built the name as `grant-1006-172226-` plus `string(uuid.NewUUID())[:4]` (pre-edit line 182). Four hex digits are 65536 names. The reach test creates many grants in one fake client, so a repeat of those four digits is `AlreadyExists`. Denied Actions run `41307543787` stays stopped. This change does not retry it, suppress `AlreadyExists`, or retry the create.

## Validators inspected

Production `grantName` in `internal/apiserver/grants.go` remains `grant-<MMDD>-<HHMMSS>-<4 hex>`: two `rand.Read` bytes, hex encoded. `TestGrantName` still expects `^grant-1006-002504-[0-9a-f]{4}$`. `grantNameRE` in `internal/apiserver/grants_test.go` remains `^grant-1006-172226-[0-9a-f]{4}$` and applies to `fixture.request`, which calls `grantName`. Those paths were not edited.

The AccessGrant name rule is size at most 63 and `^grant-[a-z0-9]([-a-z0-9]*[a-z0-9])?$` (`api/v1alpha1/accessgrant_types.go`). `grantKey` requires the `grant-` prefix and `validation.IsDNS1123Label`. Broker `checkCapabilities` requires the same prefix, a DNS-1123 label, and length at most 63. A canonical UUID is 36 lowercase hex-and-hyphen characters. `grant-1006-172226-` is 18 characters. The joined synthetic name is 54 characters, starts and ends with an alphanumeric, and matches that rule.

`addDelegatedPolicy` names a policy `delegate-fixture-` plus the grant name. It is called with `fixture.request` grants, which still use production `grantName`. It is not called with `putGrant` names, so the 54-character fixture name is not prefixed into a policy name. `grant-1006-172226-none` is a literal missing-grant path, not a generated fixture name.

## Correction

Owned source edit: `internal/apiserver/grant_decision_test.go` only. `putGrant` now uses `grant-1006-172226-` plus `string(uuid.NewUUID())`, the full generated UUID, with no `[:4]` slice. The comment on `putGrant` records the 54-character synthetic name and that production `grantName` stays the 4-hex form. Callers `childGrant` and the direct `putGrant` sites in this file and `grant_decision_integration_test.go` take that name. Authorization, delegates, and decision handling are unchanged. `TestEnvtestGrantDecisionNamedCoordinatorLaunch` does not call `putGrant`.

Post-edit locations: the name is built at `putGrant` (now line 184). The reach-test call that CI reported at line 403 is the `host-x` `putGrant` in `the requester's current pod`, now line 405, because the comment grew by two lines.

## Parent qualification

This writer ran no test, formatter, hash, or Git command. Hashes are not claimed here. Parent qualification records SHA-256 of `internal/apiserver/grant_decision_test.go` and of this note, and the exact diff against `964a134ddfbc9045c88526ba6dc1550ec8f9936b`.

These fixture callers use the fake client. `TestMain` starts envtest only when an envtest case runs, so this set does not need `KUBEBUILDER_ASSETS`. One serial run, Go 1.27.3 (`toolchain go1.27.3` in `go.mod`), shared test lock and cache, `nice -n 19`, `GOMAXPROCS=2`, `-p 1`, `-count=1`:

```
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env \
  GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local \
  go test -p 1 -count=1 -timeout=180s ./internal/apiserver \
  -run '^(TestGrantDecisionReachIsTheDelegatesTree|TestGrantDecisionDelegateSessionDecides|TestGrantDecisionRefusesIneligibleDelegates|TestGrantDecisionRefusesTheRequest|TestGrantDecisionCoordinatorReadsAndMaintenanceGate|TestGrantDecisionCoordinatorGetRequiresDirectChild|TestGrantDecisionCombinedNamedCoordinator|TestGrantRequest|TestGrantName)$'
```

`TestGrantRequest` and `TestGrantName` are the unchanged production name checks in the same package. Receipts `OP323-123940-FIXTURE-TEST.json` and `.log` belong to the parent. This note does not report a pass.

## Custody

Uncommitted allowlist for the parent, and nothing else:

- `internal/apiserver/grant_decision_test.go`
- `.agents/sagas/distributed-dev-env/verification/op323-fixture-uniqueness-2026-11-01.md`

Do not stage `.task-inputs/sealed-op323-source-slot.md`. No quota or account observation is in this record. Deployment and access flags stay disabled. Integration review and a new required CI run on the successor head remain the merge gates.
