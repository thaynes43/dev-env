# Coordination retention bound wording: source-only evidence

Story: `C7-R4-BOUND-WORDING-1015` (#317). Date: 2026-10-15 UTC.
R4 is the source-read delivery unit in DESIGN-024 and backlog 11; C7 is the
Chapter 7 Codex coordinator story prefix, not a runtime feature.

This is a bounded source-only wording correction. It does not implement, enable,
install, accept, or otherwise claim coordination archive-backed retention.

## Pickup and baseline

The reserved worktree was clean on branch `agent/dev-env-1015-152848` at accepted
base `f362fad1af31b43e5182c1a3bf40917e83dae8f0`. The sealed order and reservation
hashes matched `reservation-seals.json`. The separately retained worker-start record
has the actual TUI model/effort and the 15-minute checkpoint and 25-minute hard stop.

Before this correction, these source hashes and snippets stated that all
coordination bounds were permanently occupied:

| Path | SHA-256 | Baseline disposition |
| --- | --- | --- |
| `internal/apiserver/apiv1/types.go` | `a2632197bf3be17a54547798f4c630c71ea4f384152ab670e9a53ac50ae8a45b` | `CodeLimitExceeded` described both coordination Session and project bounds as lifetime because records are never deleted. |
| `internal/taskclaim/metrics.go` | `b76b3d181a53eeda106ee035edbfb6ea6fbef8c707842286101f68d8fd5b00df` | The `dev_env_coordination_records*` help text said records are never deleted and both scope limits were lifetime. |
| `internal/agentd/contextpublication.go` | `d5e9843dda2175a8ca6bf330db47136e70a3c52e5a6cee69a9605cc7aee7b861` | `ErrContextLimit` said the publication bound did not free up and retrying could not succeed. |

Those statements conflict with accepted ADR-004 and DESIGN-024 section 7 for the
project-bound wording: Session bounds remain lifetime, while project counts are
live and could free only after enabled archive-backed retention succeeds for
eligible records no sooner than 30 days after required Sessions are absent. The
installed retention path remains off and unaccepted, so this wording does not
promise activation, automatic retries, immediate release, or relief while an
owner remains present.

## Change and validation

Only the three source wording seams and this verification record are in scope.
The source patch preserves error identity, metric names and labels, numeric limits,
collector filtering/counting/pagination behavior, and context-client request and
retry behavior. No new prose-mirroring test was added.

At the worker checkpoint (2026-10-15T19:32:31Z), the collector value/label/count
test passed and the context-limit test failed its existing `do not retry`
assertion. The worker correctly preserved its four-path boundary and stopped.
The coordinator-authored required text had omitted that valid phrase; this was
a copy-planning error, not a stale or invalid test. The historical candidate
and its failed output remain recorded.

The coordinator revised the message to say `do not retry immediately` while
retaining the conditional explanation of lifetime Session and live project
bounds. No test was changed. The final direct constrained selection passes
**2 methods / 6 nodes**: collector values/labels/counts and all four context-limit
refusal cases, including sentinel matching, exclusion of the server-body canary
and preservation of the no-retry instruction. This validates existing behavior;
no test mirroring the new prose was added.

Use cached Go 1.27.2 and existing dependencies, without downloads:

```sh
flock -w 60 /tmp/dev-env-go-tests.lock nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -p=1 -json ./internal/taskclaim ./internal/agentd -run '^(TestRecordCollectorCountsPerProjectAndSession|TestExplicitContextClientReportsPermanentLimit)$' -count=1 -timeout=60s
```

The selection does not invoke the held TTY test or an envtest suite. The
worker's combined output summaries remain historical, not reconstructed raw
JSON. The coordinator captured the command's direct JSON stream, command and
environment under `/home/dev/work/orders/dev-env-chapter7-bound-wording-1015/`;
these PVC artifacts are supplemental, not publicly fetchable source artifacts.
The committed baseline descriptions, tests, command, hashes and ordinary public
PR checks/reviews provide portable evidence.

Raw JSON SHA-256: `189ef9cfe7438c35add2b0087c0dc08c4e529fee55853c0aac0ed1708ae66217`.

| Tested source | SHA-256 |
| --- | --- |
| `internal/apiserver/apiv1/types.go` | `4fc920e15dd16e789709963ff4bef15d5349ac9a8c1aa5cba07ac6c1a74c725b` |
| `internal/taskclaim/metrics.go` | `b18978189a6987eb0e5df40721965582e002f2f8de958ac72b1298f83811613c` |
| `internal/agentd/contextpublication.go` | `3abfd0194f9235c07387747f555fdf516a7f2cafd42aa6916d5e389fb650149e` |

Native review of `37013db31b6b7e81d01e98cfef39f0a8ebe7ed32` found a stale
metrics header still describing project bounds as permanent. The coordinator
corrected that comment to match the conditional retention contract. This final
comment-only correction does not change executable behavior. The source hashes
above attest the preceding tested bytes; final exact-head review and ordinary
CI cover the later comment and verification record. The finding and its
closure remain recorded rather than erased.

The one worker test failure and all historical counters/deviations/deadline
records remain preserved; no runtime activation, source-merge permission
expansion, manual retry or budget extension follows from the correction.
Independent native source review and exact-head ordinary required CI/advisory
finding disposition must be inspected before guarded source acceptance. This
slice neither accepts full R4 nor installs or activates retention, access,
credentials, providers, archives, deployment, v1 changes or cutover. The #301
denied-rerun and dependent R3 holds remain unchanged.
