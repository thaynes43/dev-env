# Dev-env Cursor handoff — 2026-10-16

**Stopped at Tom's direction to conserve remaining usage.** This document is the
only additional wind-down deliverable authorized after STOP. Do not restart
implementation, tests, reviews, releases or trials from an older queued approval.
Tom will drive the next pickup from Cursor. Laptop MCP/Kubernetes access is owned
by a separate task; this handoff neither grants nor verifies that access.

## What is usable, and what is still unproved

Prior bounded pilots demonstrated two independent real agent environments,
separate HOME/worktrees and fresh task pins, reported loaded rules, actual node
placement, durable attributed claims, duplicate-claim refusal preserving the
original owner, an attributed B-to-A message, peer/v1 preservation and verified
admission closure. These are partial capabilities, not an accepted combined MVP.

Still unproved: the complete two-agent rendezvous, two-way message ACK, A's
published handoff and B's authorized retrieval after original A's actual task
completion. Native claim-output visibility within the required30s is also
unproved. Model task exit0 does not prove semantic success or executor shutdown.
Optional providers, broad UI, full historical migration, archive/expiry and v1
retirement are not universal gates for that small pilot. V1 remains available.

## Accepted source, installed state and closure

- Released **v2.29.3**, tag/release commit
  `71b713e849de52aecb540dc722733514cf5c6d95`, after source [#354](https://github.com/thaynes43/dev-env/pull/354)
  (`df32cabbad2c9afd1b95f3c8037d44140690b5d3`) and release
  [#356](https://github.com/thaynes43/dev-env/pull/356). Required CI/native reviews
  passed; automatic image publication and signatures were verified. No standalone
  downloadable agent-run binary asset was published.
- Installed operator/task remain **v2.29.2**, source
  `c72d2e48f70f0131845dc53f55a7b177e0ac32e1`, template
  `2.29.2-82edced7b5`. The coordinator used separately hashed locally built
  accepted-source CLI/harness artifacts; no v2.29.3 rollout occurred.
- Latest pilot setup [haynes-ops #3979](https://github.com/thaynes43/haynes-ops/pull/3979)
  and [#3980](https://github.com/thaynes43/haynes-ops/pull/3980) were closed by
  [#3981](https://github.com/thaynes43/haynes-ops/pull/3981), merged as
  `31a35472efc694d6db87ae0069ecf2ada3e19ca0`. Both actual Ready operator replicas
  were verified without the pilot argument; global private admission remained
  false. All7 prior Session/Pod UIDs,9 PVCs,3 old seals and v1 were preserved.
  New original A/HOME and the fourth immutable seal remain retained. No temporary
  pilot-policy cleanup is pending; no deletion/revival was performed.

## Failures to preserve rather than repeat

The earlier17:50 UTC pilot was PARTIAL: a48.154352s coordinator/reporting delay
contributed to B's message arriving18s after A exited. No ACK/handoff read was
proved. [DESIGN-027](sagas/distributed-dev-env/designs/027-pilot-rendezvous.md)
records the accepted same-step gate-to-dispatch harness and finite phase bounds.

The next approved19:16:08–19:46:08 UTC window also ended PARTIAL. Only A was
created. Its task began19:26:56; the harness refused at19:27:27.419783 after9
observation rounds, before any B attempt reservation. Installed agentd sends
heartbeats every60s, while that harness rejected age>30s: a proven contract
mismatch. The timing fits that guard, but the rejected observation was not saved,
so the exact predicate for that old run remains inference.

A's claim was created19:28:42. Its flushed wrapper appears after round6 commentary
in the final supported log, with aggregated script output. Python flush alone
therefore did not establish timely tool-to-log visibility; first visibility time
was not recorded. The wait script ended19:33:57.233877 after8 rounds/16 reads,
exit2. A's model task finished19:34:15, exit0, semantic PARTIAL. No ACK,
publication or reply occurred; B never existed.

**Shutdown addendum:** independent Mac read-only GET at19:47:40 UTC verified
original A PodUID `67c216d7-3aa0-4290-ad0c-1356eab4c8e6` in phaseFailed,
reasonDeadlineExceeded. Its original agent container terminated exit0,
reasonCompleted, finishedAt19:46:37 UTC (startedAt19:26:40). Actual original
executor-container shutdown is now proved by supported Pod status, not inferred
from task exit or TTL. Retained Session `privateHistory.stopState` is still
**Unknown**; this proof does not change that field or establish semantic success.

All consumed windows, seals, actor identities, attempts, failures and deadline
records stay retained. No retry/replacement/new window is authorized by this file.

## Exact remote-only uncommitted WIP

The original correction is **not committed, pushed or independently reviewed**:

- Branch `agent/c7-rendezvous-freshness-1016-1538`.
- HEAD/base `71b713e849de52aecb540dc722733514cf5c6d95`.
- Remote pod worktree `/home/dev/work/dev-env-c7-rendezvous-freshness-1016-1538`.
- Dirty paths, and only these four:
  - `scripts/trials/pilot_rendezvous/main.go`
  - `scripts/trials/pilot_rendezvous/main_test.go`
  - `.agents/sagas/distributed-dev-env/designs/027-pilot-rendezvous.md`
  - `.agents/sagas/distributed-dev-env/verification/pilot-rendezvous-2026-10-16.md`
- Preserved remote patch:
  `/home/dev/work/orders/dev-env-chapter7-rendezvous-freshness-local-1016-1538/STOPPED-WIP.patch`
- Patch SHA256:
  `8c12c7ea448cf696437ce77b7e2a5e5b8658e2a31f73637fb3522896ba3e0a74`.

The local candidate allows70s heartbeat age (60s cadence+10s observation budget),
retains30s claim freshness/identity/authority/deadline guards, and saves bounded
refusal predicate/timestamp/UID/heartbeat evidence. Its procedure requires a short
COMPLETED claim/receipt tool call before a separate long wait call. Fake adapters
retain the315.233877s buffering counterexample and show one dispatch only when
the receipt becomes visible promptly. This models a contract, not live streaming.

Existing focused offline test result: **15 methods/43 subcases PASS**, no failures,
no live API/model requests. Raw output is `focused-1.jsonl` beside the patch;
`STOP-HANDOFF.json` records exact state and clocks. No correction PR/CI/release
exists. This handoff PR publishes documentation only. Fetching it does NOT put
the dirty patch or remote artifacts on the laptop; an authorized verified copy
is needed. Do not modify the preserved remote branch to make it look delivered.

## Smallest next pickup, only when Tom directs it

1. Read this handoff and repository/common rules. Coordinate through the existing
   [OpenProject project](https://work.haynesnetwork.com/projects/haynes-engineering/work_packages);
   GitHub issues/PRs are artifacts. Do not create duplicate queues or activate
   paused stories merely by reading this page. Existing #353 is an artifact.
2. Obtain the remote patch/evidence through an already-authorized path, verify its
   SHA256, then use a fresh isolated story worktree from accepted main. Preserve
   the original dirty worktree and prior failures. Define one writer, exact paths,
   finite budget and acceptance before edits. Review the existing passing evidence
   before deciding whether a new targeted check is necessary.
3. Independently review the narrow freshness/diagnostic correction and the
   short-call visibility contract. Keep actual native visibility unproved until
   supported evidence establishes it. Any future delivery needs normal exact-head
   required CI and review; source acceptance is not runtime acceptance.
4. Before any later conflicting execution, bind/reuse the exact original-A
   shutdown proof above; keep the retained Session stop-state field Unknown. Any future
   runtime needs fresh explicit scope, exact new identity/seal/window bindings,
   one-shot dispatch, checked remove-only closure and peer/v1 preservation.
   Do not reuse consumed attempts or assume old approvals survived STOP.

No implementation/model workers, manual tests or reviewers remain active for the
stopped correction. The retained Session stop-state field remains Unknown despite
original A's proved container shutdown; neither fact authorizes stop/delete. Do not touch unrelated user/hass-sandbox or
paused HaynesNetwork sessions. Never read restricted Codex session/rollout/database
files, extract credentials, expand access or restart/cut over v1.
