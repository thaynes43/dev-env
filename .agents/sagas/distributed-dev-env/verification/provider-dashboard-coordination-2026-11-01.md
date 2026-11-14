# Provider allowance dashboard: blocked coordination checkpoint

Recorded 2026-11-01 UTC for [OP323](https://work.haynesnetwork.com/work_packages/323),
under [Epic187](https://work.haynesnetwork.com/work_packages/187) and Saga85.
This is evidence and a handoff for the existing work, not a new backlog.

## Scope and custody

The current user work order prioritizes provider allowance/reset presentation
and visual token/cost drilldowns. Grok owns implementation through the existing
authenticated Cursor CLI on host `dev-env-1027-010058`. This session coordinates
integration; independent Codex review may support it. Claude is limited to
automated PR reviews. OP321/322 are secondary and were not started.

Coordinator: `dev-env-1101-032738`, branch `agent/dev-env-1101-032738`, worktree
`/home/dev/work/dev-env-1101-032738`. The clean initial HEAD and freshly fetched
`origin/main` both resolved to `ab0ec10a22e6a538713fad624085c4921641ce50`.
OP323 was claimed with its observed lock version and read back with this session
as Execution owner. OP305 remains In review; OP309 and OP315 remain Accepted.
Historical provider/admission constraints remain in their canonical records.

The campaign has a 90-minute maximum, at most three Cursor processes, serial
bounded test suites, and at most one independent Codex reviewer. The private
usage ceiling in the work order remains binding; account receipts are excluded
from this public document. Stop at the first transport/access dependency that
cannot be resolved within existing authority, an explicit pause, the budget
threshold, or completed acceptance. Do not start a successor story at that stop.

## Verified transport blocker

The current AgentSession's recorded parent is `dev-env-1027-010058`; its own
profile is `workbench`. A read-only Pod lookup finds the parent Running with
container `agent`. However, this pod's Kubernetes identity returns `no` for:

```sh
kubectl auth can-i create pods/exec -n dev-agents
```

No remote exec, Cursor launch, credential copy, login, grant request, RBAC
change or alternate execution route was attempted. There are **zero** campaign
Grok sessions, worktrees, branches, PTYs or controllers to claim or abandon.
No implementation START/code/test receipt exists.

The supplied raw account receipt and Cursor meter result are not present in
this pod's worktree. Their contents have not been independently inspected here.
The work order reports a prior legacy-parent inbox 404; that route was not
retried. The signed 2.78 client's supported inbox list for this session succeeds
with an empty thread. This establishes a read capability, not upward delivery
or permission to start a Claude coordination turn.

**Pending transport question for the designated coordinator:** what existing,
authorized non-Claude controller on host010058 can own the three fresh Cursor
contexts and return their correlated ACP requests and code/test receipts?
If none exists, an exact bounded transport decision is needed. No new access is
assumed. This question is preserved in OP323 for permitted coordinator pickup;
neither delivery nor an answer is claimed.

## Published source inventory at the pinned base

| Existing source | Observed boundary | Consequence for the implementation |
| --- | --- | --- |
| `internal/providerallowance/types.go`, `validate.go`, `derive.go`, `registry.go` | Existing typed observation/read contracts, source validation, caller-supplied account/entitlement trust and reset/freshness semantics | Reuse these contracts. An incoming receipt cannot establish its own trusted binding. Do not add a second registry. |
| `internal/providerallowance/claude_statusline.go` | Existing passive statusline adapter | Preserve optional windows and original age; no manual Claude inference or observer activation. |
| `internal/agentrun/usage.go` | Default local preflight returns three unconfigured provider statuses; explicit file/statusline inputs are separate | The default command is not a live collector read. |
| `internal/agentrun/usage_export.go`, `internal/usageconsolidation/report.go` | Bounded caller-provided allowance snapshots and retained initial-invocation projection | Preserve provenance, exact invocation identity and accounting exclusions; token/cost receipts do not establish allowance. |
| `internal/agentd/codex_allowance*`, `codex_owned_allowance*` | Source for bounded native/owned diagnostics exists | Source presence grants no runtime activation; held PR556/609 are not integration dependencies to revive. |
| `web/portal/session/product_allowance.go` | Pure mapper covers `anthropic` and `openai`; comments state production has no allowance reader | Cursor must be added without manufacturing quota. Mapper trust/conflict refusals must remain. |
| `web/portal/render.go` | Allowance view normalization fixes the visible provider set to Claude and Codex | Updating the mapper alone will not make Cursor visible. |
| `web/portal/session/product_activity.go` | Both page projections use disconnected allowance views; usage covers retained initial invocations | No authorized live allowance connection is established by these page paths. |
| `web/portal/views.html`, `views.css`, `preview/product_states.go` | Existing templates, styling and deterministic allowance previews; usage breakdowns are tables | Reuse these assets for cards, accessible visual summaries and optional detailed tables. |

No standalone collector/server is established by this inventory of published
main. Locate the already-authorized implementation/artifact before connecting
one; do not build a replacement or silently revive held branches.

## Prepared ownership split, not dispatched

All three contexts must freshly fetch/pin source and preserve unrelated WIP on
host010058. Suggested branch names below are reservations in this handoff only;
none has been created or claimed as running. Recheck tracker/file ownership
before launching.

| Lane | Branch suffix and exclusive write paths | Required first evidence |
| --- | --- | --- |
| A: contract/adapters | `provider-dashboard-a-1101`; `internal/providerallowance/**`, `web/portal/session/product_allowance.go`, `web/portal/session/product_allowance_test.go` | Inventory exact available source interfaces, agree the existing read-to-view boundary with B, then show a diff and focused adapter/mapper test result. No provider call, new collector or registry. |
| B: dashboard | `provider-dashboard-b-1101`; `web/portal/render.go`, `views.html`, `views.css`, `render_product_test.go`, `preview/product_states.go`, `preview/product_states_test.go`, `session/product_activity.go` | Agree typed display fields with A before implementation, then show a rendered synthetic card/drilldown and render-test result. Keep real disconnected states explicit. |
| C: integration | `provider-dashboard-c-1101`; new `internal/providerallowance/dashboard_edges_test.go`, `web/portal/session/provider_dashboard_integration_test.go`, `web/portal/provider_dashboard_render_test.go` only | After A/B agree their interface, add independent reset/identity/render regressions and provide focused results on the integrated head. Do not edit A/B files. |

Acceptance includes used/remaining percentage only when the provider reports a
compatible denominator and direction; actual window identity and duration;
reset instant, timezone and countdown where available; original observation
time, source, freshness and confidence. Monthly, rolling, multiple and absent
windows remain distinct. Past reset expires the affected window's current value;
historical values and still-current sibling windows remain preserved. It never
creates a zero-used sample. Missing, stale, partial, unsupported and conflicting data
remain visible. Synthetic previews never appear as live account data.

C checks account/window identity, duplicate receipt/retry/reclaim handling,
seconds versus milliseconds, reset boundaries/DST, incomplete reads, mobile,
keyboard and accessible non-color labels. Actual time trends require actual
historical observations; do not invent a series from one snapshot. No remote
assets, heavy chart dependency or CSP/auth/privacy widening is needed.

The coordinator handles integration seams only after Grok implementation
receipts, obtains exact-head independent review, resolves findings and required
CI, then uses the normal guarded PR/release/GitOps route within actual authority.
New provider access remains a separate concrete activation decision.

## Verification and ending state

The existing signed 2.78 CLI was invoked once as `usage -o json`. A Python check
passed: schema `providerallowance.read.v1`, zero items, exactly `cursor`,
`anthropic`, `openai`, each `unconfigured`, unknown freshness/binding, no
last-good value and an explicit `no_provider_request` attempt code. This is a
baseline missing-state check, **not** new implementation or live-provider proof.

Go is unavailable on PATH and absent from the conventional toolchain paths
checked here. No Go suite was run and no toolchain was installed for this
transport-blocked checkpoint. No dashboard code changed; no build, deployment
or user acceptance is claimed.

PR609/556, PR705/OP273, Ask4418, private-adapter/observer holds, the RD2 rollout
owner `haynes-ops-1101-024520`, and the cache-filter owner
`haynes-ops-1101-030129` remain untouched.

**Next safe step:** the designated coordinator resolves the existing-host
controller custody/transport dependency. Resume the same bounded campaign only
with verified in-scope direction; dispatch A/B, then C once their contract is
agreed. Recheck elapsed/usage budget first. OP323 and Epic187 remain unaccepted.
