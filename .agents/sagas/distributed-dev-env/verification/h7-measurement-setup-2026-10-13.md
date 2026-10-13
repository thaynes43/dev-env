# H7 resource measurement source setup — 2026-10-13

This is a source prerequisite checkpoint for DESIGN-025. **Neither cold
resource measurement has run.** No fixture Job, artifact image, archive mount,
provider call or cluster write was made. V1 and all retained canaries and failed
Jobs are preserved. The next ordered H7 GitOps unit remains the compatible
archive guard under [ADR-007](../adrs/007-preserve-v1-archive-trust-boundary.md).

## Corrected fixture contract

Initial source implementation from `agent/dev-env-h7-measurement-fixtures-1013`,
head
`f1c2d8c4a4084f0fc18bd93acdb7d5e38dc51367`, changes six source/test files:

- Export target means **total exported file bytes**, including the existing
  synthetic base. The future maximum fixture is exactly 2 GiB, not 2 GiB plus
  base files. The separate production 64 MiB manifest/content limit and tar,
  block and index overhead stay separate from that file-byte bound.
- Explicit prepare/reuse modes freeze private synthetic inputs and size/hash
  metadata. Reuse rejects unsafe roots, changed control files, links and
  unexpected file kinds. Construction is excluded from a measured phase only
  by preparing in an earlier process and cgroup. Failed preparation preserves
  its unsealed root as evidence; it cannot be reused or prepared again. A
  separately authorized future attempt needs a new root, with no automatic
  retry or deletion of the failed inputs.
- Controller measurement isolation requires explicit opt-in and exactly
  `^TestHistoryRelayMeasure$`; unsupported selectors and testing modes are
  refused before envtest or API setup. Ordinary controller tests still use
  their normal envtest setup. The relay retains its production 16 MiB cap,
  including the final newline; the deterministic fixture never raises it.
- Measurements report total process RSS/high-water and self-cgroup memory
  counters beside phase deltas. Missing counters are explicit errors. Parent
  cgroup substitution, reset peaks and delta-only acceptance are disallowed.

## Focused validation

The native worker used exact cached Go 1.27.2 under the shared test lock,
`nice -n 19`, `GOMAXPROCS=2`, package concurrency 1, test parallelism 1 and
count 1. Private-root and self-cgroup safety checks passed. Export arithmetic,
actual 6 MiB fixture preparation/reuse and the existing allowlist regression
passed. The isolated controller test passed with an 8192-byte prepared/reused
fixture, bounded canonical shapes, production cap+1 rejection before put and
opt-in/flag misuse checks. These small functional checks are not cold-limit
acceptance. Formatting and diff checks passed; hosted required CI and
independent review govern merging [PR #262](https://github.com/thaynes43/dev-env/pull/262).

The first checks exposed final-symlink following by `os.Root.OpenFile`,
insufficient padding for a bounded long-path fixture and a requested 3 MiB
total below the existing synthetic base. The implementation uses anchored
descriptor-relative `openat` with `O_NOFOLLOW`; bounded shape tests and a
6 MiB total fixture passed after those fixes. Earlier failure receipts are kept.

Local receipts on the work-order PVC:

| Receipt | SHA-256 |
|---|---|
| `dev-env-h7-measurement-fixtures-1013/implementation.json` | `94c0282ae7ef556854ed68fc41ccb5892d860ed66fdf8cc61d58422a0852de8f` |
| `dev-env-h7-measurement-fixtures-1013/next-execution.json` | `c0283b96193d5c240608c088beac4aba12a94f23582041e58944244ba87d5a39` |
| `dev-env-h7-resource-preflight-1013/measurement-plan.json` | `e683337f6fda4d2e944f5c4627185d185469b0eef21c6741d88bf944d7320cd4` |
| `dev-env-h7-resource-preflight-1013/live-resource-baseline.json` | `e6368b558e96b859d3b74ab446b3b5a2596b9bd7b322d8c175d29b6cd10c003c` |

## Execution still required

Build immutable fixture artifacts in hosted CI from reviewed merged source and
Go 1.27.2, recording signed image and binary digests. No local image build or
compiler belongs in a measured Job. Use the production 2.27.0 agent for the
export/archive path; the current 2.9.1 shelf lacks that CLI.

Prepare synthetic source, archive and receipt volumes separately from the real
history PVC. Measure each phase in a fresh limited cgroup: prepare, **plan**, then
stream+put with independent hold and shelf containers over a bounded FIFO, then
fresh verify, and a separate exact 16 MiB relay. Production stream's internal
replan/hash/reread is part of its cost. A fake-exec relay proves allocation at
the cap; it does not establish live network or webhook behavior.

| Measured container | CPU limit | Memory limit | Additional setting |
|---|---|---|---|
| Hold S plan/stream | 2 | 4 GiB | `GOMAXPROCS=2` |
| Shelf put/verify | 250m | 256 MiB | installed shelf ceiling |
| Operator relay | 500m | 256 MiB | `GOMEMLIMIT=200MiB`, `GOMAXPROCS=2` |

Every init/helper also needs a CPU limit, bounded runtime and receipts. Jobs use
an eligible worker node, nonroot execution, no API credential or secret input,
`automountServiceAccountToken: false`, network denial, no retries and no automatic
cleanup. Do not run CPU burners, stress tools or wide test loops on shared nodes.

Fresh processes alone do not establish cold cache. Review a synthetic-file-only
cache eviction/residency helper and verify residency before each plan, stream
and verify phase. No global cache drop or claim that all Ceph caches are cold.
Record actual cgroup CPU/memory ceilings, peaks, throttling, OOM/events and
external termination/image/node provenance; absent evidence is not a pass.
If cache evidence fails, stop that acceptance attempt without an automatic retry.

F4's 34 unclassified names, owner-set PV Retain, compatible guard admission,
default-off flags and non-canary H8 installed read acceptance remain distinct
gates. This setup permits no history activation or deletion.
