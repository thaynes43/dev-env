# OP323 native capture to retained export seam

This source-only test connects the existing synthetic native capture to the
actual `agent-run usage-export --allowance-read` command. It changes no
production code and adds no collector, store, scheduler or provider access.
It does not establish installed or live allowance acceptance.

The source base is `577a7501bbad584f40b1190fb91c7fccfb234bab`, which includes
PR #836. The new test is `TestNativeCaptureRetainedByUsageExport` in
`internal/agentd/codex_allowance_export_test.go`.

## Boundary exercised

The test calls the existing capture function with a qualified synthetic host
and `ReadCodexNativeAllowance`. An in-process Unix socket implements
`initialize`, `initialized`, `account/read` with refresh disabled, and
`account/rateLimits/read` with reset-credit details excluded. It supplies
keyed and legacy primary/secondary windows, fractional percentages, a known
zero, reset instants, a reset-credit count and synthetic private account data.

The captured `providerallowance.read.v1` document is retained to a new 0600
file. The actual `agentrun.Run` export path reads that file alongside one
synthetic Session served over loopback TLS. It makes one authenticated GET,
without a retry, process launch, port-forward or environment discovery. The
export directory is 0700 and both output files are 0600.

The exported snapshot must equal the retained document. Assertions preserve
source kind/version, capture and receive times, evidence references, all six
window measures, decimals and resets. Freshness and account binding remain
Unknown; provider observation time, account/entitlement references and
`currentMeasures` remain absent. No token aggregation or conversion into
`codex.get_usage_limits.current_source.v1` occurs. Synthetic private account
strings, bearer, prompt, limit id and reached type must not escape into the
retained file, export or command output.

This complements the existing native RPC and generic allowance-file tests;
it does not replace them. No production defect was found that required glue.

## Validation

Grok authored the test. The coordinator verified the frozen patch and test
file hashes, inspected the full test, and integrated it unchanged. The
existing parent runner executed the exact frozen test with Go 1.27.3,
`GOMAXPROCS=2`, one package and the existing test lock:

```sh
go test -p 1 -race -count=1 -timeout 120s -v \
  -run '^TestNativeCaptureRetainedByUsageExport$' ./internal/agentd
```

The test passed on 2026-11-02 at 00:56:04 UTC (test 0.09s; package 1.142s).
The tested file SHA256 is
`bca4ef3e87f668431b99f7ca7d3d921af1defb0695aa49f813fb1d4cd9042ae2`.
`go vet -p 1 ./internal/agentd` also passed on the same unchanged test file.
Hosted CI and any publication are separate checks.

## Remaining acceptance

The caller still supplies the retained file. A five-minute schedule is only
a proposal; no recurring acquisition is implemented or activated here.
Capture time does not establish provider freshness, publisher identity does
not establish an account, and an unattributed Codex window is not all-provider
quota. Retained windows remain separate and are never summed.

The `OP323-ACTIVATION-211804-INACTIVE` packet remains blocked on its specific
runtime approval and fresh permitted input. The optional CLI reader is not
an approval prerequisite. Existing OP273/705 observer, canary, probe and
credential holds remain. No flag, ConfigMap, RBAC, mount or live PUT changed.

A future UI should label each retained window, percentage, reset instant,
timezone and source freshness explicitly. A countdown is derived for display
and must not replace the stored reset. Missing data stays Unknown.
