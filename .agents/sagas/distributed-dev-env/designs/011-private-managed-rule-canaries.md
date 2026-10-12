# DESIGN-011: Two guarded private managed-provider canaries

**Scope:** the next bounded #173 client/admission source unit, after private
retention protection. Default off. This authorizes no trial, pairing or native
fleet UX; Q-24 remains pending. Claude/Codex use their existing subscription
CLIs. Local-model contracts stay present; inference waits for GPU readiness and
household reservations.

## Contract

Keep global private-project admission disabled. Add an independent strict
canary policy with at most two explicit records, one per subscription provider.
Only the existing client identity can request a fixed canary ID; requests cannot
choose its project, repo, profile, model, effort, size, source, rules or limits.
The trusted record fixes the client namespace/ServiceAccount and exact accepted
project/repo/provider/profile/full-model/effort/size-S tuple, catalog/rules
revision, exact template revision, verified immutable image, fixed bounded
prompt/timeout/turns and idempotency key. The request contains the canary ID only;
the fixed prompt does not contain validation marker values. No wildcard, unknown field, duplicate or caller
override is accepted. API disabled or missing prerequisites means no creation.
The operator enforces that on the reviewed record (2026-10-12, #91 rules-003;
this replaces the same day's "tests check the rendered values instead", which
left the record itself unchecked). `projectcanary.CheckPrompt` refuses a prompt
that contains a trimmed line of at least 16 bytes of the bound text, or one of
its marker-shaped tokens even mid-sentence (at least 12 characters joined by `-`
or `_`, with a letter, and with a digit or no lowercase letter). `ResolveCatalog`
applies it to the project rules, so the create route, the controller before an
executor and the result reader all refuse such a record; `Record.CheckCommon`
applies it to the retained common text and Codex header in those same three
places. A refusal is fail-closed: it only stops a record that already breaks this
contract, and the record is reworded. The live records' prompt passes against
their bound revisions. Separately, the operator's pod
(`TestProjectCanaryRenderedPromptAndArgsCarryNoRuleLine`) and agentd's launch
(`TestProjectRulesReachTheAgentOnlyAsRuleInputs`), each with and without a
retained common snapshot, carry no line of the bound rules, common text or header
outside the rule inputs.

Require trusted named catalog/clone authority, private-history retention and
the reviewed private profile/template contract. Codex also requires the existing
managed Codex gate. Only finite task mode is supported: refuse base overrides,
restoration, shared workspace, tools override, session/coordinator callers and
budget-assigned callers. Whole-task private budget dispatch remains #154's
separate acceptance. The original new-task fetch/immutable pin and saved
instruction composition are reused, never replaced by bootstrap refresh.

Derive one deterministic retained Session identity from caller and canary ID.
Exact retries return that original record even when terminal. Concurrent API
replicas, lost create acknowledgements and uncertain state cannot generate a
second name or executor. Persist an immutable canary binding in the Session.
Attach a distinct permanent canary-retention finalizer at the initial atomic
Create. Early deletion before controller reconciliation burns that original
identity without allowing a replacement; normal history-retention initialization
cannot remove this finalizer. Never release or rearm it in this unit.
The controller independently validates the policy/binding and confirms the
retention latch before creating its one permitted Pod.
An authenticated canary Session cannot create other Sessions through legacy
dispatch routes. Heartbeat and observation remain available. Disabling child
decision rendering alone does not establish the one-executor boundary.

The execution is one container, no init/sidecar/ephemeral container,
RestartNever, 600-second active deadline, five-second termination grace and
k8tz injection disabled. Keep size-S CPU/memory limits and worker placement.
Provider execution timeout is at most 180 seconds; Claude is at most three
turns, Codex keeps its supported max-turns value zero. Preserve terminal
Session/HOME and original executor attempt. Pod absence, deadline or stale
heartbeat does not establish StopConfirmed, authorize rearm or prove a native
chat boundary. Operator upgrades preserve existing agent Pods.

Codex uses only the existing keeper access-only delivery and five-minute
startup validity guard. No enrollment, rotating credential or private provider
history is cloned. Expired/short-lived access refuses provider launch. Claude
uses the existing subscription credential path. No paid API substitution,
new refresh owner or new high-power access follows.

## Paths and acceptance

- Operator flags and strict bounded policy parser; shared policy validation.
- API request canary ID, deterministic create/retry path and precise refusals.
- Typed immutable Session canary binding and generated schema.
- Controller independent admission proof and finite Pod rendering; existing
  private-history intent guard protects execution.
- Focused parser/API/controller/schema tests and durable saga/HANDOFF receipts.

Prove disabled/missing/malformed/unknown/duplicate/wildcard policies fail closed;
wrong caller or tuple creates nothing while the global gate stays false;
concurrent and uncertain retries retain one original Session/Pod UID; terminal
retry does not launch again; retention uncertainty creates no executor;
template drift/sidecars/absence of bounded resources are rejected; and source,
rules, subscription access and original executor identities remain bound.
Use sequential focused tests under nice19/GOMAXPROCS2/p2, necessary real schema
checks and affected lint/codegen. No stress, wide loops, local image builds or
provider/model/cluster invocation belongs in this source unit. Required CI and
native independent review govern merge; Claude reviews remain optional/down.

## Separate GitOps and installed acceptance

After signed publication, review exact canary profiles, policy and schema in
GitOps. Use the existing Codex read-only access-only projection; add no refresh
credential or enrollment copy. Supply nonempty versioned common/repo rule
markers through the accepted catalog, with no marker supplied in the task
prompt. Review effective canary egress under additive policies; unrelated
MCP/web permissions must not silently broaden these one-shot tasks.

Run each reviewed canary once, only with usable existing subscription access.
Prove actual placement/resources/private HOME and Git admin/worktree boundaries,
fresh selected source/pin, exact injected rule revisions and actual provider
output/events confirming both markers. File presence alone is insufficient.
Retain terminal history and failed receipts; track logical attempts across
pods/methods and stop at three failures or 60 minutes without evidence.
Managed-task success does not establish native nested rule discovery or
complete-chat routing. New native project start/injection remains separate.
Keep #173/#91 open for remaining client/pilot acceptance; no v1 restart/cutover,
elevation, destructive cleanup or retired storage retry.
