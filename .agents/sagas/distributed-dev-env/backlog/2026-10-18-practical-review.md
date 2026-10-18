# Story: practical baseline review, 2026-10-18

Saga: [distributed-dev-env](../README.md). Epic: owner-testable v2 under [plan 11](11-project-workspaces.md), with bounded corrections to the already completed MVP source pass.

Status: Review and local corrections complete; hosted delivery tracked in the associated correction PR. Owner: Codex Grok Review chat, root reviewer. Baseline: `3507f1f16a861acb65c2418d0fc3019608894587`; initial current main: `8e6183afde28b8e28a1e19a2a35c8308d0a31481` (prompt SHA documentation only).

## Scope and acceptance

Review the completed source pass pragmatically. Check its claims against code, GitHub and isolated execution; reproduce and correct concrete baseline user-impacting defects or materially incorrect handoff claims. Deliver a concise claim/evidence matrix, concrete acceptance checks, readiness assessment and durable evidence. No new features, broad refactors, speculative hardening, forward MVP gates, cluster writes, session restarts, new credentials or access expansion. Grok owns forward implementation. Release #369 and drafts #292/#231 remain owner-held.

Acceptance: report source/tested/installed/usable states separately; record tests actually run and limitations; correction PRs require `CI - Success` on their current head, independent native review and disposition of every claude[bot] finding before squash merge. No deployment is included.

## Dependencies and ownership

Baseline and handoffs are fetched. Open PR inventory was inspected: no open forward implementation PR overlaps, only release/dependency PRs and the two held drafts. Existing supplied cwd is absent in this runtime; preserve all other worktrees and sessions. Changes belong only to branch `agent/practical-review-1018`, worktree `/home/dev/work/dev-env-practical-review-1018`, and isolated temporary test files. Canonical clone is fetch-only. Supporting evidence: `/home/dev/work/orders/dev-env-practical-review-1018/`.

Independent read-only review assignments may run in parallel: (A) retention/archive end-to-end behavior, (B) idle drain/grant CLI/pilot command behavior. Workers report concrete defects and reproduction plans, do not edit product/user-facing documents, do not run tests without root serialization, and do not touch live resources. Root owns corrections, test execution, readiness synthesis and PR delivery. No CPU burners, stress tools or wide/looped tests; one suite at a time, `nice -n 19`, `GOMAXPROCS=2`, `go test -p 1`, shared test lock; reuse existing envtest assets. Runtime provider tests, if needed, only `gpt-6-luna` or `claude-haiku-4-5`.

## Proposed budget and stop

One bounded review session, target 90 minutes elapsed, checkpoint at 45 minutes; maximum concurrency root plus two read-only Sol reviewers, no parallel test suites. API cost/remaining plan quota unknown; no paid API calls authorized. Native workers use requested GPT-6.1 Sol ultra, each bounded to 20 minutes. Root driving model/effort is not exposed for verification or modification by current tools. Stop at acceptance, a real dependency/approval blocker, explicit pause or session budget/context threshold; preserve branch/commit/PR, evidence, findings and next safe step. Do not automatically begin forward work.

## Handoff

Three baseline defects reproduced and corrected: retention hold transport, archived acknowledgement listing, and lowercase MCP protocol serialization. HANDOFF #370 release ambiguity corrected. Focused six-package checks and isolated API-server retention/drain checks passed; independent Sol review found no actionable issue. [Claim/evidence matrix and acceptance checks](../verification/practical-baseline-review-2026-10-18.md). Required CI, bot finding dispositions, exact final head, merge and automatic publication are recorded in the associated correction PR conversation. No deployment or forward gate is authorized. No cluster changes performed. Max login check passed (27 days remaining).
