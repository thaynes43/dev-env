# Story190 C1: small read-report projector

**Status:** source-only projector, 2026-10-25. This is not a collector,
provider adapter, command, API, portal, scheduler, registry, schema platform, or
deployment. It does not accept Epic187, Story188, native188, Cursor, Grok, or
mandatory full-V1 coverage.

## Claude passive callback bridge (2026-10-28)

[OpenProject #191](https://work.haynesnetwork.com/work_packages/191), a component of collector Story190 and Epic187, adds explicit bounded statusline stdin to the unified usage command, retaining historical optional windows with Unknown freshness and account binding. It discards private metadata and installs no producer hook. Supported ingress, installed artifact and live acceptance remain separately authorized dependencies; no subscription coverage is claimed. See [DESIGN-030](../designs/030-provider-allowance-contract.md).

## Included

- a bounded pure Go report over existing validated allowance read snapshots and
  initial-invocation usage observations
- exact caller-provided session UID labels only
- deterministic duplicate/conflict suppression and JSON-safe copied DTOs
- source documentation, focused tests, and the saga/layout pointers

## Excluded

Storage, collection, provider calls, raw sessions or private logs, auth, account
association, aggregation, pricing, availability decisions, HTTP/CLI/RPC wiring,
portal presentation, scheduling, new registry/JSON Schema, and changes to
UsageVersion2 or Story109.

## Acceptance

Focused package tests prove snapshot preservation, Unknown/zero/reset handling,
requested versus served-model provenance, idempotent replay, conflict and
permutation handling, exact/missing/conflicting labels, scope separation, limits,
non-aliasing, JSON roundtrip, and no I/O or clock surface. Run focused
`providerallowance` and `protocol` tests, `gofmt`, layout, and diff checks under
the normal serialized CPU limits. A ready PR and independent exact-head review
remain delivery gates; merge/release/deployment are excluded.

## C2: private on-demand exporter

C2 adds only `agent-run usage-export`: a caller-supplied finite manifest of
exact Session-name and expected-UID bindings, plus optional caller-provided
`providerallowance.read.v1` snapshots. It reads each selected Session once with
the existing provider attribution and direct-v1 capture request headers, no
redirect or retry. It rejects malformed or duplicate selections before a
connection, validates the returned name/UID and direct source UID, normalizes
the existing capture view, and hands validated DTOs to this C1 projector.

The command performs no list, descendant scan, history, log, console, provider,
credential, storage, scheduler, portal, or API change. A refusal, mismatch, or
malformed response stops later selected reads and records only safe coverage
metadata; it never serializes the raw Session or backend error body. Its new
exclusive output directory is mode 0700; `report.json` and `summary.txt` are
mode 0600, created without overwrite or symlink traversal. They are private
bounded evidence, not public artifacts, live monitoring, or full Story190/V1
acceptance. See the saga decision log for the Story190 boundaries.

## U1: usage on the portal Sessions page

Selected 2026-10-26 under the frontend and usage MVP (Stories 178 and 190).
Source only; no deployment, activation, credential, grant, DB or network change.

- **Included:** the operator portal reader's opt-in `view=activity`, the
  DESIGN-029 allowlist of mode, agent state, last activity, requested model and
  the session's own initial-invocation usage; the portal Sessions page that
  requests and renders it with a fixed coverage note; docs and focused tests.
- **Excluded:** account allowance, collectors, storage, schedulers, totals,
  descendant rollups, remote/TUI consumption, history or log reads, and any
  change to the default six-key summary.
- **Acceptance:** without the view, rows keep exactly six keys; with it, Unknown
  stays distinct from zero, requested and served models stay separate, and
  usage appears only for task sessions. The portal accepts a six-key row and
  renders its activity Unknown, and refuses nulls, unknown keys, inconsistent
  Unknown scopes and a non-task mode carrying measured usage. Required CI and an independent
  exact-head review precede merge. A portal image with U1 needs an operator with
  U1; the earlier image keeps working against either operator.
- **Presentation (2026-10-26):** after Tom rejected the first look, U1 renders
  in the approved Astra v2 "Agents" design: dark-first charcoal and lilac, a
  script-free shell over the two existing routes, a Usage view without totals
  and a Planned view for Automations and Watchdog. It includes the narrow
  product-page CSP header that allows only the embedded stylesheet. See
  [portal read boundary](../../../../docs/portal-read-boundary.md).

## Owned observer source (2026-10-28)

The proposed explicitly enabled direct-child allowance diagnostic is a source-only, account-level observation, not a collector, Session/account binding, runtime approval or adoption of an existing daemon. It requires separate exact execution authorization and a qualified artifact; provider freshness and account binding remain Unknown. Synthetic tests and independent exact-head review plus CI qualify source only. Upgraded writers use a bounded cooperative generation lock; older daemons do not participate, and the observer reads but never advances the shared fence. See [owned observer context](../../../context/usage190-owned-allowance-20261028.md).

On uncertain start or stop, the execution owner stops without retry, records the run and lifecycle evidence, and retains the private tmpfs observer directory until a separately authorized existing-host procedure proves the known helper and native tree stopped. A missing PID alone is not proof. Only then may the exact owned directory be cleaned with no-follow and owner checks, leaving ordinary HOME, the shared fence and other processes alone. If proof is unavailable, retain it and report blocked custody. Future live approval must cover this bounded recovery or require a separate exact approval; this source grants neither.

## C3: guarded on-demand source callsite (2026-10-28)

The explicitly enabled `agentd codex-host allowance-read` source calls the
native allowance reader only after current host ownership and configuration
checks, and rechecks them before releasing a sanitized result. It is not a
collector, runtime installation or accepted live capture. The existing
`usage-export` still consumes supplied JSON. Account binding and provider
freshness remain Unknown; runtime use requires a separately qualified host and
callable artifact. See [the callsite context](../../../context/usage190-native-capture-callsite-20261028.md).

## Unified offline CLI seam (2026-10-28)

`agent-run usage -o json` reports local unconfigured acquisition preflight refusals without contacting providers. `--from-file` explicitly reads bounded untrusted cached read.v1 and preserves original timestamps; it does not provide a collector, trusted binding or live three-provider coverage. Cursor/Grok supported quota source and approved scope, Claude passive ingress, and separately held Codex capture remain dependencies. Synthetic tests and exact-head review/CI establish source only.

## Opt-in statusline producer (2026-10-29)

Source only. `claude-statusline-usage` is copied into the agent image and left unwired. It requires an explicit private output path, forwards a native callback's documented windows to the existing `agent-run usage -o json --claude-statusline` consumer, and stores that normalized read. An explicit display argv can stay in front of an existing status command without a settings change. Cached reads stay on `--from-file`. No hook, account, quota poll, or live acceptance is included. See [DESIGN-030](../designs/030-provider-allowance-contract.md).
