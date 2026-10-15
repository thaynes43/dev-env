# Coordination bound comments — 2026-10-15

Story C7-R4-BOUND-COMMENTS-1015 (Chapter7 source story #320), under epic #175
and backlog11 R4, corrects two comments identified by PR318’s advisory review.
The accepted prerequisite is main `51c0256fa4de9e9f02358374c905800196260a0f`.
The governing contract is [DESIGN-024 section7](../designs/024-coordination-record-retention.md#7-bounds-and-alerts).

The `Limits` comment in `internal/taskclaim/store.go` and the metrics wiring
comment in `cmd/dev-env-operator/main.go` previously said both bounds were
lifetime because records were never deleted. They now distinguish lifetime
source Session bounds from live project record counts. Enabled archive-backed
retention can free project capacity only after eligible records meet the
minimum 30-day absence requirement for their required Sessions; this promises
neither an exact-day release nor installed or enabled retention.

Only whole-line Go comments and this verification record change. Removing
whole-line comments from each before/after Go source leaves identical bytes.
The diff preserves executable behavior, limits, metric names and labels, source
ownership, API responses and retry behavior. No test was added or rerun for
this comment-only correction. Ordinary exact-head required CI and independent
native source review, with available advisory findings inspected, remain the
source acceptance gates; source acceptance is not yet claimed by this record.

| Source | Accepted-base SHA-256 | Corrected-source SHA-256 |
| --- | --- | --- |
| `internal/taskclaim/store.go` | `fd334c45fab692e4cca967c2009549944d8179c988810ee3f6e73d2664287122` | `a2c05b1f7fec3cf03a36e34314da7418ccedab6bb1e163fdcee62675828756db` |
| `cmd/dev-env-operator/main.go` | `b76ba5e3d599c9dfb3570645b8b6c0db1215848c2846e35b92dfbb47c54f1352` | `033d545301d3ed41bfb5ddbdb0d791ad8cc82c666df05f6d42b1b7c22c95bc9c` |

Portable verification: compare the two Go files against the accepted base,
confirm only comment lines changed, then inspect the public PR’s exact-head
CI and reviews. The supplemental PVC receipt at
`/home/dev/work/orders/dev-env-chapter7-bound-comments-1015/` records actual
pickup, finite deadlines and the byte comparison; it is not a public artifact.

This source-only correction does not accept full R4, install or activate
retention, or authorize runtime, access, auth, credentials, GUI, provider,
archive deletion, deployment, v1 changes or cutover. The #301 permission-denied
rerun boundary and dependent R3 hold remain unchanged. All historical failure,
deviation, deadline and stall records remain preserved.
