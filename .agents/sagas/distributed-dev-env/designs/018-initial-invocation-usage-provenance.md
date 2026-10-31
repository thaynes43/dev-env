# DESIGN-018: initial invocation usage and cost provenance

Delivery update, 2026-10-11: source and signed v2.20.0 prerequisites are deployed
through haynes-ops #3823. Actual provider/pilot acceptance remains separate. The
[installation note](../verification/installation-2026-10-11.md) preserves the
observer refusal, Directory Unknown state and remaining activation/read gates.

Contract published 2026-10-11 UTC. Parent #176; ADR-003 controls privacy and
coordination. One source PR fixes the parser-to-display defect identified by the
finite source audit. Token-only Codex results currently acquire numeric cost zero
and the CLI prints dollars. Missing cost is Unknown. This contract does not add a
pricing engine, paid API calls, provider history importer or whole-session total.

## Observation and provenance

Add a versioned optional UsageObservation alongside retained legacy raw Usage in
the private task result, heartbeat protocol and AgentSession status. The new API
Session DTO exposes the typed observation. Preserve existing legacy raw status
and wire field definitions for compatibility, without migrating old values into
observations. New agents omit deprecated unqualified raw Usage from outgoing
heartbeats; private task results and already-stored raw evidence remain intact.

A valid observation records original initial invocation identity, original
Session UID and Pod UID, requested provider/full model and original receipt time.
Use a purpose-specific canonical SHA256 observation ID with fixed terminal
ordinal zero and a stable original attempt ID. Save original launch identity
before execution through existing private durable ownership/intent helpers;
missing, foreign or ambiguous receipts remain Unknown, without reconstruction,
relaunch, config/HOME inspection or a new provider attempt. Trusted managed
source identity must work independently of output-capture being enabled.

The server derives Session/Pod/provider/requested-model attribution after fresh
existing ownership and original-UID checks, and checks the saved assertion for
agreement. Logical task UID/epoch/worker identity comes only from existing
server-written budget bindings; absence is Unknown. Current names, parents or
prompts cannot fill it in. Actual served model stays Unknown; a requested model
or native init banner is insufficient. This is provider CLI output reported by
its publisher, not an attestation of billing or expenditure.

Scope is InitialManagedInvocation, counter semantics InvocationFinalTotal.
Each input, output, cache-read and cache-create token count has independent
presence and a nonnegative bounded integer value only when explicitly reported.
Do not add cache counters to input totals when inclusion semantics are Unknown.
Cost is Unknown or Reported. A Reported cost carries a bounded canonical decimal
amount, USD unit and ProviderCLIEquivalentEstimate provenance. Explicit observed
zero remains zero; absence, null, malformed, negative or nonfinite amounts remain
Unknown. Available counters can survive an unavailable cost independently.
Unsupported/unavailable API billing, subscription quota and local resource
estimates remain Unknown. No token pricing or inferred subscription dollars.

Claude total_cost_usd requires native field presence and a valid amount. It is a
CLI equivalent estimate even on subscription-backed operation. Codex token-only
results have Unknown cost. Provider error or result.Seen does not establish a
metric's presence; preserve any explicitly observed partial metrics with truthful
coverage. Continue parsing stdout only, preserving Capture #198 protocol fences.
Local-model provenance is represented as unavailable until its adapter and exact
identity exist; inference still waits for GPU readiness and household reservations.

## Retention, replay and compatibility

Retain one final initial-invocation snapshot. Identical observation ID and
canonical payload replay is idempotent; never sum heartbeats. A changed payload
for the same ID, foreign original identity or different attempt cannot overwrite
the retained snapshot or be counted as a new event. Preserve the original and a
bounded conflict/Unknown receipt. A legacy heartbeat cannot erase or relabel a
confirmed typed observation. Optimistic status updates must preserve concurrent
fields and retained original identity. Usage-only conflict must not prevent
ordinary heartbeat/liveness processing or budget enforcement.

Native/TUI/resume coverage stays unavailable. The initial snapshot must remain
labelled initial-only after resume. Provider CLI inclusion of internal delegated
work is Unknown, so no session/model/parent-child monetary aggregate is emitted
in this PR. Whole-task elapsed effort and stable failure-attempt accounting are
separate existing units; their enforcement is unchanged.

