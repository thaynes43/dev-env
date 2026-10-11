# DESIGN-018: initial invocation usage and cost provenance

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
