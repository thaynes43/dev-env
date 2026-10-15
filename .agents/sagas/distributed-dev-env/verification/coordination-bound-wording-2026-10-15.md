# Coordination retention bound wording: source-only evidence

Story: `C7-R4-BOUND-WORDING-1015` (#317). Date: 2026-10-15 UTC.

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

The collector value/label/count test passed under the pinned cached Go toolchain,
offline environment, low CPU settings and shared test lock. The existing context
limit test reached its final literal assertion and failed because it requires the
old substring `do not retry`; the required message says `An immediate retry will not
help`. This is a stale prose assertion, not a changed retry/client behavior. The
four-path reservation does not authorize changing that test or changing the required
message. Focused raw command output, environment, and post-change source hashes are
recorded in `/home/dev/work/orders/dev-env-chapter7-bound-wording-1015/` alongside
the sealed order. This candidate requires Chapter7 coordinator acceptance; it is
neither R4 nor installed acceptance and has no release, deployment, archive
activation, deletion, v1, cutover, access, credential, or provider effect.