Root ratifies the mixed-version presentation choice: new API responses omit the
deprecated untyped Usage field for all observations, including legacy raw values
and reported CLI estimates. Otherwise old clients synthesize $0 or label an
estimate as dollars without provenance. Raw legacy CRD data is preserved. Old
clients remain able to decode the additive DTO and simply lack an unqualified
cost row. New clients talking to old operators display cost Unknown when typed
metadata is absent, regardless of old zero/nonzero raw cost. Any legacy token
values shown must be labelled unclassified. Old schema pruning cannot promote a
raw numeric zero to Reported. Already-running old binaries are not retroactively
fixed; installation acceptance identifies upgraded surfaces without restarting
existing session Pods solely to update reporting.

Native review COST-R1 establishes the remaining mixed-version path: a new agent
sending token-only legacy Usage to an old operator still manufactures numeric
zero, and an old CLI displays dollars. Root therefore requires new agents to
omit raw Usage from every outgoing heartbeat, including CLI estimates and
Unknown results. An old operator can ignore the additive typed observation
without acquiring a new unqualified cost row. Ordinary heartbeat, liveness and
budget fields must continue; test the actual outgoing JSON against the old
decoder. Preserve private results and existing stored legacy values, without
migration or restart of existing agents.

COST-R1 is corrected at `c97f800c`: collection stops forwarding raw Usage and
the final Send boundary removes it on a value copy, including direct callers.
Actual outgoing JSON/old-decoder, incoming legacy compatibility, typed/liveness
and stable-counter/deadline budget cases passed; private raw results and existing
generated schemas remain intact. Packet
`192b9a2ccc1bc0d022419fe2090f1ebfbe6c0187e94b709c4ba5aff06187c525`
preserves the original HOLD and prior fixtures. Final native review and CI remain
gates; this is a source correction, without provider or deployment acceptance.

Corrected standalone native review passed at `a3d44d5e` with receipt
`063da43dfee9faa4485dc6e435c24dcbee1a2fa0a0047d00e233a99af4229a41`;
required CI 38172643863 passed. Integration onto merged directory source #199
`7fa1705e` retains both schema additions and requires a fresh exact-head review
and CI. Optional Claude failed before a verdict; no retry or auth repair.

Integrated source `35f2f4bf` passed receipt/CAS/replay, the combined real-schema
case, affected lint, static CLI guard and exact-head generation. Packet
`9aa9a031b96eaa720e69606180dfc760809deb7d568ff2c7ba4a4ae68817ccbe`
preserves all incoming source/prior artifacts. Exact combined AgentSession CRD
SHA256 `32b2c3e3f40258694e3693a3f5ba3631b71fa729b1708a3cfb6f9e4828155703`.
The integrated exact-head native review/CI and signed deployment remain gates.

New CLI/parser wording labels Unknown cost, explicit reported zero/nonzero,
USD CLI equivalent estimate provenance, initial invocation coverage, requested
exact model and actual-model Unknown. Context/capture assertions retain their
separate Unknown model/usage/cost fields; this observation cannot upgrade them.

## Affected paths and acceptance

Affect internal/agentd/{streamjson,codex_stream,runagent,status,launch} and its
protocol/retained private result helpers; internal/apiserver/{heartbeat,view}
and apiv1 DTOs; internal/agentrun/sessions; AgentSession status schema/generated
files and focused tests. Reuse bounded private files and original source proofs.
No new service, authority, lifecycle cleanup, credential owner or provider turn.

Tests must distinguish missing/null/invalid cost from explicit zero/nonzero,
Codex token-only results, absent individual counters and preserved cache counts.
Test legacy/new DTO and schema combinations, explicit-zero survival and the old
CLI condition against the server omission policy. Repeated snapshots must be
idempotent, conflicts/foreign UID/attempt refused without original loss, and
concurrent/legacy heartbeats must not erase the typed observation. Prove no
aggregate is produced for unknown child inclusion and initial-only coverage
survives resume. Existing fake-clock/stable-attempt budget checks must continue
with unavailable provider counters. Do not alter task deadline arming, elapsed
ledger accounting, reservations, failure counters or stop receipts.

Use meaningful focused package/schema checks, affected lint and reproducible
generation at nice 19, GOMAXPROCS=2, go test -p 2. No stress, CPU burners, wide or
looped tests, local image builds or real provider probes. Preserve all original
failed fixtures, ledgers and uncertain receipts; counters follow the logical task.
Stop after three failed attempts at one blocker or 60 minutes without evidenced
progress and ask one actionable question through the native tool.

Required CI and native independent review gate squash merge. Claude is optional
while down: retain/read its failed/skipped receipt or substantive findings, with
no retry/auth repair. Release and exact schema/operator/client/agent GitOps
promotion follow with separate installation verification preserving existing
sessions. No v1 restart/cutover or new permission grant is included. Actual
provider usage, exact served model, billing/quota, local inference and complete
MVP acceptance remain separate; keep #176/#91 open.

