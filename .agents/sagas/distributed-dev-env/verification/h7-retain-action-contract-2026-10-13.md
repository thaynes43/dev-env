# Prepared one-time history PV Retain action

**Status: executed once; PV Retain accepted; the attempt is consumed.**
[Q-37/D-91](../designs/025-private-home-expiry.md#22-one-time-pv-retain-q-37-d-91)
permitted one exception to DESIGN-025 section 7's default owner-only PV rule.
The [execution acceptance](h7-retain-acceptance-2026-10-13.md) records the
source-corroborated owner "Yes", sole launch and verified postcondition.
No further executable launch or PATCH is authorized. The following protocol
is the frozen contract for that consumed operation, not a reusable grant.

## Fixed action and authority

The approved action changed only `persistentVolumeReclaimPolicy` from `Delete`
to `Retain` on `pvc-ec80a337-740f-49d3-8b27-171bb6eed5af`. It uses the existing
Mac configuration and context already used for H7 evidence. Credentials stay
on that machine. It creates no persistent permission, pod PV-write grant or
new reader, and does not adopt the dynamic PV into Flux. No archive mount,
feature activation, disk deletion, v1 change or failed-reader retry follows.

The fresh preflight must find the exact PV UID
`92143b2b-e065-4511-9310-2a5d8c069192`, resourceVersion `828181553`, phase
`Bound`, policy `Delete`, no deletion timestamp, and claim
`dev-agents/dev-env-history-archive` with UID
`ec80a337-740f-49d3-8b27-171bb6eed5af`. The fixed JSON Patch tests those seven
identity/state values before its one replacement. Any changed premise,
including resourceVersion alone, refuses this prepared action. A new proposal
would need a new concrete patch and review; do not refresh it to retry.

Default or unknown arguments refuse before configuration or API access. The
explicit owner flag and actual owner-answer receipt digest are procedural
provenance, not cryptographic consent. The coordinator must independently
verify authorization and maintain the one-attempt ledger across processes,
sessions and restarts. The executable's budget is per process and cannot
prevent someone from launching another copy; another copy is not authorized.

## Bounded execution and outcomes

The executable permits at most three attempted requests to the one fixed
endpoint: one fresh GET, at most one PATCH, and one postcondition GET. Each
request has a five-second limit within a thirty-second process budget. It
caps each response at 512 KiB and total response bytes at 2 MiB, with bounded
configuration, headers and parsing. TLS is required. Redirects, proxies,
connection reuse, automatic replay, credential plugins and fallback paths are
refused. Reports contain finite codes and safe identity/state fields; never
copy credentials, raw API responses or transport errors into a receipt.

| Outcome | Coordinator action |
|---|---|
| `NotAuthorized` | No configuration/API access occurred. Obtain the actual owner decision; do not manufacture its receipt. |
| `PreparationRefused` or `PreflightRefused` | No PATCH was sent. Preserve the refusal and report the failed prerequisite; do not repair or refresh it into a retry. |
| `DefinitiveWriteError` | The bounded PATCH response was an explicitly classified API rejection. Preserve it; no second PATCH. |
| `AppliedVerified` | The successful PATCH response and named postcondition GET establish the same bound PV and claim with `Retain`. Record the receipt; remaining H7 gates stay separate. |
| `RetainObservedAfterAmbiguousWrite` | `Retain` was observed, but write causation is unproved. Preserve that uncertainty; do not claim normal success or send another PATCH. |
| `UncertainAfterWrite` | The PATCH may have committed or may still commit. Even a subsequent `Delete` observation cannot prove cancellation. Preserve the attempt and stop; no retry, rollback or second executable launch. |

Tom is asked only for a real follow-on decision if a preserved outcome requires
one. He is not asked to run diagnostic commands. A failed or ambiguous attempt
never silently becomes a new authorization.

## Executable provenance and final gate

PR artifacts are review candidates only. The executable of record must come
from the passing `push` to `main` run of
`.github/workflows/h7-retain-action.yml` at the merged, reviewed commit.
Before asking for execution approval, independently verify GitHub's run event,
repository, branch, exact source commit, workflow path and passing fixture/build
steps; then verify the downloaded artifact digest, every source hash, binary
size/hash and Go build information against that commit. Record those values
outside the artifact. The bundled manifest alone is insufficient provenance.
This trusts GitHub's authenticated source/build/artifact delivery; it does not
claim a signed attestation. No artifact is executed during these checks.

The actual owner answer is the final execution gate against that concrete
verified executable and fixed target/patch. The later executor must verify
that answer's receipt digest and preserve the sanitized one-attempt report.
The existing GET-only checker and held cluster reader remain unchanged.

This action alone does not establish installed admission, complete baseline
freshness, personal owner identity, F4 history evidence, either cold-resource
measurement, restore, archived reads or deletion acceptance.

## Verified executable and completed operation

Dev-env [#269](https://github.com/thaynes43/dev-env/pull/269) merged at
22:57:19 UTC as `789f2b499930dadf2cf02a440b56a20d224ae9b8`, after required CI,
offline fixtures and no-findings advisory/independent reviews at
`fcd4bfa9403618e18465bc59ea916da31f7b6a46`. The initial unused-import compile
failure is preserved; it ran no fixtures and produced no artifact.

[Push-main run 38481175509](https://github.com/thaynes43/dev-env/actions/runs/38481175509),
attempt 1, passed the offline fixtures, build and artifact upload at that merge
commit. External API metadata established the event, repository, main branch,
source commit and workflow path. Artifact `11857325032` is 5,676,929 bytes with
ZIP SHA-256 `b6ff8b54545e85e71f3b66da73e2b294c124aec44f8853d4e841bd533ff2aef7`.
All eight source hashes and the workflow hash match the merged commit. The
Darwin ARM64 binary is 9,938,402 bytes with SHA-256
`22867b8b277df64f6899f75ee6b58c0705b8a98b1f4cf6c1f4b03aed62dc0694`.
Its embedded Go 1.27.1 build information matches the recorded build output,
including the merge revision and `vcs.modified=false`. Direct parsing followed
the [pinned Go build-info format](https://github.com/golang/go/blob/go1.27.1/src/debug/buildinfo/buildinfo.go#L178-L255)
because this pod has no local Go metadata reader; the executable was not run.

Private receipt `dev-env-h7-retain-main-artifact-1013/main-artifact-verification-r1.json`,
SHA-256 `acdf43190c33fe2a2400dddfbe0147d64a8e7e6ca89257f0be320a22582e068b`.
The first verification stopped at the missing local reader after hash checks;
the artifact was preserved and verification completed by reading its metadata.
That was not a client execution or cluster attempt.

Q-37 was later answered "Yes" through the source Mac conversation. D-91
records the one-time exception; the reviewed client completed once and
verified Retain. See the [acceptance](h7-retain-acceptance-2026-10-13.md).
The attempt is consumed. No permanent permission or later activation follows.
