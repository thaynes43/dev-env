# Story 190 C3 native allowance source ACK

- **Task marker:** `usage190-native-codex-c3-20261026-01`
- **Task:** implement the minimal on-demand Codex-native allowance reader that feeds
  the existing C1/C2 provider-allowance and `agent-run usage-export` contract.
- **Branch:** `agent/usage190-native-codex-c3-20261026`
- **Base:** `86cef5bb619bbde29610e3436299a3a4362b7a88` (`main`)
- **Requested model/profile:** Codex `gpt-5.6-terra`, medium, S, full; this source
  task is being executed by the assigned managed Codex session.

## Bounded source scope

Add a finite documented-protocol reader and focused integration/tests only. Reuse
existing allowance DTOs, trust and snapshot semantics, manifest/UID validation, and
the C2 export flow. Preserve Unknown values, all bucket/window/reset data, account
scope presence semantics, and distinct fetch versus provider-observation timestamps.

Excluded: collector/service/daemon/listener, raw connection to another pod, provider
HTTP, auth or identity reads, storage, polling, policy/schema framework, generic RPC
proxy/fallback, grants/access/image/portal/V1 changes, cost or task attribution, and
any permit or spending decision.

## Handoff gates

This source lane stops at a ready draft PR. Simulation tests are not live proof. A
single no-inference native read, if required, is performed only by the coordinator on
its already-authorized existing native socket after source review; this worker does
not create a connection, daemon, or model session to obtain it.