## Provider-reported served model, usage and cost (2026-10-12, #176)

This section supersedes the statements above that keep the actual served model
Unknown and forbid a parent/child aggregate. It applies to observations of
version 2. Version 1 observations keep their original rules and display.

**What the provider reports.** The final result event is the primary source; for
Codex, the thread's rollout is a second, narrower source (below). Claude's
stream-json `result` event carries `usage`, `total_cost_usd` and `modelUsage`, a map
from served model id to `inputTokens`, `outputTokens`, `cacheReadInputTokens`,
`cacheCreationInputTokens` and `costUSD`. Codex `exec --json` reports token counts
on `turn.completed`; agentd reads a served model there only from an explicit
`model` string. The pinned 0.160 schema emits none, so the stream alone leaves the
Codex served model Unknown, and Codex cost stays Unknown.

*Codex rollout (2026-10-19).* When the stream named no model, agentd reads the
Codex CLI's own rollout for the confirmed native thread, after the turn ends:
`$CODEX_HOME/sessions/YYYY/MM/DD/rollout-*-<thread-id>.jsonl`, the newest match.
The served models are the distinct `payload.model` strings of its `turn_context`
records. This is agentd's own runtime reading its own task's rollout, narrowly: no
other field of any record is decoded, so no message text reaches status, result
or log, which keeps it consistent with the agent rule against reading restricted
Codex session files. Bounds: the thread id must be a native thread UUID; no symlink
is followed at any level; a regular file only; at most 64 MiB, and a record line over
64 KiB is skipped unread. A missing, oversized or invalid rollout, an invalid model
id, or more than `MaxServedModels` models leaves the model Unknown, never a partial
list. One model takes the turn's counters, as the stream path does. Several models
share no honest split of one total, so each is listed with Unknown counters. An
explicit stream model wins over the rollout. Reporting only: nothing here changes
admission, budgets or cost.

**Version 2 observation.** agentd records, from the final result event:

- `provenance: ProviderReported`. The CLI that served the invocation reported it.
  It is not an attestation of billing.
- `billingBasis: Subscription` when the launch is plan-served by policy: a Claude
  launch that unsets `ANTHROPIC_API_KEY` and `ANTHROPIC_AUTH_TOKEN` (V-05), or a
  Codex launch that forces the ChatGPT login. Any other launch is `Unknown`. The
  USD figure is the CLI's list-price equivalent, never a charge, and its kind stays
  `ProviderCLIEquivalentEstimate`.
- `servedModels`: at most 8 entries, sorted by model id, each with its own four
  token observations and cost observation. `actualModelState` is
  `ProviderReported` exactly when the list is not empty. A malformed map, a
  malformed or overlong model id, or more than 8 models leaves the list empty and
  the state Unknown; a partial list is never kept.
- The invocation totals, as before: each counter and the cost has its own
  presence. Absent, null, malformed, negative or out-of-range values are Unknown,
  never zero. An explicit zero stays zero.

Subscription quota, API billing and local resource stay Unknown. The observation
ID, source, receipt, replay, conflict and old-decoder rules above are unchanged.
New agents still omit the raw Usage field from heartbeats.

**Mixed versions.** The CRD accepts version 1 or 2, with CEL rules for each shape.
Promote in this order: the CRD, then the operator, then the agent image. Old
agents keep sending version 1. Out of order, two things happen:

- *New operator, old CRD.* The operator's own validation accepts a version 2
  observation, and the API server refuses the status patch (422, `version` is an
  enum of 1). Liveness was patched first, so it continues, but the heartbeat reply
  is an error and no receipt is written. agentd keeps replaying the private
  snapshot, so the observation is retained once the CRD is upgraded.
- *Old operator replica during a rollout.* It refuses version 2 and writes an
  `InvalidPayload` `usageConflict` receipt. A receipt is written once and never
  cleared, so after an upgraded replica retains the observation, `show` prints
  that conflict next to a valid observation for the life of the Session.

**Reader negotiation.** Older readers decode Sessions and publications strictly
and accept only the version 1 observation and all-Unknown attribution. A reader
asks for the new shape with the request header `Dev-Env-Attribution: provider-v1`;
an older operator ignores it. Without the header the API leaves a version 2
observation and the rollup out of every Session response, so an older reader
shows Unknown instead of refusing the Session, and publications keep the
all-Unknown attribution. New agent-run sends the header on every call; new
agentd sends it on published-context calls.

**Published context attribution.** Only a managed output assertion (DESIGN-016)
can carry usage, because it is the output of the same initial invocation: the key
is the Session UID plus the Pod UID. The stored PublishedContext never changes and
keeps its publication-time attribution, Unknown. When a reader sends the header,
the API joins, at read time, the source Session's retained version 2 observation,
and only when its source is that Session UID and Pod UID, its provider and
requested model match, and the Session still has that UID. Then
`observedModelState`, `usageState` and `costState` become `ProviderReported` each
only when that part was reported, and the attribution carries the observed
models and a bounded usage summary: observation ID, billing basis, the four
counters and the cost. Otherwise all three stay Unknown. A version 1
observation stays Unknown too. A publication made before the next heartbeat
retains the observation shows Unknown until then. Explicit publications stay
Unknown.

The join is live, not stored. The observation is immutable once retained, so
while the source Session exists a reader never sees one reported value change
to another. A PublishedContext has no owner reference and outlives its source
Session: once that Session is reaped, or replaced under its name by another
UID, the join finds nothing and every reader sees Unknown again. The attribution
is durable only as long as the source Session. Keeping it after a reap needs a
bounded copy written once the observation is retained, which changes the stored
record's write-once rule; that is a separate decision (#248). The canary result reader, agentd and agent-run accept
both shapes through one validator, `apiv1.ValidContextAttribution`.

**Parent and child.** A child Session's usage belongs to the child, in its own
status; nothing is added to the parent's own observation. `GET
/v1/sessions/{name}` adds `usageRollup` with scope `RetainedDescendants`: the
Session's descendants that still exist, not the Session itself. They are found by
`spec.parent` name and created no earlier than their parent, walked once each by
UID, at most 64 Sessions. Each Session contributes at most its own retained
observation, keyed by Session UID and observation ID, and only when the source
Session UID matches. A metric is Reported only when every walked Session has an
observation, every one reported that metric and the total stays within the
per-metric token bound; otherwise it is Unknown, with no partial sum. Each
separate Session is a separate CLI process, so the sum counts nothing twice.
Work a CLI delegates inside its own process is in that CLI's own report and is
never added again.

A descendant whose Session object is deleted is not walked and its usage is
not in the rollup. A reap request that retains a protected private Session
does not remove its usage. `coverage: Complete` means every retained descendant reported, not every
descendant the Session ever had; coordinators reap finished children routinely,
so the rollup usually undercounts the whole tree. A per-parent ledger that
survives a reap is a separate decision (#248).

A coordinator may read only its direct children. On a child's view it gets the
rollup's totals and counts, which are the usage of the work it delegated, but
not `invocations`, the keys that name grandchildren it may not read. Every other
caller may read any Session and gets the keys.

`agent-run show` prints the served models, provenance, billing basis, each
model's counters and cost, and the retained descendants' rollup, saying that
deleted Session objects are excluded, and so are descendants reachable only through a deleted Session (the walk follows parent names); retained reaped/reclaimed Sessions count.

**Out of scope.** Pricing from tokens, subscription quota, API billing, local
inference, TUI/resume/native coverage, the SessionDirectory usage fields and
rollups across names that a parent never created stay Unknown or absent.

**Tests.** Claude fixtures with modelUsage present (one and several models),
absent and malformed (not an object, empty, a non-object entry, a repeated key,
an invalid or overlong id, more than 8 models); Codex token-only, with an
explicit model and with a malformed one; billing basis per launch policy; a
walk over every observation that no Unknown carries an amount; version 1 and 2
validation in Go and in the CRD (envtest); heartbeat retention and the API wire
shape; the rollup's single count, stale names, cycles, the bound, missing and
foreign observations, Codex cost, a total past the token bound, a reaped
descendant and the coordinator's view without keys; the attribution join, its
foreign-Pod, replaced-source, version 1 and explicit refusals, a reaped source
and both reader shapes; agent-run show, show --once (including a null model
list) and the canary result reader with the new shape and with inconsistent
attribution.


## Captured direct-child receipts (2026-10-23)

OpenProject #109 source slice. Issue #248 stays open: this is not an all-history
descendant total and not durable PublishedContext attribution.

When `--enable-child-usage-capture` is set, a child created by an authenticated
Session stores `spec.usageCaptureParentUID` from a fresh read of that parent.
The request body cannot set it. Human, client, and coordinator creates leave it
absent. A replaced parent keeps the UID from create time, so capture does not
follow the new object. The flag defaults off. Off, create does not write the
UID and heartbeat does not read or write a parent ledger.

`status.capturedUsage` on the surviving parent is version 1, scope
`CapturedDirectChildInitialInvocations`, coverage `CaptureOnly`, retention
`ParentLifetime`. It stores at most 64 receipts and 262144 serialized bytes.
A full ledger sets a sticky overflow mark and adds no row. There is no
eviction, archive, or reap delay. The receipt is the child's accepted initial
observation, copied as stored, including provider, requested model, actual-model
state, and explicit zero versus Unknown. A later conflict does not rewrite it.
The same child UID is one row. The same key and payload replay does not change
the capture time. `usageRollup` stays a live retained-descendant sum and does
not include these rows.

`agent-run show PARENT --captured-usage -o json` sends
`Dev-Env-Usage-Capture: direct-v1`. Text and `--once` are refused before a
request. Without the header, session JSON does not carry the ledger or the
parent UID. A disabled or older server is an unavailable view, not zero usage.
A Session sees the ledger only for its own UID. A restricted coordinator does
not receive receipt rows. This is source only. The flag is not enabled in a
deployment, and the CRD is not installed by this slice.

The overflow mark is presence-safe. A missing overflow field is not overflow,
and once it is true it stays true. Removing the whole capturedUsage object is
refused, so a later liveness or reconciler write can keep a ledger that has not
overflowed. Before a row is accepted, the writer reserves the overflow field
and the bytes of the direct-v1 view inside the 262144-byte cap. Setting
overflow then leaves both the stored ledger and the receipt view readable. No
row is evicted.

A receipt is copied only from the child stored observation after that usage
write. The observation must validate, match this request source, have no usage
conflict, and bind the authenticated request pod. When private history records
a last executor, that executor has to be the same pod. A conflict on the parent
write does not fail the heartbeat; the next identical heartbeat can record the
same accepted observation. A later conflicting snapshot does not copy the
earlier one.

An absent ledger is unavailable. It is not an empty available list and it is
not zero usage. Dev-Env-Usage-Capture: direct-v1 is its own opt-in. agent-run
sends it together with Dev-Env-Attribution: provider-v1. A read without the
capture header does not include the ledger. The client checks the whole view
before printing it. A malformed or oversized view is unavailable, with no
partial receipt list. Observation versions 1 and 2, a reported zero, and
Unknown stay as they were captured.

## Compute reclaim and retained reap read contract (2026-10-31, OP315)

A reap request is distinct from actual removal of a Session object. Private history protection keeps a
reap-requested Session and its usage while the finalizer remains. Terminal task
compute reclaim (#729/#738, DESIGN-025 section 24) removes its pod, retains
HOME/history and the Session, and reports `Suspended` with
`TaskComputeReclaimed`. Neither transition changes usage identity or counters.

The API's own observation and descendant walk read retained Session status.
The walk visits each descendant UID once and excludes the root's observation.
It includes retained reaping, suspended and archived Sessions. It never adds
`capturedUsage` to these observations: that would count a retained child twice.
The CLI's text now names retained reaped/reclaimed Sessions as included and
deleted Sessions as excluded. API wire scopes and capture permissions do not
change.

The portal reads the same validated initial observation through its activity
allowlist. Interactive cumulative `status.conversationUsage` stays separate;
its last receipt remains visible after stop, with its measured time and stale
marker. The Usage breakdowns and Sessions panels retain counters independently
of lifecycle freshness. Missing receipts remain Unknown, and the requested
model is never promoted to a served-model observation.

OP315 fixtures cover root/child/grandchild reads before and after pod removal,
a finalizer-protected descendant reap, unchanged exact totals and identities,
explicit zero/Unknown counters, served/requested model separation, and missing
evidence. Replayed API observations passed to the pure consolidation projector
produce one row per invocation. Portal fixtures retain separate task and
conversation counts across stopped phases. These prove source behavior only;
they do not execute reclaim, connect a reader, or establish runtime acceptance.

If a Session object has already been deleted, the ordinary rollup and portal
inventory cannot reconstruct it from HOME or a missing pod. Existing optional
parent receipts may preserve its initial invocation for explicit capture/export
reads; they remain a separate `CaptureOnly` view, not an all-history total.
Without a retained observation or receipt, that historical usage is outside the retained-descendant scope: it is absent from a total that can still read Complete.
No live-pod read, provider-account call, allowance connection, capture enabling
or activation is part of this contract. Tokens remain usage, never quota.

The OP315 trace also hardened the portal read contract (a conformant operator pages one consistent snapshot, so this cannot occur today): identity uniqueness was
checked within each page, but a repeated Session on a later page contributed
again to the Usage breakdowns. The read must reject duplicate Session names or
UIDs across pages before projection, preserving the existing response-contract
failure behavior (page/row diagnostics, no partial rows or totals). Both task
initial receipts and cumulative conversation snapshots follow this rule.
